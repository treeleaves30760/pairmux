package tmux

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/mux"
)

func TestParseScreen(t *testing.T) {
	s, err := parseScreen("97 34 1 0 1 0 0 220x50\n")
	want := mux.ScreenInfo{Cols: 97, Rows: 34, AltScreen: true, CursorHidden: true, Mouse: true, Fitted: true}
	if err != nil || s != want {
		t.Fatalf("parseScreen = %+v, %v", s, err)
	}
	if s, err := parseScreen("220 50 0 1 0 2 0 \n"); err != nil || s.Fitted || s.Attached != 2 || s.CursorHidden {
		t.Fatalf("unfitted = %+v, %v", s, err)
	}
	if _, err := parseScreen("220 50 0 1 0 0 1"); err == nil {
		t.Fatal("a dead pane parsed")
	}
}

// The first fit records the size from before it; a renewal only claims the
// fit; nothing is resized while a client is attached.
func TestFitArgv(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	r := &recRunner{outs: []string{"220 50 0 0 0 0 0 \n", ""}}
	if err := newClient(r).FitPane("%3", 97, 34); err != nil {
		t.Fatal(err)
	}
	want := []string{"tmux", "-L", "pairmux", "set-option", "-w", "-t", "%3", "@pairmux_fit", "220x50", ";",
		"set-option", "-w", "-t", "%3", "@pairmux_fit_pid", pid, ";",
		"resize-window", "-t", "%3", "-x", "97", "-y", "34"}
	if !reflect.DeepEqual(r.calls[1], want) {
		t.Fatalf("first fit argv\n got %q\nwant %q", r.calls[1], want)
	}

	r = &recRunner{outs: []string{"97 34 0 0 0 0 0 220x50\n", ""}}
	if err := newClient(r).FitPane("%3", 97, 34); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tmux", "-L", "pairmux", "set-option", "-w", "-t", "%3", "@pairmux_fit_pid", pid}; !reflect.DeepEqual(r.calls[1], want) {
		t.Fatalf("renewal argv %q", r.calls[1])
	}

	r = &recRunner{outs: []string{"150 40 0 0 0 1 0 \n"}}
	if err := newClient(r).FitPane("%3", 97, 34); !errors.Is(err, mux.ErrPaneWatched) || len(r.calls) != 1 {
		t.Fatalf("attached: %v after %d calls", err, len(r.calls))
	}

	r = &recRunner{outs: []string{"220 50 0 0 0 0 0 \n"}, errs: []error{nil, errors.New("tmux: unknown command: resize-window")}}
	if err := newClient(r).FitPane("%3", 97, 34); !errors.Is(err, mux.ErrFitUnsupported) {
		t.Fatalf("old tmux: %v", err)
	}
}

func TestUnfitArgv(t *testing.T) {
	unset := []string{"set-option", "-w", "-u", "-t", "%3", "window-size", ";",
		"set-option", "-w", "-u", "-t", "%3", "@pairmux_fit", ";",
		"set-option", "-w", "-u", "-t", "%3", "@pairmux_fit_pid"}
	r := &recRunner{outs: []string{"0 220x50\n", ""}}
	if err := newClient(r).UnfitPane("%3"); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"tmux", "-L", "pairmux", "resize-window", "-t", "%3", "-x", "220", "-y", "50", ";"}, unset...)
	if !reflect.DeepEqual(r.calls[1], want) {
		t.Fatalf("unfit argv\n got %q\nwant %q", r.calls[1], want)
	}
	// Attached meanwhile: only hand the size back to the client.
	r = &recRunner{outs: []string{"1 220x50\n", ""}}
	if err := newClient(r).UnfitPane("%3"); err != nil {
		t.Fatal(err)
	}
	if want := append([]string{"tmux", "-L", "pairmux"}, unset...); !reflect.DeepEqual(r.calls[1], want) {
		t.Fatalf("attached unfit argv %q", r.calls[1])
	}
	// Not fitted: nothing to do.
	r = &recRunner{outs: []string{"0 \n"}}
	if err := newClient(r).UnfitPane("%3"); err != nil || len(r.calls) != 1 {
		t.Fatalf("unfitted: %v, %d calls", err, len(r.calls))
	}
}

func TestReleaseFitsKeepsLiveOwners(t *testing.T) {
	r := &recRunner{outs: []string{"%1 100 220x50\n%2  \n%3 200 220x50\n", "0 220x50\n", ""}}
	err := newClient(r).ReleaseFits(func(pid int) bool { return pid == 100 })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || r.calls[1][len(r.calls[1])-1] != "#{session_attached} #{@pairmux_fit}" || r.calls[1][6] != "%3" {
		t.Fatalf("calls %q", r.calls)
	}
}
