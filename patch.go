package main

// Adding the Tailscale panel to birdUI's System page.
//
// The System page is /srv/birddog-web-ui/settings.html — NOT system-settings.html,
// which is the older markup the current firmware no longer serves. Both exist
// on disk; the nav in header.html links to /settings, and settings.html is the
// one built on layout.html like every other live page. Patching the wrong file
// is a silent no-op that looks exactly like a broken patch.
//
// That page also carries the System Update form. A malformed edit does not fail
// when it is written — it fails when birddog-web-ui parses the template, and it
// takes the firmware upload page with it, which is the one thing birddog-re's
// notes say never to endanger. Hence, following bdcam:
//
//   * the patch lives here in tested Go, not in sed in an installer;
//   * the insertion is wrapped in markers, so removal is exact;
//   * patching is idempotent, so re-running an installer cannot double it up;
//   * the anchor was verified to appear exactly once, and to be absent from
//     every other template in the firmware;
//   * a pristine backup is written before anything is touched.
//
// The inserted markup is deliberately trivial — an empty div and a script tag,
// with no template actions of any kind. Everything the panel does lives in the
// static JS, which is outside the template system entirely, so iterating on the
// UI never risks the parse again.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed web/bdts-tailscale.js
var tailscaleJS string

const (
	patchStart = "<!-- bdts-tailscale:start -->"
	patchEnd   = "<!-- bdts-tailscale:end -->"

	// anchor is the page's single closing body tag. The panel is inserted just
	// BEFORE it, which puts it at the bottom of the page, inside <body>, and
	// inside the {{define "settings"}} block.
	//
	// This started as "<!-- End UI Mode -->", the comment closing the last
	// section — which was verified unique on firmware 1.0.32 and 1.0.34 and
	// then found to be ABSENT on a real 1.0.30 unit, because the UI Mode
	// section did not exist yet. The patch correctly refused rather than
	// guessing, but the feature simply did not work on the hardware it was
	// written for. `</body>` occurs exactly once in every version checked and
	// does not depend on which sections a firmware happens to ship.
	//
	// The lesson generalises: anchor on structure the document must have, not
	// on a feature that a version might not.
	anchor = "</body>"

	// stockSuffix names the pristine backup kept beside the patched file, the
	// same convention bdplay uses for videoset.html.
	stockSuffix = ".bdts-stock"

	// assetName is the script as served. Under the web UI's static dir, which
	// is plain file serving, not templating.
	assetName = "bdts-tailscale.js"

	// portPlaceholder is substituted when the asset is written, so the panel
	// always talks to the port the daemon was actually configured with.
	portPlaceholder = "__BDTS_API_PORT__"
)

var errAlreadyPatched = errors.New("already patched")

// assetVersion is a short content hash of the script, used as a cache-buster in
// the URL. Browsers cache /static/ hard, and without it a change to the panel
// is invisible until someone thinks to force-reload — which gets misdiagnosed
// as a broken page.
func assetVersion(js string) string {
	sum := sha256.Sum256([]byte(js))
	return hex.EncodeToString(sum[:4])
}

// block is the entire insertion: an empty mount point and the script.
func block(js string) string {
	return patchStart +
		`<div class="row m-0 p-0 row_in_page cm_h_lg_auto" id="bdts_section"></div>` +
		`<script src="/static/` + assetName + `?v=` + assetVersion(js) + `"></script>` +
		patchEnd
}

// renderAsset substitutes build-time values into the script.
func renderAsset(port int) string {
	return strings.ReplaceAll(tailscaleJS, portPlaceholder, fmt.Sprint(port))
}

// IsPatched reports whether the panel has already been added.
func IsPatched(src string) bool { return strings.Contains(src, patchStart) }

// PatchSettings inserts the panel. Idempotent: patching twice is a no-op rather
// than an error, so re-running an installer is safe.
func PatchSettings(src, js string) (string, error) {
	if IsPatched(src) {
		return src, nil
	}
	if n := strings.Count(src, anchor); n != 1 {
		return "", fmt.Errorf("expected exactly one %q to anchor to in the System page, found %d — this firmware's settings.html differs from the one this patch was written for, so nothing was changed", anchor, n)
	}
	return strings.Replace(src, anchor, block(js)+"\n"+anchor, 1), nil
}

// UnpatchSettings removes every marked block, returning the file to stock.
func UnpatchSettings(src string) string {
	for {
		i := strings.Index(src, patchStart)
		if i < 0 {
			return src
		}
		j := strings.Index(src[i:], patchEnd)
		if j < 0 {
			// An end marker that is not there means someone edited the file by
			// hand. Removing to the end of the file would destroy it; leaving
			// it alone is recoverable from the backup.
			return src
		}
		end := i + j + len(patchEnd)
		// Take the newline the patch added after the block with it, so repeated
		// patch/unpatch cycles cannot accumulate blank lines. This mirrors
		// PatchSettings exactly — it inserts `block + "\n"` before the anchor,
		// so removing `block + "\n"` is what restores the file byte for byte.
		if end < len(src) && src[end] == '\n' {
			end++
		}
		src = src[:i] + src[end:]
	}
}

// UIPaths locates the pieces of the stock web UI this touches.
type UIPaths struct {
	Dir string // /srv/birddog-web-ui
}

func (u UIPaths) Settings() string { return filepath.Join(u.Dir, "settings.html") }
func (u UIPaths) Backup() string   { return u.Settings() + stockSuffix }
func (u UIPaths) Asset() string    { return filepath.Join(u.Dir, "static", assetName) }

// ApplyPatch writes the asset and patches the template.
//
// Order matters: the asset goes down first. The other way round leaves a window
// where the page asks for a script that is not there, and on a device someone
// is watching, a panel that renders as nothing is indistinguishable from a
// failed install.
func ApplyPatch(u UIPaths, port int) error {
	js := renderAsset(port)
	if err := writeFileAtomic(u.Asset(), []byte(js), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", u.Asset(), err)
	}

	raw, err := os.ReadFile(u.Settings())
	if err != nil {
		return fmt.Errorf("reading %s: %w", u.Settings(), err)
	}
	src := string(raw)

	// Keep a pristine copy, but never overwrite one taken before an earlier
	// patch — that copy is the only thing that is definitely stock.
	if _, err := os.Stat(u.Backup()); os.IsNotExist(err) {
		if werr := writeFileAtomic(u.Backup(), raw, 0o644); werr != nil {
			return fmt.Errorf("writing backup %s: %w", u.Backup(), werr)
		}
	}

	out, err := PatchSettings(src, js)
	if err != nil {
		return err
	}
	if out == src {
		return errAlreadyPatched
	}
	return writeFileAtomic(u.Settings(), []byte(out), 0o644)
}

// RemovePatch restores the template and deletes the asset.
func RemovePatch(u UIPaths) error {
	raw, err := os.ReadFile(u.Settings())
	if err != nil {
		return fmt.Errorf("reading %s: %w", u.Settings(), err)
	}
	out := UnpatchSettings(string(raw))
	if err := writeFileAtomic(u.Settings(), []byte(out), 0o644); err != nil {
		return err
	}
	if err := os.Remove(u.Asset()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RestoreBackup puts the pristine copy back. This is what the installer's
// rollback calls when the web UI fails to come back after a patch.
func RestoreBackup(u UIPaths) error {
	raw, err := os.ReadFile(u.Backup())
	if err != nil {
		return fmt.Errorf("reading backup %s: %w", u.Backup(), err)
	}
	return writeFileAtomic(u.Settings(), raw, 0o644)
}

// writeFileAtomic writes via a temp file in the same directory and renames, so
// a power cut mid-write cannot leave a half-written template — which on this
// page would mean no firmware upload form to recover with.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}
