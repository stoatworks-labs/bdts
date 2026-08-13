package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testAPI(t *testing.T, f *fakeRun, ui *fakeUI) (*httptest.Server, *fakeRun) {
	t.Helper()
	uiSrv := ui.server()
	t.Cleanup(uiSrv.Close)

	c := client(f)
	c.Bin = "go.mod" // any path that exists: Installed() only stats it

	api := &APIServer{TS: c, Gate: NewGate(uiSrv.URL)}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, f
}

func post(t *testing.T, srv *httptest.Server, path, session, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://192.168.1.42")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestWritesRequireASession(t *testing.T) {
	f := &fakeRun{}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})

	for _, path := range []string{"/api/up", "/api/down", "/api/logout", "/api/settings"} {
		t.Run(path, func(t *testing.T) {
			if got := post(t, srv, path, "", "{}").StatusCode; got != http.StatusForbidden {
				t.Errorf("unauthenticated %s returned %d, want 403", path, got)
			}
		})
	}
	if len(f.calls) != 0 || len(f.started) != 0 {
		t.Errorf("a refused request still reached the tailscale CLI: %v %v", f.calls, f.started)
	}
}

func TestWriteSucceedsWithASession(t *testing.T) {
	f := &fakeRun{}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})
	if got := post(t, srv, "/api/down", "good", "{}").StatusCode; got != http.StatusOK {
		t.Fatalf("authenticated request returned %d", got)
	}
	if len(f.calls) == 0 {
		t.Fatal("the request never reached the CLI")
	}
}

// Reads stay open, matching the rest of the device — the panel has to be able
// to say "sign in to birdUI first", which needs a status it can fetch.
func TestStatusIsReadableWithoutASession(t *testing.T) {
	f := &fakeRun{stdout: map[string]string{
		"status": `{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/x"}`,
		"prefs":  `{}`,
	}}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status returned %d", resp.StatusCode)
	}
	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.NeedsLogin || body.LoginURL == "" {
		t.Errorf("status did not carry the login state: %+v", body)
	}
	if body.Auth.Authenticated {
		t.Error("status claimed the caller was authenticated when it sent no cookie")
	}
	if body.Auth.Reason == "" {
		t.Error("status gave the panel nothing to explain why the controls are locked")
	}
}

// A wildcard Access-Control-Allow-Origin is not allowed to carry credentials:
// the browser drops the cookie and every write silently looks unauthenticated.
func TestCORSEchoesOriginAndAllowsCredentials(t *testing.T) {
	f := &fakeRun{stdout: map[string]string{"status": `{"BackendState":"Running"}`, "prefs": `{}`}}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/up", nil)
	req.Header.Set("Origin", "http://192.168.1.42")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://192.168.1.42" {
		t.Errorf("Allow-Origin = %q, want the caller's origin echoed", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true — without it the session cookie is not sent", got)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want Origin — a cache could otherwise serve one origin's headers to another", got)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight returned %d", resp.StatusCode)
	}
}

func TestWritesRejectGET(t *testing.T) {
	f := &fakeRun{}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})
	resp, err := http.Get(srv.URL + "/api/logout")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/logout returned %d, want 405 — a link or a prefetch must not log the device out", resp.StatusCode)
	}
}

func TestSettingsRefusesAnAuthKey(t *testing.T) {
	f := &fakeRun{}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})
	resp := post(t, srv, "/api/settings", "good", `{"authkey":"tskey-auth-abcdefghij"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("returned %d, want 400", resp.StatusCode)
	}
}

func TestStatusReportsAStoppedService(t *testing.T) {
	f := &fakeRun{
		stdout: map[string]string{"is-active": "inactive\n"},
		stderr: map[string]string{"status": "failed to connect"},
		fail:   map[string]error{"status": context.DeadlineExceeded},
	}
	srv, _ := testAPI(t, f, &fakeUI{validSession: "good"})
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ServiceActive {
		t.Error("reported the service as active")
	}
	if !strings.Contains(body.Error, "bd-tailscaled") {
		t.Errorf("error %q does not tell the user which unit to restart", body.Error)
	}
}
