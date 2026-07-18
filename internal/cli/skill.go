package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	pairmuxroot "github.com/treeleaves30760/pairmux"
	"github.com/treeleaves30760/pairmux/internal/output"
)

// skillEmbedRoot is where the canonical skill tree sits inside the embedded FS
// (module root skills_embed.go).
const skillEmbedRoot = "skills/pairmux"

// skillTargetNames is the ordered list of installable targets, paths per
// pairmux-skills/install-map.md.
var skillTargetNames = []string{"claude-code", "codex", "gemini", "cursor", "opencode", "agents"}

// skillTargetDir resolves a target to the skills/pairmux directory it installs
// into. projectRelative marks targets resolved against the current project
// directory rather than $HOME (cursor).
func skillTargetDir(target string) (dir string, projectRelative bool, err error) {
	if target == "cursor" {
		return filepath.Join(".cursor", "skills", "pairmux"), true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve home dir: %w", err)
	}
	switch target {
	case "claude-code":
		return filepath.Join(home, ".claude", "skills", "pairmux"), false, nil
	case "codex":
		return filepath.Join(home, ".codex", "skills", "pairmux"), false, nil
	case "gemini":
		return filepath.Join(home, ".gemini", "skills", "pairmux"), false, nil
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "skills", "pairmux"), false, nil
	case "agents":
		return filepath.Join(home, ".agents", "skills", "pairmux"), false, nil
	}
	return "", false, fmt.Errorf("unknown target %q", target)
}

// cmdSkill routes the skill subcommand family (currently only install).
func (c *Ctx) cmdSkill(args []string) int {
	const usageLine = "pairmux skill install [--target claude-code|codex|gemini|cursor|opencode|agents|all] [--dry-run]"
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
	const usageLine = "pairmux skill install [--target claude-code|codex|gemini|cursor|opencode|agents|all] [--dry-run]"
	var target string
	var dryRun bool
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"dry-run": &dryRun},
		vals:  map[string]*string{"target": &target},
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

	valid := target == "all"
	for _, n := range skillTargetNames {
		if n == target {
			valid = true
		}
	}
	if !valid {
		return c.fail(output.CodeBadArgs, fmt.Sprintf("unknown target %q", target),
			"valid targets: "+strings.Join(skillTargetNames, " ")+" all")
	}

	files, err := skillFiles()
	if err != nil {
		return c.fail(output.CodeInternal, err.Error(), "")
	}

	targets := []string{target}
	skipMissing := false
	if target == "all" {
		// all = every agent actually present: the agent's own directory (the
		// parent of its skills dir, e.g. ~/.codex) must already exist — never
		// conjure an agent's config dir the user does not have.
		targets = skillTargetNames
		skipMissing = true
	}

	var lines []string
	for _, tgt := range targets {
		dir, projRel, err := skillTargetDir(tgt)
		if err != nil {
			return c.fail(output.CodeInternal, err.Error(), "")
		}
		if skipMissing {
			agentBase := filepath.Dir(filepath.Dir(dir)) // <base>/skills/pairmux -> <base>
			if !isDir(agentBase) {
				lines = append(lines, fmt.Sprintf("skipped %s: %s not found (agent not installed?)", tgt, agentBase))
				continue
			}
		}
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
			if err := os.WriteFile(dst, f.data, 0o644); err != nil {
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

// skillFile is one embedded skill file: its path relative to the skill root,
// plus content.
type skillFile struct {
	rel  string
	data []byte
}

// skillFiles reads the embedded skill tree in stable walk order.
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
	return out, nil
}
