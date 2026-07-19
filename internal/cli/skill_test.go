package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/output"
)

func TestSkillTargetDirs(t *testing.T) {
	t.Setenv("HOME", "/fakehome")
	tests := []struct {
		target  string
		want    string
		projRel bool
	}{
		{"claude-code", "/fakehome/.claude/skills/pairmux", false},
		{"codex", "/fakehome/.agents/skills/pairmux", false},
		{"gemini", "/fakehome/.gemini/skills/pairmux", false},
		{"opencode", "/fakehome/.config/opencode/skills/pairmux", false},
		{"copilot", "/fakehome/.copilot/skills/pairmux", false},
		{"windsurf", "/fakehome/.codeium/windsurf/skills/pairmux", false},
		{"kiro", "/fakehome/.kiro/skills/pairmux", false},
		{"amp", "/fakehome/.config/amp/skills/pairmux", false},
		{"agents", "/fakehome/.agents/skills/pairmux", false},
		{"cursor", filepath.Join(".cursor", "skills", "pairmux"), true},
	}
	for _, tt := range tests {
		dir, projRel, err := skillTargetDir(tt.target)
		if err != nil {
			t.Errorf("%s: %v", tt.target, err)
			continue
		}
		if dir != tt.want || projRel != tt.projRel {
			t.Errorf("%s: dir=%q projRel=%v, want %q %v", tt.target, dir, projRel, tt.want, tt.projRel)
		}
	}
	if _, _, err := skillTargetDir("vscode"); err == nil {
		t.Error("unknown target should error")
	}
}

// skillRels are the files the embedded skill must contain.
var skillRels = []string{
	"SKILL.md",
	"references/collaboration.md",
	"references/commands.md",
	"references/interactive.md",
	"references/troubleshooting.md",
}

func TestSkillInstallDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--dry-run"}); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	e := decode(t, &buf)
	if !e.OK || e.Status != "dry-run" {
		t.Fatalf("envelope = %+v", e)
	}
	for _, rel := range skillRels {
		want := filepath.Join(home, ".claude", "skills", "pairmux", rel)
		if !strings.Contains(e.Output, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, e.Output)
		}
	}
	// Dry run writes nothing at all.
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not create ~/.claude (stat err = %v)", err)
	}
}

func TestSkillInstallWritesAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills", "pairmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "keepme.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install"}); rc != 0 { // default target claude-code
		t.Fatalf("rc = %d, want 0", rc)
	}
	e := decode(t, &buf)
	if !e.OK || e.Status != "installed" {
		t.Fatalf("envelope = %+v", e)
	}

	for _, rel := range skillRels {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || len(b) == 0 {
			t.Fatalf("expected installed file %s (err=%v, %d bytes)", rel, err, len(b))
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); !strings.Contains(string(b), "name: pairmux") {
		t.Errorf("SKILL.md missing frontmatter name")
	}
	// An unrelated pre-existing file survives untouched.
	if b, err := os.ReadFile(keep); err != nil || string(b) != "mine" {
		t.Errorf("keepme.txt clobbered (err=%v, content=%q)", err, b)
	}
}

func TestSkillInstallReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "skills", "pairmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "SKILL.md")
	if err := os.Symlink(outside, dst); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("rc = %d, output = %s", rc, buf.String())
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "keep" {
		t.Fatalf("symlink target changed: content=%q err=%v", got, err)
	}
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("installed SKILL.md is still a symlink")
	}
	if got, err := os.ReadFile(dst); err != nil || !strings.Contains(string(got), "name: pairmux") {
		t.Fatalf("installed SKILL.md invalid: err=%v", err)
	}
}

func TestSkillInstallAllSkipsMissingAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Only the shared Codex/agents skill root is "installed" on this machine.
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--target", "all"}); rc != 0 {
		t.Fatalf("rc = %d, want 0", rc)
	}
	e := decode(t, &buf)
	want := filepath.Join(home, ".agents", "skills", "pairmux", "SKILL.md")
	if !strings.Contains(e.Output, want) {
		t.Fatalf("all should install into present ~/.agents:\n%s", e.Output)
	}
	if strings.Count(e.Output, want) != 1 || !strings.Contains(e.Output, "skipped agents: same destination as codex") {
		t.Fatalf("all should write the Codex/agents alias destination once:\n%s", e.Output)
	}
	if !strings.Contains(e.Output, "skipped claude-code") {
		t.Fatalf("all should report skipping absent agents:\n%s", e.Output)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("codex skill not written: %v", err)
	}
	// Absent agents' directories are never conjured.
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Errorf("must not create ~/.claude for a skipped agent (stat err = %v)", err)
	}
}

func TestSkillUnknownTarget(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--target", "vscode"}); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
	e := decode(t, &buf)
	if e.Error == nil || e.Error.Code != output.CodeBadArgs {
		t.Fatalf("envelope = %+v", e)
	}
	for _, name := range append(append([]string{}, skillTargetNames...), "all") {
		if !strings.Contains(e.Error.Hint, name) {
			t.Errorf("hint should list %q: %q", name, e.Error.Hint)
		}
	}
}

func TestSkillBareUsage(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.dispatch([]string{"skill"}); rc != 2 {
		t.Errorf("bare skill: rc = %d, want 2", rc)
	}
	buf.Reset()
	if rc := c.dispatch([]string{"skill", "remove"}); rc != 2 {
		t.Errorf("unknown action: rc = %d, want 2", rc)
	}
}
