// Package tmux is a thin exec wrapper around the tmux binary. Each method maps
// to one tmux subcommand on a dedicated socket; the package holds no business
// logic. All invocations use an argv array — never sh -c with interpolated
// strings. The single exception is pipe-pane's command argument, which tmux
// itself runs via sh (see PipePaneAppend).
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// runner executes a fully-formed tmux argv (argv[0] == "tmux") and returns
// stdout. On failure the returned error includes stderr text. It is a field on
// Client so unit tests can assert on constructed argv without a real tmux.
type runner func(argv []string) (string, error)

// Client targets one tmux server socket.
type Client struct {
	Socket string
	runner runner
}

// ErrInvalidSocket is returned before exec when a -L socket name could escape
// tmux's per-user socket directory or otherwise falls outside pairmux's
// conservative portable grammar.
var ErrInvalidSocket = errors.New("tmux: invalid socket name")

var socketNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ValidSocketName reports whether socket is safe to pass to tmux -L. tmux
// accepts path separators (including ../), so argv separation alone does not
// prevent socket-directory traversal.
func ValidSocketName(socket string) bool { return socketNameRe.MatchString(socket) }

// New returns a Client for the given socket; "" selects core.DefaultSocket.
func New(socket string) *Client {
	if socket == "" {
		socket = core.DefaultSocket
	}
	return &Client{Socket: socket, runner: execRunner}
}

// run prepends the "tmux -L <socket>" prefix and dispatches to the runner.
func (c *Client) run(args ...string) (string, error) {
	if !ValidSocketName(c.Socket) {
		return "", fmt.Errorf("%w: %q", ErrInvalidSocket, c.Socket)
	}
	argv := make([]string, 0, len(args)+3)
	argv = append(argv, "tmux", "-L", c.Socket)
	argv = append(argv, args...)
	return c.runner(argv)
}

func execRunner(argv []string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if se := strings.TrimSpace(stderr.String()); se != "" {
			return stdout.String(), fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, se)
		}
		return stdout.String(), fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return stdout.String(), nil
}

// Version returns the tmux version token, e.g. "tmux 3.7b" -> "3.7b".
func (c *Client) Version() (string, error) {
	out, err := c.run("-V")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 2 {
		return "", fmt.Errorf("tmux: unexpected version output %q", out)
	}
	return fields[1], nil
}

// EnsureSession creates the pairmux session if it does not already exist.
func (c *Client) EnsureSession() error {
	if _, err := c.run("has-session", "-t", core.SessionName); err == nil {
		return nil
	}
	_, createErr := c.run("new-session", "-d", "-s", core.SessionName, "-x", "220", "-y", "50")
	if createErr == nil {
		return nil
	}
	// Check-then-create races with concurrent callers: the loser's new-session
	// fails ("duplicate session") even though the session now exists. Re-check
	// before failing; propagate the create error only if the session is truly
	// absent.
	if _, err := c.run("has-session", "-t", core.SessionName); err == nil {
		return nil
	}
	return createErr
}

// NewWindowReq describes a window (one terminal) to create.
type NewWindowReq struct {
	Name string            // window name
	Dir  string            // cwd; "" inherits
	Env  map[string]string // -e KEY=VAL for each entry
	Argv []string          // command; empty runs the default shell
	// Gate is a FIFO the pane blocks on before the command starts. tmux runs a
	// pane's command the moment the window exists, which is before pipe-pane can
	// be attached to it — anything printed in that window is never journalled,
	// and a program whose first act is to prompt can therefore be invisible to
	// pairmux entirely. Holding the pane on a read the caller releases after
	// wiring the journal removes the race rather than narrowing it. Ignored when
	// Argv is empty: a shell prints nothing until its rc files run, and hooks
	// mode already nudges a prompt cycle to cover the same gap.
	Gate string
}

// NewWindow creates a detached window in the pairmux session and returns its
// pane id (e.g. "%3").
func (c *Client) NewWindow(r NewWindowReq) (string, error) {
	args := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", core.SessionName + ":", "-n", r.Name}
	if r.Dir != "" {
		args = append(args, "-c", r.Dir)
	}
	for _, k := range sortedKeys(r.Env) {
		args = append(args, "-e", k+"="+r.Env[k])
	}
	if len(r.Argv) > 0 {
		args = append(args, "--")
		if r.Gate != "" {
			// exec so the pane's process really is the command: the holding shell
			// must not survive to change what pane_current_command reports or to
			// sit between the program and its exit status.
			args = append(args, "sh", "-c",
				`read -r _ <"$1" || :; shift; exec "$@"`, "sh", r.Gate)
		}
		args = append(args, r.Argv...)
	}
	out, err := c.run(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PipePaneAppend streams pane output, appending to file. tmux runs the command
// argument via sh, so the path is single-quoted with embedded quotes escaped.
func (c *Client) PipePaneAppend(paneID, file string) error {
	_, err := c.run("pipe-pane", "-t", paneID, "-o", "cat >> "+shSingleQuote(file))
	return err
}

// SendLiteral sends text verbatim (no key-name interpretation).
func (c *Client) SendLiteral(paneID, text string) error {
	_, err := c.run("send-keys", "-t", paneID, "-l", "--", text)
	return err
}

// SendKeys sends already-valid tmux key names (e.g. "Enter", "C-c").
func (c *Client) SendKeys(paneID string, keys ...string) error {
	args := append([]string{"send-keys", "-t", paneID}, keys...)
	_, err := c.run(args...)
	return err
}

// CapturePane returns pane contents. historyLines <= 0 captures the visible
// screen only; a positive value also captures that many scrollback lines.
func (c *Client) CapturePane(paneID string, historyLines int) (string, error) {
	args := []string{"capture-pane", "-p", "-t", paneID}
	if historyLines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(historyLines))
	}
	return c.run(args...)
}

// SetPaneOption sets a pane-scoped option (e.g. @pairmux_name).
func (c *Client) SetPaneOption(paneID, opt, val string) error {
	_, err := c.run("set-option", "-p", "-t", paneID, opt, val)
	return err
}

// PaneInfo describes one managed pane.
type PaneInfo struct {
	PaneID     string
	Window     string
	Name       string
	CurrentCmd string
	Dead       bool
}

// tmux may sanitize control characters in format output under a non-UTF-8
// locale (notably Alpine's default C locale), turning tabs into underscores.
// A printable separator is stable across those environments. The surrounding
// fields are either tmux identifiers or pairmux-controlled names; if the
// current command itself contains a separator, ListManaged joins it back.
const listFormat = "#{pane_id}|#{window_name}|#{@pairmux_name}|#{pane_current_command}|#{pane_dead}"

// ListManaged lists panes across all sessions on the socket, keeping only those
// tagged with a non-empty @pairmux_name.
func (c *Client) ListManaged() ([]PaneInfo, error) {
	out, err := c.run("list-panes", "-a", "-F", listFormat)
	if err != nil {
		return nil, err
	}
	var infos []PaneInfo
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		name := parts[2]
		if name == "" {
			continue // unmanaged pane
		}
		// pane_dead is the final field; anything between current_command and it
		// is treated as command text (defensive against tabs in the command).
		last := len(parts) - 1
		infos = append(infos, PaneInfo{
			PaneID:     parts[0],
			Window:     parts[1],
			Name:       name,
			CurrentCmd: strings.Join(parts[3:last], "|"),
			Dead:       parts[last] == "1",
		})
	}
	return infos, nil
}

// KillWindowOf kills the window containing the given pane.
func (c *Client) KillWindowOf(paneID string) error {
	_, err := c.run("kill-window", "-t", paneID)
	return err
}

// AttachArgv returns the argv a caller would exec to attach interactively.
func (c *Client) AttachArgv() []string {
	return []string{"tmux", "-L", c.Socket, "attach", "-t", core.SessionName}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// shSingleQuote returns s as a single-quoted sh word: each embedded single
// quote is replaced by the close-quote/escaped-quote/reopen-quote idiom so the
// result is safe as a pipe-pane command argument.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
