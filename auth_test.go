package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeUI stands in for birddog-web-ui: it serves the System page to a request
// carrying a known session cookie and the login page to anything else.
type fakeUI struct {
	validSession string
	unprotected  bool // no password set: everyone gets the System page
	redirect     bool // send a 302 to /login instead of rendering the login page
	garbage      bool // serve a page that is neither
	hits         int
}

func (f *fakeUI) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits++
		if f.garbage {
			_, _ = w.Write([]byte("<html>something else entirely</html>"))
			return
		}
		c, err := r.Cookie(SessionCookie)
		authed := f.unprotected || (err == nil && c.Value == f.validSession)
		if authed {
			_, _ = w.Write([]byte("<html>" + settingsMarker + "</html>"))
			return
		}
		if f.redirect {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`<html><div class="` + loginMarker + `"></div></html>`))
	}))
}

func gateFor(t *testing.T, f *fakeUI) *Gate {
	t.Helper()
	srv := f.server()
	t.Cleanup(srv.Close)
	return NewGate(srv.URL)
}

func request(session string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/up", nil)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	return r
}

func TestGateAcceptsValidSession(t *testing.T) {
	g := gateFor(t, &fakeUI{validSession: "good"})
	st, err := g.Check(context.Background(), request("good"))
	if err != nil {
		t.Fatalf("valid session refused: %v", err)
	}
	if !st.Authenticated || !st.DeviceProtected {
		t.Errorf("unexpected state: %+v", st)
	}
}

func TestGateRejectsMissingAndWrongSessions(t *testing.T) {
	for name, session := range map[string]string{"none": "", "wrong": "nope"} {
		t.Run(name, func(t *testing.T) {
			g := gateFor(t, &fakeUI{validSession: "good"})
			if _, err := g.Check(context.Background(), request(session)); err == nil {
				t.Fatal("expected refusal, got none")
			}
		})
	}
}

// The check that stops this being security theatre: with no password set on the
// device, an unauthenticated probe also succeeds, so a naive implementation
// would wave everyone through while appearing to verify a session.
func TestGateRefusesWhenDeviceHasNoPassword(t *testing.T) {
	g := gateFor(t, &fakeUI{validSession: "good", unprotected: true})
	st, err := g.Check(context.Background(), request("good"))
	if err == nil {
		t.Fatal("gate passed on a device with no password — writes would be open to anyone on the network")
	}
	if st.DeviceProtected {
		t.Error("device reported as protected when it is not")
	}
}

func TestAllowUnprotectedOverride(t *testing.T) {
	g := gateFor(t, &fakeUI{validSession: "good", unprotected: true})
	g.AllowUnprotected = true
	st, err := g.Check(context.Background(), request(""))
	if err != nil {
		t.Fatalf("override did not take effect: %v", err)
	}
	if st.Required {
		t.Error("state should report that authentication is not being required")
	}
}

func TestGateTreatsRedirectAsUnauthenticated(t *testing.T) {
	// Without CheckRedirect the client would follow to /login and get a 200,
	// turning the clearest possible refusal into an apparent success.
	g := gateFor(t, &fakeUI{validSession: "good", redirect: true})
	if _, err := g.Check(context.Background(), request("stale")); err == nil {
		t.Fatal("a redirect to the login page was accepted as a valid session")
	}
}

// Unfamiliar markup must fail closed. Failing open here would mean a firmware
// update silently disabling the gate.
func TestGateFailsClosedOnUnfamiliarPage(t *testing.T) {
	g := gateFor(t, &fakeUI{validSession: "good", garbage: true})
	if _, err := g.Check(context.Background(), request("good")); err == nil {
		t.Fatal("gate passed on a page it did not recognise")
	}
}

func TestGateFailsClosedWhenUIUnreachable(t *testing.T) {
	g := NewGate("http://127.0.0.1:1") // nothing listens here
	if _, err := g.Check(context.Background(), request("good")); err == nil {
		t.Fatal("gate passed while unable to check the session")
	}
}

// The unprotected probe is cached, so a burst of requests does not multiply
// load on the stock UI.
func TestUnprotectedProbeIsCached(t *testing.T) {
	f := &fakeUI{validSession: "good"}
	g := gateFor(t, f)
	for range 4 {
		if _, err := g.Check(context.Background(), request("good")); err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
	// One probe without a cookie, plus one session check per request.
	if f.hits != 5 {
		t.Errorf("made %d requests to the web UI, want 5 (1 cached probe + 4 session checks)", f.hits)
	}
}

func TestDescribeDoesNotError(t *testing.T) {
	g := gateFor(t, &fakeUI{validSession: "good"})
	st := g.Describe(context.Background(), request(""))
	if st.Authenticated {
		t.Error("Describe reported an unauthenticated request as authenticated")
	}
	if st.Reason == "" {
		t.Error("Describe gave the panel no reason to show the user")
	}
}
