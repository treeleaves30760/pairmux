//go:build unix

package journal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

func intp(i int) *int { return &i }

// writeRaw puts content into a journal's raw.log.
func writeRaw(t *testing.T, j *Journal, content string) {
	t.Helper()
	if err := os.WriteFile(j.RawPath(), []byte(content), 0o644); err != nil {
		t.Fatalf("write raw.log: %v", err)
	}
}

func newJournal(t *testing.T) *Journal {
	t.Helper()
	j, err := Open(filepath.Join(t.TempDir(), "term"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return j
}

func TestOpenCreatesDir(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c")
	j, err := Open(nested)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if fi, err := os.Stat(nested); err != nil || !fi.IsDir() {
		t.Fatalf("Open did not create dir: err=%v", err)
	}
	if j.RawPath() != filepath.Join(nested, "raw.log") {
		t.Fatalf("RawPath = %q", j.RawPath())
	}
}

func TestSize(t *testing.T) {
	j := newJournal(t)
	if got := j.Size(); got != 0 {
		t.Fatalf("Size on missing = %d, want 0", got)
	}
	writeRaw(t, j, "hello")
	if got := j.Size(); got != 5 {
		t.Fatalf("Size = %d, want 5", got)
	}
}

func TestReadRangeClamping(t *testing.T) {
	j := newJournal(t)
	writeRaw(t, j, "0123456789") // 10 bytes

	tests := []struct {
		name     string
		from, to int64
		want     string
	}{
		{"whole explicit", 0, 10, "0123456789"},
		{"middle", 2, 5, "234"},
		{"to EOF via -1", 0, -1, "0123456789"},
		{"from mid to EOF", 5, -1, "56789"},
		{"to clamps to size", 5, 100, "56789"},
		{"from clamps negative", -3, 4, "0123"},
		{"from beyond size", 100, 200, ""},
		{"inverted range", 8, 3, ""},
		{"empty at eof", 10, 10, ""},
		{"single byte", 0, 1, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := j.ReadRange(tt.from, tt.to)
			if err != nil {
				t.Fatalf("ReadRange(%d,%d): %v", tt.from, tt.to, err)
			}
			if string(got) != tt.want {
				t.Fatalf("ReadRange(%d,%d) = %q, want %q", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestReadRangeMissingFile(t *testing.T) {
	j := newJournal(t)
	got, err := j.ReadRange(0, -1)
	if err != nil {
		t.Fatalf("ReadRange on missing raw.log: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadRange on missing = %q, want empty", got)
	}
}

func TestTailBytes(t *testing.T) {
	j := newJournal(t)
	writeRaw(t, j, "0123456789")

	tests := []struct {
		name      string
		max       int64
		wantData  string
		wantStart int64
	}{
		{"tail 4", 4, "6789", 6},
		{"tail exceeds size", 20, "0123456789", 0},
		{"tail equals size", 10, "0123456789", 0},
		{"tail zero returns whole", 0, "0123456789", 0},
		{"tail 1", 1, "9", 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, start, err := j.TailBytes(tt.max)
			if err != nil {
				t.Fatalf("TailBytes(%d): %v", tt.max, err)
			}
			if string(data) != tt.wantData || start != tt.wantStart {
				t.Fatalf("TailBytes(%d) = (%q,%d), want (%q,%d)", tt.max, data, start, tt.wantData, tt.wantStart)
			}
		})
	}
}

func TestLastModified(t *testing.T) {
	j := newJournal(t)
	if _, ok := j.LastModified(); ok {
		t.Fatalf("LastModified ok=true on missing raw.log")
	}
	writeRaw(t, j, "x")
	mt, ok := j.LastModified()
	if !ok {
		t.Fatalf("LastModified ok=false after write")
	}
	if time.Since(mt) > time.Minute {
		t.Fatalf("LastModified too old: %v", mt)
	}
}

func TestEventRoundTrip(t *testing.T) {
	j := newJournal(t)
	base := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	in := []core.Event{
		{TS: base, Type: core.EvCreated, Text: "created"},
		{TS: base.Add(time.Second), Type: core.EvCmdStart, CmdID: 1, Offset: 0, Text: "echo hi"},
		{TS: base.Add(2 * time.Second), Type: core.EvCmdEnd, CmdID: 1, ExitCode: intp(0)},
	}
	for _, ev := range in {
		if err := j.AppendEvent(ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	got, err := j.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("got %d events, want %d", len(got), len(in))
	}
	for i := range in {
		if !eventsEqual(in[i], got[i]) {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], in[i])
		}
	}
}

func eventsEqual(a, b core.Event) bool {
	if !a.TS.Equal(b.TS) || a.Type != b.Type || a.CmdID != b.CmdID || a.Offset != b.Offset || a.Text != b.Text {
		return false
	}
	switch {
	case a.ExitCode == nil && b.ExitCode == nil:
		return true
	case a.ExitCode != nil && b.ExitCode != nil:
		return *a.ExitCode == *b.ExitCode
	default:
		return false
	}
}

func TestAppendEventDefaultsTS(t *testing.T) {
	j := newJournal(t)
	if err := j.AppendEvent(core.Event{Type: core.EvNote, Text: "n"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	got, err := j.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 || got[0].TS.IsZero() {
		t.Fatalf("expected defaulted TS, got %+v", got)
	}
}

// writeIndex writes literal index.jsonl content (used for corrupt-line cases).
func writeIndex(t *testing.T, j *Journal, content string) {
	t.Helper()
	if err := os.WriteFile(j.indexPath(), []byte(content), 0o644); err != nil {
		t.Fatalf("write index.jsonl: %v", err)
	}
}

const cmdStart1 = `{"ts":"2026-07-18T10:00:00Z","type":"cmd_start","cmd_id":1,"offset":0,"text":"a"}`
const cmdEnd1 = `{"ts":"2026-07-18T10:00:01Z","type":"cmd_end","cmd_id":1,"exit_code":0}`
const cmdStart2 = `{"ts":"2026-07-18T10:00:02Z","type":"cmd_start","cmd_id":2,"offset":10,"text":"b"}`
const cmdEnd2 = `{"ts":"2026-07-18T10:00:03Z","type":"cmd_end","cmd_id":2,"exit_code":1}`
const cmdStart5 = `{"ts":"2026-07-18T10:00:04Z","type":"cmd_start","cmd_id":5,"offset":20,"text":"c"}`

// garbage lines that Events must skip.
const junk = "not json at all\n{ broken json\n\n   \n"

func TestEventsSkipsCorruptLines(t *testing.T) {
	j := newJournal(t)
	writeIndex(t, j, "garbage\n"+cmdStart1+"\n{bad\n"+cmdEnd1+"\n\n")
	got, err := j.Events()
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (corrupt skipped)", len(got))
	}
	if got[0].Type != core.EvCmdStart || got[1].Type != core.EvCmdEnd {
		t.Fatalf("unexpected events: %+v", got)
	}
}

func TestNextCmdID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 1},
		{"missing file uses default", "", 1},
		{"one start", cmdStart1 + "\n", 2},
		{"start+end", cmdStart1 + "\n" + cmdEnd1 + "\n", 2},
		{"max is 2", cmdStart1 + "\n" + cmdStart2 + "\n", 3},
		{"gap to 5", cmdStart1 + "\n" + cmdStart5 + "\n", 6},
		{"corrupt lines ignored", junk + cmdStart2 + "\n" + junk, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newJournal(t)
			if tt.content != "" {
				writeIndex(t, j, tt.content)
			}
			got, err := j.NextCmdID()
			if err != nil {
				t.Fatalf("NextCmdID: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NextCmdID = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPendingCmd(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantOK     bool
		wantCmdID  int
		wantOffset int64
	}{
		{"empty", "", false, 0, 0},
		{"start only", cmdStart1 + "\n", true, 1, 0},
		{"start+end settled", cmdStart1 + "\n" + cmdEnd1 + "\n", false, 0, 0},
		{"latest start pending", cmdStart1 + "\n" + cmdEnd1 + "\n" + cmdStart2 + "\n", true, 2, 10},
		{"earlier start unsettled", cmdStart1 + "\n" + cmdStart2 + "\n" + cmdEnd2 + "\n", true, 1, 0},
		{"corrupt lines ignored", junk + cmdStart2 + "\n" + junk, true, 2, 10},
		{"corrupt around settled", cmdStart1 + "\n" + junk + cmdEnd1 + "\n", false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newJournal(t)
			if tt.content != "" {
				writeIndex(t, j, tt.content)
			}
			ev, ok, err := j.PendingCmd()
			if err != nil {
				t.Fatalf("PendingCmd: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("PendingCmd ok = %v, want %v", ok, tt.wantOK)
			}
			if ok {
				if ev.CmdID != tt.wantCmdID {
					t.Fatalf("PendingCmd CmdID = %d, want %d", ev.CmdID, tt.wantCmdID)
				}
				if ev.Offset != tt.wantOffset {
					t.Fatalf("PendingCmd Offset = %d, want %d", ev.Offset, tt.wantOffset)
				}
			}
		})
	}
}

func TestMetaRoundTrip(t *testing.T) {
	j := newJournal(t)
	created := time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)
	in := core.Meta{
		Name:      "term",
		PaneID:    "%3",
		Shell:     "zsh",
		Mode:      core.ModeHooks,
		Socket:    "pairmux",
		CreatedAt: created,
	}
	if err := j.WriteMeta(in); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	got, err := j.ReadMeta()
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if got.Name != in.Name || got.PaneID != in.PaneID || got.Shell != in.Shell ||
		got.Mode != in.Mode || got.Socket != in.Socket || !got.CreatedAt.Equal(in.CreatedAt) {
		t.Fatalf("ReadMeta = %+v, want %+v", got, in)
	}
	// meta.json must exist as a real file (temp+rename left no residue).
	if _, err := os.Stat(j.metaPath()); err != nil {
		t.Fatalf("meta.json missing after WriteMeta: %v", err)
	}
	entries, _ := os.ReadDir(j.Dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestReadMetaMissing(t *testing.T) {
	j := newJournal(t)
	if _, err := j.ReadMeta(); err == nil {
		t.Fatalf("ReadMeta on missing meta.json: want error")
	}
}

func TestWriteLockExclusion(t *testing.T) {
	j := newJournal(t)

	release1, err := j.AcquireWriteLock()
	if err != nil {
		t.Fatalf("first AcquireWriteLock: %v", err)
	}

	// LockHolder must read back our own pid while held.
	if pid, ok := LockHolder(j.Dir); !ok || pid != os.Getpid() {
		t.Fatalf("LockHolder = (%d,%v), want (%d,true)", pid, ok, os.Getpid())
	}

	// A second acquisition on a distinct fd (flock is per open file
	// description) must fail with ErrLocked.
	release2, err := j.AcquireWriteLock()
	if err == nil {
		release2()
		t.Fatalf("second AcquireWriteLock succeeded while held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireWriteLock err = %v, want ErrLocked", err)
	}

	// After release, re-acquisition succeeds.
	release1()
	release3, err := j.AcquireWriteLock()
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release3()
	// release is idempotent — a double call must not panic.
	release3()
}

func TestLockHolderMissing(t *testing.T) {
	j := newJournal(t)
	if pid, ok := LockHolder(j.Dir); ok {
		t.Fatalf("LockHolder on missing write.lock = (%d,true), want ok=false", pid)
	}
	// Empty/garbage lock file -> not ok.
	if err := os.WriteFile(j.lockPath(), []byte("  \n"), 0o644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
	if _, ok := LockHolder(j.Dir); ok {
		t.Fatalf("LockHolder on blank write.lock: want ok=false")
	}
}

// TestSendLockExcludesOtherSenders pins the property send relies on: two
// processes delivering input to one pane must not interleave their keystrokes.
func TestSendLockExcludesOtherSenders(t *testing.T) {
	j := newJournalDir(t)
	defer swapSendLockWait(50 * time.Millisecond)()

	release, err := j.AcquireSendLock()
	if err != nil {
		t.Fatalf("first AcquireSendLock: %v", err)
	}
	// A second descriptor on the same file is what a second pairmux process
	// would have, and it must not get in.
	other := &Journal{Dir: j.Dir}
	if _, err := other.AcquireSendLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireSendLock err = %v, want ErrLocked", err)
	}
	release()

	again, err := other.AcquireSendLock()
	if err != nil {
		t.Fatalf("AcquireSendLock after release: %v", err)
	}
	again()
}

// TestSendLockIgnoresWriteLock is the reason it is a separate lock at all. The
// write lock means "a command is running here", which is precisely when an
// interactive answer most needs to get through: if send waited on it, a handoff
// prompt raised by a running command could never be answered.
func TestSendLockIgnoresWriteLock(t *testing.T) {
	j := newJournalDir(t)
	defer swapSendLockWait(50 * time.Millisecond)()

	releaseWrite, err := j.AcquireWriteLock()
	if err != nil {
		t.Fatalf("AcquireWriteLock: %v", err)
	}
	defer releaseWrite()

	releaseSend, err := j.AcquireSendLock()
	if err != nil {
		t.Fatalf("AcquireSendLock while the write lock is held: %v", err)
	}
	releaseSend()
}

// TestSendLockReleaseIsIdempotent matches AcquireWriteLock's contract: cmdSend
// releases through a defer that may also have run on an error path.
func TestSendLockReleaseIsIdempotent(t *testing.T) {
	j := newJournalDir(t)
	release, err := j.AcquireSendLock()
	if err != nil {
		t.Fatalf("AcquireSendLock: %v", err)
	}
	release()
	release()
	again, err := j.AcquireSendLock()
	if err != nil {
		t.Fatalf("re-acquire after a double release: %v", err)
	}
	again()
}

func swapSendLockWait(d time.Duration) func() {
	prev := sendLockWait
	sendLockWait = d
	return func() { sendLockWait = prev }
}

func newJournalDir(t *testing.T) *Journal {
	t.Helper()
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return j
}
