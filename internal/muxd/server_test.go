package muxd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/rundir"
)

var testSeq int32

// startServer runs a server in-process on a private runtime root and returns a
// client for it. The root is short (not t.TempDir) because Unix socket paths
// are limited to about 100 bytes.
func startServer(t *testing.T) *Client {
	t.Helper()
	t.Setenv("PAIRMUX_TMPDIR", shortRoot(t))
	socket := fmt.Sprintf("t%d-%d", os.Getpid(), atomic.AddInt32(&testSeq, 1))
	done := make(chan error, 1)
	go func() { done <- Serve(socket) }()
	c := New(socket)
	deadline := time.Now().Add(5 * time.Second)
	for !c.HasSession() {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		_ = c.KillServer()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	return c
}

// shortRoot is a private runtime root short enough for a socket path. Socket
// paths are limited to about 100 bytes, which t.TempDir (named after the test,
// and deep on macOS and Windows) can exceed.
func shortRoot(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "pmxd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func scriptArgv(unix, windows string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/c", windows}
	}
	return []string{"/bin/sh", "-c", unix}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fileContains(path, want string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), want)
}

func TestTerminalLifecycle(t *testing.T) {
	c := startServer(t)
	journal := filepath.Join(t.TempDir(), "raw.log")
	argv := scriptArgv(`printf 'first-byte\n'; read x; echo "got-$x"; sleep 60`,
		`echo first-byte& set /p x=& call echo got-%x%& ping -n 60 127.0.0.1 >NUL`)
	id, err := c.NewWindow(mux.NewWindowReq{Name: "w1", Argv: argv, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	// The journal is open before the program starts: its very first line is
	// there without any start gate.
	waitFor(t, "first output in journal", func() bool { return fileContains(journal, "first-byte") })

	if err := c.SetPaneOption(id, "@pairmux_name", "alpha"); err != nil {
		t.Fatal(err)
	}
	panes, err := c.ListManaged()
	if err != nil || len(panes) != 1 || panes[0].Name != "alpha" || panes[0].PaneID != id || panes[0].Window != "w1" {
		t.Fatalf("ListManaged = %+v, %v", panes, err)
	}
	if err := c.SendLiteral(id, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.SendKeys(id, "Enter"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "echoed input", func() bool { return fileContains(journal, "got-abc") })
	waitFor(t, "screen capture", func() bool {
		s, err := c.CapturePane(id, 0)
		return err == nil && strings.Contains(s, "got-abc")
	})
	if runtime.GOOS != "windows" {
		if tty, err := c.PaneTTY(id); err != nil || tty == "" {
			t.Fatalf("PaneTTY = %q, %v", tty, err)
		}
	}
	if n, active, err := c.PaneAudience(id); err != nil || n != 0 || active {
		t.Fatalf("PaneAudience = %d, %v, %v", n, active, err)
	}
	if err := c.KillWindowOf(id); err != nil {
		t.Fatal(err)
	}
	if panes, err := c.ListManaged(); err != nil || len(panes) != 0 {
		t.Fatalf("after kill: %+v, %v", panes, err)
	}
	if err := c.SendLiteral(id, "x"); err == nil {
		t.Fatal("send to a killed pane succeeded")
	}
}

func TestExitedProgramLeavesTheList(t *testing.T) {
	c := startServer(t)
	journal := filepath.Join(t.TempDir(), "raw.log")
	id, err := c.NewWindow(mux.NewWindowReq{Name: "short", Argv: scriptArgv("echo bye", "echo bye"), Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetPaneOption(id, "@pairmux_name", "short")
	waitFor(t, "pane removal", func() bool {
		panes, err := c.ListManaged()
		return err == nil && len(panes) == 0
	})
	if !fileContains(journal, "bye") {
		t.Fatal("final output missing from the journal")
	}
}

func TestSecondServerYieldsTheEndpoint(t *testing.T) {
	c := startServer(t)
	if err := Serve(c.Socket); err != nil {
		t.Fatalf("second Serve = %v, want nil (endpoint already owned)", err)
	}
	if !c.HasSession() {
		t.Fatal("the first server stopped")
	}
}

func TestNoServerIsReportedAsSuch(t *testing.T) {
	t.Setenv("PAIRMUX_TMPDIR", shortRoot(t))
	_, err := New("absent").ListManaged()
	if err == nil || !strings.Contains(err.Error(), "no server running") {
		t.Fatalf("ListManaged without a server = %v", err)
	}
	if Running("absent") {
		t.Fatal("Running reported an absent server")
	}
}

// TestAttachStream drives the attach protocol the way the attach client does:
// the first frame redraws the screen, keystrokes reach the program, output
// streams back, and detaching returns the pane to the agents' default size.
func TestAttachStream(t *testing.T) {
	c := startServer(t)
	argv := scriptArgv(`while read l; do echo "echo:$l"; done`,
		`for /l %i in (1,1,100) do @(set /p l=& call echo echo:%l%)`)
	id, err := c.NewWindow(mux.NewWindowReq{Name: "att", Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := c.dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeMsg(conn, request{Op: "attach", Proto: protoVersion, Pane: id, Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	var resp response
	if err := readMsg(conn, &resp); err != nil || !resp.OK {
		t.Fatalf("attach response %+v, %v", resp, err)
	}
	if n, _, _ := c.PaneAudience(id); n != 1 {
		t.Fatalf("audience = %d, want 1", n)
	}
	got := make(chan string, 64)
	go func() {
		for {
			typ, payload, err := readFrame(conn)
			if err != nil {
				close(got)
				return
			}
			if typ == frameOutput {
				got <- string(payload)
			}
		}
	}()
	if _, err := conn.Write(encodeFrame(frameInput, []byte("ping\r"))); err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	deadline := time.After(10 * time.Second)
	for !strings.Contains(seen.String(), "echo:ping") {
		select {
		case s, ok := <-got:
			if !ok {
				t.Fatalf("stream closed; saw %q", seen.String())
			}
			seen.WriteString(s)
		case <-deadline:
			t.Fatalf("no echo through the attach stream; saw %q", seen.String())
		}
	}
	if _, err := conn.Write(encodeFrame(frameDetach, nil)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "detach", func() bool { n, _, _ := c.PaneAudience(id); return n == 0 })
}

func TestServeRejectsBadSocketNames(t *testing.T) {
	if err := Serve("../escape"); err == nil {
		t.Fatal("Serve accepted a traversing socket name")
	}
	if _, err := New("../escape").ListManaged(); err == nil {
		t.Fatal("client accepted a traversing socket name")
	}
}

func TestRuntimeDirIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACL-based on Windows")
	}
	t.Setenv("PAIRMUX_TMPDIR", shortRoot(t))
	d, err := rundir.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := rundir.Ensure(); err == nil {
		t.Fatal("a group/world-readable runtime dir was accepted")
	}
}
