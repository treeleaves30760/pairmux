package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// The updater's flow is exercised with fake operations (update_test.go, Unix:
// the fakes build uv's symlinked tool layout); only TestUpdateRefusesWindows
// switches to the Windows refusal, which runs on every platform.
func init() { updateGOOS = "linux" }

func TestUpdateRefusesWindows(t *testing.T) {
	defer func(old string) { updateGOOS = old }(updateGOOS)
	updateGOOS = "windows"
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	// Nil operations would panic if the refusal came after discovery.
	if rc := c.cmdUpdateWith(nil, "0.6.0", updateOps{}); rc != 1 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	e := decode(t, &buf)
	if e.OK || e.Error == nil || e.Error.Code != output.CodeUpdate {
		t.Fatalf("envelope=%+v", e)
	}
	if !strings.Contains(e.Error.Message, "not supported on Windows") || !strings.Contains(e.Error.Hint, "uv tool install --upgrade pairmux") {
		t.Fatalf("envelope=%+v", e)
	}
}
