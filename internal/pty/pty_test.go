package pty

import (
	"bytes"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// readAll drains p until EOF or the deadline, so a broken EOF path fails the
// test instead of hanging it.
func readAll(t *testing.T, p Pty, within time.Duration) string {
	t.Helper()
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var buf bytes.Buffer
		b := make([]byte, 4096)
		for {
			n, err := p.Read(b)
			buf.Write(b[:n])
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				ch <- res{buf.String(), err}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read: %v", r.err)
		}
		return r.out
	case <-time.After(within):
		t.Fatalf("no EOF within %s", within)
		return ""
	}
}

func shellArgv(script string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/d", "/c", script}
	}
	return []string{"/bin/sh", "-c", script}
}

func TestStartReadsOutputAndExitCode(t *testing.T) {
	script := "echo hello-from-pty; exit 3"
	if runtime.GOOS == "windows" {
		script = "echo hello-from-pty & exit /b 3"
	}
	p, err := Start(Options{Argv: shellArgv(script), Env: os.Environ(), Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	out := readAll(t, p, 10*time.Second)
	if !strings.Contains(out, "hello-from-pty") {
		t.Fatalf("output %q lacks greeting", out)
	}
	code, err := p.Wait()
	if err != nil || code != 3 {
		t.Fatalf("Wait = %d, %v; want 3", code, err)
	}
}

func TestFinalOutputIsNotLost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY renders a screen, not a byte stream; covered by the unix drain")
	}
	// A burst written immediately before exit must all arrive: this is the
	// output an agent most needs and the one darwin can drop if the slave
	// closes before the master drains.
	p, err := Start(Options{Argv: shellArgv("i=0; while [ $i -lt 2000 ]; do echo line-$i; i=$((i+1)); done"), Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	out := readAll(t, p, 20*time.Second)
	if !strings.Contains(out, "line-1999") {
		t.Fatalf("last line missing; got %d bytes ending %q", len(out), out[max(0, len(out)-80):])
	}
}

func TestResizeAndTTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stty is unix-only")
	}
	p, err := Start(Options{Argv: shellArgv("sleep 0.3; stty size; tty"), Env: os.Environ(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.TTY() == "" {
		t.Fatal("no tty path")
	}
	if err := p.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	out := readAll(t, p, 10*time.Second)
	if !strings.Contains(out, "43 132") {
		t.Fatalf("stty size after resize: %q", out)
	}
	if !strings.Contains(out, p.TTY()) {
		t.Fatalf("tty output %q does not name %s", out, p.TTY())
	}
}

func TestKillEndsTheProgram(t *testing.T) {
	script := "sleep 60"
	if runtime.GOOS == "windows" {
		script = "ping -n 60 127.0.0.1 >NUL"
	}
	p, err := Start(Options{Argv: shellArgv(script), Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	time.Sleep(200 * time.Millisecond)
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("program survived Kill")
	}
	readAll(t, p, 10*time.Second)
}

func TestInputReachesProgram(t *testing.T) {
	argv := shellArgv("read x; echo got-$x")
	if runtime.GOOS == "windows" {
		argv = shellArgv("set /p x= & call echo got-%x%")
	}
	p, err := Start(Options{Argv: argv, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	time.Sleep(300 * time.Millisecond)
	if _, err := p.Write([]byte("abc\r")); err != nil {
		t.Fatal(err)
	}
	out := readAll(t, p, 10*time.Second)
	if !strings.Contains(out, "got-abc") {
		t.Fatalf("output %q lacks echoed input", out)
	}
}

func TestLookPathUsesTheProgramsPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	exe := dir + "/only-here"
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := lookPath("only-here", []string{"PATH=" + dir})
	if err != nil || got != exe {
		t.Fatalf("lookPath = %q, %v", got, err)
	}
	if _, err := lookPath("only-here", []string{"PATH=/nonexistent"}); err == nil {
		t.Fatal("found a program outside the given PATH")
	}
}
