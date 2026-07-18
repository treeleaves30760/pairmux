package cli

import (
	"bytes"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/output"
)

func TestMCPServeIsOnlySupportedMCPCommand(t *testing.T) {
	for _, args := range [][]string{nil, {"http"}, {"serve", "extra"}} {
		var buf bytes.Buffer
		ctx := newTestCtx(&buf, true)
		if rc := ctx.cmdMCP(args); rc != 2 {
			t.Errorf("cmdMCP(%v) rc = %d, want 2", args, rc)
			continue
		}
		envelope := decode(t, &buf)
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != output.CodeBadArgs {
			t.Errorf("cmdMCP(%v) envelope = %+v", args, envelope)
		}
	}
}

func TestDispatchRoutesMCP(t *testing.T) {
	var buf bytes.Buffer
	ctx := newTestCtx(&buf, true)
	if rc := ctx.dispatch([]string{"mcp", "http"}); rc != 2 {
		t.Fatalf("rc = %d, want 2", rc)
	}
	if envelope := decode(t, &buf); envelope.Error == nil || envelope.Error.Code != output.CodeBadArgs {
		t.Errorf("envelope = %+v", envelope)
	}
}
