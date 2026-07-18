package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/tmux"
	"github.com/treeleaves30760/pairmux/internal/version"
)

func TestStripGlobals(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantRest []string
		wantJSON bool
		wantSock string
	}{
		{"none", []string{"ls"}, []string{"ls"}, false, ""},
		{"json before", []string{"--json", "ls"}, []string{"ls"}, true, ""},
		{"json after", []string{"ls", "--json"}, []string{"ls"}, true, ""},
		{"json deep", []string{"run", "t", "echo", "hi", "--json"}, []string{"run", "t", "echo", "hi"}, true, ""},
		{"socket space", []string{"--socket", "s1", "ls"}, []string{"ls"}, false, "s1"},
		{"socket eq", []string{"ls", "--socket=s2"}, []string{"ls"}, false, "s2"},
		{"both mixed", []string{"run", "--json", "t", "--socket", "s3", "cmd"}, []string{"run", "t", "cmd"}, true, "s3"},
		{"dashdash guards", []string{"run", "t", "--", "prog", "--json"}, []string{"run", "t", "--", "prog", "--json"}, false, ""},
		{"dangling socket dropped", []string{"ls", "--socket"}, []string{"ls"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, js, sock := stripGlobals(tt.args, false, "")
			if !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tt.wantRest)
			}
			if js != tt.wantJSON {
				t.Errorf("json = %v, want %v", js, tt.wantJSON)
			}
			if sock != tt.wantSock {
				t.Errorf("socket = %q, want %q", sock, tt.wantSock)
			}
		})
	}
}

func TestParseRun(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantTimeout string
		wantHead    string
		wantPos     []string
	}{
		{"simple", []string{"t", "echo", "hi"}, "", "", []string{"t", "echo", "hi"}},
		{"timeout after cmd", []string{"t", "sleep", "3", "--timeout", "1s"}, "1s", "", []string{"t", "sleep", "3"}},
		{"timeout before cmd", []string{"--timeout", "2s", "t", "ls"}, "2s", "", []string{"t", "ls"}},
		{"preserve dash flag", []string{"t", "echo", "-n", "hi"}, "", "", []string{"t", "echo", "-n", "hi"}},
		{"preserve unknown longflag", []string{"t", "git", "--oneline"}, "", "", []string{"t", "git", "--oneline"}},
		{"head eq", []string{"t", "seq", "9", "--head=5"}, "", "5", []string{"t", "seq", "9"}},
		{"dashdash stops extraction", []string{"t", "--", "x", "--timeout", "9s"}, "", "", []string{"t", "x", "--timeout", "9s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			to, head, _, pos, err := parseRun(tt.args)
			if err != nil {
				t.Fatalf("parseRun: %v", err)
			}
			if to != tt.wantTimeout {
				t.Errorf("timeout = %q, want %q", to, tt.wantTimeout)
			}
			if head != tt.wantHead {
				t.Errorf("head = %q, want %q", head, tt.wantHead)
			}
			if !reflect.DeepEqual(pos, tt.wantPos) {
				t.Errorf("pos = %v, want %v", pos, tt.wantPos)
			}
		})
	}
}

func TestParseFlagsUnknown(t *testing.T) {
	var v string
	_, err := parseFlags([]string{"name", "--bogus", "x"}, flagSpec{vals: map[string]*string{"cmd": &v}})
	if err == nil {
		t.Fatal("expected unknown flag error")
	}
}

func TestParseFlagsSeen(t *testing.T) {
	var text string
	var enter bool
	seen := map[string]bool{}
	pos, err := parseFlags([]string{"t", "--text", "", "--enter"}, flagSpec{
		bools: map[string]*bool{"enter": &enter},
		vals:  map[string]*string{"text": &text},
		seen:  seen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"t"}) {
		t.Errorf("pos = %v", pos)
	}
	if !seen["text"] || !seen["enter"] {
		t.Errorf("seen = %v, want text+enter", seen)
	}
	if !enter {
		t.Errorf("enter flag not set")
	}
}

func TestValidKey(t *testing.T) {
	good := []string{"Enter", "Escape", "Tab", "Space", "Up", "Down", "Left", "Right",
		"Home", "End", "PPage", "NPage", "BSpace", "DC", "F1", "F9", "F12", "C-a", "C-z", "M-a", "M-x"}
	for _, k := range good {
		if !validKey(k) {
			t.Errorf("validKey(%q) = false, want true", k)
		}
	}
	bad := []string{"", "enter", "F0", "F13", "C-A", "C-1", "M-1", "Ctrl-c", "ping", "C-"}
	for _, k := range bad {
		if validKey(k) {
			t.Errorf("validKey(%q) = true, want false", k)
		}
	}
}

func TestLastLines(t *testing.T) {
	body, omitted := lastLines("a\nb\nc\nd\ne", 2)
	if body != "d\ne" || omitted != 3 {
		t.Errorf("lastLines = %q, %d", body, omitted)
	}
	body, omitted = lastLines("a\nb", 5)
	if body != "a\nb" || omitted != 0 {
		t.Errorf("lastLines short = %q, %d", body, omitted)
	}
}

// newTestCtx returns a Ctx writing JSON into buf. The tmux client points at a
// socket that is never contacted (these cases fail before any tmux call).
func newTestCtx(buf *bytes.Buffer, jsonMode bool) *Ctx {
	return &Ctx{Tmux: tmux.New("pmx-unit-nonexistent"), JSON: jsonMode, StateDir: "/nonexistent", Stdout: buf}
}

func decode(t *testing.T, buf *bytes.Buffer) output.Envelope {
	t.Helper()
	var e output.Envelope
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return e
}

func TestRunRejectsNewline(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	rc := c.cmdRun([]string{"t", "echo\nrm"})
	if rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Errorf("envelope = %+v", e)
	}
	if !strings.Contains(e.Error.Hint, "pairmux send") {
		t.Errorf("hint = %q", e.Error.Hint)
	}
}

func TestNewRejectsBadName(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	rc := c.cmdNew([]string{"--name", "Bad Name"})
	if rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error.Code != output.CodeBadArgs {
		t.Errorf("envelope = %+v", e)
	}
}

func TestSendRequiresAction(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	rc := c.cmdSend([]string{"t"})
	if rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error.Code != output.CodeBadArgs {
		t.Errorf("envelope = %+v", e)
	}
}

func TestSendRejectsBadKey(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	rc := c.cmdSend([]string{"t", "--key", "Ctrl-c"})
	if rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Errorf("envelope = %+v", e)
	}
}

func TestRunUsageNoArgs(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdRun(nil); rc != 2 {
		t.Errorf("rc = %d, want 2 (usage)", rc)
	}
}

func TestVersionJSON(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdVersion(); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
	e := decode(t, &buf)
	if !e.OK || e.Output != version.Version {
		t.Errorf("envelope = %+v, want output %q", e, version.Version)
	}
}

func TestHelpText(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, false)
	c.printHelp()
	s := buf.String()
	for _, cmd := range []string{"new", "run", "peek", "wait", "send", "log", "ls", "kill",
		"attach", "watch", "note", "doctor", "skill install"} {
		if !strings.Contains(s, cmd) {
			t.Errorf("help missing %q", cmd)
		}
	}
	for _, group := range []string{"agent commands:", "human commands:"} {
		if !strings.Contains(s, group) {
			t.Errorf("help missing group %q", group)
		}
	}
	if !strings.Contains(s, "--json") {
		t.Errorf("help should mention --json")
	}
	if lines := strings.Count(s, "\n"); lines > 40 {
		t.Errorf("help has %d lines, want <= 40", lines)
	}
}

func TestUnknownCommandJSON(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.dispatch([]string{"frobnicate"}); rc != 2 {
		t.Errorf("rc = %d, want 2", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil {
		t.Errorf("envelope = %+v", e)
	}
}

func TestUnseenNotes(t *testing.T) {
	ts := func(sec int) time.Time { return time.Unix(int64(sec), 0) }
	ev := func(typ core.EventType, sec int, text string) core.Event {
		return core.Event{Type: typ, TS: ts(sec), Text: text}
	}
	tests := []struct {
		name string
		evs  []core.Event
		want []string
	}{
		{"empty", nil, nil},
		{"no cmd_end keeps all", []core.Event{
			ev(core.EvCreated, 1, ""), ev(core.EvNote, 2, "a"), ev(core.EvNote, 3, "b"),
		}, []string{"a", "b"}},
		{"note before end hidden", []core.Event{
			ev(core.EvNote, 1, "old"), ev(core.EvCmdEnd, 2, ""),
		}, nil},
		{"note after end shown", []core.Event{
			ev(core.EvCmdEnd, 1, ""), ev(core.EvNote, 2, "fresh"),
		}, []string{"fresh"}},
		{"only latest end counts", []core.Event{
			ev(core.EvCmdEnd, 1, ""), ev(core.EvNote, 2, "mid"),
			ev(core.EvCmdEnd, 3, ""), ev(core.EvNote, 4, "late"),
		}, []string{"late"}},
	}
	for _, tt := range tests {
		if got := unseenNotes(tt.evs); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: unseenNotes = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAwaitingNext(t *testing.T) {
	next := awaitingNext("t1", "Continue [y/N]?")
	joined := strings.Join(next, " | ")
	if !strings.Contains(joined, "pairmux send t1 --text") {
		t.Errorf("plain prompt next = %v, want send hint", next)
	}

	next = awaitingNext("t1", "Password:")
	joined = strings.Join(next, " | ")
	if !strings.Contains(joined, "do NOT guess") || !strings.Contains(joined, "wait t1 --human") {
		t.Errorf("secret prompt next = %v", next)
	}
	if strings.Contains(joined, "--text") {
		t.Errorf("secret prompt must not suggest typing: %v", next)
	}
}

func TestParseRangeSpec(t *testing.T) {
	tests := []struct {
		in    string
		a, b  int
		valid bool
	}{
		{"1:5", 1, 5, true},
		{"7:7", 7, 7, true},
		{" 2 : 4 ", 2, 4, true},
		{"5:2", 0, 0, false},
		{"0:3", 0, 0, false},
		{"3", 0, 0, false},
		{"a:b", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tt := range tests {
		a, b, err := parseRange(tt.in)
		if tt.valid != (err == nil) {
			t.Errorf("parseRange(%q) err = %v, want valid=%v", tt.in, err, tt.valid)
			continue
		}
		if tt.valid && (a != tt.a || b != tt.b) {
			t.Errorf("parseRange(%q) = %d,%d, want %d,%d", tt.in, a, b, tt.a, tt.b)
		}
	}
}

func TestAppendGuard(t *testing.T) {
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := appendGuard(nil, j, "t1"); len(got) != 0 {
		t.Errorf("empty journal guard = %v", got)
	}
	// A sparse file makes a >256MB raw.log instantly.
	if err := os.Truncate(j.RawPath(), 300<<20); err != nil {
		t.Skipf("cannot create sparse file: %v", err)
	}
	got := appendGuard([]string{"x"}, j, "t1")
	if len(got) != 2 || !strings.Contains(got[1], "journal is large (300MB)") ||
		!strings.Contains(got[1], "pairmux kill t1") {
		t.Errorf("large journal guard = %v", got)
	}
}

func TestLockHolderPID(t *testing.T) {
	dir := t.TempDir()
	if pid := lockHolderPID(dir); pid != 0 {
		t.Errorf("no lock file: pid = %d, want 0", pid)
	}
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	release, err := j.AcquireWriteLock()
	if err != nil {
		t.Fatal(err)
	}
	if pid := lockHolderPID(dir); pid != os.Getpid() {
		t.Errorf("held lock: pid = %d, want %d", pid, os.Getpid())
	}
	release()
	// The pid text remains in write.lock after release; the flock probe must
	// still report the lock as free.
	if pid := lockHolderPID(dir); pid != 0 {
		t.Errorf("released lock: pid = %d, want 0", pid)
	}
}

func TestLogFlagsMutuallyExclusive(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdLog([]string{"t", "--cmd", "1", "--grep", "x"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (usage)", rc)
	}
	buf.Reset()
	if rc := c.cmdLog([]string{"t", "--grep", "x", "--range", "1:2"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (usage)", rc)
	}
}

func TestKillAllConflictsWithName(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdKill([]string{"--all", "t1"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (usage)", rc)
	}
}

func TestNoteUsage(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdNote([]string{"t1"}); rc != 2 {
		t.Errorf("note with no text: rc = %d, want 2", rc)
	}
}

// TestNotesSurviveLazySettlement reproduces the acceptance ordering: a prior
// command's cmd_start, a human note left while it was unsettled, then run's
// settlement appending cmd_end with a fresh timestamp. The note must still
// surface via the baseline-before-settlement collection cmdRun uses.
func TestNotesSurviveLazySettlement(t *testing.T) {
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Previous command started; the human answered it in the pane and left a
	// note. No cmd_end has been recorded yet.
	must(j.AppendEvent(core.Event{Type: core.EvCmdStart, CmdID: 1, Text: "sudo make install"}))
	must(j.AppendEvent(core.Event{Type: core.EvNote, Text: "answered the password prompt"}))

	// cmdRun's ordering: baseline BEFORE the settlement write...
	base, notes := notesBaseline(j)
	if len(notes) != 1 || notes[0] != "answered the password prompt" {
		t.Fatalf("baseline notes = %v", notes)
	}

	// ...then settlement appends cmd_end stamped now.
	ec := 0
	must(j.AppendEvent(core.Event{Type: core.EvCmdEnd, CmdID: 1, ExitCode: &ec}))

	// The eclipse this fix addresses: a post-settlement TS-based collection
	// misses the note entirely...
	if got := readUnseenNotes(j); len(got) != 0 {
		t.Fatalf("precondition: settlement cmd_end should eclipse the note for late collectors, got %v", got)
	}

	// ...but the envelope built from baseline + arrived-since still carries it.
	envNotes := append(notes, notesArrivedSince(j, base)...)
	if len(envNotes) != 1 || envNotes[0] != "answered the password prompt" {
		t.Fatalf("envelope notes = %v, want the pre-settlement note", envNotes)
	}

	// A note arriving after the baseline (mid-run) is merged in exactly once.
	must(j.AppendEvent(core.Event{Type: core.EvNote, Text: "also check the logs"}))
	envNotes = append(notes[:len(notes):len(notes)], notesArrivedSince(j, base)...)
	if len(envNotes) != 2 || envNotes[1] != "also check the logs" {
		t.Fatalf("envelope notes with mid-run addition = %v", envNotes)
	}
}

func TestSendKeyHints(t *testing.T) {
	// A single printable character leads with the --text fix, then the list.
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSend([]string{"t", "--key", "q"}); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope = %+v", e)
	}
	if !strings.HasPrefix(e.Error.Hint, "use --text q to type the character q (then --enter if needed)") {
		t.Errorf("single-char hint = %q, want the --text fix first", e.Error.Hint)
	}
	if !strings.Contains(e.Error.Hint, "C-a..C-z") {
		t.Errorf("single-char hint should still list valid keys: %q", e.Error.Hint)
	}

	// A multi-char mistake keeps the plain valid-key examples (incl. C-c form).
	buf.Reset()
	if rc := c.cmdSend([]string{"t", "--key", "Ctrl-C"}); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	e = decode(t, &buf)
	if strings.Contains(e.Error.Hint, "use --text") {
		t.Errorf("multi-char hint must not suggest --text: %q", e.Error.Hint)
	}
	if !strings.Contains(e.Error.Hint, "C-a..C-z") {
		t.Errorf("multi-char hint = %q, want the C-a..C-z examples", e.Error.Hint)
	}
}
