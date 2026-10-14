//go:build integration

package integration

import (
	"runtime"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// These checks need neither tmux nor a managed terminal. The test binary is a
// development build, so update must fail before any package-manager invocation.
func TestUpdateRefusesDevelopmentBinary(t *testing.T) {
	e, code := pmx(t, tenv{}, "update")
	if code != 1 || e.OK || e.Error == nil || e.Error.Code != output.CodeUpdate {
		t.Fatalf("update development binary: code=%d envelope=%+v", code, e)
	}
	// Windows refuses before looking at the version at all (uv's Windows tool
	// layout and in-use executables cannot be updated in place).
	want := "version"
	if runtime.GOOS == "windows" {
		want = "not supported on Windows"
	}
	if !strings.Contains(e.Error.Message, want) || e.Error.Hint == "" {
		t.Fatalf("missing %q refusal or recovery hint: %+v", want, e.Error)
	}
}

func TestUpdateRejectsArgumentsBeforeDiscovery(t *testing.T) {
	for _, args := range [][]string{
		{"update", "pairmux"},
		{"update", "--dry-run"},
		{"update", "--all"},
		{"update", "--version", "0.6.0"},
		{"update", "--default-index", "https://example.invalid/simple"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e, code := pmx(t, tenv{}, args...)
			if code != 2 || e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs {
				t.Fatalf("update invalid arguments: code=%d envelope=%+v", code, e)
			}
		})
	}
}
