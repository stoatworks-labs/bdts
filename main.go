package main

// bdts — Tailscale authentication and settings inside birdUI, on the BirdDog PLAY.
//
// Two jobs, both of them small:
//
//   --patch-ui   adds the panel to the stock System page (and removes it again)
//   --serve      answers the panel's API on its own port
//
// The firmware patch that installs Tailscale calls the first at install time
// and runs the second as bd-tailscale-ui.service. Before this existed, a PLAY
// came up with Tailscale installed but unauthenticated, and the only way to
// finish was to SSH in on port 9031 and run `tailscale up` by hand — which the
// public firmware patcher has to tell people to do, because it deliberately
// bakes no auth key into the package.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// version is set by build.sh.
var version = "dev"

const defaultAPIPort = 8092

func main() {
	var (
		serveAddr   = flag.String("serve", "", "serve the panel API on this address (e.g. :8092)")
		patchUI     = flag.Bool("patch-ui", false, "add the Tailscale panel to birdUI's System page")
		unpatchUI   = flag.Bool("unpatch-ui", false, "remove the panel, restoring the stock System page")
		restoreUI   = flag.Bool("restore-ui", false, "restore the System page from the backup taken before patching")
		uiDir       = flag.String("ui-dir", "/srv/birddog-web-ui", "the stock web UI's directory")
		uiBase      = flag.String("ui-base", "http://127.0.0.1", "where the stock web UI answers, for session checks")
		apiPort     = flag.Int("api-port", defaultAPIPort, "port the panel calls; baked into the script at patch time")
		tsBin       = flag.String("tailscale", defaultTailscaleBin, "path to the tailscale CLI")
		tsSocket    = flag.String("socket", defaultSocket, "tailscaled's socket")
		tsUnit      = flag.String("unit", defaultUnit, "systemd unit running tailscaled")
		allowUnprot = flag.Bool("allow-unprotected", false, "allow changes even when birdUI has no password set (see README)")
		showStatus  = flag.Bool("status", false, "print Tailscale status as JSON and exit")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	u := UIPaths{Dir: *uiDir}

	switch {
	case *patchUI:
		if err := ApplyPatch(u, *apiPort); err != nil {
			if errors.Is(err, errAlreadyPatched) {
				fmt.Println("System page already carries the Tailscale panel; nothing to do.")
				return
			}
			fatal(err)
		}
		fmt.Printf("patched %s\nwrote   %s\n", u.Settings(), u.Asset())
		fmt.Println("birddog-web-ui must be restarted for this to appear (the unit is BirdDogWebUI).")
		return

	case *unpatchUI:
		if err := RemovePatch(u); err != nil {
			fatal(err)
		}
		fmt.Printf("restored %s to stock\n", u.Settings())
		return

	case *restoreUI:
		if err := RestoreBackup(u); err != nil {
			fatal(err)
		}
		fmt.Printf("restored %s from %s\n", u.Settings(), u.Backup())
		return
	}

	ts := NewClient()
	ts.Bin, ts.Socket, ts.Unit = *tsBin, *tsSocket, *tsUnit

	if *showStatus {
		ctx, cancel := context.WithTimeout(context.Background(), StatusTimeout)
		defer cancel()
		st, err := ts.Status(ctx)
		if err != nil {
			fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return
	}

	if *serveAddr == "" {
		flag.Usage()
		os.Exit(2)
	}

	gate := NewGate(*uiBase)
	gate.AllowUnprotected = *allowUnprot
	if *allowUnprot {
		fmt.Fprintln(os.Stderr, "WARNING: --allow-unprotected: anyone who can reach this port can join this device to a tailnet")
	}

	api := &APIServer{TS: ts, Gate: gate}
	srv := &http.Server{
		Addr:              *serveAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Generous, because an auth-key connect is waited on inline.
		WriteTimeout: 90 * time.Second,
	}
	fmt.Printf("bdts %s serving on %s (tailscale=%s socket=%s)\n", version, *serveAddr, ts.Bin, ts.Socket)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
