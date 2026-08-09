package detect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
)

func mustOpen(t *testing.T) *journal.Journal {
	t.Helper()
	j, err := journal.Open(filepath.Join(t.TempDir(), "term"))
	if err != nil {
		t.Fatalf("journal.Open: %v", err)
	}
	return j
}

func writeRawFile(t *testing.T, j *journal.Journal, b []byte) {
	t.Helper()
	if err := os.WriteFile(j.RawPath(), b, 0o644); err != nil {
		t.Fatalf("write raw.log: %v", err)
	}
}

func writeIndexFile(t *testing.T, j *journal.Journal, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(j.Dir, "index.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write index.jsonl: %v", err)
	}
}

// appendRaw appends to raw.log. Safe to call from a writer goroutine: it uses
// only Errorf (never FailNow) for reporting.
func appendRaw(t *testing.T, j *journal.Journal, b []byte) {
	f, err := os.OpenFile(j.RawPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Errorf("append open raw.log: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Errorf("append write raw.log: %v", err)
	}
}

// TestWaitCompletionGoroutineWriter is the mandated case: WaitCompletion must
// return correct output-region offsets while a goroutine appends output and the
// completion mark (itself split across appends) with delays.
func TestWaitCompletionGoroutineWriter(t *testing.T) {
	j := mustOpen(t)
	initial := []byte("$ ")
	from := int64(len(initial))
	writeRawFile(t, j, initial)

	output := []byte("run seq\nline1\nline2\n")
	markSeq := buildOSC("133;D;0", "bel")

	done := make(chan struct{})
	go func() {
		defer close(done)
		appendRaw(t, j, output[:8])
		time.Sleep(60 * time.Millisecond)
		appendRaw(t, j, output[8:])
		time.Sleep(60 * time.Millisecond)
		appendRaw(t, j, markSeq[:4]) // mark split across two appends
		time.Sleep(40 * time.Millisecond)
		appendRaw(t, j, markSeq[4:])
	}()

	res, err := WaitCompletion(j, from, core.ModeHooks, 5*time.Second, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitCompletion: %v", err)
	}
	<-done

	if res.Outcome != OutcomeDone {
		t.Fatalf("outcome = %v, want Done", res.Outcome)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	wantMarkStart := from + int64(len(output))
	if res.MarkStart != wantMarkStart {
		t.Fatalf("MarkStart = %d, want %d", res.MarkStart, wantMarkStart)
	}
	if res.EndOffset != wantMarkStart+int64(len(markSeq)) {
		t.Fatalf("EndOffset = %d, want %d", res.EndOffset, wantMarkStart+int64(len(markSeq)))
	}
	// The output region [from, MarkStart) must be exactly the command output.
	region, err := j.ReadRange(from, res.MarkStart)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if string(region) != string(output) {
		t.Fatalf("output region = %q, want %q", region, output)
	}
}

func TestWaitCompletionTimeout(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("$ echo hi\nhi\n")) // no completion mark
	res, err := WaitCompletion(j, 0, core.ModeHooks, 150*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitCompletion: %v", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("outcome = %v, want Timeout", res.Outcome)
	}
	if res.MarkStart != -1 || res.ExitCode != -1 {
		t.Fatalf("timeout result = %+v, want MarkStart=-1 ExitCode=-1", res)
	}
}

func TestWaitCommandReturnsQuietPromptBeforeTimeout(t *testing.T) {
	j := mustOpen(t)
	if err := os.WriteFile(j.RawPath(), []byte("Continue [y/N]? "), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Second)
	if err := os.Chtimes(j.RawPath(), old, old); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	res, err := WaitCommand(j, 0, core.ModeHooks, 5*time.Second, 20*time.Millisecond, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAwaitingInput || res.Prompt.Line != "Continue [y/N]?" {
		t.Fatalf("result = %+v, want awaiting-input prompt", res)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("prompt-aware wait took %v, want well before command timeout", elapsed)
	}
}

// TestWaitCompletionModeSelect confirms sentinel mode ignores a D mark and
// waits for the sentinel; hooks mode does the reverse.
func TestWaitCompletionModeSelect(t *testing.T) {
	build := func() (content []byte, dStart, sentStart int64) {
		content = append(content, []byte("output\n")...)
		dStart = int64(len(content))
		content = append(content, buildOSC("133;D;9", "bel")...)
		sentStart = int64(len(content))
		content = append(content, buildOSC("7779;p;3", "bel")...)
		return
	}

	t.Run("hooks stops at D", func(t *testing.T) {
		j := mustOpen(t)
		content, dStart, _ := build()
		writeRawFile(t, j, content)
		res, err := WaitCompletion(j, 0, core.ModeHooks, 2*time.Second, 20*time.Millisecond)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if res.Outcome != OutcomeDone || res.ExitCode != 9 || res.MarkStart != dStart {
			t.Fatalf("res = %+v, want Done exit=9 start=%d", res, dStart)
		}
	})

	t.Run("sentinel stops at sentinel", func(t *testing.T) {
		j := mustOpen(t)
		content, _, sentStart := build()
		writeRawFile(t, j, content)
		res, err := WaitCompletion(j, 0, core.ModeSentinel, 2*time.Second, 20*time.Millisecond)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if res.Outcome != OutcomeDone || res.ExitCode != 3 || res.MarkStart != sentStart {
			t.Fatalf("res = %+v, want Done exit=3 start=%d", res, sentStart)
		}
	})
}

func TestWaitReadyHooksGetsA(t *testing.T) {
	j := mustOpen(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		appendRaw(t, j, []byte("welcome\n"))
		appendRaw(t, j, buildOSC("133;A", "bel"))
	}()
	got, err := WaitReady(j, core.ModeHooks, 3*time.Second)
	<-done
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if !got {
		t.Fatalf("WaitReady hooks = false, want true (MarkA seen)")
	}
}

func TestWaitReadyHooksTimeoutDegrades(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("no marks here\n"))
	got, err := WaitReady(j, core.ModeHooks, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if got {
		t.Fatalf("WaitReady hooks = true, want false (no MarkA -> caller degrades)")
	}
}

func TestWaitReadySentinelQuiesces(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("prompt$ "))
	start := time.Now()
	got, err := WaitReady(j, core.ModeSentinel, 5*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if got {
		t.Fatalf("WaitReady sentinel = true, want false")
	}
	// It should return after the ~800ms idle window, well before the timeout.
	if elapsed > 3*time.Second {
		t.Fatalf("WaitReady sentinel took %v, expected to quiesce near 800ms", elapsed)
	}
}

func TestQuiesceDetectsIdle(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("x"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			appendRaw(t, j, []byte("y"))
			time.Sleep(30 * time.Millisecond)
		}
	}()
	quiet, err := Quiesce(j, 200*time.Millisecond, 5*time.Second)
	<-done
	if err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if !quiet {
		t.Fatalf("Quiesce = false, want true (writer stopped)")
	}
}

func TestQuiesceTimeout(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("x"))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			appendRaw(t, j, []byte("z"))
			time.Sleep(20 * time.Millisecond)
		}
	}()
	// Idle window longer than the timeout and a writer that never stops in time:
	// must report not-quiet.
	quiet, err := Quiesce(j, 600*time.Millisecond, 400*time.Millisecond)
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if quiet {
		t.Fatalf("Quiesce = true, want false (never idle before timeout)")
	}
}

// index event fixtures. offset 5 means the command was sent at raw.log offset 5.
const evStart = `{"ts":"2026-07-18T10:00:00Z","type":"cmd_start","cmd_id":1,"offset":5,"text":"cmd"}`
const evEnd = `{"ts":"2026-07-18T10:00:01Z","type":"cmd_end","cmd_id":1,"exit_code":0}`

func TestDeriveStatus(t *testing.T) {
	markD := string(buildOSC("133;D;0", "bel"))
	markSent := string(buildOSC("7779;p;0", "bel"))

	tests := []struct {
		name   string
		alive  bool
		mode   core.Mode
		index  string
		raw    string
		rawOld bool // backdate raw.log mtime by 2s
		noRaw  bool // do not create raw.log
		want   core.Status
	}{
		{"dead overrides pending", false, core.ModeHooks, evStart + "\n", "PROMPrunning", false, false, core.StatusDead},
		{"hooks settled idle", true, core.ModeHooks, evStart + "\n" + evEnd + "\n", "PROMP", false, false, core.StatusIdle},
		{"hooks no events idle", true, core.ModeHooks, "", "", false, true, core.StatusIdle},
		{"hooks pending running", true, core.ModeHooks, evStart + "\n", "PROMPstill working", false, false, core.StatusRunning},
		{"hooks pending finished settles idle", true, core.ModeHooks, evStart + "\n", "PROMPdone\n" + markD, false, false, core.StatusIdle},
		{"sentinel pending running", true, core.ModeSentinel, evStart + "\n", "PROMPworking", false, false, core.StatusRunning},
		{"sentinel pending finished idle", true, core.ModeSentinel, evStart + "\n", "PROMPdone\n" + markSent, false, false, core.StatusIdle},
		{"sentinel pending needs sentinel not D", true, core.ModeSentinel, evStart + "\n", "PROMPdone\n" + markD, false, false, core.StatusRunning},
		{"sentinel no pending fresh unknown", true, core.ModeSentinel, "", "recent activity", false, false, core.StatusUnknown},
		{"sentinel no pending old idle", true, core.ModeSentinel, "", "old activity", true, false, core.StatusIdle},
		{"sentinel no pending no raw idle", true, core.ModeSentinel, "", "", false, true, core.StatusIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := mustOpen(t)
			if tt.index != "" {
				writeIndexFile(t, j, tt.index)
			}
			if !tt.noRaw {
				writeRawFile(t, j, []byte(tt.raw))
				if tt.rawOld {
					old := time.Now().Add(-2 * time.Second)
					if err := os.Chtimes(j.RawPath(), old, old); err != nil {
						t.Fatalf("chtimes: %v", err)
					}
				}
			}
			if got := DeriveStatus(j, tt.alive, tt.mode); got != tt.want {
				t.Fatalf("DeriveStatus = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDeriveStatusReadOnly guards the read-only contract: computing status must
// not append any events.
func TestDeriveStatusReadOnly(t *testing.T) {
	j := mustOpen(t)
	writeIndexFile(t, j, evStart+"\n")
	writeRawFile(t, j, []byte("PROMPrunning"))

	before, err := j.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	_ = DeriveStatus(j, true, core.ModeHooks)
	_ = DeriveStatus(j, true, core.ModeSentinel)
	after, err := j.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("DeriveStatus wrote events: before=%d after=%d", len(before), len(after))
	}
}
