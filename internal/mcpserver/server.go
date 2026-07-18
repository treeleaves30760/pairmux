// Package mcpserver exposes pairmux's existing CLI commands as a stdio Model
// Context Protocol server. The server owns protocol framing and typed argv
// construction only; terminal behavior remains in the pairmux CLI subprocess.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// ProtocolVersion is the MCP revision implemented by the stdio server.
const ProtocolVersion = "2025-11-25"

const (
	maxMessageBytes        = 8 << 20
	maxConcurrentToolCalls = 4
	maxQueuedToolCalls     = 32
)

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

type lifecycle uint8

const (
	lifecycleNew lifecycle = iota
	lifecycleInitialized
	lifecycleReady
)

// Server is one stateful MCP stdio connection.
type Server struct {
	executor Executor
	socket   string
	version  string
	stderr   io.Writer
	tools    []tool
	state    lifecycle
	logMu    sync.Mutex
}

// New constructs an MCP server. socket selects the same tmux endpoint that
// launched `pairmux mcp serve`; version is the pairmux build version.
func New(executor Executor, socket, version string, stderr io.Writer) *Server {
	if socket == "" {
		socket = core.DefaultSocket
	}
	if version == "" {
		version = "unknown"
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &Server{
		executor: executor,
		socket:   socket,
		version:  version,
		stderr:   stderr,
		tools:    tools(),
	}
}

// Serve reads newline-delimited JSON-RPC messages until stdin reaches EOF.
// stdout receives protocol responses only; diagnostics go to the configured
// stderr writer.
func (s *Server) Serve(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()

	input := make(chan readEvent)
	go scanMessages(serveCtx, stdin, input)

	jobs := make(chan toolJob, maxQueuedToolCalls)
	completions := make(chan toolCompletion, maxConcurrentToolCalls)
	var workers sync.WaitGroup
	for range maxConcurrentToolCalls {
		workers.Add(1)
		go s.toolWorker(serveCtx, jobs, completions, &workers)
	}

	inflight := make(map[string]*inflightRequest)
	cancelAll := func() {
		for _, request := range inflight {
			request.cancelled = true
			request.cancel()
		}
	}
	shutdown := func() {
		cancelServe()
		cancelAll()
		if closer, ok := stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		workers.Wait()
	}
	defer shutdown()

	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	writeResponse := func(response *rpcResponse) error {
		if response == nil {
			return nil
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write MCP response: %w", err)
		}
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-input:
			if !ok {
				return nil
			}
			if event.err != nil {
				return fmt.Errorf("read MCP request: %w", event.err)
			}
			outcome := s.handle(event.line)
			if outcome.cancelKey != "" {
				if request := inflight[outcome.cancelKey]; request != nil {
					request.cancelled = true
					request.cancel()
				}
			}
			if outcome.call != nil {
				key, _ := requestIDKey(outcome.call.id)
				if _, duplicate := inflight[key]; duplicate {
					outcome.response = errorResponse(outcome.call.id, codeInvalidRequest, "Invalid Request", "request id is already in flight")
				} else {
					callCtx, cancel := context.WithCancel(serveCtx)
					job := toolJob{key: key, ctx: callCtx, call: *outcome.call}
					inflight[key] = &inflightRequest{cancel: cancel}
					select {
					case jobs <- job:
					case <-serveCtx.Done():
						cancel()
						delete(inflight, key)
						return nil
					default:
						cancel()
						delete(inflight, key)
						outcome.response = errorResponse(outcome.call.id, codeInternalError, "Internal error", "too many pending tool calls")
					}
				}
			}
			if err := writeResponse(outcome.response); err != nil {
				return err
			}
		case completion := <-completions:
			request := inflight[completion.key]
			if request == nil {
				continue
			}
			delete(inflight, completion.key)
			request.cancel()
			if request.cancelled {
				continue
			}
			if err := writeResponse(completion.response); err != nil {
				return err
			}
		}
	}
}

type readEvent struct {
	line []byte
	err  error
}

func scanMessages(ctx context.Context, stdin io.Reader, output chan<- readEvent) {
	defer close(output)
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		select {
		case output <- readEvent{line: line}:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case output <- readEvent{err: err}:
		case <-ctx.Done():
		}
	}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type parsedMessage struct {
	id           json.RawMessage
	hasID        bool
	method       string
	params       json.RawMessage
	hasParams    bool
	notification bool
}

type dispatchOutcome struct {
	response  *rpcResponse
	call      *toolCall
	cancelKey string
}

type toolCall struct {
	id   json.RawMessage
	argv []string
}

type toolJob struct {
	key  string
	ctx  context.Context
	call toolCall
}

type toolCompletion struct {
	key      string
	response *rpcResponse
}

type inflightRequest struct {
	cancel    context.CancelFunc
	cancelled bool
}

func (s *Server) handle(line []byte) dispatchOutcome {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		s.logf("ignored empty input line")
		return dispatchOutcome{}
	}
	if !json.Valid(line) {
		s.logf("parse error: invalid JSON-RPC message")
		return dispatchOutcome{response: errorResponse(nil, codeParseError, "Parse error", nil)}
	}
	if len(line) == 0 || line[0] != '{' {
		return dispatchOutcome{response: errorResponse(nil, codeInvalidRequest, "Invalid Request", "message must be an object")}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil || object == nil {
		return dispatchOutcome{response: errorResponse(nil, codeInvalidRequest, "Invalid Request", "message must be an object")}
	}

	message, response := s.parseObject(object)
	if response != nil {
		return dispatchOutcome{response: response}
	}
	if message == nil {
		return dispatchOutcome{}
	}

	switch message.method {
	case "initialize":
		if message.notification {
			s.logf("ignored initialize notification")
			return dispatchOutcome{}
		}
		return dispatchOutcome{response: s.initialize(message)}
	case "notifications/initialized":
		return dispatchOutcome{response: s.initialized(message)}
	case "notifications/cancelled":
		if !message.notification {
			return dispatchOutcome{response: errorResponse(message.id, codeInvalidRequest, "notifications/cancelled must be a notification", nil)}
		}
		key, err := cancellationKey(message)
		if err != nil {
			s.logf("ignored malformed cancellation notification: %v", err)
			return dispatchOutcome{}
		}
		return dispatchOutcome{cancelKey: key}
	case "ping":
		if message.notification {
			return dispatchOutcome{}
		}
		return dispatchOutcome{response: resultResponse(message.id, map[string]any{})}
	}

	if message.notification {
		s.logf("ignored unknown notification %q", message.method)
		return dispatchOutcome{}
	}
	if s.state != lifecycleReady {
		return dispatchOutcome{response: errorResponse(message.id, codeInvalidRequest, "Server is not initialized", nil)}
	}

	switch message.method {
	case "tools/list":
		return dispatchOutcome{response: s.listTools(message)}
	case "tools/call":
		call, response := s.prepareToolCall(message)
		return dispatchOutcome{response: response, call: call}
	default:
		return dispatchOutcome{response: errorResponse(message.id, codeMethodNotFound, "Method not found: "+message.method, nil)}
	}
}

func (s *Server) parseObject(object map[string]json.RawMessage) (*parsedMessage, *rpcResponse) {
	id, hasID := object["id"]
	responseID := id
	if !hasID || !validRequestID(id) {
		responseID = nil
	}

	allowed := map[string]bool{"jsonrpc": true, "id": true, "method": true, "params": true}
	for key := range object {
		if !allowed[key] {
			if !hasID {
				s.logf("ignored malformed notification with field %q", key)
				return nil, nil
			}
			return nil, errorResponse(responseID, codeInvalidRequest, "Invalid Request", "unexpected field "+key)
		}
	}

	var jsonrpc string
	if err := json.Unmarshal(object["jsonrpc"], &jsonrpc); err != nil || jsonrpc != "2.0" {
		if !hasID {
			s.logf("ignored malformed notification with invalid jsonrpc")
			return nil, nil
		}
		return nil, errorResponse(responseID, codeInvalidRequest, "Invalid Request", "jsonrpc must be \"2.0\"")
	}
	var method string
	if err := json.Unmarshal(object["method"], &method); err != nil || method == "" {
		if !hasID {
			s.logf("ignored message without a method")
			return nil, nil
		}
		return nil, errorResponse(responseID, codeInvalidRequest, "Invalid Request", "method must be a non-empty string")
	}
	if hasID && !validRequestID(id) {
		return nil, errorResponse(nil, codeInvalidRequest, "Invalid Request", "id must be a string or number")
	}
	params, hasParams := object["params"]
	if hasParams && !isJSONObject(params) {
		if !hasID {
			s.logf("ignored notification %q with non-object params", method)
			return nil, nil
		}
		return nil, errorResponse(id, codeInvalidParams, "Invalid params", "params must be an object")
	}
	return &parsedMessage{
		id: id, hasID: hasID, method: method, params: params, hasParams: hasParams, notification: !hasID,
	}, nil
}

func (s *Server) initialize(message *parsedMessage) *rpcResponse {
	if s.state != lifecycleNew {
		return errorResponse(message.id, codeInvalidRequest, "Server is already initialized", nil)
	}
	params, err := decodeObject(message.params, message.hasParams)
	if err != nil {
		return errorResponse(message.id, codeInvalidParams, "Invalid initialize params", err.Error())
	}
	requested, err := requiredString(params, "protocolVersion")
	if err != nil {
		return errorResponse(message.id, codeInvalidParams, "Invalid initialize params", err.Error())
	}
	if err := requireObject(params, "capabilities"); err != nil {
		return errorResponse(message.id, codeInvalidParams, "Invalid initialize params", err.Error())
	}
	if err := requireImplementation(params, "clientInfo"); err != nil {
		return errorResponse(message.id, codeInvalidParams, "Invalid initialize params", err.Error())
	}

	negotiated := ProtocolVersion
	if requested == ProtocolVersion {
		negotiated = requested
	}
	s.state = lifecycleInitialized
	return resultResponse(message.id, map[string]any{
		"protocolVersion": negotiated,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "pairmux",
			"title":   "pairmux terminal tools",
			"version": s.version,
		},
		"instructions": "Use pairmux_new once per workstream, then pairmux_run and inspect the pairmux.v1 status. Continue with pairmux_wait rather than sleeping. Never guess or send secrets; hand secret prompts to a human.",
	})
}

func (s *Server) initialized(message *parsedMessage) *rpcResponse {
	if !message.notification {
		return errorResponse(message.id, codeInvalidRequest, "notifications/initialized must be a notification", nil)
	}
	if s.state != lifecycleInitialized {
		s.logf("ignored notifications/initialized outside initialization")
		return nil
	}
	s.state = lifecycleReady
	return nil
}

func (s *Server) listTools(message *parsedMessage) *rpcResponse {
	params, err := decodeObject(message.params, message.hasParams)
	if err != nil {
		return errorResponse(message.id, codeInvalidParams, "Invalid tools/list params", err.Error())
	}
	if err := allowParams(params, "cursor", "_meta"); err != nil {
		return errorResponse(message.id, codeInvalidParams, "Invalid tools/list params", err.Error())
	}
	if cursor, ok := params["cursor"]; ok {
		var value string
		if isJSONNull(cursor) || json.Unmarshal(cursor, &value) != nil {
			return errorResponse(message.id, codeInvalidParams, "Invalid tools/list params", "cursor must be a string")
		}
		return errorResponse(message.id, codeInvalidParams, "Invalid tools/list params", "this server did not issue a cursor")
	}

	definitions := make([]toolDefinition, 0, len(s.tools))
	for _, registered := range s.tools {
		definitions = append(definitions, registered.definition)
	}
	return resultResponse(message.id, map[string]any{"tools": definitions})
}

func (s *Server) prepareToolCall(message *parsedMessage) (*toolCall, *rpcResponse) {
	params, err := decodeObject(message.params, message.hasParams)
	if err != nil {
		return nil, errorResponse(message.id, codeInvalidParams, "Invalid tools/call params", err.Error())
	}
	if err := allowParams(params, "name", "arguments", "_meta", "task"); err != nil {
		return nil, errorResponse(message.id, codeInvalidParams, "Invalid tools/call params", err.Error())
	}
	if _, taskRequested := params["task"]; taskRequested {
		return nil, errorResponse(message.id, codeMethodNotFound, "Task-augmented tool calls are not supported", nil)
	}
	name, err := requiredString(params, "name")
	if err != nil {
		return nil, errorResponse(message.id, codeInvalidParams, "Invalid tools/call params", err.Error())
	}
	var selected *tool
	for i := range s.tools {
		if s.tools[i].definition.Name == name {
			selected = &s.tools[i]
			break
		}
	}
	if selected == nil {
		return nil, errorResponse(message.id, codeInvalidParams, "Unknown tool: "+name, nil)
	}

	toolArgs := arguments{}
	if raw, ok := params["arguments"]; ok {
		if err := json.Unmarshal(raw, &toolArgs); err != nil || toolArgs == nil {
			return nil, errorResponse(message.id, codeInvalidParams, "Invalid tools/call params", "arguments must be an object")
		}
	}
	command, err := selected.build(toolArgs)
	if err != nil {
		return nil, resultResponse(message.id, toolError("Invalid tool arguments: "+err.Error()))
	}
	argv := make([]string, 0, len(command)+5)
	argv = append(argv, "--json", "--socket", s.socket, "--")
	argv = append(argv, command...)

	if s.executor == nil {
		return nil, errorResponse(message.id, codeInternalError, "Internal error", "pairmux executor is not configured")
	}
	return &toolCall{id: message.id, argv: argv}, nil
}

func (s *Server) executeTool(ctx context.Context, call toolCall) *rpcResponse {
	if err := ctx.Err(); err != nil {
		return nil
	}
	execution, err := s.executor.Execute(ctx, call.argv)
	if len(execution.Stderr) > 0 {
		s.logChildStderr(execution.Stderr, execution.StderrTruncated)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return errorResponse(call.id, codeInternalError, "Internal error", "execute pairmux subprocess: "+err.Error())
	}
	if execution.StdoutTruncated || execution.StderrTruncated {
		return resultResponse(call.id, toolError(
			"pairmux subprocess output exceeded the MCP capture limit and was terminated; narrow the request by reducing run head/tail, or use pairmux_log with command_id, grep, or a smaller range",
		))
	}
	result, err := envelopeResult(execution)
	if err != nil {
		return errorResponse(call.id, codeInternalError, "Internal error", err.Error())
	}
	return resultResponse(call.id, result)
}

func (s *Server) toolWorker(ctx context.Context, jobs <-chan toolJob, completions chan<- toolCompletion, workers *sync.WaitGroup) {
	defer workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			response := s.executeTool(job.ctx, job.call)
			select {
			case completions <- toolCompletion{key: job.key, response: response}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func envelopeResult(execution Execution) (map[string]any, error) {
	raw := bytes.TrimSpace(execution.Stdout)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var envelope map[string]any
	if len(raw) == 0 || decoder.Decode(&envelope) != nil || envelope == nil {
		return nil, fmt.Errorf("pairmux subprocess returned an invalid JSON envelope")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("pairmux subprocess returned more than one JSON value")
	}
	if envelope["schema"] != core.SchemaID {
		return nil, fmt.Errorf("pairmux subprocess returned an unsupported envelope schema")
	}
	ok, valid := envelope["ok"].(bool)
	if !valid {
		return nil, fmt.Errorf("pairmux subprocess envelope is missing boolean ok")
	}
	text, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode pairmux envelope: %w", err)
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": envelope,
		"isError":           !ok || execution.ExitCode != 0,
	}, nil
}

func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": true,
	}
}

func decodeObject(raw json.RawMessage, present bool) (map[string]json.RawMessage, error) {
	if !present {
		return map[string]json.RawMessage{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("params must be an object")
	}
	return object, nil
}

func requiredString(object map[string]json.RawMessage, name string) (string, error) {
	raw, ok := object[name]
	if !ok {
		return "", fmt.Errorf("missing required field %q", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", fmt.Errorf("field %q must be a non-empty string", name)
	}
	return value, nil
}

func requireObject(object map[string]json.RawMessage, name string) error {
	raw, ok := object[name]
	if !ok || !isJSONObject(raw) {
		return fmt.Errorf("field %q must be an object", name)
	}
	return nil
}

func requireImplementation(object map[string]json.RawMessage, name string) error {
	raw, ok := object[name]
	if !ok {
		return fmt.Errorf("missing required field %q", name)
	}
	var implementation map[string]json.RawMessage
	if err := json.Unmarshal(raw, &implementation); err != nil || implementation == nil {
		return fmt.Errorf("field %q must be an object", name)
	}
	if _, err := requiredString(implementation, "name"); err != nil {
		return fmt.Errorf("field %q: %w", name, err)
	}
	if _, err := requiredString(implementation, "version"); err != nil {
		return fmt.Errorf("field %q: %w", name, err)
	}
	return nil
}

func cancellationKey(message *parsedMessage) (string, error) {
	params, err := decodeObject(message.params, message.hasParams)
	if err != nil {
		return "", err
	}
	if err := allowParams(params, "requestId", "reason", "_meta"); err != nil {
		return "", err
	}
	raw, ok := params["requestId"]
	if !ok || !validRequestID(raw) {
		return "", fmt.Errorf("requestId must be a string or number")
	}
	if reason, ok := params["reason"]; ok {
		var value string
		if isJSONNull(reason) || json.Unmarshal(reason, &value) != nil {
			return "", fmt.Errorf("reason must be a string")
		}
	}
	key, _ := requestIDKey(raw)
	return key, nil
}

func allowParams(object map[string]json.RawMessage, names ...string) error {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("unexpected field %q", name)
		}
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func validRequestID(raw json.RawMessage) bool {
	_, ok := requestIDKey(raw)
	return ok
}

func requestIDKey(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return "", false
	}
	switch id := value.(type) {
	case string:
		return "s:" + id, true
	case json.Number:
		return "n:" + id.String(), true
	default:
		return "", false
	}
}

func resultResponse(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: responseID(id), Result: result}
}

func errorResponse(id json.RawMessage, code int, message string, data any) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      responseID(id),
		Error:   &rpcError{Code: code, Message: message, Data: data},
	}
}

func responseID(id json.RawMessage) json.RawMessage {
	if validRequestID(id) {
		return id
	}
	return json.RawMessage("null")
}

func (s *Server) logChildStderr(raw []byte, truncated bool) {
	const max = 4096
	text := strings.TrimSpace(string(raw))
	if len(text) > max {
		text = text[:max] + "..."
	}
	if truncated {
		text += " [capture truncated]"
	}
	s.logf("pairmux child stderr: %s", text)
}

func (s *Server) logf(format string, args ...any) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_, _ = fmt.Fprintf(s.stderr, "pairmux mcp: "+format+"\n", args...)
}
