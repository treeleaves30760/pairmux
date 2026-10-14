//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPowerShellRunsAcrossScreenScroll runs enough commands to scroll the
// pane. On the run where the screen first scrolls, ConPTY emits its
// cursor-show between the \r and \n that end the output line; shaping read
// that as a carriage-return overwrite and returned the run with no output.
func TestPowerShellRunsAcrossScreenScroll(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell is Windows-only")
	}
	t.Setenv("PAIRMUX_SHELL", filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	e := newEnv(t, "")
	dumpJournalOnFailure(t, e, "ps")
	if env, code := pmx(t, e, "new", "--name", "ps"); code != 0 || !env.OK {
		t.Fatalf("new: code=%d env=%+v", code, env)
	}
	for n := 1; n <= 60; n++ {
		want := fmt.Sprintf("iter-%d", n)
		env, code := pmx(t, e, "run", "ps", "echo "+want)
		if code != 0 || env.Status != "done" || env.ExitCode == nil || *env.ExitCode != 0 {
			t.Fatalf("run %d: code=%d env=%+v", n, code, env)
		}
		if got := strings.TrimSpace(env.Output); got != want {
			t.Fatalf("run %d: output %q, want %q", n, env.Output, want)
		}
	}
}
