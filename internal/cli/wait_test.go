package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
)

func TestWaitUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"only separators", []string{",", " , "}},
		{"bad idle", []string{"t", "--idle", "abc"}},
		{"zero idle", []string{"t", "--idle", "0"}},
		{"bad timeout", []string{"t", "--timeout", "xyz"}},
		{"unknown flag", []string{"t", "--bogus"}},
		{"idle missing value", []string{"t", "--idle"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := newTestCtx(&buf, true)
			if rc := c.cmdWait(tt.args); rc != 2 {
				t.Fatalf("rc = %d, want 2 (usage); output %s", rc, buf.String())
			}
		})
	}
}

// TestWaitNames pins the fan-out argument grammar: spaces and commas both
// separate, duplicates collapse (arming one terminal twice would poll it twice
// a tick for no gain), and nothing at all is a usage error rather than a wait
// on everything.
func TestWaitRejectsBadNameAmongSeveral(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	// One bad name aborts the whole wait rather than being skipped: a caller
	// that mistyped one of five terminals is not waiting on what it thinks.
	if rc := c.cmdWait([]string{"a,NOT-A-NAME"}); rc != 1 {
		t.Fatalf("rc = %d, want 1; output %s", rc, buf.String())
	}
	if !strings.Contains(buf.String(), "NOT-A-NAME") {
		t.Fatalf("error should name the offending terminal: %s", buf.String())
	}
}

func TestWaitNames(t *testing.T) {
	tests := []struct {
		name    string
		pos     []string
		want    []string
		wantErr bool
	}{
		{"one name", []string{"a"}, []string{"a"}, false},
		{"spaces separate", []string{"a", "b", "c"}, []string{"a", "b", "c"}, false},
		{"commas separate", []string{"a,b,c"}, []string{"a", "b", "c"}, false},
		{"both at once", []string{"a,b", "c"}, []string{"a", "b", "c"}, false},
		{"order is preserved", []string{"c,a", "b"}, []string{"c", "a", "b"}, false},
		{"duplicates collapse", []string{"a,b,a", "b"}, []string{"a", "b"}, false},
		{"padding is trimmed", []string{" a , b "}, []string{"a", "b"}, false},
		{"empty fields are skipped", []string{"a,,b,"}, []string{"a", "b"}, false},
		{"nothing at all", nil, nil, true},
		{"only separators", []string{",", " "}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := waitNames(tt.pos)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("waitNames = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWaitBadPattern(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	rc := c.cmdWait([]string{"t", "--pattern", "("})
	if rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope = %+v, want E_BAD_ARGS", e)
	}
}

func TestMatchShapedPattern(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		re      string
		want    string
		wantHit bool
	}{
		{"two context lines", "a\nb\nc\nd\n", "^c$", "a\nb\nc", true},
		{"match on first line", "hit\nrest\n", "hit", "hit", true},
		{"one context line available", "x\nhit\n", "hit", "x\nhit", true},
		{"ansi stripped before match", "\x1b[31mERROR:\x1b[0m boom\n", "^ERROR: boom$", "ERROR: boom", true},
		{"cr overwrite resolved", "10%\r100%\ndone\n", "^100%$", "100%", true},
		{"earliest match wins", "err one\nmid\nerr two\n", "^err", "err one", true},
		{"no match", "abc\ndef\n", "zzz", "", false},
		{"empty input", "", "x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hit := matchShapedPattern([]byte(tt.raw), regexp.MustCompile(tt.re))
			if hit != tt.wantHit || got != tt.want {
				t.Fatalf("matchShapedPattern = (%q, %v), want (%q, %v)", got, hit, tt.want, tt.wantHit)
			}
		})
	}
}

func TestNewNote(t *testing.T) {
	note := func(text string) core.Event { return core.Event{Type: core.EvNote, Text: text} }
	start := core.Event{Type: core.EvCmdStart, CmdID: 1}

	tests := []struct {
		name    string
		evs     []core.Event
		base    int
		want    string
		wantHit bool
	}{
		{"nil events", nil, 0, "", false},
		{"new note found", []core.Event{note("done reviewing")}, 0, "done reviewing", true},
		{"old note skipped", []core.Event{note("old"), start}, 1, "", false},
		{"note after baseline", []core.Event{start, note("new")}, 1, "new", true},
		{"first new note wins", []core.Event{note("a"), note("b")}, 0, "a", true},
		{"non-note events ignored", []core.Event{start, start}, 0, "", false},
		{"base beyond length clamps", []core.Event{note("x")}, 5, "", false},
		{"negative base clamps", []core.Event{note("y")}, -3, "y", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hit := newNote(tt.evs, tt.base)
			if hit != tt.wantHit || got != tt.want {
				t.Fatalf("newNote = (%q, %v), want (%q, %v)", got, hit, tt.want, tt.wantHit)
			}
		})
	}
}

func TestLatestUnseenNote(t *testing.T) {
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	note := func(sec int, text string) core.Event {
		return core.Event{TS: at(sec), Type: core.EvNote, Text: text}
	}
	end := func(sec int) core.Event { return core.Event{TS: at(sec), Type: core.EvCmdEnd, CmdID: 1} }
	start := func(sec int) core.Event { return core.Event{TS: at(sec), Type: core.EvCmdStart, CmdID: 1} }
	sent := func(sec int) core.Event { return core.Event{TS: at(sec), Type: core.EvSent, Text: "text+enter"} }

	tests := []struct {
		name    string
		evs     []core.Event
		want    string
		wantHit bool
	}{
		{"nil events", nil, "", false},
		{"pre-existing note, no command ever", []core.Event{note(1, "look at this")}, "look at this", true},
		{"latest of several notes wins", []core.Event{note(1, "a"), note(2, "b")}, "b", true},
		{"note consumed by later cmd_end", []core.Event{note(1, "old"), end(2)}, "", false},
		{"note after last cmd_end", []core.Event{end(1), note(2, "fresh")}, "fresh", true},
		{"mixed: only post-end notes count", []core.Event{note(1, "old"), end(2), note(3, "n1"), note(4, "n2")}, "n2", true},
		{"cmd_start does not consume notes", []core.Event{note(1, "kept"), start(2)}, "kept", true},
		{"note at exactly cmd_end TS is seen", []core.Event{note(2, "tied"), end(2)}, "", false},
		{"zero-TS note with no end counts", []core.Event{{Type: core.EvNote, Text: "z"}}, "z", true},
		{"zero-TS note with an end is seen", []core.Event{{Type: core.EvNote, Text: "z"}, end(1)}, "", false},
		{"non-note events only", []core.Event{start(1), end(2)}, "", false},
		// A terminal running a long-lived program never completes a command, so
		// send is the only thing that can mark a note answered. Without these the
		// second --human wait of any driving loop returns the first note again,
		// instantly, for the life of the terminal.
		{"note consumed by a later send", []core.Event{note(1, "old"), sent(2)}, "", false},
		{"note after the last send", []core.Event{sent(1), note(2, "fresh")}, "fresh", true},
		{"send and cmd_end: the later one wins", []core.Event{end(1), note(2, "mid"), sent(3)}, "", false},
		{"cmd_end after send does not un-consume", []core.Event{sent(1), note(2, "kept"), end(1)}, "kept", true},
		{"note at exactly the send TS is seen", []core.Event{note(2, "tied"), sent(2)}, "", false},
		{"repeated sends leave later notes alone", []core.Event{sent(1), sent(2), note(3, "n"), sent(4)}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hit := latestUnseenNote(tt.evs)
			if hit != tt.wantHit || got != tt.want {
				t.Fatalf("latestUnseenNote = (%q, %v), want (%q, %v)", got, hit, tt.want, tt.wantHit)
			}
		})
	}
}

func TestJournalQuiet(t *testing.T) {
	newJ := func(t *testing.T) *journal.Journal {
		t.Helper()
		j, err := journal.Open(filepath.Join(t.TempDir(), "term"))
		if err != nil {
			t.Fatalf("journal.Open: %v", err)
		}
		return j
	}

	t.Run("missing raw.log is quiet", func(t *testing.T) {
		if !journalQuiet(newJ(t), 800*time.Millisecond) {
			t.Fatal("journalQuiet = false, want true for missing raw.log")
		}
	})

	t.Run("future mtime is busy", func(t *testing.T) {
		// A future mtime keeps time.Since below the threshold no matter how
		// slowly a loaded machine runs this test.
		j := newJ(t)
		if err := os.WriteFile(j.RawPath(), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(j.RawPath(), future, future); err != nil {
			t.Fatal(err)
		}
		if journalQuiet(j, 800*time.Millisecond) {
			t.Fatal("journalQuiet = true, want false for fresh activity")
		}
	})

	t.Run("old mtime is quiet", func(t *testing.T) {
		j := newJ(t)
		if err := os.WriteFile(j.RawPath(), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * time.Second)
		if err := os.Chtimes(j.RawPath(), old, old); err != nil {
			t.Fatal(err)
		}
		if !journalQuiet(j, 800*time.Millisecond) {
			t.Fatal("journalQuiet = false, want true for backdated raw.log")
		}
	})
}

// TestRetryHintKeepsConditions: the hint a timed-out wait hands back is the
// command the agent is expected to run next, so it has to be the *same* wait.
// Dropping --human silently downgraded the retry to an idle wait, which returns
// the handoff prompt instantly — the agent's only move was to hand off again.
func TestRetryHintKeepsConditions(t *testing.T) {
	tests := []struct {
		name     string
		seen     map[string]bool
		idle     string
		pattern  string
		human    bool
		done     bool
		note     bool
		notify   bool
		timeout  time.Duration
		wantHint string
	}{
		{
			name: "handoff keeps human and notify and floors at 300s",
			seen: map[string]bool{"human": true, "notify": true}, human: true, notify: true,
			timeout: 3 * time.Second,
			// --notify repeats deliberately: the human did not come, so ping again.
			wantHint: "pairmux wait t1 --human --notify --timeout 5m0s",
		},
		{
			name: "long handoff doubles past the floor",
			seen: map[string]bool{"human": true}, human: true, timeout: 400 * time.Second,
			wantHint: "pairmux wait t1 --human --timeout 13m20s",
		},
		{
			name: "pattern is requoted so the hint is runnable",
			seen: map[string]bool{"pattern": true}, pattern: "error|panic now",
			timeout:  30 * time.Second,
			wantHint: `pairmux wait t1 --pattern 'error|panic now' --timeout 1m0s`,
		},
		{
			name: "subscription keeps done, and no floor applies",
			seen: map[string]bool{"done": true}, done: true, timeout: 10 * time.Second,
			wantHint: "pairmux wait t1 --done --timeout 20s",
		},
		{
			name: "an unrequested idle is not invented",
			seen: map[string]bool{}, timeout: 30 * time.Second,
			wantHint: "pairmux wait t1 --timeout 1m0s",
		},
		{
			name: "an explicit idle survives alongside its racing condition",
			seen: map[string]bool{"idle": true, "pattern": true}, idle: "1500", pattern: "ready",
			timeout:  30 * time.Second,
			wantHint: "pairmux wait t1 --idle 1500 --pattern ready --timeout 1m0s",
		},
		{
			name: "a note wait retries as a note wait",
			seen: map[string]bool{"note": true}, note: true, timeout: 30 * time.Second,
			wantHint: "pairmux wait t1 --note --timeout 1m0s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := waitRetryHint([]string{"t1"},
				retryFlags(tc.seen, tc.idle, tc.pattern, tc.human, tc.done, tc.note, tc.notify),
				retryTimeout(tc.timeout, tc.human))
			if got != tc.wantHint {
				t.Fatalf("hint = %q, want %q", got, tc.wantHint)
			}
		})
	}
}
