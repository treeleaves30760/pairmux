package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/version"
)

func TestSkillTargetDirs(t *testing.T) {
	setHome(t, "/fakehome")
	tests := []struct {
		target  string
		want    string
		projRel bool
	}{
		{"claude-code", filepath.FromSlash("/fakehome/.claude/skills/pairmux"), false},
		{"codex", filepath.FromSlash("/fakehome/.agents/skills/pairmux"), false},
		{"gemini", filepath.FromSlash("/fakehome/.gemini/skills/pairmux"), false},
		{"opencode", filepath.FromSlash("/fakehome/.config/opencode/skills/pairmux"), false},
		{"copilot", filepath.FromSlash("/fakehome/.copilot/skills/pairmux"), false},
		{"windsurf", filepath.FromSlash("/fakehome/.codeium/windsurf/skills/pairmux"), false},
		{"kiro", filepath.FromSlash("/fakehome/.kiro/skills/pairmux"), false},
		{"amp", filepath.FromSlash("/fakehome/.config/amp/skills/pairmux"), false},
		{"cline", filepath.FromSlash("/fakehome/.cline/skills/pairmux"), false},
		{"roo-code", filepath.FromSlash("/fakehome/.roo/skills/pairmux"), false},
		{"kilo-code", filepath.FromSlash("/fakehome/.kilo/skills/pairmux"), false},
		{"droid", filepath.FromSlash("/fakehome/.factory/skills/pairmux"), false},
		{"agents", filepath.FromSlash("/fakehome/.agents/skills/pairmux"), false},
		{"cursor", filepath.Join(".cursor", "skills", "pairmux"), true},
	}
	if len(tests) != len(skillTargetNames) {
		t.Fatalf("path tests cover %d targets, registry has %d", len(tests), len(skillTargetNames))
	}
	for i, target := range skillTargetNames {
		for _, previous := range skillTargetNames[:i] {
			if target == previous {
				t.Fatalf("duplicate target %q", target)
			}
		}
		found := false
		for _, tt := range tests {
			found = found || tt.target == target
		}
		if !found {
			t.Errorf("no path test for %q", target)
		}
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
	setHome(t, home)

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

func TestSkillInstallTargetAlias(t *testing.T) {
	for _, target := range []string{"claude-code", "codex", "all"} {
		for _, dryRun := range []bool{false, true} {
			for _, flag := range [][]string{{"--target", target}, {"-t", target}, {"-t=" + target}} {
				t.Run(strings.Join(flag, " ")+"/dry-run="+fmt.Sprint(dryRun), func(t *testing.T) {
					home := t.TempDir()
					setHome(t, home)
					t.Chdir(t.TempDir())
					if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
						t.Fatal(err)
					}
					args := append([]string{"install"}, flag...)
					wantStatus := "installed"
					if dryRun {
						args = append(args, "--dry-run")
						wantStatus = "dry-run"
					}
					var buf bytes.Buffer
					c := newTestCtx(&buf, true)
					if rc := c.cmdSkill(args); rc != 0 {
						t.Fatalf("rc = %d, output = %s", rc, buf.String())
					}
					e := decode(t, &buf)
					if !e.OK || e.Status != wantStatus {
						t.Fatalf("envelope = %+v", e)
					}
					resolved := target
					if resolved == "all" {
						resolved = "claude-code"
					}
					dir, _, err := skillTargetDir(resolved)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(e.Output, filepath.Join(dir, "SKILL.md")) {
						t.Fatalf("missing target destination: %s", e.Output)
					}
					if dryRun {
						if _, err := os.Stat(dir); !os.IsNotExist(err) {
							t.Fatalf("dry-run created %s: %v", dir, err)
						}
					} else {
						files, err := skillFiles()
						if err != nil {
							t.Fatal(err)
						}
						assertSkillBundle(t, dir, files)
					}
				})
			}
		}
	}
}

func TestSkillInstallTargetAliasMissingValue(t *testing.T) {
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "-t"}); rc != 2 {
		t.Fatalf("rc = %d, want 2", rc)
	}
	if !strings.Contains(buf.String(), "needs a value") {
		t.Fatalf("expected missing value error: %s", buf.String())
	}
}

func TestSkillInstallWritesAndPreserves(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
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
	setHome(t, home)
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
	setHome(t, home)
	t.Chdir(t.TempDir())
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

func TestSkillInstallEachTarget(t *testing.T) {
	for _, target := range skillTargetNames {
		t.Run(target, func(t *testing.T) {
			setHome(t, t.TempDir())
			t.Chdir(t.TempDir())
			var buf bytes.Buffer
			c := newTestCtx(&buf, true)
			if rc := c.cmdSkill([]string{"install", "-t", target}); rc != 0 {
				t.Fatalf("rc = %d, output = %s", rc, buf.String())
			}
			dir, _, err := skillTargetDir(target)
			if err != nil {
				t.Fatal(err)
			}
			files, err := skillFiles()
			if err != nil {
				t.Fatal(err)
			}
			assertSkillBundle(t, dir, files)
		})
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

func TestSkillDigest(t *testing.T) {
	files := []skillFile{
		{rel: "SKILL.md", data: []byte("skill")},
		{rel: "references/commands.md", data: []byte("commands")},
	}
	const want = "94a4122f227c161d5c8fa9820ecc416b98c2f4d0b94efcad99b314b67da8996f"
	if got := skillDigest(files); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	for _, changed := range [][]skillFile{
		{{rel: "SKILL.md", data: []byte("changed")}, files[1]},
		{files[0], {rel: "references/renamed.md", data: files[1].data}},
		files[:1],
		append(append([]skillFile{}, files...), skillFile{rel: "references/new.md"}),
	} {
		if skillDigest(changed) == want {
			t.Error("bundle change must change its digest")
		}
	}
}

func TestSkillInstallReportsBundleSource(t *testing.T) {
	setHome(t, t.TempDir())
	files, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	want := "source: embedded in pairmux " + version.Version + "; bundle sha256:" + skillDigest(files)
	for _, args := range [][]string{{"install", "--dry-run"}, {"install"}} {
		var buf bytes.Buffer
		c := newTestCtx(&buf, true)
		if rc := c.cmdSkill(args); rc != 0 {
			t.Fatalf("rc = %d, output = %s", rc, buf.String())
		}
		e := decode(t, &buf)
		if !strings.Contains(e.Output, want) || !strings.Contains(e.Output, "not a remote latest version") {
			t.Errorf("missing bundle provenance: %s", e.Output)
		}
	}
}

func TestSkillInstallAllRefreshesBundle(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Chdir(t.TempDir())
	files, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	dirs := make(map[string]bool)
	for _, target := range skillTargetNames {
		dir, _, err := skillTargetDir(target)
		if err != nil {
			t.Fatal(err)
		}
		dirs[dir] = true
	}
	if len(dirs) != len(skillTargetNames)-1 { // codex and agents share one destination
		t.Fatalf("expected %d distinct install destinations, got %d", len(skillTargetNames)-1, len(dirs))
	}
	custom := []string{"local-notes.txt", "references/local-notes.txt"}
	for dir := range dirs {
		for _, f := range files {
			dst := filepath.Join(dir, f.rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, []byte("stale "+f.rel), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, rel := range custom {
			if err := os.WriteFile(filepath.Join(dir, rel), []byte("mine"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Preview the upgrade without changing any existing file.
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--target", "all", "--dry-run"}); rc != 0 {
		t.Fatalf("dry-run rc = %d, output = %s", rc, buf.String())
	}
	for dir := range dirs {
		for _, f := range files {
			got, err := os.ReadFile(filepath.Join(dir, f.rel))
			if err != nil || string(got) != "stale "+f.rel {
				t.Fatalf("dry-run changed %s/%s: %q, err=%v", dir, f.rel, got, err)
			}
		}
	}
	for i := 0; i < 2; i++ {
		buf.Reset()
		if rc := c.cmdSkill([]string{"install", "--target", "all"}); rc != 0 {
			t.Fatalf("install %d rc = %d, output = %s", i, rc, buf.String())
		}
		e := decode(t, &buf)
		if !e.OK || e.Status != "installed" {
			t.Fatalf("install %d envelope = %+v", i, e)
		}
		if !strings.Contains(e.Output, "skipped agents: same destination as codex") {
			t.Error("missing alias deduplication")
		}
		for dir := range dirs {
			assertSkillBundle(t, dir, files)
			for _, rel := range custom {
				if got, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || string(got) != "mine" {
					t.Errorf("custom file %s/%s changed: %q, err=%v", dir, rel, got, err)
				}
			}
			if strings.Count(e.Output, filepath.Join(dir, "SKILL.md")) != 1 {
				t.Errorf("destination should be installed once: %s", dir)
			}
		}
	}
}

func TestSkillInstallAllDryRunCreatesNothing(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	for _, base := range []string{filepath.Join(home, ".claude"), filepath.Join(project, ".cursor")} {
		if err := os.Mkdir(base, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--target", "all", "--dry-run"}); rc != 0 {
		t.Fatalf("rc = %d, output = %s", rc, buf.String())
	}
	for _, target := range skillTargetNames {
		dir, _, err := skillTargetDir(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("dry-run created %s: %v", dir, err)
		}
	}
}

func TestSkillInstallAllSkipsEveryMissingAgent(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--target", "all"}); rc != 0 {
		t.Fatalf("rc = %d, output = %s", rc, buf.String())
	}
	e := decode(t, &buf)
	for _, target := range skillTargetNames {
		if !strings.Contains(e.Output, "skipped "+target+":") {
			t.Errorf("missing skip for %s", target)
		}
	}
	for _, root := range []string{home, project} {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Errorf("absent agents must not create files in %s: %v, %v", root, entries, err)
		}
	}
}

func TestSkillInstallAllPreservesDirectorySymlink(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Chdir(t.TempDir())
	shared := filepath.Join(home, ".agents", "skills", "pairmux")
	claude := filepath.Join(home, ".claude", "skills", "pairmux")
	for _, dir := range []string{shared, filepath.Dir(claude)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "..", ".agents", "skills", "pairmux"), claude); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shared, "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	c := newTestCtx(&buf, true)
	if rc := c.cmdSkill([]string{"install", "--target", "all"}); rc != 0 {
		t.Fatalf("rc = %d, output = %s", rc, buf.String())
	}
	info, err := os.Lstat(claude)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skill directory symlink was replaced: %v", err)
	}
	files, err := skillFiles()
	if err != nil {
		t.Fatal(err)
	}
	assertSkillBundle(t, shared, files)
	assertSkillBundle(t, claude, files)
}

func assertSkillBundle(t *testing.T, dir string, files []skillFile) {
	t.Helper()
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(dir, f.rel))
		if err != nil || !bytes.Equal(got, f.data) {
			t.Errorf("%s/%s differs from embedded bundle (err=%v)", dir, f.rel, err)
		}
		parent := filepath.Dir(filepath.Join(dir, f.rel))
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".pairmux-skill-") {
				t.Errorf("temporary file left in %s: %s", parent, entry.Name())
			}
		}
	}
}

// setHome points the user's home directory at dir on every platform:
// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
