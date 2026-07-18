package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeExecutor struct {
	mu         sync.Mutex
	calls      [][]string
	executions []Execution
	err        error
}

func (f *fakeExecutor) Execute(_ context.Context, argv []string) (Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), argv...))
	if f.err != nil {
		return Execution{}, f.err
	}
	if len(f.executions) > 0 {
		execution := f.executions[0]
		f.executions = f.executions[1:]
		return execution, nil
	}
	return Execution{Stdout: []byte(`{"schema":"pairmux.v1","ok":true,"status":"ok"}`)}, nil
}

func (f *fakeExecutor) callsSnapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([][]string, len(f.calls))
	for i := range f.calls {
		calls[i] = append([]string(nil), f.calls[i]...)
	}
	return calls
}

func TestProtocolLifecycle(t *testing.T) {
	fake := &fakeExecutor{}
	responses, stderr := serveMessages(t, fake,
		request(0, "tools/list", map[string]any{}),
		request(1, "initialize", initializeParams(ProtocolVersion)),
		request(2, "tools/list", map[string]any{}),
		notification("notifications/initialized", map[string]any{}),
		request(3, "ping", map[string]any{}),
		request(4, "unknown/method", map[string]any{}),
	)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if len(responses) != 5 {
		t.Fatalf("got %d responses, want 5: %#v", len(responses), responses)
	}
	wantCodes := []int{codeInvalidRequest, 0, codeInvalidRequest, 0, codeMethodNotFound}
	for i, want := range wantCodes {
		if got := responseErrorCode(responses[i]); got != want {
			t.Errorf("response %d error code = %d, want %d", i, got, want)
		}
	}
	initResult := resultMap(t, responses[1])
	if got := initResult["protocolVersion"]; got != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", got, ProtocolVersion)
	}
	capabilities := initResult["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Errorf("initialize capabilities missing tools: %#v", capabilities)
	}
	serverInfo := initResult["serverInfo"].(map[string]any)
	if serverInfo["name"] != "pairmux" || serverInfo["version"] != "test-version" {
		t.Errorf("serverInfo = %#v", serverInfo)
	}
	if got := resultMap(t, responses[3]); len(got) != 0 {
		t.Errorf("ping result = %#v, want empty object", got)
	}
}

func TestInitializeNegotiatesLatestVersion(t *testing.T) {
	responses, _ := serveMessages(t, &fakeExecutor{},
		request(1, "initialize", initializeParams("2024-11-05")),
	)
	if got := resultMap(t, responses[0])["protocolVersion"]; got != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want server version %s", got, ProtocolVersion)
	}
}

func TestToolSchemas(t *testing.T) {
	responses, _ := serveMessages(t, &fakeExecutor{},
		request(1, "initialize", initializeParams(ProtocolVersion)),
		notification("notifications/initialized", map[string]any{}),
		request(2, "tools/list", map[string]any{}),
	)
	result := resultMap(t, responses[1])
	listed, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %#v", result["tools"])
	}
	wantNames := []string{
		"pairmux_new", "pairmux_run", "pairmux_peek", "pairmux_wait", "pairmux_send",
		"pairmux_log", "pairmux_ls", "pairmux_kill", "pairmux_note", "pairmux_doctor",
	}
	if len(listed) != len(wantNames) {
		t.Fatalf("got %d tools, want %d", len(listed), len(wantNames))
	}
	for i, item := range listed {
		definition := item.(map[string]any)
		if got := definition["name"]; got != wantNames[i] {
			t.Errorf("tool %d name = %v, want %s", i, got, wantNames[i])
		}
		schema := definition["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s schema = %#v", wantNames[i], schema)
		}
		if _, ok := definition["annotations"].(map[string]any); !ok {
			t.Errorf("%s annotations missing", wantNames[i])
		}
	}
	run := listed[1].(map[string]any)
	runAnnotations := run["annotations"].(map[string]any)
	if runAnnotations["destructiveHint"] != true || runAnnotations["openWorldHint"] != true {
		t.Errorf("run annotations = %#v", runAnnotations)
	}
	peekAnnotations := listed[2].(map[string]any)["annotations"].(map[string]any)
	if peekAnnotations["readOnlyHint"] != true {
		t.Errorf("peek annotations = %#v", peekAnnotations)
	}
	peekProperties := listed[2].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if got := peekProperties["tail"].(map[string]any)["minimum"]; got != float64(1) {
		t.Errorf("peek tail minimum = %v, want 1", got)
	}
	logProperties := listed[5].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any)
	rangeSchema := logProperties["range"].(map[string]any)
	if got := rangeSchema["pattern"]; got != `^[1-9][0-9]*:(?:[1-9][0-9]*|end)$` {
		t.Errorf("log range pattern = %v", got)
	}
	if got := rangeSchema["description"].(string); !strings.Contains(got, "A:end") {
		t.Errorf("log range description = %q", got)
	}
}

func TestToolCallsBuildExactArgv(t *testing.T) {
	fake := &fakeExecutor{}
	messages := []string{
		request(1, "initialize", initializeParams(ProtocolVersion)),
		notification("notifications/initialized", map[string]any{}),
	}
	calls := []struct {
		name      string
		arguments map[string]any
		want      []string
	}{
		{"pairmux_new", map[string]any{"name": "build", "cwd": "/tmp/work tree", "command": "python -q"}, []string{"new", "--name", "build", "--cwd", "/tmp/work tree", "--cmd", "python -q"}},
		{"pairmux_run", map[string]any{"terminal": "build", "command": "make test", "timeout": "30s", "head": 2, "tail": 3}, []string{"run", "build", "--timeout", "30s", "--head", "2", "--tail", "3", "--", "make test"}},
		{"pairmux_peek", map[string]any{"terminal": "build", "screen": true}, []string{"peek", "build", "--screen"}},
		{"pairmux_wait", map[string]any{"terminal": "build", "idle_ms": 900, "pattern": "ready|done", "human": true, "notify": true, "timeout": "2m"}, []string{"wait", "build", "--idle", "900", "--pattern", "ready|done", "--human", "--notify", "--timeout", "2m"}},
		{"pairmux_send", map[string]any{"terminal": "build", "text": "", "keys": []string{"C-c", "Enter"}, "enter": true}, []string{"send", "build", "--text", "", "--key", "C-c", "--key", "Enter", "--enter"}},
		{"pairmux_log", map[string]any{"terminal": "build", "command_id": 7}, []string{"log", "build", "--cmd", "7"}},
		{"pairmux_ls", map[string]any{}, []string{"ls"}},
		{"pairmux_kill", map[string]any{"all": true}, []string{"kill", "--all"}},
		{"pairmux_note", map[string]any{"terminal": "build", "text": "--json is literal"}, []string{"note", "build", "--", "--json is literal"}},
		{"pairmux_doctor", map[string]any{}, []string{"doctor"}},
	}
	for i, call := range calls {
		messages = append(messages, request(i+2, "tools/call", map[string]any{
			"name": call.name, "arguments": call.arguments,
		}))
	}
	responses, stderr := serveMessages(t, fake, messages...)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if len(responses) != len(calls)+1 {
		t.Fatalf("responses = %d, want %d", len(responses), len(calls)+1)
	}
	gotCalls := fake.callsSnapshot()
	if len(gotCalls) != len(calls) {
		t.Fatalf("executor calls = %d, want %d", len(gotCalls), len(calls))
	}
	prefix := []string{"--json", "--socket", "mcp-test-socket", "--"}
	wantCalls := make([][]string, 0, len(calls))
	for i, call := range calls {
		want := append(append([]string(nil), prefix...), call.want...)
		wantCalls = append(wantCalls, want)
		result := resultMap(t, responseByIntegerID(t, responses, i+2))
		if result["isError"] != false {
			t.Errorf("%s result = %#v", call.name, result)
		}
		structured := result["structuredContent"].(map[string]any)
		if structured["schema"] != "pairmux.v1" || structured["ok"] != true {
			t.Errorf("%s structuredContent = %#v", call.name, structured)
		}
	}
	sortArgv(gotCalls)
	sortArgv(wantCalls)
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Errorf("executor argv = %#v\nwant %#v", gotCalls, wantCalls)
	}
}

func TestInvalidRequestsAndToolArguments(t *testing.T) {
	t.Run("parse error", func(t *testing.T) {
		responses, stderr := serveMessages(t, &fakeExecutor{}, `{not json`)
		if got := responseErrorCode(responses[0]); got != codeParseError {
			t.Errorf("code = %d, want %d", got, codeParseError)
		}
		if !strings.Contains(stderr, "parse error") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("valid non-object is invalid request", func(t *testing.T) {
		responses, _ := serveMessages(t, &fakeExecutor{}, `[]`)
		if got := responseErrorCode(responses[0]); got != codeInvalidRequest {
			t.Errorf("code = %d, want %d", got, codeInvalidRequest)
		}
	})

	t.Run("malformed initialize", func(t *testing.T) {
		responses, _ := serveMessages(t, &fakeExecutor{}, request(1, "initialize", map[string]any{}))
		if got := responseErrorCode(responses[0]); got != codeInvalidParams {
			t.Errorf("code = %d, want %d", got, codeInvalidParams)
		}
	})

	t.Run("call protocol and validation errors", func(t *testing.T) {
		fake := &fakeExecutor{}
		responses, _ := serveMessages(t, fake,
			request(1, "initialize", initializeParams(ProtocolVersion)),
			notification("notifications/initialized", map[string]any{}),
			request(2, "tools/call", map[string]any{"name": "missing_tool", "arguments": map[string]any{}}),
			request(3, "tools/call", map[string]any{"name": "pairmux_run", "arguments": map[string]any{"command": "echo hi"}}),
			request(4, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{"extra": true}}),
			request(5, "tools/call", map[string]any{"name": 42}),
			request(6, "tools/list", map[string]any{"cursor": "never-issued"}),
			request(7, "tools/call", map[string]any{"name": "pairmux_ls", "task": map[string]any{}}),
		)
		if got := responseErrorCode(responses[1]); got != codeInvalidParams {
			t.Errorf("unknown tool code = %d", got)
		}
		for _, index := range []int{2, 3} {
			result := resultMap(t, responses[index])
			if result["isError"] != true {
				t.Errorf("response %d = %#v, want tool error", index, result)
			}
		}
		if got := responseErrorCode(responses[4]); got != codeInvalidParams {
			t.Errorf("bad name code = %d", got)
		}
		if got := responseErrorCode(responses[5]); got != codeInvalidParams {
			t.Errorf("cursor code = %d", got)
		}
		if got := responseErrorCode(responses[6]); got != codeMethodNotFound {
			t.Errorf("task code = %d", got)
		}
		if calls := fake.callsSnapshot(); len(calls) != 0 {
			t.Errorf("invalid requests executed child argv: %#v", calls)
		}
	})
}

func TestNullToolArgumentsAreRejected(t *testing.T) {
	fake := &fakeExecutor{}
	messages := []string{
		request(1, "initialize", initializeParams(ProtocolVersion)),
		notification("notifications/initialized", map[string]any{}),
		request(2, "tools/list", map[string]any{"cursor": nil}),
	}
	tests := []struct {
		name      string
		arguments map[string]any
	}{
		{"pairmux_new", map[string]any{"cwd": nil}},
		{"pairmux_peek", map[string]any{"terminal": "build", "screen": nil}},
		{"pairmux_peek", map[string]any{"terminal": "build", "tail": nil}},
		{"pairmux_send", map[string]any{"terminal": "build", "keys": nil}},
		{"pairmux_send", map[string]any{"terminal": "build", "keys": []any{nil}}},
		{"pairmux_kill", map[string]any{"all": nil}},
	}
	for i, test := range tests {
		messages = append(messages, request(i+3, "tools/call", map[string]any{
			"name": test.name, "arguments": test.arguments,
		}))
	}

	responses, _ := serveMessages(t, fake, messages...)
	if got := responseErrorCode(responseByIntegerID(t, responses, 2)); got != codeInvalidParams {
		t.Errorf("cursor:null code = %d, want %d", got, codeInvalidParams)
	}
	for i, test := range tests {
		result := resultMap(t, responseByIntegerID(t, responses, i+3))
		if result["isError"] != true {
			t.Errorf("%s null argument result = %#v", test.name, result)
		}
	}
	if calls := fake.callsSnapshot(); len(calls) != 0 {
		t.Errorf("null arguments executed child argv: %#v", calls)
	}
}

func TestRequestIDsAcceptEveryJSONNumberForm(t *testing.T) {
	for _, raw := range []string{"0", "-0", "1.5", "-0.25", "1e3", "2E-4", "1e999999"} {
		t.Run(raw, func(t *testing.T) {
			id := json.RawMessage(raw)
			if !validRequestID(id) {
				t.Fatalf("validRequestID(%q) = false", raw)
			}
			response := resultResponse(id, map[string]any{})
			if got := string(response.ID); got != raw {
				t.Errorf("response id = %q, want %q", got, raw)
			}
		})
	}
	for _, raw := range []string{"null", "true", "[]", "{}", `"ok" 1`, "01"} {
		if validRequestID(json.RawMessage(raw)) {
			t.Errorf("validRequestID(%q) = true", raw)
		}
	}
}

func TestEnvelopeConversion(t *testing.T) {
	t.Run("pairmux error remains structured tool error", func(t *testing.T) {
		result, err := envelopeResult(Execution{
			Stdout:   []byte(`{"schema":"pairmux.v1","ok":false,"status":"error","error":{"code":"E_BUSY","message":"busy"}}`),
			ExitCode: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result["isError"] != true {
			t.Errorf("result = %#v", result)
		}
		structured := result["structuredContent"].(map[string]any)
		if structured["ok"] != false || structured["schema"] != "pairmux.v1" {
			t.Errorf("structuredContent = %#v", structured)
		}
		content := result["content"].([]map[string]any)
		var textEnvelope map[string]any
		if err := json.Unmarshal([]byte(content[0]["text"].(string)), &textEnvelope); err != nil {
			t.Fatalf("text content is not JSON: %v", err)
		}
		if textEnvelope["ok"] != false {
			t.Errorf("text envelope = %#v", textEnvelope)
		}
	})

	for _, test := range []struct {
		name string
		raw  string
	}{
		{"non-json", "not-json"},
		{"wrong schema", `{"schema":"other","ok":true}`},
		{"missing ok", `{"schema":"pairmux.v1"}`},
		{"multiple values", `{"schema":"pairmux.v1","ok":true} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := envelopeResult(Execution{Stdout: []byte(test.raw)}); err == nil {
				t.Fatal("expected envelope validation error")
			}
		})
	}
}

func TestExecutorFailureIsInternalError(t *testing.T) {
	fake := &fakeExecutor{err: fmt.Errorf("exec unavailable")}
	responses, _ := serveMessages(t, fake,
		request(1, "initialize", initializeParams(ProtocolVersion)),
		notification("notifications/initialized", map[string]any{}),
		request(2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}),
	)
	response := responseByIntegerID(t, responses, 2)
	if code := responseErrorCode(response); code != codeInternalError {
		t.Errorf("executor failure code = %d, want %d: %#v", code, codeInternalError, response)
	}
}

func TestMalformedTrustedOutputIsInternalError(t *testing.T) {
	for _, test := range []struct {
		name      string
		execution Execution
	}{
		{"non-json", Execution{Stdout: []byte("not-json")}},
		{"wrong-schema", Execution{Stdout: []byte(`{"schema":"other","ok":true}`)}},
		{"multiple-values", Execution{Stdout: []byte(`{"schema":"pairmux.v1","ok":true} {}`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeExecutor{executions: []Execution{test.execution}}
			responses, _ := serveMessages(t, fake,
				request(1, "initialize", initializeParams(ProtocolVersion)),
				notification("notifications/initialized", map[string]any{}),
				request(2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}),
			)
			response := responseByIntegerID(t, responses, 2)
			if code := responseErrorCode(response); code != codeInternalError {
				t.Errorf("code = %d, want %d: %#v", code, codeInternalError, response)
			}
		})
	}
}

func TestTruncatedToolOutputReturnsActionableToolError(t *testing.T) {
	for _, test := range []struct {
		name      string
		execution Execution
	}{
		{"stdout", Execution{Stdout: []byte(`{"schema":"pairmux.v1","ok":true}`), StdoutTruncated: true}},
		{"stderr", Execution{Stderr: []byte("diagnostic flood"), StderrTruncated: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeExecutor{executions: []Execution{test.execution}}
			responses, _ := serveMessages(t, fake,
				request(1, "initialize", initializeParams(ProtocolVersion)),
				notification("notifications/initialized", map[string]any{}),
				request(2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}),
			)
			result := resultMap(t, responseByIntegerID(t, responses, 2))
			if result["isError"] != true {
				t.Fatalf("result = %#v", result)
			}
			content := result["content"].([]any)
			text := content[0].(map[string]any)["text"].(string)
			for _, fragment := range []string{"terminated", "pairmux_log", "command_id", "grep", "smaller range"} {
				if !strings.Contains(text, fragment) {
					t.Errorf("tool error %q missing %q", text, fragment)
				}
			}
		})
	}
}

type blockingExecutor struct {
	started   chan []string
	cancelled chan struct{}
	once      sync.Once
}

func newBlockingExecutor(buffer int) *blockingExecutor {
	return &blockingExecutor{
		started:   make(chan []string, buffer),
		cancelled: make(chan struct{}),
	}
}

func (e *blockingExecutor) Execute(ctx context.Context, argv []string) (Execution, error) {
	e.started <- append([]string(nil), argv...)
	<-ctx.Done()
	e.once.Do(func() { close(e.cancelled) })
	return Execution{}, ctx.Err()
}

func TestToolCallDoesNotBlockPingAndCancellationSuppressesResponse(t *testing.T) {
	executor := newBlockingExecutor(1)
	session := newTestSession(t, executor)
	initializeSession(t, session)

	session.send(request(2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}))
	waitReceive(t, executor.started, "tool executor start")
	session.send(request(3, "ping", map[string]any{}))
	if responseIntegerID(t, session.receive()) != 3 {
		t.Fatal("ping did not bypass blocked tool call")
	}

	session.send(notification("notifications/cancelled", map[string]any{"requestId": 2, "reason": "test cancellation"}))
	waitClosed(t, executor.cancelled, "tool executor cancellation")
	session.send(request(4, "ping", map[string]any{}))
	if responseIntegerID(t, session.receive()) != 4 {
		t.Fatal("cancelled tool response was not suppressed")
	}

	session.closeInput()
	assertNoResponseID(t, session.drainResponses(), 2)
}

func TestEOFAndRootCancellationCancelInflightTools(t *testing.T) {
	for _, test := range []struct {
		name string
		stop func(*testSession)
	}{
		{"eof", func(session *testSession) { session.closeInput() }},
		{"root-context", func(session *testSession) { session.cancelRoot() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := newBlockingExecutor(1)
			session := newTestSession(t, executor)
			initializeSession(t, session)
			session.send(request(2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}))
			waitReceive(t, executor.started, "tool executor start")

			test.stop(session)
			waitClosed(t, executor.cancelled, "tool executor cancellation")
			assertNoResponseID(t, session.drainResponses(), 2)
		})
	}
}

func TestToolWorkerPoolIsBoundedAndPingRemainsResponsive(t *testing.T) {
	executor := newBlockingExecutor(maxConcurrentToolCalls + 1)
	session := newTestSession(t, executor)
	initializeSession(t, session)
	for i := 0; i < maxConcurrentToolCalls; i++ {
		session.send(request(i+2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}))
	}
	for i := 0; i < maxConcurrentToolCalls; i++ {
		waitReceive(t, executor.started, "tool executor start")
	}
	select {
	case argv := <-executor.started:
		t.Fatalf("worker pool started more than %d calls; extra argv: %#v", maxConcurrentToolCalls, argv)
	default:
	}
	for i := 0; i < maxQueuedToolCalls; i++ {
		session.send(request(maxConcurrentToolCalls+i+2, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}))
	}
	overflowID := maxConcurrentToolCalls + maxQueuedToolCalls + 2
	session.send(request(overflowID, "tools/call", map[string]any{"name": "pairmux_ls", "arguments": map[string]any{}}))
	overflow := session.receive()
	if responseIntegerID(t, overflow) != overflowID || responseErrorCode(overflow) != codeInternalError {
		t.Fatalf("queue overflow response = %#v, want id %d and code %d", overflow, overflowID, codeInternalError)
	}

	session.send(request(100, "ping", map[string]any{}))
	if responseIntegerID(t, session.receive()) != 100 {
		t.Fatal("ping did not remain responsive while worker pool was saturated")
	}
	session.cancelRoot()
}

func TestSubprocessExecutorPreservesArgv(t *testing.T) {
	executor := SubprocessExecutor{
		Path: os.Args[0],
		Env:  append(os.Environ(), "GO_WANT_MCP_HELPER=1"),
	}
	argv := []string{"-test.run=^TestMCPHelperProcess$", "--", "alpha beta", "$(touch nope)", "", "--json"}
	execution, err := executor.Execute(context.Background(), argv)
	if err != nil {
		t.Fatal(err)
	}
	if execution.ExitCode != 0 || len(execution.Stderr) != 0 {
		t.Fatalf("execution = %+v", execution)
	}
	var got []string
	if err := json.Unmarshal(bytes.TrimSpace(execution.Stdout), &got); err != nil {
		t.Fatalf("helper stdout %q: %v", execution.Stdout, err)
	}
	want := argv[2:]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %#v, want %#v", got, want)
	}
}

func TestSubprocessExecutorReturnsNonzeroExitStatus(t *testing.T) {
	executor := SubprocessExecutor{
		Path: os.Args[0],
		Env:  append(os.Environ(), "GO_WANT_MCP_HELPER=1", "GO_MCP_HELPER_FAIL=1"),
	}
	execution, err := executor.Execute(context.Background(), []string{"-test.run=^TestMCPHelperProcess$", "--", "still captured"})
	if err != nil {
		t.Fatalf("nonzero child exit became execution error: %v", err)
	}
	if execution.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", execution.ExitCode)
	}
	if !bytes.Contains(execution.Stdout, []byte("still captured")) {
		t.Errorf("stdout = %q", execution.Stdout)
	}
}

func TestBoundedBufferRetainsPrefixAndReportsTruncation(t *testing.T) {
	buffer := newBoundedBuffer(5)
	if n, err := buffer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
	}
	if got := buffer.String(); got != "abcde" {
		t.Errorf("buffer = %q, want abcde", got)
	}
	if !buffer.Truncated() {
		t.Error("buffer did not report truncation")
	}
	if n, err := buffer.Write([]byte("ij")); err != nil || n != 2 {
		t.Fatalf("second Write = (%d, %v), want (2, nil)", n, err)
	}
}

func TestSubprocessExecutorBoundsBothOutputStreams(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			executor := SubprocessExecutor{
				Path: os.Args[0],
				Env:  append(os.Environ(), "GO_WANT_MCP_HELPER=1", "GO_MCP_HELPER_BIG="+stream),
			}
			execution, err := executor.Execute(context.Background(), []string{"-test.run=^TestMCPHelperProcess$"})
			if err != nil {
				t.Fatal(err)
			}
			if stream == "stdout" {
				if len(execution.Stdout) != maxChildStdoutBytes || !execution.StdoutTruncated {
					t.Errorf("stdout length/truncated = %d/%t, want %d/true", len(execution.Stdout), execution.StdoutTruncated, maxChildStdoutBytes)
				}
			} else if len(execution.Stderr) != maxChildStderrBytes || !execution.StderrTruncated {
				t.Errorf("stderr length/truncated = %d/%t, want %d/true", len(execution.Stderr), execution.StderrTruncated, maxChildStderrBytes)
			}
		})
	}
}

func TestSubprocessExecutorTerminatesContinuousOutputAtLimit(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			executor := SubprocessExecutor{
				Path: os.Args[0],
				Env:  append(os.Environ(), "GO_WANT_MCP_HELPER=1", "GO_MCP_HELPER_FLOOD="+stream),
			}
			result := make(chan struct {
				execution Execution
				err       error
			}, 1)
			go func() {
				execution, err := executor.Execute(ctx, []string{"-test.run=^TestMCPHelperProcess$"})
				result <- struct {
					execution Execution
					err       error
				}{execution: execution, err: err}
			}()

			select {
			case got := <-result:
				if got.err != nil {
					t.Fatalf("Execute: %v", got.err)
				}
				if stream == "stdout" && !got.execution.StdoutTruncated {
					t.Fatalf("stdout flood did not set StdoutTruncated: %+v", got.execution)
				}
				if stream == "stderr" && !got.execution.StderrTruncated {
					t.Fatalf("stderr flood did not set StderrTruncated: %+v", got.execution)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("continuous-output helper remained alive after exceeding capture limit")
			}
		})
	}
}

func TestSubprocessCancellationKillsProcessGroupGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	executor := SubprocessExecutor{
		Path: os.Args[0],
		Env: append(os.Environ(),
			"GO_WANT_MCP_HELPER=1",
			"GO_MCP_HELPER_TREE=1",
			"GO_MCP_HELPER_PID_FILE="+pidFile,
		),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(ctx, []string{"-test.run=^TestMCPHelperProcess$"})
		result <- err
	}()

	grandchildPID := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(grandchildPID, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subprocess executor did not return after cancellation")
	}
	waitForProcessExit(t, grandchildPID)
}

func TestSubprocessCancellationBoundsDetachedPipeHolderWait(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "detached.pid")
	executor := SubprocessExecutor{
		Path: os.Args[0],
		Env: append(os.Environ(),
			"GO_WANT_MCP_HELPER=1",
			"GO_MCP_HELPER_DETACHED_TREE=1",
			"GO_MCP_HELPER_PID_FILE="+pidFile,
		),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := executor.Execute(ctx, []string{"-test.run=^TestMCPHelperProcess$"})
		result <- err
	}()

	detachedPID := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(detachedPID, syscall.SIGKILL) })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subprocess executor waited indefinitely on detached inherited pipes")
	}
	if err := syscall.Kill(detachedPID, 0); err != nil {
		t.Fatalf("detached pipe holder was not alive during WaitDelay test: %v", err)
	}
	if err := syscall.Kill(detachedPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill detached pipe holder: %v", err)
	}
	waitForProcessExit(t, detachedPID)
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	if os.Getenv("GO_MCP_GRANDCHILD") == "1" {
		signal.Ignore(syscall.SIGTERM)
		select {}
	}
	if os.Getenv("GO_MCP_DETACHED_GRANDCHILD") == "1" {
		if _, err := syscall.Setsid(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "setsid: %v\n", err)
			os.Exit(2)
		}
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP)
		if err := os.WriteFile(os.Getenv("GO_MCP_HELPER_PID_FILE"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "write detached pid: %v\n", err)
			os.Exit(2)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("GO_MCP_HELPER_TREE") == "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMCPHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_MCP_GRANDCHILD=1")
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "start grandchild: %v\n", err)
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("GO_MCP_HELPER_PID_FILE"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "write grandchild pid: %v\n", err)
			os.Exit(2)
		}
		select {}
	}
	if os.Getenv("GO_MCP_HELPER_DETACHED_TREE") == "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMCPHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_MCP_DETACHED_GRANDCHILD=1")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "start detached grandchild: %v\n", err)
			os.Exit(2)
		}
		select {}
	}
	if stream := os.Getenv("GO_MCP_HELPER_BIG"); stream != "" {
		if stream == "stdout" {
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{'o'}, maxChildStdoutBytes+1024))
		} else {
			_, _ = os.Stderr.Write(bytes.Repeat([]byte{'e'}, maxChildStderrBytes+1024))
		}
		os.Exit(0)
	}
	if stream := os.Getenv("GO_MCP_HELPER_FLOOD"); stream != "" {
		signal.Ignore(syscall.SIGTERM)
		writer := io.Writer(os.Stdout)
		if stream == "stderr" {
			writer = os.Stderr
		}
		chunk := bytes.Repeat([]byte{'x'}, 32*1024)
		for {
			_, _ = writer.Write(chunk)
		}
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		_, _ = fmt.Fprintln(os.Stderr, "missing --")
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(os.Args[separator+1:])
	if os.Getenv("GO_MCP_HELPER_FAIL") == "1" {
		os.Exit(7)
	}
	os.Exit(0)
}

func serveMessages(t *testing.T, executor Executor, messages ...string) ([]map[string]any, string) {
	t.Helper()
	session := newTestSession(t, executor)
	for _, message := range messages {
		session.send(message)
	}
	responses := make([]map[string]any, 0, expectedResponseCount(messages))
	for range expectedResponseCount(messages) {
		responses = append(responses, session.receive())
	}
	session.closeInput()
	responses = append(responses, session.drainResponses()...)
	return responses, session.stderr.String()
}

type testSession struct {
	t           *testing.T
	input       *io.PipeWriter
	cancel      context.CancelFunc
	responses   chan map[string]any
	decodeError chan error
	done        chan struct{}
	serveErr    error
	stderr      bytes.Buffer
}

func newTestSession(t *testing.T, executor Executor) *testSession {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	session := &testSession{
		t:           t,
		input:       inputWriter,
		cancel:      cancel,
		responses:   make(chan map[string]any, 256),
		decodeError: make(chan error, 1),
		done:        make(chan struct{}),
	}
	server := New(executor, "mcp-test-socket", "test-version", &session.stderr)
	go func() {
		session.serveErr = server.Serve(ctx, inputReader, outputWriter)
		_ = outputWriter.Close()
		close(session.done)
	}()
	go func() {
		defer close(session.responses)
		decoder := json.NewDecoder(outputReader)
		for {
			var response map[string]any
			if err := decoder.Decode(&response); err != nil {
				if !errors.Is(err, io.EOF) {
					session.decodeError <- err
				}
				return
			}
			session.responses <- response
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = inputWriter.Close()
		session.awaitDone()
	})
	return session
}

func (s *testSession) send(message string) {
	s.t.Helper()
	if _, err := fmt.Fprintln(s.input, message); err != nil {
		s.t.Fatalf("write MCP request: %v", err)
	}
}

func (s *testSession) receive() map[string]any {
	s.t.Helper()
	select {
	case response, ok := <-s.responses:
		if !ok {
			<-s.done
			s.t.Fatalf("MCP response stream closed early (Serve error: %v)", s.serveErr)
		}
		return response
	case err := <-s.decodeError:
		s.t.Fatalf("decode MCP response: %v", err)
	case <-time.After(3 * time.Second):
		s.t.Fatal("timed out waiting for MCP response")
	}
	return nil
}

func (s *testSession) closeInput() {
	s.t.Helper()
	_ = s.input.Close()
	s.awaitDone()
}

func (s *testSession) cancelRoot() {
	s.t.Helper()
	s.cancel()
	s.awaitDone()
}

func (s *testSession) awaitDone() {
	s.t.Helper()
	select {
	case <-s.done:
		if s.serveErr != nil {
			s.t.Fatalf("Serve: %v", s.serveErr)
		}
	case <-time.After(3 * time.Second):
		s.t.Fatal("timed out waiting for MCP server shutdown")
	}
}

func (s *testSession) drainResponses() []map[string]any {
	s.t.Helper()
	var responses []map[string]any
	for response := range s.responses {
		responses = append(responses, response)
	}
	select {
	case err := <-s.decodeError:
		s.t.Fatalf("decode MCP response: %v", err)
	default:
	}
	return responses
}

func initializeSession(t *testing.T, session *testSession) {
	t.Helper()
	session.send(request(1, "initialize", initializeParams(ProtocolVersion)))
	if responseIntegerID(t, session.receive()) != 1 {
		t.Fatal("unexpected initialize response id")
	}
	session.send(notification("notifications/initialized", map[string]any{}))
}

func expectedResponseCount(messages []string) int {
	count := 0
	for _, raw := range messages {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
			count++
			continue
		}
		if _, hasID := object["id"]; hasID {
			count++
		}
	}
	return count
}

func responseByIntegerID(t *testing.T, responses []map[string]any, id int) map[string]any {
	t.Helper()
	for _, response := range responses {
		value, ok := response["id"].(float64)
		if ok && int(value) == id && value == float64(id) {
			return response
		}
	}
	t.Fatalf("no response for id %d in %#v", id, responses)
	return nil
}

func responseIntegerID(t *testing.T, response map[string]any) int {
	t.Helper()
	value, ok := response["id"].(float64)
	if !ok || value != float64(int(value)) {
		t.Fatalf("response id is not an integer: %#v", response)
	}
	return int(value)
}

func assertNoResponseID(t *testing.T, responses []map[string]any, id int) {
	t.Helper()
	for _, response := range responses {
		if value, ok := response["id"].(float64); ok && value == float64(id) {
			t.Fatalf("unexpected response for cancelled id %d: %#v", id, response)
		}
	}
}

func sortArgv(calls [][]string) {
	sort.Slice(calls, func(i, j int) bool {
		return strings.Join(calls[i], "\x00") < strings.Join(calls[j], "\x00")
	})
}

func waitReceive[T any](t *testing.T, channel <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
	var zero T
	return zero
}

func waitClosed(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid helper pid file %q: %v", raw, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper pid file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper pid file")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("probe grandchild %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d remained alive after process-group cancellation", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func request(id int, method string, params any) string {
	return marshalMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func notification(method string, params any) string {
	return marshalMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func marshalMessage(message any) string {
	raw, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func initializeParams(protocolVersion string) map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
	}
}

func responseErrorCode(response map[string]any) int {
	errorValue, ok := response["error"].(map[string]any)
	if !ok {
		return 0
	}
	return int(errorValue["code"].(float64))
}

func resultMap(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result object: %#v", response)
	}
	return result
}
