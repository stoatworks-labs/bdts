package main

// Driving the Tailscale CLI on the PLAY.
//
// tailscaled runs from /userdata/tailscale under bd-tailscaled.service with
// --socket=/run/bd-tailscaled.sock and --tun=userspace-networking, because the
// PLAY's 4.4 kernel has no TUN driver and no loadable modules at all. Every
// call here therefore has to pass --socket; without it the CLI looks for the
// distro default at /var/run/tailscale/tailscaled.sock and fails with a
// misleading "is tailscaled running?".
//
// Two consequences of userspace-networking shape the API surface:
//
//   * this node can never advertise routes or be an exit node — both need a
//     kernel interface to forward packets into. The panel says so rather than
//     offering a switch that would fail; see ExitNodeCapable.
//   * inbound to the device's own services still works (verified on hardware:
//     ssh, :80 and the :8080 API are all reachable over the tailnet), because
//     the netstack proxies inbound TCP to localhost.
//
// Interactive login cannot be done by waiting on `tailscale up`: it blocks
// until the user finishes in a browser, which is minutes, and an HTTP handler
// cannot hold that. So Up starts it detached and the login URL is read back out
// of `tailscale status --json`, which carries AuthURL for as long as the
// backend is in NeedsLogin.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	defaultTailscaleBin = "/userdata/tailscale/tailscale"
	defaultSocket       = "/run/bd-tailscaled.sock"
	defaultUnit         = "bd-tailscaled"
)

// Runner runs a command to completion and returns what it wrote. Injected so
// tests never shell out.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// Starter launches a command without waiting for it. Used only for interactive
// `tailscale up`, which does not return until the user has authenticated.
type Starter func(name string, args ...string) error

// execRunner is the real Runner.
func execRunner(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// execStarter launches a detached command, sending its output to logPath so a
// failed login is diagnosable after the fact. The Wait goroutine is what stops
// the child becoming a zombie — bdts is pid 1 of nothing, but it is long-lived,
// and an unreaped child per login attempt adds up.
func execStarter(logPath string) Starter {
	return func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
			defer f.Close()
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
}

// Client talks to tailscaled through the CLI.
type Client struct {
	Bin    string
	Socket string
	Unit   string // systemd unit, for is-active and restart
	Run    Runner
	Start  Starter
}

func NewClient() *Client {
	return &Client{
		Bin:    defaultTailscaleBin,
		Socket: defaultSocket,
		Unit:   defaultUnit,
		Run:    execRunner,
		Start:  execStarter("/userdata/tailscale/bdts-login.log"),
	}
}

// Peer is the subset of tailscale's ipnstate.PeerStatus this needs. Decoding a
// subset on purpose: the full struct changes between Tailscale releases and
// nothing here should break when it does.
type Peer struct {
	ID             string   `json:"ID"`
	HostName       string   `json:"HostName"`
	DNSName        string   `json:"DNSName"`
	OS             string   `json:"OS"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	Online         bool     `json:"Online"`
	ExitNode       bool     `json:"ExitNode"`       // currently in use as our exit node
	ExitNodeOption bool     `json:"ExitNodeOption"` // offers itself as one
}

// Tailnet describes the network the node has joined.
type Tailnet struct {
	Name           string `json:"Name"`
	MagicDNSSuffix string `json:"MagicDNSSuffix"`
}

// Status is the subset of `tailscale status --json` this needs.
type Status struct {
	Version        string           `json:"Version"`
	BackendState   string           `json:"BackendState"` // NoState, NeedsLogin, Starting, Running, Stopped
	AuthURL        string           `json:"AuthURL"`
	TUN            bool             `json:"TUN"`
	MagicDNSSuffix string           `json:"MagicDNSSuffix"`
	Self           *Peer            `json:"Self"`
	CurrentTailnet *Tailnet         `json:"CurrentTailnet"`
	Peer           map[string]*Peer `json:"Peer"`
}

// NeedsLogin reports whether the node is installed but not authenticated —
// the state every PLAY is in immediately after the firmware patch installs
// Tailscale, and the one this whole panel exists to get out of.
func (s *Status) NeedsLogin() bool {
	return s != nil && (s.BackendState == "NeedsLogin" || s.BackendState == "NoState")
}

// ExitNodeCapable reports whether this node could advertise itself as an exit
// node or a subnet router. On the PLAY it is always false: both need a kernel
// TUN interface to forward packets into, and the kernel has none.
func (s *Status) ExitNodeCapable() bool { return s != nil && s.TUN }

// ExitNodeOptions lists peers offering themselves as exit nodes, for the
// panel's dropdown.
func (s *Status) ExitNodeOptions() []*Peer {
	var out []*Peer
	if s == nil {
		return out
	}
	for _, p := range s.Peer {
		if p != nil && p.ExitNodeOption {
			out = append(out, p)
		}
	}
	return out
}

// Status runs `tailscale status --json`.
//
// A non-zero exit is not automatically an error: the CLI exits 1 when the
// backend merely needs a login, and still prints valid JSON saying so. Only
// treat it as failure when nothing parseable came back.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	stdout, stderr, err := c.Run(ctx, c.Bin, "--socket="+c.Socket, "status", "--json")
	if strings.TrimSpace(stdout) == "" {
		if err != nil {
			return nil, fmt.Errorf("tailscale status: %w: %s", err, strings.TrimSpace(stderr))
		}
		return nil, errors.New("tailscale status: empty response")
	}
	var s Status
	if jerr := json.Unmarshal([]byte(stdout), &s); jerr != nil {
		return nil, fmt.Errorf("tailscale status: parsing JSON: %w", jerr)
	}
	return &s, nil
}

// Prefs is the subset of tailscaled's saved preferences the panel shows as
// toggle state. `tailscale status --json` does not carry these — it describes
// the network, not what was asked for — so they come from `tailscale debug
// prefs`, which prints them as JSON.
//
// That is a debug command, and a future Tailscale release could rename or drop
// it. Everything here therefore treats missing prefs as "unknown" rather than
// as an error: the panel shows the toggles blank with a note, and connecting
// and disconnecting still work. Do not make any control path depend on this.
type Prefs struct {
	RouteAll    bool   `json:"RouteAll"` // --accept-routes
	CorpDNS     bool   `json:"CorpDNS"`  // --accept-dns
	RunSSH      bool   `json:"RunSSH"`   // --ssh
	WantRunning bool   `json:"WantRunning"`
	LoggedOut   bool   `json:"LoggedOut"`
	Hostname    string `json:"Hostname"`
	ExitNodeID  string `json:"ExitNodeID"`
	ExitNodeIP  string `json:"ExitNodeIP"`
}

// Prefs reads the current preferences. A failure is not fatal — see the type.
func (c *Client) Prefs(ctx context.Context) (*Prefs, error) {
	stdout, stderr, err := c.Run(ctx, c.Bin, "--socket="+c.Socket, "debug", "prefs")
	if strings.TrimSpace(stdout) == "" {
		if err != nil {
			return nil, fmt.Errorf("tailscale debug prefs: %w: %s", err, strings.TrimSpace(stderr))
		}
		return nil, errors.New("tailscale debug prefs: empty response")
	}
	var p Prefs
	if jerr := json.Unmarshal([]byte(stdout), &p); jerr != nil {
		return nil, fmt.Errorf("tailscale debug prefs: parsing JSON: %w", jerr)
	}
	return &p, nil
}

// UpOptions are the settings the panel can send with a connect request.
//
// The pointer fields distinguish "not mentioned" from "set to the zero value",
// which matters for Set: a form that omits a field must leave it alone, and a
// form that clears the exit node must actually clear it.
type UpOptions struct {
	AuthKey      string  `json:"authkey"`
	Hostname     string  `json:"hostname"`
	AcceptDNS    *bool   `json:"accept_dns"`
	AcceptRoutes *bool   `json:"accept_routes"`
	SSH          *bool   `json:"ssh"`
	ExitNode     *string `json:"exit_node"` // nil leaves it; "" clears it
}

// authKeyPattern is deliberately loose about the suffix: Tailscale has shipped
// tskey-auth-, tskey-client- and plain tskey- over time, and rejecting a valid
// key here would look like a broken panel.
var authKeyPattern = regexp.MustCompile(`^tskey-[A-Za-z0-9\-_]{10,}$`)

// hostnamePattern matches what Tailscale will accept as a machine name. Checked
// here so a bad value is a clear error in the panel rather than a CLI failure
// buried in a log nobody reads.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// Validate rejects input that would otherwise reach the command line.
func (o UpOptions) Validate() error {
	if o.AuthKey != "" && !authKeyPattern.MatchString(o.AuthKey) {
		return errors.New("that does not look like a Tailscale auth key (they start with tskey-)")
	}
	if o.Hostname != "" && !hostnamePattern.MatchString(o.Hostname) {
		return errors.New("hostname must be letters, digits and hyphens, and cannot start or end with a hyphen")
	}
	if o.ExitNode != nil && strings.ContainsAny(*o.ExitNode, " \t\n") {
		return errors.New("exit node must be a node name or a tailnet IP")
	}
	return nil
}

// args builds the `tailscale up` command line.
func (o UpOptions) args(socket string) []string {
	a := []string{"--socket=" + socket, "up"}
	if o.AuthKey != "" {
		a = append(a, "--authkey="+o.AuthKey)
	}
	if o.Hostname != "" {
		a = append(a, "--hostname="+o.Hostname)
	}
	if o.AcceptDNS != nil {
		a = append(a, fmt.Sprintf("--accept-dns=%t", *o.AcceptDNS))
	}
	if o.AcceptRoutes != nil {
		a = append(a, fmt.Sprintf("--accept-routes=%t", *o.AcceptRoutes))
	}
	if o.SSH != nil {
		a = append(a, fmt.Sprintf("--ssh=%t", *o.SSH))
	}
	if o.ExitNode != nil && *o.ExitNode != "" {
		a = append(a, "--exit-node="+*o.ExitNode)
	}
	return a
}

// Up authenticates the node.
//
// With an auth key it runs to completion and reports the result directly.
// Without one it starts the command detached and returns immediately: the login
// URL appears in `tailscale status --json` a moment later, and the panel polls
// for it. Blocking here instead would hold an HTTP handler open for however
// long someone takes to finish in their browser.
func (c *Client) Up(ctx context.Context, o UpOptions) error {
	if err := o.Validate(); err != nil {
		return err
	}
	args := o.args(c.Socket)
	if o.AuthKey == "" {
		return c.Start(c.Bin, args...)
	}
	stdout, stderr, err := c.Run(ctx, c.Bin, args...)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// Down disconnects but stays authenticated, so reconnecting needs no key.
func (c *Client) Down(ctx context.Context) error {
	return c.simple(ctx, "down")
}

// Logout disconnects and forgets the node key. Reconnecting needs a fresh login.
func (c *Client) Logout(ctx context.Context) error {
	return c.simple(ctx, "logout")
}

func (c *Client) simple(ctx context.Context, verb string) error {
	stdout, stderr, err := c.Run(ctx, c.Bin, "--socket="+c.Socket, verb)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("tailscale %s: %s", verb, msg)
	}
	return nil
}

// Set changes preferences on an already-authenticated node, via `tailscale set`
// rather than `up` — `up` on a running node re-runs the whole login flow and
// resets anything not named on the command line.
func (c *Client) Set(ctx context.Context, o UpOptions) error {
	if err := o.Validate(); err != nil {
		return err
	}
	args := []string{"--socket=" + c.Socket, "set"}
	if o.Hostname != "" {
		args = append(args, "--hostname="+o.Hostname)
	}
	if o.AcceptDNS != nil {
		args = append(args, fmt.Sprintf("--accept-dns=%t", *o.AcceptDNS))
	}
	if o.AcceptRoutes != nil {
		args = append(args, fmt.Sprintf("--accept-routes=%t", *o.AcceptRoutes))
	}
	if o.SSH != nil {
		args = append(args, fmt.Sprintf("--ssh=%t", *o.SSH))
	}
	// Unlike the others an empty exit node is meaningful: it clears the setting.
	// Hence the pointer — nil means the caller did not mention it.
	if o.ExitNode != nil {
		args = append(args, "--exit-node="+*o.ExitNode)
	}
	if len(args) == 2 {
		return nil // nothing was asked for; `tailscale set` alone is an error
	}
	stdout, stderr, err := c.Run(ctx, c.Bin, args...)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// ServiceActive reports whether bd-tailscaled.service is running. Distinct from
// BackendState: the daemon can be down entirely, in which case every other call
// here fails with a socket error and the panel should say so plainly instead of
// showing "not authenticated".
func (c *Client) ServiceActive(ctx context.Context) bool {
	stdout, _, _ := c.Run(ctx, "systemctl", "is-active", c.Unit)
	return strings.TrimSpace(stdout) == "active"
}

// Installed reports whether the Tailscale binaries are actually on the box.
func (c *Client) Installed() bool {
	_, err := os.Stat(c.Bin)
	return err == nil
}

// StatusTimeout bounds a CLI call. The socket is local and these are fast; a
// hang means tailscaled is wedged, and the panel should learn that quickly.
const StatusTimeout = 10 * time.Second
