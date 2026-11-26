package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/paneexec"
)

// recRunner records every argv it is handed and replays canned responses by
// call index.
type recRunner struct {
	calls [][]string
	outs  []string
	errs  []error
}

func (r *recRunner) fn(argv []string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), argv...))
	i := len(r.calls) - 1
	var out string
	if i < len(r.outs) {
		out = r.outs[i]
	}
	var err error
	if i < len(r.errs) {
		err = r.errs[i]
	}
	return out, err
}

func newClient(r *recRunner) *Client {
	return &Client{Socket: core.DefaultSocket, runner: r.fn}
}

func TestNewDefaultsSocket(t *testing.T) {
	if got := New("").Socket; got != core.DefaultSocket {
		t.Errorf("New(\"\").Socket = %q, want %q", got, core.DefaultSocket)
	}
	if got := New("custom").Socket; got != "custom" {
		t.Errorf("New(\"custom\").Socket = %q, want %q", got, "custom")
	}
	if New("x").runner == nil {
		t.Error("New must set a runner")
	}
}

// TestArgv exercises one-shot methods and asserts the exact argv constructed.
func TestArgv(t *testing.T) {
	tests := []struct {
		name string
		out  string
		call func(c *Client) error
		want []string
	}{
		{
			name: "EnsureSession present",
			call: func(c *Client) error { return c.EnsureSession() },
			want: []string{"tmux", "-L", "pairmux", "has-session", "-t", "pairmux"},
		},
		{
			name: "PipePaneAppend simple",
			call: func(c *Client) error { return c.PipePaneAppend("%1", "/tmp/a/raw.log") },
			want: []string{"tmux", "-L", "pairmux", "pipe-pane", "-t", "%1", "-o", "cat >> '/tmp/a/raw.log'"},
		},
		{
			name: "SendLiteral",
			call: func(c *Client) error { return c.SendLiteral("%2", "echo hi") },
			want: []string{"tmux", "-L", "pairmux", "send-keys", "-t", "%2", "-l", "--", "echo hi"},
		},
		{
			name: "SendKeys multiple",
			call: func(c *Client) error { return c.SendKeys("%3", "Enter", "C-c") },
			want: []string{"tmux", "-L", "pairmux", "send-keys", "-t", "%3", "Enter", "C-c"},
		},
		{
			name: "CapturePane visible only",
			call: func(c *Client) error { _, err := c.CapturePane("%4", 0); return err },
			want: []string{"tmux", "-L", "pairmux", "capture-pane", "-p", "-t", "%4"},
		},
		{
			name: "CapturePane with history",
			call: func(c *Client) error { _, err := c.CapturePane("%4", 120); return err },
			want: []string{"tmux", "-L", "pairmux", "capture-pane", "-p", "-t", "%4", "-S", "-120"},
		},
		{
			name: "SetPaneOption",
			call: func(c *Client) error { return c.SetPaneOption("%5", core.PaneOptName, "t1") },
			want: []string{"tmux", "-L", "pairmux", "set-option", "-p", "-t", "%5", "@pairmux_name", "t1"},
		},
		{
			name: "KillWindowOf",
			call: func(c *Client) error { return c.KillWindowOf("%6") },
			want: []string{"tmux", "-L", "pairmux", "kill-window", "-t", "%6"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{outs: []string{tt.out}}
			if err := tt.call(newClient(r)); err != nil {
				t.Fatalf("call error: %v", err)
			}
			if len(r.calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(r.calls))
			}
			if !reflect.DeepEqual(r.calls[0], tt.want) {
				t.Errorf("argv = %v, want %v", r.calls[0], tt.want)
			}
		})
	}
}

func TestPipePaneEscaping(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{"/tmp/plain/raw.log", "cat >> '/tmp/plain/raw.log'"},
		{"/tmp/o'brien/raw.log", `cat >> '/tmp/o'\''brien/raw.log'`},
		{"/a'b'c", `cat >> '/a'\''b'\''c'`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			r := &recRunner{outs: []string{""}}
			if err := newClient(r).PipePaneAppend("%1", tt.file); err != nil {
				t.Fatalf("err: %v", err)
			}
			got := r.calls[0][len(r.calls[0])-1]
			if got != tt.want {
				t.Errorf("command arg = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    string
		wantErr bool
	}{
		{"release", "tmux 3.7b\n", "3.7b", false},
		{"next branch", "tmux next-3.4\n", "next-3.4", false},
		{"no trailing newline", "tmux 3.3a", "3.3a", false},
		{"garbage", "not-tmux\n", "", true},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{outs: []string{tt.out}}
			got, err := newClient(r).Version()
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Version() = %q, want %q", got, tt.want)
			}
			if want := []string{"tmux", "-L", "pairmux", "-V"}; !reflect.DeepEqual(r.calls[0], want) {
				t.Errorf("argv = %v, want %v", r.calls[0], want)
			}
		})
	}
}

func TestEnsureSession(t *testing.T) {
	hasArgv := []string{"tmux", "-L", "pairmux", "has-session", "-t", "pairmux"}
	newArgv := []string{"tmux", "-L", "pairmux", "new-session", "-d", "-s", "pairmux", "-x", "220", "-y", "50"}
	absentErr := errors.New("no server running")
	createErr := errors.New("duplicate session: pairmux")
	tests := []struct {
		name      string
		errs      []error    // per-call runner results, in order
		wantCalls [][]string // exact argv sequence
		wantErr   error      // nil, or the exact error EnsureSession must return
	}{
		{
			name:      "already present",
			errs:      []error{nil},
			wantCalls: [][]string{hasArgv},
		},
		{
			name:      "absent then created",
			errs:      []error{absentErr, nil},
			wantCalls: [][]string{hasArgv, newArgv},
		},
		{
			// Lost a concurrent-create race: new-session fails but the session
			// exists on re-check, so the call must succeed.
			name:      "create race lost but session exists",
			errs:      []error{absentErr, createErr, nil},
			wantCalls: [][]string{hasArgv, newArgv, hasArgv},
		},
		{
			// Genuine create failure: re-check also fails; propagate the
			// original new-session error, not the re-check error.
			name:      "create fails and recheck fails",
			errs:      []error{absentErr, createErr, errors.New("still no server")},
			wantCalls: [][]string{hasArgv, newArgv, hasArgv},
			wantErr:   createErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{errs: tt.errs}
			err := newClient(r).EnsureSession()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("EnsureSession: %v", err)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want the new-session error %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(r.calls, tt.wantCalls) {
				t.Errorf("calls =\n %v\nwant\n %v", r.calls, tt.wantCalls)
			}
		})
	}
}

func TestNewWindow(t *testing.T) {
	tests := []struct {
		name string
		req  NewWindowReq
		out  string
		want []string
		id   string
	}{
		{
			name: "minimal default shell",
			req:  NewWindowReq{Name: "t1"},
			out:  "%9\n",
			want: []string{"tmux", "-L", "pairmux", "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "pairmux:", "-n", "t1"},
			id:   "%9",
		},
		{
			name: "dir env sorted and argv",
			req: NewWindowReq{
				Name: "work",
				Dir:  "/repo",
				Env:  map[string]string{"ZDOTDIR": "/s/shim-zsh", "PAIRMUX": "1"},
				Argv: []string{"zsh", "-i"},
			},
			out: "%10",
			want: []string{
				"tmux", "-L", "pairmux", "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "pairmux:", "-n", "work",
				"-c", "/repo",
				"-e", "PAIRMUX=1", "-e", "ZDOTDIR=/s/shim-zsh",
				"--", "zsh", "-i",
			},
			id: "%10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{outs: []string{tt.out}}
			id, err := newClient(r).NewWindow(tt.req)
			if err != nil {
				t.Fatalf("NewWindow: %v", err)
			}
			if id != tt.id {
				t.Errorf("paneID = %q, want %q", id, tt.id)
			}
			if !reflect.DeepEqual(r.calls[0], tt.want) {
				t.Errorf("argv =\n %v\nwant\n %v", r.calls[0], tt.want)
			}
		})
	}
}

func TestListManaged(t *testing.T) {
	out := strings.Join([]string{
		"%0|main||zsh|0",     // unmanaged: empty @pairmux_name
		"%1|work|t1|vim|0",   // managed, alive
		"%2|build|t2|bash|1", // managed, dead
		"",                   // blank line ignored
		"%3|x|t3|git|push|0", // separator inside current_command
		"malformed-line",     // too few fields, skipped
	}, "\n")
	r := &recRunner{outs: []string{out}}
	got, err := newClient(r).ListManaged()
	if err != nil {
		t.Fatalf("ListManaged: %v", err)
	}
	want := []PaneInfo{
		{PaneID: "%1", Window: "work", Name: "t1", CurrentCmd: "vim", Dead: false},
		{PaneID: "%2", Window: "build", Name: "t2", CurrentCmd: "bash", Dead: true},
		{PaneID: "%3", Window: "x", Name: "t3", CurrentCmd: "git|push", Dead: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListManaged() =\n %#v\nwant\n %#v", got, want)
	}
	wantArgv := []string{"tmux", "-L", "pairmux", "list-panes", "-a", "-F", listFormat}
	if !reflect.DeepEqual(r.calls[0], wantArgv) {
		t.Errorf("argv = %v, want %v", r.calls[0], wantArgv)
	}
}

func TestListManagedError(t *testing.T) {
	r := &recRunner{errs: []error{errors.New("no server running")}}
	if _, err := newClient(r).ListManaged(); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestAttachArgv(t *testing.T) {
	got := New("sock1").AttachArgv()
	want := []string{"tmux", "-L", "sock1", "attach", "-t", "pairmux"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AttachArgv() = %v, want %v", got, want)
	}
}

func TestErrorPropagation(t *testing.T) {
	r := &recRunner{errs: []error{errors.New("target not found")}}
	err := newClient(r).SendLiteral("%99", "x")
	if err == nil || !strings.Contains(err.Error(), "target not found") {
		t.Fatalf("SendLiteral err = %v, want it to carry runner error", err)
	}
}

// execRunner uses a real subprocess (POSIX sh, never tmux) to verify stdout
// capture and stderr inclusion in errors.
func TestExecRunnerStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh")
	}
	out, err := execRunner([]string{"/bin/sh", "-c", "printf hello"})
	if err != nil {
		t.Fatalf("execRunner: %v", err)
	}
	if out != "hello" {
		t.Errorf("stdout = %q, want %q", out, "hello")
	}
}

func TestExecRunnerStderrInError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh")
	}
	_, err := execRunner([]string{"/bin/sh", "-c", "echo boom 1>&2; exit 3"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q must include stderr text", err.Error())
	}
}

// helperClient is a client whose panes start through a stub helper path.
func helperClient(r *recRunner) *Client {
	c := newClient(r)
	c.paneExec = func() (string, bool) { return "/opt/pmx/pairmux", true }
	return c
}

func TestNewWindowHandsEnvironmentOffThroughFile(t *testing.T) {
	t.Setenv("PMX_CALLER_SECRET", "sk-caller-do-not-leak")
	t.Setenv("TMUX", "/tmp/tmux-1/outer,1,0")
	envFile := filepath.Join(t.TempDir(), ".pane-env")
	r := &recRunner{outs: []string{"%11\n"}}
	id, err := helperClient(r).NewWindow(NewWindowReq{
		Name:    "work",
		Dir:     "/repo",
		Env:     map[string]string{"PAIRMUX_NAME": "work", "API_TOKEN": "tok-extra-do-not-leak"},
		Argv:    []string{"zsh", "-i"},
		Gate:    "/s/work/.start-gate",
		EnvFile: envFile,
	})
	if err != nil || id != "%11" {
		t.Fatalf("NewWindow = %q, %v", id, err)
	}
	want := []string{
		"tmux", "-L", "pairmux", "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "pairmux:", "-n", "work",
		"-c", "/repo",
		"--", "/opt/pmx/pairmux", paneexec.Command, "--env-file", envFile, "--gate", "/s/work/.start-gate",
		"--", "zsh", "-i",
	}
	if !reflect.DeepEqual(r.calls[0], want) {
		t.Fatalf("argv =\n %q\nwant\n %q", r.calls[0], want)
	}
	// No value of the environment — secrets above all — is on tmux's argv.
	joined := strings.Join(r.calls[0], " ")
	for _, secret := range []string{"sk-caller-do-not-leak", "tok-extra-do-not-leak", "PAIRMUX_NAME"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("tmux argv carries %q: %q", secret, r.calls[0])
		}
	}
	fi, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("env file mode = %o, want 600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := paneexec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		has[k] = v
	}
	if has["PMX_CALLER_SECRET"] != "sk-caller-do-not-leak" || has["API_TOKEN"] != "tok-extra-do-not-leak" || has["PAIRMUX_NAME"] != "work" {
		t.Fatalf("env file lacks the caller's or the request's variables: %q", got)
	}
	if _, ok := has["TMUX"]; ok {
		t.Fatalf("env file carries the caller's TMUX: %q", got)
	}
}

func TestNewWindowDefaultShellIsLoginShell(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".pane-env")
	r := &recRunner{outs: []string{"/usr/local/bin/fish\n", "%12"}}
	if _, err := helperClient(r).NewWindow(NewWindowReq{Name: "sh", EnvFile: envFile}); err != nil {
		t.Fatal(err)
	}
	wantShow := []string{"tmux", "-L", "pairmux", "show-options", "-gv", "default-shell"}
	if len(r.calls) != 2 || !reflect.DeepEqual(r.calls[0], wantShow) {
		t.Fatalf("calls = %q, want default-shell lookup first", r.calls)
	}
	if tail := r.calls[1][len(r.calls[1])-3:]; !reflect.DeepEqual(tail, []string{"--login", "--", "/usr/local/bin/fish"}) {
		t.Fatalf("argv = %q, want a login run of the default shell", r.calls[1])
	}
}

func TestNewWindowDefaultShellFallsBackToCallerShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/caller-shell")
	r := &recRunner{outs: []string{"", "%13"}, errs: []error{errors.New("no server")}}
	c := helperClient(r)
	if got := c.defaultShell(); got != "/bin/caller-shell" {
		t.Fatalf("defaultShell = %q, want the caller's SHELL", got)
	}
	t.Setenv("SHELL", "")
	r2 := &recRunner{outs: []string{"\n"}}
	if got := helperClient(r2).defaultShell(); got != "/bin/sh" {
		t.Fatalf("defaultShell = %q, want /bin/sh", got)
	}
}

func TestNewWindowFailureRemovesEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".pane-env")
	r := &recRunner{errs: []error{errors.New("new-window failed")}}
	if _, err := helperClient(r).NewWindow(NewWindowReq{Name: "x", Argv: []string{"true"}, EnvFile: envFile}); err == nil {
		t.Fatal("NewWindow succeeded on a failing tmux")
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatalf("env file left behind after a failed create: %v", err)
	}
	// An env file that cannot be written fails the create before tmux runs.
	r2 := &recRunner{}
	if _, err := helperClient(r2).NewWindow(NewWindowReq{Name: "x", Argv: []string{"true"}, EnvFile: filepath.Join(t.TempDir(), "missing", "f")}); err == nil {
		t.Fatal("NewWindow succeeded without its env file")
	}
	if len(r2.calls) != 0 {
		t.Fatalf("tmux ran without the env file: %q", r2.calls)
	}
}

func TestNewWindowRequiresEnvFile(t *testing.T) {
	r := &recRunner{}
	if _, err := helperClient(r).NewWindow(NewWindowReq{Name: "x", Argv: []string{"true"}}); err == nil {
		t.Fatal("NewWindow without EnvFile must fail rather than pick a path the caller cannot clean up")
	}
	if len(r.calls) != 0 {
		t.Fatalf("tmux ran without an env file: %q", r.calls)
	}
}
