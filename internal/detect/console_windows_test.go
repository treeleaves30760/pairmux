//go:build windows

package detect

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/pty"
)

// These tests run real programs inside a real pseudo console, started the way
// the multiplexer starts a terminal (pty.Start), and read them back through
// the real helper — this test binary, which TestMain dispatches exactly as
// cli.Main does. Nothing is mocked: a pass here means a getpass-shaped read on
// a ConPTY is seen as one from another process.

var procSetConsoleMode = modkernel32.NewProc("SetConsoleMode")

func init() {
	testChildren["__test-console-getpass"] = func() int {
		return consoleChild(func(m uint32) uint32 { return (m | enableLineInput) &^ enableEchoInput }, false)
	}
	testChildren["__test-console-raw"] = func() int {
		return consoleChild(func(m uint32) uint32 { return m &^ (enableLineInput | enableEchoInput) }, false)
	}
	testChildren["__test-console-cooked"] = func() int {
		return consoleChild(func(m uint32) uint32 { return m | enableLineInput | enableEchoInput }, false)
	}
	testChildren["__test-console-spin"] = func() int {
		return consoleChild(func(m uint32) uint32 { return m }, true)
	}
}

// consoleChild sets its console's input mode, says READY, and then either
// blocks in a read — the shape of a program waiting on its keyboard — or keeps
// a thread on the CPU. It opens CONIN$/CONOUT$ itself rather than trusting its
// standard handles, so the test does not depend on how they were inherited.
func consoleChild(set func(uint32) uint32, spin bool) int {
	open := func(name string) syscall.Handle {
		p, _ := syscall.UTF16PtrFromString(name)
		h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
		if err != nil {
			os.Exit(3)
		}
		return h
	}
	conin, conout := open("CONIN$"), open("CONOUT$")
	var mode uint32
	if syscall.GetConsoleMode(conin, &mode) != nil {
		return 4
	}
	if r, _, _ := procSetConsoleMode.Call(uintptr(conin), uintptr(set(mode))); r == 0 {
		return 5
	}
	var n uint32
	syscall.WriteFile(conout, []byte("READY\r\n"), &n, nil)
	if spin {
		for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		}
		return 0
	}
	buf := make([]byte, 256)
	syscall.ReadFile(conin, buf, &n, nil) // blocks until the test kills us
	return 0
}

// useTestProbe makes this test binary the helper and turns the cache off, so
// every read below is a fresh probe of the console as it is now.
func useTestProbe(t *testing.T) {
	t.Helper()
	EnableConsoleProbe()
	probeAllowTestBinary = true
	saved := probes
	probes = &probeCache{ttl: 0, dir: t.TempDir(), now: time.Now, run: runProbe}
	t.Cleanup(func() { probes = saved; probeAllowTestBinary = false })
}

// startConsoleChild runs a child in a pseudo console and waits for its READY.
func startConsoleChild(t *testing.T, name string) pty.Pty {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := pty.Start(pty.Options{Argv: []string{exe, name}, Env: os.Environ(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		b := make([]byte, 4096)
		for {
			n, err := p.Read(b)
			mu.Lock()
			out.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		p.Kill()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
		}
		p.Close()
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		ready := strings.Contains(out.String(), "READY")
		text := out.String()
		mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %s never became ready; output %q", name, text)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, _, ok := pty.ParseConsoleID(p.TTY()); !ok {
		t.Fatalf("pty.TTY() = %q, not a console id", p.TTY())
	}
	return p
}

// eventually polls f until it returns true or the deadline passes. Scheduler
// state is a snapshot: a waiting runtime can be caught mid-wakeup, so a single
// read is not a fair test of either verdict.
func eventually(t *testing.T, what string, f func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		ok, desc := f()
		if ok {
			return
		}
		last = desc
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: never observed; last %s", what, last)
}

func TestConsoleProbeSeesGetpassSignature(t *testing.T) {
	useTestProbe(t)
	p := startConsoleChild(t, "__test-console-getpass")
	eventually(t, "echo off, line on", func() (bool, string) {
		s := ReadTTYState(p.TTY())
		return s.Secret(), describe(s)
	})
	// The child is blocked in ReadFile on its console: parked.
	eventually(t, "parked", func() (bool, string) {
		w := ReadFgWait(p.TTY())
		return w == FgParked, w.String()
	})
	// And the whole decision, as Classify makes it, needs no wording at all.
	if got := classify(ReadTTYState(p.TTY()), "", false, 0, false, func() FgWait { return ReadFgWait(p.TTY()) }); got.Kind != KindSecret {
		t.Fatalf("classify on a getpass-shaped console = %+v, want KindSecret", got)
	}
}

func TestConsoleProbeSeesRawAndCooked(t *testing.T) {
	useTestProbe(t)
	raw := startConsoleChild(t, "__test-console-raw")
	eventually(t, "raw input", func() (bool, string) {
		s := ReadTTYState(raw.TTY())
		return s.RawInput(), describe(s)
	})
	cooked := startConsoleChild(t, "__test-console-cooked")
	eventually(t, "cooked, echoing", func() (bool, string) {
		s := ReadTTYState(cooked.TTY())
		return s.LineOriented() && s.Echo && !s.Secret(), describe(s)
	})
}

func TestConsoleFgWaitSeesBusyProgram(t *testing.T) {
	useTestProbe(t)
	p := startConsoleChild(t, "__test-console-spin")
	eventually(t, "working", func() (bool, string) {
		w := ReadFgWait(p.TTY())
		return w == FgWorking, w.String()
	})
}

// TestConsoleProbeRefusesReplacedProcess pins the guard on pid reuse: an
// identifier whose creation time does not match the live process is refused
// by the helper, not resolved to whatever console that pid is on now.
func TestConsoleProbeRefusesReplacedProcess(t *testing.T) {
	useTestProbe(t)
	p := startConsoleChild(t, "__test-console-cooked")
	pid, created, _ := pty.ParseConsoleID(p.TTY())
	wrong := pty.FormatConsoleID(pid, created+1)
	if _, err := runProbe(wrong); err == nil || !strings.Contains(err.Error(), "process replaced") {
		t.Fatalf("runProbe(%s) = %v, want a replaced-process refusal", wrong, err)
	}
	if s := ReadTTYState(wrong); s.Known {
		t.Fatalf("ReadTTYState(%s) = %+v, want unknown", wrong, s)
	}
	if w := ReadFgWait(wrong); w != FgUnknown {
		t.Fatalf("ReadFgWait(%s) = %v, want unknown", wrong, w)
	}
}

// TestConsoleProbeIsReadOnly pins that probing leaves the console's mode as
// the program set it, however often it is probed.
func TestConsoleProbeIsReadOnly(t *testing.T) {
	useTestProbe(t)
	p := startConsoleChild(t, "__test-console-getpass")
	var first consoleProbe
	for i := range 5 {
		got, err := runProbe(p.TTY())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = got
		} else if got.Mode != first.Mode {
			t.Fatalf("mode changed between probes: %#x then %#x", first.Mode, got.Mode)
		}
	}
	if !consoleModeState(first.Mode).Secret() {
		t.Fatalf("mode %#x lost the child's echo-off", first.Mode)
	}
}

func TestConsoleProbeNeedsEnabling(t *testing.T) {
	saved := probeAllowTestBinary
	probeAllowTestBinary = false
	defer func() { probeAllowTestBinary = saved }()
	// The test binary may never be run as the helper unless a test asked:
	// re-executing it would run this whole suite again.
	if _, ok := probeExecutable(); ok {
		t.Fatal("a test binary was accepted as the console helper")
	}
}

func describe(s TTYState) string {
	return "echo=" + yes(s.Echo) + " canonical=" + yes(s.Canonical) + " known=" + yes(s.Known)
}

func yes(b bool) string {
	if b {
		return "y"
	}
	return "n"
}
