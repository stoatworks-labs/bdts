package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRun records what was asked for and replays canned output.
type fakeRun struct {
	calls   [][]string
	stdout  map[string]string
	stderr  map[string]string
	fail    map[string]error
	started [][]string
}

func (f *fakeRun) runner() Runner {
	return func(_ context.Context, name string, args ...string) (string, string, error) {
		f.calls = append(f.calls, append([]string{name}, args...))
		key := verbOf(args)
		return f.stdout[key], f.stderr[key], f.fail[key]
	}
}

func (f *fakeRun) starter() Starter {
	return func(name string, args ...string) error {
		f.started = append(f.started, append([]string{name}, args...))
		return nil
	}
}

// verbOf picks the tailscale subcommand out of an argument list, skipping the
// --socket flag that every call carries. `debug prefs` is two words, so the
// second one is what identifies it.
func verbOf(args []string) string {
	var words []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
	}
	if len(words) == 0 {
		return ""
	}
	if words[0] == "debug" && len(words) > 1 {
		return words[1]
	}
	return words[0]
}

func client(f *fakeRun) *Client {
	return &Client{Bin: "/userdata/tailscale/tailscale", Socket: defaultSocket, Unit: defaultUnit, Run: f.runner(), Start: f.starter()}
}

func lastCall(f *fakeRun) []string { return f.calls[len(f.calls)-1] }

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// Every call must carry --socket. Without it the CLI looks for the distro
// default path, which does not exist here, and reports "is tailscaled running?"
// on a device where it plainly is.
func TestEveryCallPassesTheSocket(t *testing.T) {
	f := &fakeRun{stdout: map[string]string{"status": `{"BackendState":"Running"}`, "prefs": `{}`}}
	c := client(f)
	ctx := context.Background()

	_, _ = c.Status(ctx)
	_ = c.Down(ctx)
	_ = c.Logout(ctx)
	_, _ = c.Prefs(ctx)

	for _, call := range f.calls {
		if call[0] != c.Bin {
			continue // systemctl
		}
		if !hasArg(call, "--socket="+defaultSocket) {
			t.Errorf("call without --socket: %v", call)
		}
	}
}

// `tailscale status --json` exits non-zero when the node merely needs a login,
// while still printing perfectly good JSON. Treating that as a failure would
// make the panel unusable in exactly the state it exists to fix.
func TestStatusAcceptsNonZeroExitWithJSON(t *testing.T) {
	f := &fakeRun{
		stdout: map[string]string{"status": `{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/abc123"}`},
		fail:   map[string]error{"status": errors.New("exit status 1")},
	}
	st, err := client(f).Status(context.Background())
	if err != nil {
		t.Fatalf("status refused usable JSON: %v", err)
	}
	if !st.NeedsLogin() {
		t.Error("NeedsLogin false for BackendState NeedsLogin")
	}
	if st.AuthURL == "" {
		t.Error("login URL not carried through; the panel would have nothing to show")
	}
}

func TestStatusReportsRealFailure(t *testing.T) {
	f := &fakeRun{
		stderr: map[string]string{"status": "failed to connect to local tailscaled"},
		fail:   map[string]error{"status": errors.New("exit status 1")},
	}
	if _, err := client(f).Status(context.Background()); err == nil {
		t.Fatal("expected an error when nothing parseable came back")
	}
}

func TestStatusParsesSelfAndExitNodes(t *testing.T) {
	f := &fakeRun{stdout: map[string]string{"status": `{
		"Version":"1.102.2","BackendState":"Running","TUN":false,
		"Self":{"HostName":"birddog-play-42","DNSName":"birddog-play-42.tail1234.ts.net.","TailscaleIPs":["100.88.59.79"],"Online":true},
		"CurrentTailnet":{"Name":"example.com"},
		"Peer":{"a":{"HostName":"gw","DNSName":"gw.tail1234.ts.net.","ExitNodeOption":true,"Online":true},
		        "b":{"HostName":"laptop","ExitNodeOption":false}}}`}}
	st, err := client(f).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Self.HostName != "birddog-play-42" || len(st.Self.TailscaleIPs) != 1 {
		t.Errorf("Self parsed wrong: %+v", st.Self)
	}
	opts := st.ExitNodeOptions()
	if len(opts) != 1 || opts[0].HostName != "gw" {
		t.Errorf("exit node options wrong: %+v", opts)
	}
	// TUN is false on the PLAY, so the panel must not offer to advertise routes.
	if st.ExitNodeCapable() {
		t.Error("reported as exit-node capable with no TUN interface")
	}
}

// Interactive login must not block: `tailscale up` without a key waits for a
// human, and an HTTP handler cannot hold that open.
func TestInteractiveUpIsStartedNotWaitedOn(t *testing.T) {
	f := &fakeRun{}
	c := client(f)
	if err := c.Up(context.Background(), UpOptions{Hostname: "play-42"}); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 1 {
		t.Fatalf("expected the command to be started detached, got %d starts and %d waited calls", len(f.started), len(f.calls))
	}
	if !hasArg(f.started[0], "--hostname=play-42") {
		t.Errorf("hostname not passed: %v", f.started[0])
	}
}

func TestAuthKeyUpIsWaitedOn(t *testing.T) {
	f := &fakeRun{}
	c := client(f)
	if err := c.Up(context.Background(), UpOptions{AuthKey: "tskey-auth-abcdef123456"}); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 0 {
		t.Error("an auth-key connect was detached; its result would be lost")
	}
	if !hasArg(lastCall(f), "--authkey=tskey-auth-abcdef123456") {
		t.Errorf("auth key not passed: %v", lastCall(f))
	}
}

func TestUpSurfacesTailscaleError(t *testing.T) {
	f := &fakeRun{
		stderr: map[string]string{"up": "invalid key: unauthorized"},
		fail:   map[string]error{"up": errors.New("exit status 1")},
	}
	err := client(f).Up(context.Background(), UpOptions{AuthKey: "tskey-auth-abcdef123456"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("tailscale's own message was lost: %v", err)
	}
}

func TestValidateRejectsShellUnfriendlyInput(t *testing.T) {
	bad := []UpOptions{
		{AuthKey: "not-a-key"},
		{AuthKey: "tskey-auth-abc; rm -rf /"},
		{Hostname: "-leading-hyphen"},
		{Hostname: "has spaces"},
		{ExitNode: ptr("two words")},
	}
	for _, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("accepted bad input: %+v", o)
		}
	}
	good := []UpOptions{
		{},
		{AuthKey: "tskey-auth-kM7fH2CNTRL-abcdefghij"},
		{Hostname: "birddog-play-42"},
		{ExitNode: ptr("")},
		{ExitNode: ptr("100.64.0.1")},
	}
	for _, o := range good {
		if err := o.Validate(); err != nil {
			t.Errorf("rejected good input %+v: %v", o, err)
		}
	}
}

// Settings changes go through `tailscale set`. Using `up` would re-run the
// login flow and reset everything not named on the command line.
func TestSetUsesSetNotUp(t *testing.T) {
	f := &fakeRun{}
	yes := true
	if err := client(f).Set(context.Background(), UpOptions{Hostname: "play-42", AcceptDNS: &yes}); err != nil {
		t.Fatal(err)
	}
	call := lastCall(f)
	if verbOf(call[1:]) != "set" {
		t.Fatalf("expected `set`, got: %v", call)
	}
	if !hasArg(call, "--accept-dns=true") || !hasArg(call, "--hostname=play-42") {
		t.Errorf("flags missing: %v", call)
	}
}

// nil means "the form did not mention it"; "" means "clear it". Collapsing the
// two would silently drop the exit node every time any other setting is saved.
func TestSetDistinguishesUnsetFromCleared(t *testing.T) {
	f := &fakeRun{}
	c := client(f)
	yes := true

	if err := c.Set(context.Background(), UpOptions{AcceptDNS: &yes}); err != nil {
		t.Fatal(err)
	}
	for _, a := range lastCall(f) {
		if strings.HasPrefix(a, "--exit-node") {
			t.Error("exit node was touched by a request that did not mention it")
		}
	}

	if err := c.Set(context.Background(), UpOptions{ExitNode: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if !hasArg(lastCall(f), "--exit-node=") {
		t.Errorf("clearing the exit node did not reach the CLI: %v", lastCall(f))
	}
}

func TestSetWithNothingToDoDoesNotRun(t *testing.T) {
	f := &fakeRun{}
	if err := client(f).Set(context.Background(), UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v for an empty change; bare `tailscale set` is an error", f.calls)
	}
}

func TestServiceActive(t *testing.T) {
	f := &fakeRun{stdout: map[string]string{"is-active": "active\n"}}
	if !client(f).ServiceActive(context.Background()) {
		t.Error("active unit reported as inactive")
	}
	f = &fakeRun{stdout: map[string]string{"is-active": "failed\n"}}
	if client(f).ServiceActive(context.Background()) {
		t.Error("failed unit reported as active")
	}
}

func TestPrefsParse(t *testing.T) {
	f := &fakeRun{stdout: map[string]string{"prefs": `{"RouteAll":true,"CorpDNS":false,"RunSSH":true,"Hostname":"play-42"}`}}
	p, err := client(f).Prefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !p.RouteAll || p.CorpDNS || !p.RunSSH || p.Hostname != "play-42" {
		t.Errorf("prefs parsed wrong: %+v", p)
	}
}

func ptr[T any](v T) *T { return &v }
