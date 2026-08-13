package main

// The API behind the Tailscale panel on birdUI's System page.
//
// Runs as its own process under bd-tailscale-ui.service, separate from
// tailscaled itself, so that a wedged or stopped tailscaled still leaves a page
// that can say so — the same reason bdcam's settings API is a separate unit
// from its streamer.
//
// Reads are open, matching the rest of the device. Writes go through Gate; see
// auth.go for why this one draws the line differently from bdcam.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type APIServer struct {
	TS   *Client
	Gate *Gate
}

func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/up", a.write(a.handleUp))
	mux.HandleFunc("/api/down", a.write(a.handleDown))
	mux.HandleFunc("/api/logout", a.write(a.handleLogout))
	mux.HandleFunc("/api/settings", a.write(a.handleSettings))
	return cors(mux)
}

// cors answers the panel's cross-origin calls.
//
// Unlike bdcam's, this cannot use Access-Control-Allow-Origin: *. The panel
// sends the birdUI session cookie, and a wildcard origin is not permitted to
// carry credentials — the browser drops the cookie and every write looks
// unauthenticated. So echo the caller's origin and allow credentials.
//
// Echoing is not a weakening here: the cookie is what authorises the write, and
// a hostile page in someone's browser could not read the response of a
// same-cookie request it was not allowed to make in the first place... which is
// precisely why writes are POST-only with a JSON content type, so they are not
// simple requests and always face a preflight.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// readLimited reads at most n bytes, so a page that is unexpectedly enormous
// cannot be turned into memory pressure on a 2 GB device.
func readLimited(r io.Reader, n int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, n))
	return string(b), err
}

// write wraps a mutating handler with the method check and the auth gate.
func (a *APIServer) write(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, errors.New("POST only"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), StatusTimeout)
		defer cancel()
		if _, err := a.Gate.Check(ctx, r); err != nil {
			// 403 rather than 401: a WWW-Authenticate challenge would make the
			// browser show its own basic-auth dialog, which is not the login
			// this device uses.
			writeErr(w, http.StatusForbidden, err)
			return
		}
		h(w, r)
	}
}

type statusResponse struct {
	Installed       bool       `json:"installed"`
	ServiceActive   bool       `json:"service_active"`
	BackendState    string     `json:"backend_state"`
	NeedsLogin      bool       `json:"needs_login"`
	Connected       bool       `json:"connected"`
	LoginURL        string     `json:"login_url,omitempty"`
	Hostname        string     `json:"hostname,omitempty"`
	DNSName         string     `json:"dns_name,omitempty"`
	IPs             []string   `json:"ips,omitempty"`
	Tailnet         string     `json:"tailnet,omitempty"`
	Version         string     `json:"version,omitempty"`
	TUN             bool       `json:"tun"`
	ExitNodeCapable bool       `json:"exit_node_capable"`
	ExitNodes       []exitNode `json:"exit_nodes,omitempty"`
	Prefs           *Prefs     `json:"prefs,omitempty"`
	PrefsError      string     `json:"prefs_error,omitempty"`
	Auth            AuthState  `json:"auth"`
	Error           string     `json:"error,omitempty"`
}

type exitNode struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Online   bool   `json:"online"`
	InUse    bool   `json:"in_use"`
	Hostname string `json:"hostname"`
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), StatusTimeout)
	defer cancel()

	resp := statusResponse{
		Installed: a.TS.Installed(),
		Auth:      a.Gate.Describe(ctx, r),
	}
	if !resp.Installed {
		resp.Error = "Tailscale is not installed on this device."
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.ServiceActive = a.TS.ServiceActive(ctx)

	st, err := a.TS.Status(ctx)
	if err != nil {
		// Distinguish the two ways this fails, because the fix differs: a dead
		// daemon is a systemctl problem, anything else is worth reading.
		if !resp.ServiceActive {
			resp.Error = "The Tailscale service is not running. Try: systemctl restart bd-tailscaled"
		} else {
			resp.Error = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.BackendState = st.BackendState
	resp.NeedsLogin = st.NeedsLogin()
	resp.Connected = st.BackendState == "Running"
	resp.LoginURL = st.AuthURL
	resp.Version = st.Version
	resp.TUN = st.TUN
	resp.ExitNodeCapable = st.ExitNodeCapable()
	if st.Self != nil {
		resp.Hostname = st.Self.HostName
		resp.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		resp.IPs = st.Self.TailscaleIPs
	}
	if st.CurrentTailnet != nil {
		resp.Tailnet = st.CurrentTailnet.Name
	}
	for _, p := range st.ExitNodeOptions() {
		ip := ""
		if len(p.TailscaleIPs) > 0 {
			ip = p.TailscaleIPs[0]
		}
		resp.ExitNodes = append(resp.ExitNodes, exitNode{
			Name:     strings.TrimSuffix(p.DNSName, "."),
			Hostname: p.HostName,
			IP:       ip,
			Online:   p.Online,
			InUse:    p.ExitNode,
		})
	}
	if prefs, perr := a.TS.Prefs(ctx); perr == nil {
		resp.Prefs = prefs
	} else {
		resp.PrefsError = perr.Error()
	}

	writeJSON(w, http.StatusOK, resp)
}

func decodeOptions(r *http.Request) (UpOptions, error) {
	var o UpOptions
	body, err := readLimited(r.Body, 64*1024)
	if err != nil {
		return o, err
	}
	if strings.TrimSpace(body) == "" {
		return o, nil
	}
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		return o, errors.New("could not read the request")
	}
	return o, nil
}

func (a *APIServer) handleUp(w http.ResponseWriter, r *http.Request) {
	o, err := decodeOptions(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// An auth key completes without a browser, so it is worth waiting for; an
	// interactive login returns immediately and the panel polls for the URL.
	timeout := StatusTimeout
	if o.AuthKey != "" {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
	defer cancel()

	if err := a.TS.Up(ctx, o); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "interactive": o.AuthKey == ""})
}

func (a *APIServer) handleDown(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), StatusTimeout)
	defer cancel()
	if err := a.TS.Down(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), StatusTimeout)
	defer cancel()
	if err := a.TS.Logout(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *APIServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	o, err := decodeOptions(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// An auth key has no meaning here and would end up on a command line for no
	// reason; refuse it rather than ignoring it silently.
	if o.AuthKey != "" {
		writeErr(w, http.StatusBadRequest, errors.New("auth keys belong to the connect step, not to settings"))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), StatusTimeout)
	defer cancel()
	if err := a.TS.Set(ctx, o); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
