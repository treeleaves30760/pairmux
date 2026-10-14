package muxd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/rundir"
	"github.com/treeleaves30760/pairmux/internal/version"
)

// ErrNoServer reports that no built-in server is running for the endpoint.
// Its text contains "no server running", which is the phrase the registry
// already treats as "zero terminals" for tmux.
var ErrNoServer = errors.New("no server running")

// startWait bounds how long EnsureSession waits for a freshly spawned server
// to accept connections.
const startWait = 10 * time.Second

// callTimeout bounds one request. Every operation is local and quick; kill
// waits for a terminal's output to drain, which is bounded by killWait.
const callTimeout = 30 * time.Second

// Client talks to the built-in server for one endpoint. It implements
// mux.Mux, so the CLI drives it exactly like the tmux client.
type Client struct {
	Socket string
}

var _ mux.Mux = (*Client)(nil)

// New returns a Client for socket; "" selects core.DefaultSocket.
func New(socket string) *Client {
	if socket == "" {
		socket = core.DefaultSocket
	}
	return &Client{Socket: socket}
}

// Backend reports mux.BackendBuiltin.
func (c *Client) Backend() string { return mux.BackendBuiltin }

// SocketName is the endpoint name.
func (c *Client) SocketName() string { return c.Socket }

// AtomicJournal is true: the server opens the journal before it starts the
// program, so no output can escape it and no start gate is needed.
func (c *Client) AtomicJournal() bool { return true }

// Running reports whether a server is accepting connections for socket.
func Running(socket string) bool {
	if !mux.ValidSocketName(socket) {
		return false
	}
	_, err := New(socket).call(request{Op: "ping"})
	return err == nil
}

func (c *Client) dial() (net.Conn, error) {
	if !mux.ValidSocketName(c.Socket) {
		return nil, fmt.Errorf("built-in multiplexer: invalid socket name %q", c.Socket)
	}
	path, err := rundir.SocketPath(c.Socket)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		if isAbsent(path, err) {
			return nil, fmt.Errorf("built-in multiplexer: %w on %s", ErrNoServer, path)
		}
		return nil, fmt.Errorf("built-in multiplexer: error connecting to %s: %w", path, err)
	}
	return conn, nil
}

// isAbsent separates "nobody is listening" (no socket file, or a stale one)
// from failures that need the user's attention, such as permissions. The
// socket file's absence is checked directly because Windows reports dialing a
// missing AF_UNIX path as WSAENETDOWN ("a socket operation encountered a dead
// network"), not as a missing file.
func isAbsent(path string, err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if _, serr := os.Lstat(path); errors.Is(serr, os.ErrNotExist) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && (errno == wsaECONNREFUSED || errno == wsaENETDOWN) {
		return true // a stale socket file left by a server that died (Windows)
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such file") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "actively refused") ||
		strings.Contains(s, "cannot find the file")
}

// Winsock error numbers, spelled out so this file builds on every platform.
const (
	wsaENETDOWN     = syscall.Errno(10050)
	wsaECONNREFUSED = syscall.Errno(10061)
)

func (c *Client) call(req request) (*response, error) {
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(callTimeout))
	req.Proto = protoVersion
	if err := writeMsg(conn, req); err != nil {
		return nil, fmt.Errorf("built-in multiplexer: send %s: %w", req.Op, err)
	}
	var resp response
	if err := readMsg(conn, &resp); err != nil {
		return nil, fmt.Errorf("built-in multiplexer: %s: no reply: %w", req.Op, err)
	}
	if !resp.OK {
		msg := resp.Error
		if resp.Proto != protoVersion {
			msg += fmt.Sprintf(" (server protocol %d, client %d: a server from another pairmux version is running; `pairmux kill --all` and retry once its terminals are no longer needed)", resp.Proto, protoVersion)
		}
		return nil, fmt.Errorf("built-in multiplexer: %s: %s", req.Op, msg)
	}
	return &resp, nil
}

// Version reports the running server's pairmux version, or this binary's when
// no server is running.
func (c *Client) Version() (string, error) {
	if resp, err := c.call(request{Op: "ping"}); err == nil {
		return resp.Version, nil
	}
	return version.Version, nil
}

// ServerPID returns the running server's process id.
func (c *Client) ServerPID() (int, error) {
	resp, err := c.call(request{Op: "ping"})
	if err != nil {
		return 0, err
	}
	return resp.Pid, nil
}

// EnsureSession starts the endpoint's server unless one is already running.
func (c *Client) EnsureSession() error {
	if _, err := c.call(request{Op: "ping"}); err == nil {
		return nil
	} else if !errors.Is(err, ErrNoServer) {
		return err
	}
	if err := spawnServer(c.Socket); err != nil {
		return fmt.Errorf("built-in multiplexer: start server: %w", err)
	}
	deadline := time.Now().Add(startWait)
	for {
		_, err := c.call(request{Op: "ping"})
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			hint := ""
			if base, berr := rundir.FileBase(c.Socket); berr == nil {
				if d, derr := rundir.Dir(); derr == nil {
					hint = fmt.Sprintf("; see %s", d+string(os.PathSeparator)+base+".log")
				}
			}
			return fmt.Errorf("built-in multiplexer: server did not start: %w%s", err, hint)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// paneEnv is the environment a new terminal starts with: this command's own
// (so a terminal sees the PATH, virtualenv and credentials helpers of the agent
// that created it, not those of whichever command first started the server),
// minus variables that would misdescribe where it runs, plus the request's.
func paneEnv(extra map[string]string) []string {
	drop := map[string]bool{"TMUX": true, "TMUX_PANE": true, BuiltinPaneEnv: true}
	merged := map[string]string{}
	var order []string
	add := func(k, v string) {
		key := k
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(k)
		}
		if _, seen := merged[key]; !seen {
			order = append(order, key)
		}
		merged[key] = k + "=" + v
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || drop[k] {
			continue
		}
		add(k, v)
	}
	if runtime.GOOS != "windows" {
		// The screen emulator speaks xterm; programs should not assume the
		// capabilities of whatever terminal the creating command ran in.
		add("TERM", "xterm-256color")
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k, extra[k])
	}
	env := make([]string, 0, len(order))
	for _, k := range order {
		env = append(env, merged[k])
	}
	return env
}

// NewWindow starts a terminal and returns its pane id.
func (c *Client) NewWindow(r mux.NewWindowReq) (string, error) {
	argv := r.Argv
	if len(argv) == 0 {
		argv = []string{defaultShell()}
	}
	req := request{Op: "new-window", Name: r.Name, Dir: r.Dir, Env: paneEnv(r.Env), Argv: argv, Journal: r.Journal}
	resp, err := c.call(req)
	if errors.Is(err, ErrNoServer) {
		if err := c.EnsureSession(); err != nil {
			return "", err
		}
		resp, err = c.call(req)
	}
	if err != nil {
		return "", err
	}
	return resp.Pane, nil
}

func defaultShell() string {
	if runtime.GOOS == "windows" {
		if c := os.Getenv("COMSPEC"); c != "" {
			return c
		}
		return "cmd.exe"
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

// PipePaneAppend starts appending the pane's output to file, unless it is
// already being journalled.
func (c *Client) PipePaneAppend(paneID, file string) error {
	_, err := c.call(request{Op: "pipe", Pane: paneID, Journal: file})
	return err
}

// SendLiteral types text verbatim.
func (c *Client) SendLiteral(paneID, text string) error {
	_, err := c.call(request{Op: "send-text", Pane: paneID, Text: text})
	return err
}

// SendKeys sends named keys (the tmux names pairmux's allowlist admits).
func (c *Client) SendKeys(paneID string, keys ...string) error {
	_, err := c.call(request{Op: "send-keys", Pane: paneID, Keys: keys})
	return err
}

// CapturePane returns the visible screen, plus historyLines of scrollback when
// positive, with tmux capture-pane -p's line semantics.
func (c *Client) CapturePane(paneID string, historyLines int) (string, error) {
	resp, err := c.call(request{Op: "capture", Pane: paneID, History: historyLines})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// SetPaneOption sets a pane option such as @pairmux_name.
func (c *Client) SetPaneOption(paneID, opt, val string) error {
	_, err := c.call(request{Op: "set-option", Pane: paneID, Opt: opt, Val: val})
	return err
}

// ListManaged lists panes carrying a non-empty @pairmux_name.
func (c *Client) ListManaged() ([]mux.PaneInfo, error) {
	resp, err := c.call(request{Op: "list"})
	if err != nil {
		return nil, err
	}
	var out []mux.PaneInfo
	for _, p := range resp.Panes {
		if p.Name != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// KillWindowOf kills the pane's program (and everything it started) and waits
// for its journal to be closed.
func (c *Client) KillWindowOf(paneID string) error {
	_, err := c.call(request{Op: "kill-window", Pane: paneID})
	return err
}

// SelectWindow checks that a window named name exists. A built-in attach
// targets a pane directly, so there is no focus to move.
func (c *Client) SelectWindow(name string) error {
	_, err := c.call(request{Op: "select-window", Name: name})
	return err
}

// HasSession reports whether the endpoint's server is running.
func (c *Client) HasSession() bool {
	_, err := c.call(request{Op: "ping"})
	return err == nil
}

// KillServer stops the endpoint's server and every terminal on it.
func (c *Client) KillServer() error {
	_, err := c.call(request{Op: "kill-server"})
	return err
}

// PaneTTY returns the pane's terminal device ("" on Windows, which has none).
func (c *Client) PaneTTY(paneID string) (string, error) {
	resp, err := c.call(request{Op: "pane-tty", Pane: paneID})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// PaneAudience reports how many humans are attached to the pane.
func (c *Client) PaneAudience(paneID string) (int, bool, error) {
	resp, err := c.call(request{Op: "audience", Pane: paneID})
	if err != nil {
		return 0, false, err
	}
	return resp.Attached, resp.Active, nil
}
