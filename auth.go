package main

// Gating writes behind birdUI's own login.
//
// The panel runs on the device's web UI origin but talks to this daemon on a
// different port, exactly as bdcam's UVC tab talks to :8090. bdcam documents
// its API as unauthenticated on the grounds that the stock :8080 API already is
// — true, and fine for a camera setting. It is not fine here: anyone who can
// reach this port could otherwise join the box to *their* tailnet and keep
// remote access to it indefinitely, or log it out of yours. That is a different
// class of thing from changing a frame rate.
//
// So writes are gated on the caller holding a valid birdUI session. This needs
// no new password prompt and no second credential store, because cookies are
// scoped by host and NOT by port: the BirdDogSession cookie the browser already
// holds for the device is sent to this port too, as long as the panel fetches
// with credentials and we answer with a concrete Access-Control-Allow-Origin
// plus Allow-Credentials. A wildcard origin is not allowed to carry credentials
// and would silently strip the cookie, so cors() echoes the origin instead.
//
// Validating the cookie means asking the thing that issued it. We re-request a
// known-authenticated page from the web UI on 127.0.0.1 with the caller's
// cookie and see whether we get the page or the login screen.
//
// The trap in that: it only proves anything if the page is actually protected.
// If the device has no password set, the unauthenticated request succeeds too
// and the check would pass for everyone while looking rigorous. So every check
// is differential — probe without the cookie as well, and if that also comes
// back authenticated, report the device as unprotected and refuse the write
// rather than pretending to have gated it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SessionCookie is the name birddog-web-ui gives its session cookie. Recovered
// from the binary's string table alongside BirdDogNetwork.
const SessionCookie = "BirdDogSession"

// probePath is the page used to test a session. It is the System page itself:
// whatever gates that page gates the panel we are adding to it, which is the
// property we want.
const probePath = "/settings"

// settingsMarker appears on the rendered System page and not on the login page.
// Chosen from the stock markup and deliberately NOT one of our own injected
// strings, so the check still means something on an unpatched device.
const settingsMarker = "Reset System Default"

// loginMarker appears on the login page.
const loginMarker = "div_login_box"

// Gate decides whether a request may change Tailscale state.
type Gate struct {
	// UIBase is where the stock web UI answers, from the device's own loopback.
	UIBase string

	// AllowUnprotected turns the gate off. It exists because a device with no
	// birdUI password cannot be gated at all, and someone running one on a
	// trusted lab network still needs the panel to work. Off by default, and
	// the panel says loudly when it is on.
	AllowUnprotected bool

	HTTP *http.Client

	// Probing the unprotected case on every write would double the request
	// count for something that changes only when someone sets a password.
	mu             sync.Mutex
	unprotected    bool
	unprotectedAt  time.Time
	unprotectedTTL time.Duration
}

func NewGate(uiBase string) *Gate {
	return &Gate{
		UIBase: strings.TrimRight(uiBase, "/"),
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
			// Following a redirect to /login would turn the clearest possible
			// "not authenticated" signal into a 200.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		unprotectedTTL: 30 * time.Second,
	}
}

// AuthState is what the panel is told about its own access.
type AuthState struct {
	// Required is false only when gating has been switched off.
	Required bool `json:"required"`
	// DeviceProtected reports whether birdUI asks for a password at all.
	DeviceProtected bool `json:"device_protected"`
	// Authenticated reports whether this particular request carried a session.
	Authenticated bool `json:"authenticated"`
	// Reason explains a denial in words the panel can show as-is.
	Reason string `json:"reason,omitempty"`
}

var (
	errNoSession      = errors.New("log in to birdUI first — this panel uses the same session as the rest of the web UI")
	errBadSession     = errors.New("your birdUI session has expired; reload the page and log in again")
	errUnprotected    = errors.New("this device has no birdUI password set, so Tailscale changes cannot be authenticated. Set a password in Password Settings above, or start bdts with --allow-unprotected")
	errUIUnreachable  = errors.New("cannot reach the birdUI login service to check your session")
	errUIUnexpectedly = errors.New("the birdUI page used to check sessions did not look like either the System page or the login page — refusing to guess")
)

// Check reports whether r may perform a write.
func (g *Gate) Check(ctx context.Context, r *http.Request) (AuthState, error) {
	if g.AllowUnprotected {
		return AuthState{Required: false, DeviceProtected: false, Authenticated: true}, nil
	}

	protected, err := g.deviceProtected(ctx)
	if err != nil {
		return AuthState{Required: true, Reason: err.Error()}, err
	}
	if !protected {
		return AuthState{Required: true, DeviceProtected: false, Reason: errUnprotected.Error()}, errUnprotected
	}

	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return AuthState{Required: true, DeviceProtected: true, Reason: errNoSession.Error()}, errNoSession
	}

	ok, err := g.sessionValid(ctx, cookie.Value)
	if err != nil {
		return AuthState{Required: true, DeviceProtected: true, Reason: err.Error()}, err
	}
	if !ok {
		return AuthState{Required: true, DeviceProtected: true, Reason: errBadSession.Error()}, errBadSession
	}
	return AuthState{Required: true, DeviceProtected: true, Authenticated: true}, nil
}

// Describe reports the gate's view of a request without failing the request —
// used by /api/status so the panel can grey out the controls before anyone
// clicks them, rather than only finding out on submit.
func (g *Gate) Describe(ctx context.Context, r *http.Request) AuthState {
	st, _ := g.Check(ctx, r)
	return st
}

// deviceProtected reports whether birdUI refuses the probe page without a
// session. Cached briefly: it changes only when someone sets a password.
func (g *Gate) deviceProtected(ctx context.Context) (bool, error) {
	g.mu.Lock()
	if time.Since(g.unprotectedAt) < g.unprotectedTTL {
		res := !g.unprotected
		g.mu.Unlock()
		return res, nil
	}
	g.mu.Unlock()

	authed, err := g.probe(ctx, "")
	if err != nil {
		return false, err
	}

	g.mu.Lock()
	g.unprotected = authed
	g.unprotectedAt = time.Now()
	g.mu.Unlock()
	return !authed, nil
}

func (g *Gate) sessionValid(ctx context.Context, value string) (bool, error) {
	return g.probe(ctx, value)
}

// probe fetches the System page with the given session value (empty for none)
// and reports whether the response is the page rather than the login screen.
func (g *Gate) probe(ctx context.Context, session string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.UIBase+probePath, nil)
	if err != nil {
		return false, err
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errUIUnreachable, err)
	}
	defer resp.Body.Close()

	// A redirect is birdUI sending us to the login page. Unambiguous.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return false, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w (HTTP %d)", errUIUnreachable, resp.StatusCode)
	}

	body, err := readLimited(resp.Body, 512*1024)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errUIUnreachable, err)
	}
	switch {
	case strings.Contains(body, settingsMarker):
		return true, nil
	case strings.Contains(body, loginMarker):
		return false, nil
	default:
		// Neither marker: a firmware whose System page differs from the one
		// this was written against. Failing closed is the only safe answer —
		// guessing "authenticated" would open the gate on every request.
		return false, errUIUnexpectedly
	}
}
