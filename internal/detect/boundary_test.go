package detect

import (
	"bytes"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

func TestScannerBoundary(t *testing.T) {
	bnd := []byte(core.SentinelBoundary)
	stream := append([]byte("out\r\n"), bnd...)
	stream = append(stream, "D:\\>"...)
	wantStart := int64(len("out\r\n"))
	wantEnd := wantStart + int64(len(bnd))

	check := func(t *testing.T, marks []Mark) {
		t.Helper()
		if len(marks) != 1 {
			t.Fatalf("got %d marks, want 1: %+v", len(marks), marks)
		}
		if m := marks[0]; m.Kind != MarkBoundary || m.ExitCode != -1 || m.Start != wantStart || m.End != wantEnd {
			t.Fatalf("mark = %+v, want boundary [%d,%d)", m, wantStart, wantEnd)
		}
	}
	t.Run("whole", func(t *testing.T) {
		check(t, NewScanner(0).Feed(stream))
	})
	t.Run("byte by byte", func(t *testing.T) {
		sc := NewScanner(0)
		var marks []Mark
		for i := range stream {
			marks = append(marks, sc.Feed(stream[i:i+1])...)
		}
		check(t, marks)
	})
	t.Run("split mid rune", func(t *testing.T) {
		cut := int(wantStart) + 4 // inside the second ZERO WIDTH SPACE
		sc := NewScanner(0)
		marks := append(sc.Feed(stream[:cut]), sc.Feed(stream[cut:])...)
		check(t, marks)
	})
}

// TestScannerBoundaryRestart: a broken-off partial boundary must not hide a
// complete one that begins inside the bytes already matched.
func TestScannerBoundaryRestart(t *testing.T) {
	bnd := []byte(core.SentinelBoundary)
	prefix := append([]byte("x"), bnd[:len(bnd)-2]...) // all but the last two bytes
	stream := append(append([]byte{}, prefix...), bnd...)
	marks := NewScanner(0).Feed(stream)
	if len(marks) != 1 || marks[0].Kind != MarkBoundary || marks[0].Start != int64(len(prefix)) {
		t.Fatalf("marks = %+v, want one boundary at %d", marks, len(prefix))
	}
}

// TestScannerBoundaryNotInEscape: the boundary is ground-state text; the same
// bytes inside an OSC payload belong to that sequence, and fewer than the
// whole boundary is ordinary text.
func TestScannerBoundaryNotInEscape(t *testing.T) {
	stream := buildOSC("0;title"+core.SentinelBoundary, "bel")
	stream = append(stream, "\u200b\u200b"...)
	if marks := NewScanner(0).Feed(stream); len(marks) != 0 {
		t.Fatalf("marks = %+v, want none", marks)
	}
}

// conptyCmdStream is a cmd.exe sentinel run as ConPTY renders it: the echoed
// command line, then the sentinel OSC hoisted to the start of the render frame
// it was written in, then that frame's text in order — the command's output,
// the boundary set /p printed after the OSC, and the next prompt.
func conptyCmdStream(exit string) (stream []byte, from, bndStart, bndEnd int64) {
	stream = []byte("D:\\x>")
	from = int64(len(stream))
	stream = append(stream, `echo cmd-marker & <nul set /p "=%PAIRMUX_ESC%]7779;p;!ERRORLEVEL!%PAIRMUX_BEL%%PAIRMUX_EOC%" & (call )`...)
	stream = append(stream, buildOSC("7779;p;"+exit, "bel")...)
	stream = append(stream, "\x1b[?25l\r\ncmd-marker\r\n"...)
	bndStart = int64(len(stream))
	stream = append(stream, core.SentinelBoundary...)
	bndEnd = int64(len(stream))
	stream = append(stream, "\x1b[4;1HD:\\x>\x1b]0;C:\\WINDOWS\\System32\\cmd.exe\x07\x1b[?25h"...)
	return
}

func TestWaitCompletionBoundaryAfterHoistedSentinel(t *testing.T) {
	j := mustOpen(t)
	stream, from, bndStart, bndEnd := conptyCmdStream("0")
	writeRawFile(t, j, stream)

	res, err := WaitCompletion(j, from, core.ModeSentinel, true, 2*time.Second, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDone || res.ExitCode != 0 || res.MarkStart != bndStart || res.EndOffset != bndEnd {
		t.Fatalf("res = %+v, want Done exit=0 [%d,%d)", res, bndStart, bndEnd)
	}
	if out := stream[from:res.MarkStart]; !bytes.Contains(out, []byte("cmd-marker\r\n")) {
		t.Fatalf("output region %q lacks the command's output", out)
	}
}

// TestWaitCommandBoundaryLater: the boundary may land in a later append than
// the sentinel; the run waits for it rather than cutting the output short, and
// keeps the sentinel's exit code.
func TestWaitCommandBoundaryLater(t *testing.T) {
	j := mustOpen(t)
	stream, from, bndStart, _ := conptyCmdStream("7")
	cut := bndStart - int64(len("cmd-marker\r\n"))
	writeRawFile(t, j, stream[:cut])
	go func() {
		time.Sleep(100 * time.Millisecond)
		appendRaw(t, j, stream[cut:])
	}()
	res, err := WaitCommand(j, from, core.ModeSentinel, true, 5*time.Second, 20*time.Millisecond, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDone || res.ExitCode != 7 || res.MarkStart != bndStart {
		t.Fatalf("res = %+v, want Done exit=7 start=%d", res, bndStart)
	}
}

// TestWaitCompletionBoundaryIgnoredBeforeSentinel: only a boundary after the
// sentinel ends the command; one in earlier output is the command's own text.
func TestWaitCompletionBoundaryIgnoredBeforeSentinel(t *testing.T) {
	j := mustOpen(t)
	stream := append([]byte("early"+core.SentinelBoundary), buildOSC("7779;p;0", "bel")...)
	stream = append(stream, "late\r\n"...)
	bndStart := int64(len(stream))
	stream = append(stream, core.SentinelBoundary...)
	writeRawFile(t, j, stream)
	res, _ := WaitCompletion(j, 0, core.ModeSentinel, true, 2*time.Second, 20*time.Millisecond)
	if res.Outcome != OutcomeDone || res.MarkStart != bndStart {
		t.Fatalf("res = %+v, want Done start=%d", res, bndStart)
	}
}

// TestWaitCompletionBoundaryFallback: a bounded shell whose boundary never
// shows still completes, at the sentinel, once the grace has passed — and a
// zero-timeout scan (lazy settlement) settles at the sentinel immediately.
func TestWaitCompletionBoundaryFallback(t *testing.T) {
	j := mustOpen(t)
	stream := append([]byte("out\n"), buildOSC("7779;p;4", "bel")...)
	writeRawFile(t, j, stream)

	start := time.Now()
	res, _ := WaitCompletion(j, 0, core.ModeSentinel, true, 5*time.Second, 20*time.Millisecond)
	if el := time.Since(start); el < graceBoundary {
		t.Fatalf("bounded wait returned after %v, before the %v grace", el, graceBoundary)
	}
	if res.Outcome != OutcomeDone || res.ExitCode != 4 || res.MarkStart != 4 || res.EndOffset != int64(len(stream)) {
		t.Fatalf("res = %+v, want Done exit=4 at the sentinel", res)
	}

	res, _ = WaitCompletion(j, 0, core.ModeSentinel, true, 0, 0)
	if res.Outcome != OutcomeDone || res.ExitCode != 4 || res.MarkStart != 4 {
		t.Fatalf("zero-timeout scan = %+v, want Done at the sentinel", res)
	}
}

// TestWaitCompletionUnboundedSentinelIsImmediate: POSIX and fish sentinels are
// written in order on a Unix pty and carry no boundary, so they must complete
// on the sentinel without sitting out the boundary grace.
func TestWaitCompletionUnboundedSentinelIsImmediate(t *testing.T) {
	j := mustOpen(t)
	stream := append([]byte("out\n"), buildOSC("7779;p;0", "bel")...)
	writeRawFile(t, j, stream)
	start := time.Now()
	res, _ := WaitCompletion(j, 0, core.ModeSentinel, false, 5*time.Second, 20*time.Millisecond)
	if el := time.Since(start); el >= graceBoundary/2 {
		t.Fatalf("unbounded sentinel took %v", el)
	}
	if res.Outcome != OutcomeDone || res.MarkStart != 4 {
		t.Fatalf("res = %+v, want Done at the sentinel", res)
	}
}

func TestCompletionWatcherBoundary(t *testing.T) {
	j := mustOpen(t)
	stream, from, bndStart, bndEnd := conptyCmdStream("3")
	sentEnd := bytes.IndexByte(stream, bel) + 1 // the hoisted sentinel's BEL
	writeRawFile(t, j, stream[:sentEnd])

	w := NewCompletionWatcher(from, core.ModeSentinel, true)
	if res, done, _ := w.Poll(j); done {
		t.Fatalf("completed on the hoisted sentinel, before the output: %+v", res)
	}
	appendRaw(t, j, stream[sentEnd:])
	res, done, err := w.Poll(j)
	if err != nil || !done {
		t.Fatalf("boundary not accepted: done=%v err=%v", done, err)
	}
	if res.ExitCode != 3 || res.MarkStart != bndStart || res.EndOffset != bndEnd {
		t.Fatalf("res = %+v, want exit=3 [%d,%d)", res, bndStart, bndEnd)
	}
}

func TestCompletionWatcherBoundaryFallback(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("$ "))
	w := NewCompletionWatcher(2, core.ModeSentinel, true)
	appendRaw(t, j, buildOSC("7779;p;6", "bel"))
	if res, done, _ := w.Poll(j); done {
		t.Fatalf("bounded watcher accepted a sentinel without its boundary: %+v", res)
	}
	time.Sleep(graceBoundary + 50*time.Millisecond)
	res, done, err := w.Poll(j)
	if err != nil || !done || res.ExitCode != 6 || res.MarkStart != 2 {
		t.Fatalf("fallback: done=%v err=%v res=%+v", done, err, res)
	}
}
