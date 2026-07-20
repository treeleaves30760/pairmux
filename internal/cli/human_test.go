package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in        string
		maj, min  int
		ok        bool
		atLeast32 bool
	}{
		{"3.2", 3, 2, true, true},
		{"3.7b", 3, 7, true, true},
		{"3.1a", 3, 1, true, false},
		{"2.9", 2, 9, true, false},
		{"4.0", 4, 0, true, true},
		{"3", 3, 0, true, false},    // 3.0 < 3.2
		{"3.10", 3, 10, true, true}, // multi-digit minor
		{"10.1", 10, 1, true, true}, // multi-digit major
		{"next-3.4", 3, 4, true, true},
		{"tmux 3.4", 3, 4, true, true},
		{"", 0, 0, false, false},
		{"abc", 0, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			maj, min, ok := parseVersion(tt.in)
			if maj != tt.maj || min != tt.min || ok != tt.ok {
				t.Errorf("parseVersion(%q) = %d,%d,%v want %d,%d,%v", tt.in, maj, min, ok, tt.maj, tt.min, tt.ok)
			}
			if got := versionAtLeast(tt.in, 3, 2); got != tt.atLeast32 {
				t.Errorf("versionAtLeast(%q, 3, 2) = %v want %v", tt.in, got, tt.atLeast32)
			}
		})
	}
}

func TestHumanizeAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{3 * time.Second, "3s"},
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{59 * time.Second, "59s"},
		{2 * time.Minute, "2m"},
		{90 * time.Second, "1m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h"},
		{25 * time.Hour, "1d"},
		{49 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		if got := humanizeAge(tt.d); got != tt.want {
			t.Errorf("humanizeAge(%s) = %q want %q", tt.d, got, tt.want)
		}
	}
}

func TestParseInterval(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 2 * time.Second, false},
		{"2s", 2 * time.Second, false},
		{"500ms", 500 * time.Millisecond, false},
		{"1m", time.Minute, false},
		{"0s", 0, true},
		{"-1s", 0, true},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseInterval(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseInterval(%q) err = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseInterval(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseInterval(%q) = %s want %s", tt.in, got, tt.want)
			}
		})
	}
}

// withTTY overrides the isTTY/insideTmux seams for the duration of a test.
func withTTY(t *testing.T, tty, inTmux bool) {
	t.Helper()
	oldTTY, oldTmux := isTTY, insideTmux
	t.Cleanup(func() { isTTY, insideTmux = oldTTY, oldTmux })
	isTTY = func() bool { return tty }
	insideTmux = func() bool { return inTmux }
}

func TestAttachRefusesInsideTmux(t *testing.T) {
	withTTY(t, true, true) // a tty, but nested in tmux
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdAttach(nil); rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope = %+v", e)
	}
	if !strings.Contains(e.Error.Hint, "detach") || !strings.Contains(e.Error.Hint, "outer shell") {
		t.Errorf("hint = %q, want detach and outer-shell guidance", e.Error.Hint)
	}
}

func TestAttachRefusesNonTTY(t *testing.T) {
	withTTY(t, false, false) // not a tty
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdAttach([]string{"build"}); rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope = %+v", e)
	}
	if !strings.Contains(e.Error.Message, "tty") {
		t.Errorf("message = %q, want tty mention", e.Error.Message)
	}
}

func TestWatchRefusesNonTTY(t *testing.T) {
	withTTY(t, false, false)
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdWatch(nil); rc != 1 {
		t.Errorf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope = %+v", e)
	}
}

func TestWatchBadInterval(t *testing.T) {
	// Bad interval is a usage error (exit 2), reported before the tty gate.
	withTTY(t, true, false)
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdWatch([]string{"--interval", "nope"}); rc != 2 {
		t.Errorf("rc = %d, want 2 (usage)", rc)
	}
}

func TestRenderDoctorMarksIssues(t *testing.T) {
	checks := []doctorCheck{
		{name: "tmux", ok: true, detail: "3.4 (>= 3.2)"},
		{name: "state dir", ok: false, detail: "not writable", fix: "set PAIRMUX_STATE_DIR"},
	}
	body, issues, topFix := renderDoctor(checks)
	if !issues {
		t.Error("expected issues = true")
	}
	if topFix != "set PAIRMUX_STATE_DIR" {
		t.Errorf("topFix = %q", topFix)
	}
	if !strings.Contains(body, "✓") || !strings.Contains(body, "✗") {
		t.Errorf("body missing marks:\n%s", body)
	}
}

func TestDeriveTier(t *testing.T) {
	tests := []struct {
		mode                    core.Mode
		gotHooks, gotC, gotDone bool
		want                    string
	}{
		{core.ModeHooks, true, true, true, "hooks"},                       // A+C+D: full integration (zsh, bash >= 4.4)
		{core.ModeHooks, true, false, true, "hooks-no-C"},                 // A+D, no C: bash 3.2
		{core.ModeHooks, false, false, false, "hooks-degraded->sentinel"}, // no A
		{core.ModeHooks, false, true, true, "hooks-degraded->sentinel"},   // no A beats C+D
		{core.ModeHooks, true, false, false, "hooks-degraded->sentinel"},  // A but no D
		{core.ModeHooks, true, true, false, "hooks-degraded->sentinel"},   // A+C but no D
		{core.ModeSentinel, false, false, true, "sentinel"},
		{core.ModeSentinel, false, false, false, "sentinel"},
	}
	for _, tt := range tests {
		if got, _ := deriveTier(tt.mode, tt.gotHooks, tt.gotC, tt.gotDone); got != tt.want {
			t.Errorf("deriveTier(%v,%v,%v,%v) = %q want %q", tt.mode, tt.gotHooks, tt.gotC, tt.gotDone, got, tt.want)
		}
	}
	if _, note := deriveTier(core.ModeHooks, true, false, true); !strings.Contains(note, "correlation is weaker") {
		t.Errorf("hooks-no-C note = %q, want weaker-correlation detail", note)
	}
}

func TestProbeLockHolder(t *testing.T) {
	dir := t.TempDir()

	// No write.lock file yet: free.
	if pid := probeLockHolder(dir); pid != 0 {
		t.Errorf("fresh dir: pid = %d, want 0", pid)
	}

	// Held: reports the actual holder pid.
	j := &journal.Journal{Dir: dir}
	release, err := j.AcquireWriteLock()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if pid := probeLockHolder(dir); pid != os.Getpid() {
		t.Errorf("held: pid = %d, want %d", pid, os.Getpid())
	}

	// Released: the pid text persists in write.lock, but the probe must report
	// free — this is exactly the stale-display bug watch had.
	release()
	if pid := probeLockHolder(dir); pid != 0 {
		t.Errorf("released: pid = %d, want 0 (stale pid text must not show)", pid)
	}
}
