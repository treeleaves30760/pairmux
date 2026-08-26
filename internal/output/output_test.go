package output

import (
	"bytes"
	"strings"
	"testing"
)

func intp(v int) *int     { return &v }
func i64p(v int64) *int64 { return &v }

// TestEmitJSONGolden pins the exact single-line JSON for the scenarios the
// architect checks: run-done (exit_code 0 present via pointer), run-done whose
// command failed (exit_code 1 + truncation), and run-timeout (status running +
// next hints).
func TestEmitJSONGolden(t *testing.T) {
	tests := []struct {
		name string
		env  Envelope
		want string
	}{
		{
			name: "run done exit 0",
			env: Envelope{
				OK: true, Status: "done", Terminal: "web", Mode: "hooks",
				ExitCode: intp(0), DurationMS: i64p(1234), Output: "hello",
			},
			want: `{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"web","mode":"hooks","exit_code":0,"duration_ms":1234,"output":"hello"}` + "\n",
		},
		{
			name: "run done command failed with truncation",
			env: Envelope{
				OK: true, Status: "done", Terminal: "web", Mode: "sentinel",
				ExitCode: intp(1), Output: "boom",
				Truncated: &TruncInfo{OmittedLines: 750, GetFull: "pairmux log web --cmd 3"},
			},
			want: `{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"web","mode":"sentinel","exit_code":1,"output":"boom","truncated":{"omitted_lines":750,"get_full":"pairmux log web --cmd 3"}}` + "\n",
		},
		{
			name: "run timeout still running",
			env: Envelope{
				OK: true, Status: "running", Terminal: "web", Output: "partial",
				Next: []string{"pairmux peek web", "pairmux log web --cmd 3"},
			},
			want: `{"schema":"pairmux.v1","ok":true,"status":"running","terminal":"web","output":"partial","next":["pairmux peek web","pairmux log web --cmd 3"]}` + "\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			Emit(&buf, true, tc.env)
			if got := buf.String(); got != tc.want {
				t.Fatalf("Emit JSON mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestEmitStampsSchema verifies Emit fills in the schema id when the caller left
// it blank, so every response is self-describing.
func TestEmitStampsSchema(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, true, Envelope{OK: true, Status: "sent", Terminal: "web"})
	if !strings.HasPrefix(buf.String(), `{"schema":"pairmux.v1",`) {
		t.Fatalf("schema not stamped: %s", buf.String())
	}
}

// TestExitCodeZeroPresent guards the pointer semantics: a command that exited 0
// must still carry exit_code:0 (not be omitted).
func TestExitCodeZeroPresent(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, true, Envelope{OK: true, Status: "done", ExitCode: intp(0)})
	if !strings.Contains(buf.String(), `"exit_code":0`) {
		t.Fatalf("exit_code:0 missing: %s", buf.String())
	}
}

func TestFailGolden(t *testing.T) {
	var buf bytes.Buffer
	code := Fail(&buf, true, CodeBusy,
		"terminal 'web' busy: command already running (holder pid 12345)",
		"pairmux peek web")
	if code != 1 {
		t.Fatalf("Fail exit code = %d, want 1", code)
	}
	want := `{"schema":"pairmux.v1","ok":false,"status":"error","next":["pairmux peek web"],` +
		`"error":{"code":"E_BUSY","message":"terminal 'web' busy: command already running (holder pid 12345)","hint":"pairmux peek web"}}` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("Fail JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestErrorCodeConstants(t *testing.T) {
	want := map[string]string{
		CodeNoTerminal: "E_NO_TERMINAL",
		CodeExists:     "E_EXISTS",
		CodeBusy:       "E_BUSY",
		CodeDead:       "E_DEAD",
		CodeBadArgs:    "E_BAD_ARGS",
		CodeTmux:       "E_TMUX",
		CodeInternal:   "E_INTERNAL",
	}
	for got, exp := range want {
		if got != exp {
			t.Fatalf("error code constant = %q, want %q", got, exp)
		}
	}
}

func TestLsEmptyGolden(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, true, Envelope{OK: true, Status: "idle", Next: []string{"pairmux new"}})
	want := `{"schema":"pairmux.v1","ok":true,"status":"idle","next":["pairmux new"]}` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("ls empty JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestLsRowsGolden(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, true, Envelope{
		OK:     true,
		Status: "idle",
		Terminals: []TerminalRow{
			// web has neither lock nor notes: proves omitempty on the new fields.
			{Name: "web", Status: "idle", Mode: "hooks", LastActivity: "2026-07-18T10:00:00Z"},
			{Name: "api", Status: "running", Mode: "sentinel", CurrentCmd: "npm run dev",
				LockHolder: 4242, Notes: 2},
		},
	})
	want := `{"schema":"pairmux.v1","ok":true,"status":"idle","terminals":[` +
		`{"name":"web","status":"idle","mode":"hooks","last_activity":"2026-07-18T10:00:00Z"},` +
		`{"name":"api","status":"running","mode":"sentinel","current_cmd":"npm run dev","lock_holder":4242,"notes":2}]}` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("ls rows JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

// TestNotesGoldenJSON pins the envelope-level notes field: human messages left
// via `pairmux note` ride along with a normal command reply.
func TestNotesGoldenJSON(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, true, Envelope{
		OK: true, Status: "done", Terminal: "web", ExitCode: intp(0),
		Output: "hello",
		Notes:  []string{"deploy is frozen until 3pm", "use the staging db"},
	})
	want := `{"schema":"pairmux.v1","ok":true,"status":"done","terminal":"web","exit_code":0,"output":"hello",` +
		`"notes":["deploy is frozen until 3pm","use the staging db"]}` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("notes JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

// --- human rendering smoke tests ---

func TestHumanRunDone(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, false, Envelope{
		OK: true, Status: "done", Terminal: "web", Mode: "hooks",
		ExitCode: intp(0), DurationMS: i64p(1234), Output: "hello",
	})
	out := buf.String()
	for _, sub := range []string{"done", "terminal=web", "exit=0", "duration=1.234s", "hello"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("human run-done missing %q in:\n%s", sub, out)
		}
	}
}

func TestHumanTimeoutHints(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, false, Envelope{
		OK: true, Status: "running", Terminal: "web", Output: "partial",
		Next: []string{"pairmux peek web", "pairmux log web --cmd 3"},
	})
	out := buf.String()
	if !strings.Contains(out, "running") {
		t.Fatalf("missing status line:\n%s", out)
	}
	if !strings.Contains(out, "next:\n  pairmux peek web\n  pairmux log web --cmd 3\n") {
		t.Fatalf("next hints not one-per-line:\n%s", out)
	}
}

func TestHumanError(t *testing.T) {
	var buf bytes.Buffer
	Fail(&buf, false, CodeBusy, "terminal 'web' busy", "pairmux peek web")
	out := buf.String()
	for _, sub := range []string{"error", "E_BUSY", "terminal 'web' busy", "next:", "pairmux peek web"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("human error missing %q in:\n%s", sub, out)
		}
	}
}

// TestHumanErrorHintFallback verifies a manually built error envelope with only
// ErrInfo.Hint (no Next) still surfaces the hint in the next section.
func TestHumanErrorHintFallback(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, false, Envelope{
		OK: false, Status: "error",
		Error: &ErrInfo{Code: CodeDead, Message: "terminal 'web' is dead", Hint: "pairmux new --name web"},
	})
	out := buf.String()
	if !strings.Contains(out, "next:\n  pairmux new --name web\n") {
		t.Fatalf("hint fallback missing:\n%s", out)
	}
}

func TestHumanLsTable(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, false, Envelope{
		OK:     true,
		Status: "idle",
		Terminals: []TerminalRow{
			{Name: "web", Status: "idle", Mode: "hooks"},
			{Name: "api", Status: "running", Mode: "sentinel", CurrentCmd: "npm run dev",
				LockHolder: 4242, Notes: 2},
		},
	})
	out := buf.String()
	for _, sub := range []string{"NAME", "web", "api", "npm run dev  [lock:4242]  [notes:2]"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("human ls missing %q in:\n%s", sub, out)
		}
	}
	if strings.Contains(out, "[lock:0]") || strings.Contains(out, "[notes:0]") {
		t.Fatalf("zero-valued markers must not render:\n%s", out)
	}
}

// TestHumanNotesBlock verifies placement: the notes block sits after the output
// section and before the truncation note, one note per line.
func TestHumanNotesBlock(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, false, Envelope{
		OK: true, Status: "done", Terminal: "web", Output: "hello",
		Notes:     []string{"first note", "second note"},
		Truncated: &TruncInfo{OmittedLines: 3, GetFull: "pairmux log web --cmd 1"},
	})
	out := buf.String()
	block := "hello\nnotes from human:\n  first note\n  second note\n… 3 more line(s)"
	if !strings.Contains(out, block) {
		t.Fatalf("notes block misplaced or malformed, want to contain:\n%q\ngot:\n%s", block, out)
	}
}

func TestHumanByteOnlyTruncationIsVisible(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, false, Envelope{
		OK: true, Status: "ok", Output: "tail",
		Truncated: &TruncInfo{OmittedBytes: 65536, GetFull: "pairmux log t1 --range 1:end"},
	})
	out := buf.String()
	for _, want := range []string{"65536 earlier byte(s) omitted", "pairmux log t1 --range 1:end"} {
		if !strings.Contains(out, want) {
			t.Fatalf("byte-only truncation missing %q in:\n%s", want, out)
		}
	}
}

func TestJSONIncludesOmittedBytes(t *testing.T) {
	var buf bytes.Buffer
	Emit(&buf, true, Envelope{
		OK: true, Status: "ok",
		Truncated: &TruncInfo{OmittedBytes: 42, GetFull: "pairmux log t1 --range 1:end"},
	})
	if !strings.Contains(buf.String(), `"omitted_bytes":42`) {
		t.Fatalf("omitted byte count missing from JSON: %s", buf.String())
	}
}

// TestTerminalRowLayerBadge pins that a terminal driving a nested layer says so
// in the listing: a human looking for the agents an agent is running has no
// other way to discover that the layer exists, or which endpoint reaches it.
func TestTerminalRowLayerBadge(t *testing.T) {
	var b strings.Builder
	writeTerminals(&b, []TerminalRow{
		{Name: "solo", Status: "idle", Mode: "hooks"},
		{Name: "boss", Status: "idle", Mode: "hooks", ChildSocket: "pairmux-boss"},
	})
	out := b.String()
	if !strings.Contains(out, "[layer:pairmux-boss]") {
		t.Errorf("listing = %q, want a layer badge", out)
	}
	if strings.Count(out, "[layer:") != 1 {
		t.Errorf("listing = %q, want exactly one layer badge", out)
	}
}
