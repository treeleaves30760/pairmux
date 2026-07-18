// Package shellhooks prepares the shell invocation for a new terminal. For zsh
// and bash it writes shim files (embedded below) that emit OSC 133 marks and
// returns ModeHooks; for unknown shells or an explicit --cmd override it returns
// ModeSentinel and no shim. Shim bodies expand $HOME at runtime, so nothing
// host-specific is baked into them.
package shellhooks

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/treeleaves30760/pairmux/internal/core"
)

//go:embed embed/zshrc.sh
var zshrcShim string

//go:embed embed/zshenv.sh
var zshenvShim string

//go:embed embed/bashrc.sh
var bashrcShim string

// Prepare returns the argv, extra environment, and mode for launching a terminal.
//
//   - cmdOverride non-empty: run it via ["/bin/sh","-lc", <cmd>...], ModeSentinel,
//     no shim, no env.
//   - zsh: write the zsh shim dir and return ["zsh","-i"] with ZDOTDIR/PAIRMUX
//     env, ModeHooks.
//   - bash/sh: write the bash shim and return ["bash","--rcfile",<path>,"-i"],
//     ModeHooks.
//   - anything else: run [shell], ModeSentinel.
func Prepare(stateDir, shell string, cmdOverride []string) (argv []string, env map[string]string, mode core.Mode, err error) {
	if len(cmdOverride) > 0 {
		return append([]string{"/bin/sh", "-lc"}, cmdOverride...), nil, core.ModeSentinel, nil
	}

	switch filepath.Base(shell) {
	case "zsh":
		shimDir := filepath.Join(stateDir, "shim-zsh")
		if err := ensureDir(shimDir); err != nil {
			return nil, nil, "", err
		}
		if err := writeShim(filepath.Join(shimDir, ".zshenv"), zshenvShim); err != nil {
			return nil, nil, "", err
		}
		if err := writeShim(filepath.Join(shimDir, ".zshrc"), zshrcShim); err != nil {
			return nil, nil, "", err
		}
		return []string{"zsh", "-i"}, map[string]string{"ZDOTDIR": shimDir, "PAIRMUX": "1"}, core.ModeHooks, nil
	case "bash", "sh":
		if err := ensureDir(stateDir); err != nil {
			return nil, nil, "", err
		}
		rc := filepath.Join(stateDir, "shim-bash.rc")
		if err := writeShim(rc, bashrcShim); err != nil {
			return nil, nil, "", err
		}
		return []string{"bash", "--rcfile", rc, "-i"}, nil, core.ModeHooks, nil
	default:
		return []string{shell}, nil, core.ModeSentinel, nil
	}
}

// ensureDir creates dir (and parents) and forces 0700 despite umask.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("shellhooks: mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("shellhooks: chmod %s: %w", dir, err)
	}
	return nil
}

// writeShim writes content to path (overwrite: idempotent) and forces 0600.
func writeShim(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("shellhooks: write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("shellhooks: chmod %s: %w", path, err)
	}
	return nil
}
