package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
)

// The updater's Unix flow is exercised with fake operations in update_test.go
// (the fakes build uv's symlinked tool layout). update_aside_test.go switches
// updateGOOS to "windows" to exercise uv's copied Windows layout on every
// platform; update_windows_test.go adds real Windows file-locking semantics.
func init() { updateGOOS = "linux" }

// withUpdateGOOS selects a tool layout for one test.
func withUpdateGOOS(t *testing.T, goos string) {
	t.Helper()
	old := updateGOOS
	updateGOOS = goos
	t.Cleanup(func() { updateGOOS = old })
}

type updateTestCall struct {
	path      string
	args, env []string
}

func writeUpdateTestFile(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatal(err)
	}
}

func updateTestVersionReply(t *testing.T, v string) updateRunResult {
	t.Helper()
	b, err := json.Marshal(output.Envelope{Schema: core.SchemaID, OK: true, Status: "ok", Output: v})
	if err != nil {
		t.Fatal(err)
	}
	return updateRunResult{stdout: append(b, '\n')}
}

func assertUpdateError(t *testing.T, rc int, buf *bytes.Buffer) output.Envelope {
	t.Helper()
	if rc != 1 {
		t.Fatalf("rc=%d, want 1: %s", rc, buf.String())
	}
	e := decode(t, buf)
	if e.OK || e.Status != "error" || e.Error == nil || e.Error.Code != output.CodeUpdate || e.Error.Hint == "" || len(e.Next) != 1 || e.Next[0] != e.Error.Hint {
		t.Fatalf("invalid E_UPDATE envelope: %+v", e)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("JSON must be a single envelope: %q", buf.String())
	}
	return e
}

// Windows is no longer refused up front: an unverifiable executable fails the
// same ownership check as on Unix, before uv is even looked up.
func TestUpdateWindowsRefusesUnownedExecutableBeforeUV(t *testing.T) {
	withUpdateGOOS(t, "windows")
	root := t.TempDir()
	path := root + string(os.PathSeparator) + "manual.exe"
	writeUpdateTestFile(t, path, []byte("manual binary"), 0o700)
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	// Nil lookPath/run would panic if discovery continued.
	ops := updateOps{executable: func() (string, error) { return path, nil }}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.6.0", ops), &buf)
	if !strings.Contains(e.Error.Message, "cannot verify a persistent uv-owned") || e.Error.Hint != updateOwnershipHint {
		t.Fatalf("envelope=%+v", e)
	}
}
