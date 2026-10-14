package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/shellhooks"
)

func TestDoctorProbeCommand(t *testing.T) {
	tests := []struct {
		name         string
		shell        string
		preparedMode core.Mode
		gotHooks     bool
		wantCommand  string
		wantMode     core.Mode
	}{
		{"fish native hooks", "/opt/homebrew/bin/fish", core.ModeHooks, true, "true", core.ModeHooks},
		{"fish fallback", "/opt/homebrew/bin/fish", core.ModeHooks, false, "true" + shellhooks.SentinelSuffix("fish"), core.ModeSentinel},
		{"zsh degraded fallback", "/bin/zsh", core.ModeHooks, false, "true" + core.SentinelSuffix, core.ModeSentinel},
		{"dash sentinel", "/bin/dash", core.ModeSentinel, false, "true" + core.SentinelSuffix, core.ModeSentinel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, mode := doctorProbeCommand(tt.shell, tt.preparedMode, tt.gotHooks)
			if command != tt.wantCommand || mode != tt.wantMode {
				t.Fatalf("doctorProbeCommand() = (%q, %q), want (%q, %q)", command, mode, tt.wantCommand, tt.wantMode)
			}
		})
	}
}

func TestDeriveTierReportsFallbackEvidence(t *testing.T) {
	if _, note := deriveTier(core.ModeHooks, false, false, true); note != "no prompt mark (A); sentinel fallback verified" {
		t.Fatalf("verified fallback note = %q", note)
	}
	if _, note := deriveTier(core.ModeHooks, false, false, false); note != "no prompt mark (A); sentinel fallback did not complete" {
		t.Fatalf("failed fallback note = %q", note)
	}
}

func TestShellNamePreservesConfiguredPath(t *testing.T) {
	t.Setenv("PAIRMUX_SHELL", "")
	shell := "/opt/homebrew/bin/fish"
	if runtime.GOOS == "windows" {
		// Windows honours $SHELL only when it names a real file (Git Bash
		// exports /usr/bin/bash, which Windows cannot start).
		shell = filepath.Join(t.TempDir(), "bash.exe")
		if err := os.WriteFile(shell, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SHELL", shell)
	if got := shellName(); got != shell {
		t.Fatalf("shellName() = %q, want configured path", got)
	}
}

func TestPairmuxShellOverridesShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PAIRMUX_SHELL", "/usr/local/bin/fish")
	if got := shellName(); got != "/usr/local/bin/fish" {
		t.Fatalf("shellName() = %q, want PAIRMUX_SHELL", got)
	}
}

func TestWindowsIgnoresUnstartableShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows shell resolution")
	}
	t.Setenv("PAIRMUX_SHELL", "")
	t.Setenv("SHELL", "/usr/bin/bash") // what Git Bash exports
	if got := shellName(); got == "/usr/bin/bash" {
		t.Fatal("an unstartable POSIX $SHELL was used on Windows")
	}
}

func TestCheckSecretPromptEnv(t *testing.T) {
	t.Setenv(detect.SecretPromptEnv, "")
	if _, present := checkSecretPromptEnv(); present {
		t.Fatal("check must be absent when the variable is unset")
	}

	t.Setenv(detect.SecretPromptEnv, `hasłem.*:$`)
	ck, present := checkSecretPromptEnv()
	if !present || !ck.ok {
		t.Fatalf("valid pattern: check = %+v, present = %v", ck, present)
	}

	t.Setenv(detect.SecretPromptEnv, `([unclosed`)
	ck, present = checkSecretPromptEnv()
	if !present || ck.ok || ck.fix == "" {
		t.Fatalf("invalid pattern must fail with a fix: %+v", ck)
	}
}
