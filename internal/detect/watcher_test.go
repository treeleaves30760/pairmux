package detect

import (
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// TestCompletionWatcherIncremental is the case that separates a watcher from
// WaitCompletion: output arrives across many polls, the completion mark itself
// is split across two appends, and the caller does other work in between.
func TestCompletionWatcherIncremental(t *testing.T) {
	j := mustOpen(t)
	initial := []byte("$ ")
	from := int64(len(initial))
	writeRawFile(t, j, initial)

	pre := append([]byte("sudo -v\n"), buildOSC("133;C", "bel")...)
	pre = append(pre, []byte("Password: ")...)
	dSeq := buildOSC("133;D;0", "bel")

	w := NewCompletionWatcher(from, core.ModeHooks)
	feed := [][]byte{pre[:4], pre[4:], dSeq[:2], dSeq[2:]}
	for i, chunk := range feed {
		if res, done, err := w.Poll(j); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		} else if done {
			t.Fatalf("poll %d: completed early: %+v", i, res)
		}
		appendRaw(t, j, chunk)
	}
	res, done, err := w.Poll(j)
	if err != nil || !done {
		t.Fatalf("final poll: done=%v err=%v", done, err)
	}
	wantStart := from + int64(len(pre))
	if res.Outcome != OutcomeDone || res.ExitCode != 0 {
		t.Fatalf("res = %+v, want Done exit=0", res)
	}
	if res.MarkStart != wantStart || res.EndOffset != wantStart+int64(len(dSeq)) {
		t.Fatalf("offsets = (%d,%d), want (%d,%d)", res.MarkStart, res.EndOffset, wantStart, wantStart+int64(len(dSeq)))
	}
}

// TestCompletionWatcherIgnoresStrayD covers R1 for the watcher: a D from a
// command the human typed in the same pane arrives before our C and must not
// end the wait once our own C has been seen.
func TestCompletionWatcherIgnoresStrayD(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("$ "))
	from := int64(2)
	w := NewCompletionWatcher(from, core.ModeHooks)

	// The human's command finishes: a stray D with no C of ours before it.
	appendRaw(t, j, buildOSC("133;D;7", "bel"))
	if res, done, _ := w.Poll(j); done {
		t.Fatalf("stray D accepted immediately: %+v", res)
	}
	// Our command starts before the grace window elapses, invalidating the held D.
	appendRaw(t, j, buildOSC("133;C", "bel"))
	if _, done, _ := w.Poll(j); done {
		t.Fatal("completed on the stray D after our C appeared")
	}
	time.Sleep(2 * graceNoC)
	if _, done, _ := w.Poll(j); done {
		t.Fatal("held D survived our C past the grace window")
	}
	appendRaw(t, j, buildOSC("133;D;0", "bel"))
	res, done, err := w.Poll(j)
	if err != nil || !done {
		t.Fatalf("our D not accepted: done=%v err=%v", done, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 (the stray D carried 7)", res.ExitCode)
	}
}

// TestCompletionWatcherGraceAcceptsCLessD is the bash 3.2 tier: no C is ever
// emitted, so a lone D must be accepted once its grace window expires.
func TestCompletionWatcherGraceAcceptsCLessD(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("$ "))
	w := NewCompletionWatcher(2, core.ModeHooks)

	appendRaw(t, j, buildOSC("133;D;5", "bel"))
	if _, done, _ := w.Poll(j); done {
		t.Fatal("C-less D accepted before its grace window")
	}
	time.Sleep(2 * graceNoC)
	res, done, err := w.Poll(j)
	if err != nil || !done {
		t.Fatalf("C-less D not accepted after grace: done=%v err=%v", done, err)
	}
	if res.ExitCode != 5 {
		t.Fatalf("exit = %d, want 5", res.ExitCode)
	}
}

// TestCompletionWatcherSentinel takes the first sentinel mark: sentinel mode has
// no C to correlate against and no grace window.
func TestCompletionWatcherSentinel(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("$ "))
	w := NewCompletionWatcher(2, core.ModeSentinel)

	appendRaw(t, j, buildOSC("133;D;9", "bel")) // a hooks mark: not ours in sentinel mode
	if _, done, _ := w.Poll(j); done {
		t.Fatal("sentinel watcher accepted a 133;D mark")
	}
	appendRaw(t, j, buildOSC("7779;p;2", "bel"))
	res, done, err := w.Poll(j)
	if err != nil || !done {
		t.Fatalf("sentinel mark not accepted: done=%v err=%v", done, err)
	}
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", res.ExitCode)
	}
}

// TestCompletionWatcherIgnoresMarksBeforeBaseline guards the subscriber case:
// a watcher armed at the current end of the journal must not be satisfied by
// the completion of a command that finished before it subscribed.
func TestCompletionWatcherIgnoresMarksBeforeBaseline(t *testing.T) {
	j := mustOpen(t)
	past := append(buildOSC("133;C", "bel"), buildOSC("133;D;0", "bel")...)
	writeRawFile(t, j, past)

	w := NewCompletionWatcher(int64(len(past)), core.ModeHooks)
	if res, done, _ := w.Poll(j); done {
		t.Fatalf("woke on a pre-baseline completion: %+v", res)
	}
	appendRaw(t, j, buildOSC("133;C", "bel"))
	appendRaw(t, j, buildOSC("133;D;4", "bel"))
	res, done, err := w.Poll(j)
	if err != nil || !done {
		t.Fatalf("next completion missed: done=%v err=%v", done, err)
	}
	if res.ExitCode != 4 {
		t.Fatalf("exit = %d, want 4", res.ExitCode)
	}
}
