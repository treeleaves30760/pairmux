package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// Side by side, the list is the smaller box at every width, within its
// bounds; the preview gets the rest.
func TestListIsTheSmallerSide(t *testing.T) {
	for _, w := range []int{110, 120, 140, 160, 200, 250, 320} {
		lw := listWidth(w)
		if lw < minListW || lw > maxListW {
			t.Errorf("%d cols: list %d outside [%d, %d]", w, lw, minListW, maxListW)
		}
		if lw*100 > w*40 {
			t.Errorf("%d cols: list %d is more than 40%% of the width", w, lw)
		}
		a := &tuiApp{w: w, h: 40}
		if pw, _ := a.previewBox(); pw != w-lw || pw <= lw {
			t.Errorf("%d cols: preview %d, list %d", w, pw, lw)
		}
	}
	// Stacked below 110 columns: both boxes are full width.
	a := &tuiApp{w: 100, h: 40}
	if pw, ph := a.previewBox(); pw != 100 || ph != a.bodyHeight()-a.listBoxHeight() {
		t.Errorf("stacked preview box %dx%d", pw, ph)
	}
}

func TestStatusShortLabels(t *testing.T) {
	want := map[core.Status]string{core.StatusRunning: "run", core.StatusAwaitingInput: "input",
		core.StatusIdle: "idle", core.StatusDead: "dead"}
	for s, w := range want {
		if got := statusShort(s); got != w || strWidth(got) >= statusW {
			t.Errorf("%s: %q", s, got)
		}
	}
	a, out := widthApp(t, 160, 20)
	a.draw()
	lines := frameLines(out)
	frame := strings.Join(lines, "\n")
	if !strings.Contains(frame, "input") || !strings.Contains(frame, "◆ 1 needs input") {
		t.Fatalf("short label in the list, full words in the header expected:\n%s", frame)
	}
	for _, l := range lines[2:9] { // the list rows: no long words
		if left := l[:strings.Index(l, "││")]; strings.Contains(left, "running") || strings.Contains(left, "needs input") {
			t.Errorf("long status word in the list: %q", left)
		}
	}
}

// previewRow is the i-th body row of the preview box (right of the list).
func previewRow(lines []string, i int) string {
	l := lines[2+i]
	side := gl("│", "|")
	return l[strings.Index(l, side+side)+len(side):]
}

// listPart is a wide-layout row left of the preview box.
func listPart(l string) string {
	side := gl("│", "|")
	return l[:strings.Index(l, side+side)]
}

func TestPreviewShowsTheCommandOnOneLine(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	for _, mode := range []string{"narrow", "wide"} {
		f := newFakeTerm(mode)
		ct, _ := detectCells(f, f.in)
		withCells(t, ct)
		a, out := widthApp(t, 140, 24)
		a.nodes[0].Pending = "go test ./internal/cli/... -run 'TestPreviewShowsTheCommandOnOneLine|TestEverything' -count=1 -race -v 測試\nsecond line"
		a.selPath = "boss"
		a.clampSel()
		a.draw()
		lines := frameLines(out)
		cmd, meta := previewRow(lines, 0), previewRow(lines, 1)
		ell := gl("…", "~")
		if !strings.Contains(cmd, "$ go test ./internal/cli/") || !strings.HasSuffix(cmd, ell+gl("│", "|")) {
			t.Errorf("%s: command row %q", mode, cmd)
		}
		if strings.Contains(strings.Join(lines, "\n"), "second line") {
			t.Errorf("%s: a second command line was drawn", mode)
		}
		if !strings.Contains(meta, "created 2s ago · bash") {
			t.Errorf("%s: meta row %q", mode, meta)
		}
		if got := lineCells(mode == "wide", lines[2]); got != 140 {
			t.Errorf("%s: command row is %d cells", mode, got)
		}
		// The list keeps only a teaser of it, after name, state and age.
		row := listPart(lines[3])
		if !strings.Contains(row, "go te") || strings.Contains(row, "-count") {
			t.Errorf("%s: list teaser %q", mode, row)
		}
	}
	// A short command is shown whole; no command says so.
	a, out := widthApp(t, 140, 24)
	a.nodes[0].Pending = "make"
	a.draw()
	if cmd := previewRow(frameLines(out), 0); !strings.HasPrefix(cmd, gl("│", "|")+" $ make  ") {
		t.Errorf("short command row %q", cmd)
	}
	a.nodes[0].Pending = ""
	a.draw()
	if cmd := previewRow(frameLines(out), 0); !strings.Contains(cmd, "no command running") {
		t.Errorf("idle command row %q", cmd)
	}
	// A program terminal never has a command line; it is not called idle.
	a.nodes[0].Term.Meta.Name = "claude"
	a.draw()
	if cmd := previewRow(frameLines(out), 0); !strings.Contains(cmd, "runs its own program") {
		t.Errorf("program terminal command row %q", cmd)
	}
}

// The teaser is dropped rather than squeezed: name, state and age keep their
// cells at the narrowest list.
func TestListTeaserNeverSqueezesTheColumns(t *testing.T) {
	a, out := widthApp(t, 110, 20)
	a.nodes[0].Pending = strings.Repeat("x", 200)
	a.nodes[0].Term.Name = "bossbossbossbossbossboss"
	a.selPath = "zed"
	a.clampSel()
	a.draw()
	lines := frameLines(out)
	head := lines[2][:strings.Index(lines[2], "││")]
	row := lines[3][:strings.Index(lines[3], "││")]
	for _, col := range []string{"state", "age"} {
		if c := strings.Index(head, col); c < 0 {
			t.Fatalf("header lost %q: %q", col, head)
		}
	}
	if !strings.Contains(row, "run ") || !strings.Contains(row, "bossboss") {
		t.Fatalf("row %q", row)
	}
}

func TestMarqueeStepsAndWraps(t *testing.T) {
	name := "worker-with-a-long-name"
	if got := marquee(name, 30, 0); got != pad(name, 30) {
		t.Fatalf("a name that fits is padded: %q", got)
	}
	w := 10
	at := func(steps int) string { return marquee(name, w, marqueePause+time.Duration(steps)*marqueeStep) }
	if got := marquee(name, w, 0); got != "worker-wit" {
		t.Fatalf("at rest: %q", got)
	}
	if got := marquee(name, w, marqueePause-time.Millisecond); got != "worker-wit" {
		t.Fatalf("still resting: %q", got)
	}
	if got := at(1); got != "orker-with" {
		t.Fatalf("one step: %q", got)
	}
	if got := at(len(name) - 2); got != "me   worke" {
		t.Fatalf("wrapping: %q", got)
	}
	n := len(name) + len(marqueeGap)
	if got := at(n); got != "worker-wit" {
		t.Fatalf("a full cycle returns to the start: %q", got)
	}
	if got := at(n + 3); got != "worker-wit" {
		t.Fatalf("and rests there again: %q", got)
	}
	// Wide runes cut by an edge become spaces; every frame is w cells.
	cjk := "工作者的名字很長很長"
	for s := 0; s < 2*strWidth(cjk); s++ {
		for _, w := range []int{5, 6, 9} {
			got := marquee(cjk, w, marqueePause+time.Duration(s)*marqueeStep)
			if strWidth(got) != w {
				t.Fatalf("step %d width %d: %q is %d cells", s, w, got, strWidth(got))
			}
		}
	}
}

// The selected row's long name scrolls, others are clipped; every frame of
// the scroll keeps every row exactly the terminal's width, and the scroll
// restarts when the selection moves.
func TestMarqueeFramesFitAndReset(t *testing.T) {
	t.Setenv(AmbiguousWidthEnv, "")
	for _, mode := range []string{"narrow", "wide"} {
		f := newFakeTerm(mode)
		ct, _ := detectCells(f, f.in)
		withCells(t, ct)
		for _, w := range []int{80, 110, 150} {
			a, out := widthApp(t, w, 30)
			a.nodes[3].Term.Name = "beta-有一個非常非常長的名字-and-more-and-more-and-more-of-it"
			a.nodes[6].Term.Name = "zed-also-has-a-rather-long-name-that-goes-on-and-on-and-on-indeed"
			a.selPath = "boss/beta"
			a.clampSel()
			a.draw()
			frameLines(out)
			if !a.marqueeLive {
				t.Fatalf("%s %d: nothing scrolls", mode, w)
			}
			start := a.marqueeAt
			var frames []string
			for s := 0; s < 60; s++ {
				a.marqueeAt = start.Add(-marqueePause - time.Duration(s)*marqueeStep)
				a.draw()
				lines := frameLines(out)
				for i, l := range lines {
					if got := lineCells(mode == "wide", l); got != w {
						t.Fatalf("%s %d step %d: row %d is %d cells:\n%q", mode, w, s, i, got, l)
					}
				}
				frames = append(frames, strings.Join(lines, "\n"))
			}
			if frames[0] == frames[5] {
				t.Errorf("%s %d: the name did not move", mode, w)
			}
			if !strings.Contains(frames[0], "zed-also") || strings.Contains(frames[0], "long-name-indeed") {
				t.Errorf("%s %d: an unselected long name is not clipped:\n%s", mode, w, frames[0])
			}
			a.move(1) // to solo: nothing to scroll there
			a.draw()
			frameLines(out)
			if a.marqueeLive || a.marqueeRow != "solo" {
				t.Errorf("%s %d: marquee live %v on %q after moving", mode, w, a.marqueeLive, a.marqueeRow)
			}
			before := time.Now()
			a.move(2) // to zed: starts again from rest
			a.draw()
			frameLines(out)
			if !a.marqueeLive || a.marqueeAt.Before(before) {
				t.Errorf("%s %d: marquee did not restart on zed", mode, w)
			}
		}
	}
}
