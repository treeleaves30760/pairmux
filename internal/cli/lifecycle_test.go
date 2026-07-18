package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
)

type testCloser struct {
	err    error
	closed *bool
}

func (c testCloser) Close() error {
	if c.closed != nil {
		*c.closed = true
	}
	return c.err
}

func TestPrepareTerminalJournal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "terminal")
	j, err := prepareTerminalJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{dir, 0o700},
		{j.RawPath(), 0o600},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != check.mode {
			t.Errorf("mode %s = %o, want %o", check.path, got, check.mode)
		}
	}
}

func TestPrepareTerminalJournalPropagatesEveryOperationFailure(t *testing.T) {
	chmodErr := errors.New("chmod failed")
	openErr := errors.New("open failed")
	closeErr := errors.New("close failed")
	tests := []struct {
		name       string
		want       error
		wantOpened bool
		wantClosed bool
	}{
		{"chmod", chmodErr, false, false},
		{"open raw", openErr, true, false},
		{"close raw", closeErr, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := false
			closed := false
			ops := terminalJournalOps{
				chmod: func(string, os.FileMode) error {
					if errors.Is(tt.want, chmodErr) {
						return chmodErr
					}
					return nil
				},
				openRaw: func(string, int, os.FileMode) (closeOnly, error) {
					opened = true
					if errors.Is(tt.want, openErr) {
						return nil, openErr
					}
					return testCloser{err: closeErr, closed: &closed}, nil
				},
			}
			j, err := prepareTerminalJournalWith(filepath.Join(t.TempDir(), "terminal"), ops)
			if j != nil {
				t.Fatalf("journal = %+v, want nil on preparation failure", j)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want it to wrap %v", err, tt.want)
			}
			if opened != tt.wantOpened || closed != tt.wantClosed {
				t.Errorf("opened,closed = %v,%v, want %v,%v", opened, closed, tt.wantOpened, tt.wantClosed)
			}
		})
	}
}

func TestRecordAndSendCommandClosesFailedDispatch(t *testing.T) {
	literalErr := errors.New("send literal failed")
	enterErr := errors.New("send enter failed")
	tests := []struct {
		name       string
		literalErr error
		enterErr   error
		wantStage  runSendStage
		wantCalls  []string
	}{
		{"literal", literalErr, nil, runSendLiteral, []string{"literal"}},
		{"enter", nil, enterErr, runSendEnter, []string{"literal", "enter"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := journal.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var calls []string
			err = recordAndSendCommand(j, core.Event{
				Type: core.EvCmdStart, CmdID: 7, Offset: 11, Text: "make",
			}, func() error {
				calls = append(calls, "literal")
				return tt.literalErr
			}, func() error {
				calls = append(calls, "enter")
				return tt.enterErr
			})
			var sendErr *runSendError
			if !errors.As(err, &sendErr) || sendErr.Stage != tt.wantStage {
				t.Fatalf("err = %#v, want runSendError stage %q", err, tt.wantStage)
			}
			if sendErr.AbortErr != nil {
				t.Fatalf("abort err = %v", sendErr.AbortErr)
			}
			if strings.Join(calls, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("calls = %v, want %v", calls, tt.wantCalls)
			}
			if pending, ok, err := j.PendingCmd(); err != nil || ok {
				t.Fatalf("PendingCmd = %+v,%v,%v, want no pending command", pending, ok, err)
			}
			events, err := j.Events()
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[0].Type != core.EvCmdStart || events[1].Type != core.EvCmdEnd {
				t.Fatalf("events = %+v, want cmd_start then cmd_end", events)
			}
			if events[1].CmdID != 7 || events[1].ExitCode == nil || *events[1].ExitCode != -1 {
				t.Errorf("abort event = %+v, want cmd 7 exit -1", events[1])
			}
		})
	}
}

func TestRecordAndSendCommandDoesNotSendAfterRecordFailure(t *testing.T) {
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(j.Dir, "index.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	called := false
	err = recordAndSendCommand(j, core.Event{Type: core.EvCmdStart, CmdID: 1}, func() error {
		called = true
		return nil
	}, func() error {
		called = true
		return nil
	})
	var sendErr *runSendError
	if !errors.As(err, &sendErr) || sendErr.Stage != runSendRecord {
		t.Fatalf("err = %#v, want record-stage error", err)
	}
	if called {
		t.Fatal("tmux send called after cmd_start could not be recorded")
	}
}

func TestRunSendEnterHintIsActionable(t *testing.T) {
	hint := runSendEnterHint("build")
	for _, want := range []string{"pairmux send build --key C-c", "retry", "buffered"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
}
