package cli

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pairmuxroot "github.com/treeleaves30760/pairmux"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/version"
)

// skillEmbedRoot is where the canonical skill tree sits inside the embedded FS
// (module root skills_embed.go).
const skillEmbedRoot = "skills/pairmux"

// skillTarget is one installable agent: where its user-level skills directory
// lives (relative to $HOME, or to the project for projectRelative targets) and
// which directories prove the agent is installed for `--target all`. Paths per
// pairmux-skills/install-map.md.
type skillTarget struct {
	name string
	// skillsDir is the agent's skills directory relative to $HOME; pairmux
	// installs into <skillsDir>/pairmux.
	skillsDir string
	// detect lists directories (relative to $HOME) whose existence means the
	// agent is present. Empty means the parent of skillsDir.
	detect          []string
	projectRelative bool
}

// skillTargets is the ordered registry of installable targets.
var skillTargets = []skillTarget{
	{name: "claude-code", skillsDir: ".claude/skills"},
	{name: "codex", skillsDir: ".agents/skills", detect: []string{".codex", ".agents"}},
	{name: "gemini", skillsDir: ".gemini/skills"},
	{name: "antigravity", skillsDir: ".gemini/config/skills", detect: []string{".gemini/antigravity-cli", ".gemini/antigravity", ".gemini/config"}},
	{name: "cursor", skillsDir: ".cursor/skills", projectRelative: true},
	{name: "opencode", skillsDir: ".config/opencode/skills"},
	{name: "copilot", skillsDir: ".copilot/skills"},
	{name: "windsurf", skillsDir: ".codeium/windsurf/skills"},
	{name: "kiro", skillsDir: ".kiro/skills"},
	{name: "amp", skillsDir: ".config/amp/skills"},
	{name: "cline", skillsDir: ".cline/skills"},
	{name: "roo-code", skillsDir: ".roo/skills"},
	{name: "kilo-code", skillsDir: ".kilo/skills"},
	{name: "droid", skillsDir: ".factory/skills"},
	{name: "qwen-code", skillsDir: ".qwen/skills"},
	{name: "goose", skillsDir: ".config/goose/skills"},
	{name: "crush", skillsDir: ".config/crush/skills"},
	{name: "augment", skillsDir: ".augment/skills"},
	{name: "continue", skillsDir: ".continue/skills"},
	{name: "junie", skillsDir: ".junie/skills"},
	{name: "trae", skillsDir: ".trae/skills"},
	{name: "trae-cn", skillsDir: ".trae-cn/skills"},
	{name: "qoder", skillsDir: ".qoder/skills"},
	{name: "codebuddy", skillsDir: ".codebuddy/skills"},
	{name: "iflow", skillsDir: ".iflow/skills"},
	{name: "lingma", skillsDir: ".lingma/skills"},
	{name: "openhands", skillsDir: ".openhands/skills"},
	{name: "mistral-vibe", skillsDir: ".vibe/skills"},
	{name: "devin", skillsDir: ".config/devin/skills"},
	{name: "rovodev", skillsDir: ".rovodev/skills"},
	{name: "tabnine", skillsDir: ".tabnine/agent/skills", detect: []string{".tabnine"}},
	{name: "forgecode", skillsDir: ".forge/skills"},
	{name: "zencoder", skillsDir: ".zencoder/skills"},
	{name: "command-code", skillsDir: ".commandcode/skills"},
	{name: "grok", skillsDir: ".grok/skills"},
	{name: "hermes", skillsDir: ".hermes/skills"},
	{name: "cortex", skillsDir: ".snowflake/cortex/skills"},
	// Agents whose user-level skills directory is the shared ~/.agents/skills
	// standard. They resolve to the same destination as codex/agents; named
	// targets exist so `all` finds them and users can ask for them by name.
	{name: "kimi", skillsDir: ".agents/skills", detect: []string{".kimi", ".kimi-code"}},
	{name: "warp", skillsDir: ".agents/skills", detect: []string{".warp"}},
	{name: "zed", skillsDir: ".agents/skills", detect: []string{".config/zed"}},
	{name: "pi", skillsDir: ".agents/skills", detect: []string{".pi/agent"}},
	{name: "agents", skillsDir: ".agents/skills"},
}

// skillTargetAliases maps accepted shorthand to a registry name.
var skillTargetAliases = map[string]string{
	"agy":             "antigravity",
	"antigravity-cli": "antigravity",
	"qwen":            "qwen-code",
	"factory":         "droid",
	"vibe":            "mistral-vibe",
	"kimi-code":       "kimi",
}

// skillTargetNames is the ordered list of installable target names.
var skillTargetNames = func() []string {
	names := make([]string, len(skillTargets))
	for i, t := range skillTargets {
		names[i] = t.name
	}
	return names
}()

func lookupSkillTarget(name string) (skillTarget, bool) {
	if canonical, ok := skillTargetAliases[name]; ok {
		name = canonical
	}
	for _, t := range skillTargets {
		if t.name == name {
			return t, true
		}
	}
	return skillTarget{}, false
}

// skillTargetDir resolves a target to the skills/pairmux directory it installs
// into. projectRelative marks targets resolved against the current project
// directory rather than $HOME (cursor).
func skillTargetDir(target string) (dir string, projectRelative bool, err error) {
	t, ok := lookupSkillTarget(target)
	if !ok {
		return "", false, fmt.Errorf("unknown target %q", target)
	}
	rel := filepath.Join(filepath.FromSlash(t.skillsDir), "pairmux")
	if t.projectRelative {
		return rel, true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, rel), false, nil
}

// skillTargetPresent reports whether the agent behind target looks installed,
// and the directory that was checked first (for the skip message).
func skillTargetPresent(target, dir string) (bool, string) {
	t, _ := lookupSkillTarget(target)
	if len(t.detect) == 0 || t.projectRelative {
		base := filepath.Dir(filepath.Dir(dir)) // <base>/skills/pairmux -> <base>
		return isDir(base), base
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "~"
	}
	for _, d := range t.detect {
		if isDir(filepath.Join(home, filepath.FromSlash(d))) {
			return true, ""
		}
	}
	return false, filepath.Join(home, filepath.FromSlash(t.detect[0]))
}

// cmdSkill routes the skill subcommand family (currently only install).
func (c *Ctx) cmdSkill(args []string) int {
	const usageLine = "pairmux skill install [--target T|all] [--dry-run] (-t is an alias for --target)"
	if len(args) == 0 {
		return c.usage(usageLine, "pairmux skill install")
	}
	if args[0] != "install" {
		return c.usage(usageLine, fmt.Sprintf("unknown skill action %q — only install exists", args[0]))
	}
	return c.cmdSkillInstall(args[1:])
}

// cmdSkillInstall copies the embedded skill tree into the chosen agent's
// skills directory. It only ever writes our own files — a user's unrelated
// files in the same directory are never touched or deleted.
func (c *Ctx) cmdSkillInstall(args []string) int {
	const usageLine = "pairmux skill install [--target T|all] [--dry-run] (-t is an alias for --target)"
	var target string
	var dryRun bool
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"dry-run": &dryRun},
		vals:  map[string]*string{"target": &target, "t": &target},
	})
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	if len(pos) > 0 {
		return c.usage(usageLine, "unexpected argument "+pos[0])
	}
	if target == "" {
		target = "claude-code"
	}

	if canonical, ok := skillTargetAliases[target]; ok {
		target = canonical
	}
	if _, ok := lookupSkillTarget(target); !ok && target != "all" {
		return c.fail(output.CodeBadArgs, fmt.Sprintf("unknown target %q", target),
			"valid targets: "+strings.Join(skillTargetNames, " ")+" all (aliases: "+skillAliasList()+")")
	}

	files, err := skillFiles()
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}

	targets := []string{target}
	skipMissing := false
	if target == "all" {
		// all = every agent actually present: the agent's own directory (the
		// parent of its skills dir, e.g. ~/.agents) must already exist — never
		// conjure an agent's config dir the user does not have.
		targets = skillTargetNames
		skipMissing = true
	}

	lines := []string{
		fmt.Sprintf("source: embedded in pairmux %s; bundle sha256:%s", version.Version, skillDigest(files)),
		"installs this executable's bundled files, not a remote latest version; replaces matching files and preserves other files",
	}
	installedDirs := make(map[string]string)
	for _, tgt := range targets {
		dir, projRel, err := skillTargetDir(tgt)
		if err != nil {
			return c.fail(output.CodeInternal, err.Error(), "")
		}
		if skipMissing {
			if present, checked := skillTargetPresent(tgt, dir); !present {
				lines = append(lines, fmt.Sprintf("skipped %s: %s not found (agent not installed?)", tgt, checked))
				continue
			}
		}
		cleanDir := filepath.Clean(dir)
		if previous, duplicate := installedDirs[cleanDir]; duplicate {
			lines = append(lines, fmt.Sprintf("skipped %s: same destination as %s", tgt, previous))
			continue
		}
		installedDirs[cleanDir] = tgt
		for _, f := range files {
			dst := filepath.Join(dir, f.rel)
			line := tgt + ": " + dst
			if projRel {
				line += "   (project-relative)"
			}
			lines = append(lines, line)
			if dryRun {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return c.fail(output.CodeInternal, "create "+filepath.Dir(dst)+": "+err.Error(), "")
			}
			if err := writeSkillFile(dst, f.data); err != nil {
				return c.fail(output.CodeInternal, "write "+dst+": "+err.Error(), "")
			}
		}
	}

	status := "installed"
	if dryRun {
		status = "dry-run"
	}
	return c.emit(output.Envelope{
		Status: status,
		Output: strings.Join(lines, "\n"),
		Next: []string{
			"restart your agent so it picks up the skill",
			"pairmux skill install --target all",
		},
	})
}

// skillAliasList renders the accepted aliases as "alias=target" pairs.
func skillAliasList() string {
	pairs := make([]string, 0, len(skillTargetAliases))
	for alias, name := range skillTargetAliases {
		pairs = append(pairs, alias+"="+name)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, " ")
}

// writeSkillFile atomically replaces one file without following an existing
// destination symlink. Agents can reload skills while install runs, so they
// should observe either the old complete file or the new complete file.
func writeSkillFile(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pairmux-skill-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// skillFile is one embedded skill file: its path relative to the skill root,
// plus content.
type skillFile struct {
	rel  string
	data []byte
}

// skillDigest identifies a bundle independently of the CLI version. skillFiles
// supplies sorted slash-separated paths; each entry is path + NUL + content's
// SHA-256 bytes, making both file names and boundaries part of the digest.
func skillDigest(files []skillFile) string {
	h := sha256.New()
	for _, f := range files {
		_, _ = h.Write([]byte(f.rel))
		_, _ = h.Write([]byte{0})
		sum := sha256.Sum256(f.data)
		_, _ = h.Write(sum[:])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// skillFiles reads the embedded skill tree in relative-path order.
func skillFiles() ([]skillFile, error) {
	var out []skillFile
	err := fs.WalkDir(pairmuxroot.SkillFS, skillEmbedRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := pairmuxroot.SkillFS.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, skillFile{rel: strings.TrimPrefix(path, skillEmbedRoot+"/"), data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read embedded skill: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}
