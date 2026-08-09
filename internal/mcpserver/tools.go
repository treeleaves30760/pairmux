package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations annotations    `json:"annotations"`
}

type annotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

type tool struct {
	definition toolDefinition
	build      func(arguments) ([]string, error)
}

type arguments map[string]json.RawMessage

func tools() []tool {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integerProperty := func(description string, minimum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "description": description}
	}
	booleanProperty := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}

	return []tool{
		{
			definition: toolDefinition{
				Name:        "pairmux_new",
				Title:       "Create pairmux terminal",
				Description: "Create a tmux-backed pairmux terminal. The optional command starts a program immediately and may have arbitrary side effects; obtain user approval when appropriate.",
				InputSchema: objectSchema(map[string]any{
					"name":    stringProperty("Terminal name; omit to auto-generate one."),
					"cwd":     stringProperty("Initial working directory."),
					"command": stringProperty("Program command for a program terminal instead of an interactive shell."),
				}),
				Annotations: annotations{DestructiveHint: true, OpenWorldHint: true},
			},
			build: buildNew,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_run",
				Title:       "Run command in terminal",
				Description: "Run a shell command in an existing pairmux terminal and block until it completes, needs input, or reaches the timeout. Commands can modify files, invoke networks, or perform other arbitrary side effects; obtain user approval when appropriate.",
				InputSchema: objectSchema(map[string]any{
					"terminal": stringProperty("Existing pairmux terminal name."),
					"command":  stringProperty("Shell command string to run in the terminal."),
					"timeout":  stringProperty("Maximum blocking duration as a Go duration, for example 30s or 5m."),
					"head":     integerProperty("Leading output lines to retain.", 0),
					"tail":     integerProperty("Trailing output lines to retain.", 0),
				}, "terminal", "command"),
				Annotations: annotations{DestructiveHint: true, OpenWorldHint: true},
			},
			build: buildRun,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_peek",
				Title:       "Inspect terminal",
				Description: "Read recent output and derived status from a pairmux terminal without blocking or taking its writer lock.",
				InputSchema: objectSchema(map[string]any{
					"terminal": stringProperty("Existing pairmux terminal name."),
					"screen":   booleanProperty("Capture the live tmux viewport instead of the journal tail."),
					"tail":     integerProperty("Number of journal-tail lines to return.", 1),
				}, "terminal"),
				Annotations: annotations{ReadOnlyHint: true, IdempotentHint: true},
			},
			build: buildPeek,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_wait",
				Title:       "Wait for terminal condition",
				Description: "Block until the configured completion, idle, output-pattern, or human-handoff condition is met, the terminal dies, or the timeout expires. Default and explicit idle waits also return when the program needs input. Any number of agents may wait on one terminal at once, so done is a broadcast every subscriber wakes on. Setting notify may send a desktop notification.",
				InputSchema: objectSchema(map[string]any{
					"terminal": stringProperty("Existing pairmux terminal name."),
					"idle_ms":  integerProperty("Required output-quiescence interval in milliseconds.", 1),
					"pattern":  stringProperty("RE2 regular expression matched against new shaped output."),
					"human":    booleanProperty("Hand off to a human: wait for a note, or for the prompt the handoff was about to be answered. Withholds output, so a secret typed into the pane is never quoted back."),
					"done":     booleanProperty("Wait for the running command to finish, or for the next one when the terminal is idle. Reports its exit code. Shell terminals only."),
					"notify":   booleanProperty("Send a best-effort desktop notification."),
					"timeout":  stringProperty("Overall deadline as a Go duration, for example 300s."),
				}, "terminal"),
				Annotations: annotations{OpenWorldHint: true},
			},
			build: buildWait,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_send",
				Title:       "Send terminal input",
				Description: "Send literal text, named tmux keys, and/or Enter to a live program. Input can confirm destructive actions or expose secrets; never guess or send credentials.",
				InputSchema: objectSchema(map[string]any{
					"terminal": stringProperty("Existing pairmux terminal name."),
					"text":     stringProperty("Literal text to send without shell expansion."),
					"keys": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"description": "Named tmux keys, sent in order.",
					},
					"enter": booleanProperty("Append an Enter key."),
				}, "terminal"),
				Annotations: annotations{DestructiveHint: true, OpenWorldHint: true},
			},
			build: buildSend,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_log",
				Title:       "Read terminal journal",
				Description: "Read the shaped pairmux journal tail, one recorded command, regex matches, or a 1-based inclusive line range.",
				InputSchema: objectSchema(map[string]any{
					"terminal":   stringProperty("Existing pairmux terminal name."),
					"command_id": integerProperty("Recorded command number to read.", 1),
					"grep":       stringProperty("RE2 regular expression used to filter journal lines."),
					"range": map[string]any{
						"type":        "string",
						"pattern":     `^[1-9][0-9]*:(?:[1-9][0-9]*|end)$`,
						"description": "1-based inclusive shaped-line range A:B or A:end.",
					},
				}, "terminal"),
				Annotations: annotations{ReadOnlyHint: true, IdempotentHint: true},
			},
			build: buildLog,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_ls",
				Title:       "List pairmux terminals",
				Description: "List terminals on the configured pairmux socket with status, mode, command, notes, lock holder, and last activity.",
				InputSchema: objectSchema(nil),
				Annotations: annotations{ReadOnlyHint: true, IdempotentHint: true},
			},
			build: buildNoArgs("ls"),
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_kill",
				Title:       "Kill pairmux terminal",
				Description: "Kill one pairmux terminal, or every managed terminal on the configured socket. Journals are retained. Killing active programs can lose in-memory work and requires user approval.",
				InputSchema: objectSchema(map[string]any{
					"terminal": stringProperty("Terminal to kill; mutually exclusive with all."),
					"all":      booleanProperty("Kill every managed terminal on the socket; mutually exclusive with terminal."),
				}),
				Annotations: annotations{DestructiveHint: true},
			},
			build: buildKill,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_note",
				Title:       "Leave human note",
				Description: "Append a human-visible coordination note to a pairmux terminal journal.",
				InputSchema: objectSchema(map[string]any{
					"terminal": stringProperty("Existing pairmux terminal name."),
					"text":     stringProperty("Note text."),
				}, "terminal", "text"),
				Annotations: annotations{},
			},
			build: buildNote,
		},
		{
			definition: toolDefinition{
				Name:        "pairmux_doctor",
				Title:       "Diagnose pairmux",
				Description: "Probe tmux, state-directory, shell integration, and notification support using an isolated live check.",
				InputSchema: objectSchema(nil),
				Annotations: annotations{IdempotentHint: true},
			},
			build: buildNoArgs("doctor"),
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func buildNew(a arguments) ([]string, error) {
	if err := a.allow("name", "cwd", "command"); err != nil {
		return nil, err
	}
	argv := []string{"new"}
	for _, field := range []struct{ property, flag string }{{"name", "--name"}, {"cwd", "--cwd"}, {"command", "--cmd"}} {
		if value, present, err := a.optionalString(field.property); err != nil {
			return nil, err
		} else if present {
			argv = append(argv, field.flag, value)
		}
	}
	return argv, nil
}

func buildRun(a arguments) ([]string, error) {
	if err := a.allow("terminal", "command", "timeout", "head", "tail"); err != nil {
		return nil, err
	}
	terminal, err := a.requiredString("terminal")
	if err != nil {
		return nil, err
	}
	command, err := a.requiredString("command")
	if err != nil {
		return nil, err
	}
	argv := []string{"run", terminal}
	if timeout, present, err := a.optionalString("timeout"); err != nil {
		return nil, err
	} else if present {
		argv = append(argv, "--timeout", timeout)
	}
	for _, field := range []struct{ property, flag string }{{"head", "--head"}, {"tail", "--tail"}} {
		if value, present, err := a.optionalInt(field.property, 0); err != nil {
			return nil, err
		} else if present {
			argv = append(argv, field.flag, strconv.Itoa(value))
		}
	}
	return append(argv, "--", command), nil
}

func buildPeek(a arguments) ([]string, error) {
	if err := a.allow("terminal", "screen", "tail"); err != nil {
		return nil, err
	}
	terminal, err := a.requiredString("terminal")
	if err != nil {
		return nil, err
	}
	screen, screenPresent, err := a.optionalBool("screen")
	if err != nil {
		return nil, err
	}
	tail, tailPresent, err := a.optionalInt("tail", 1)
	if err != nil {
		return nil, err
	}
	if screen && tailPresent {
		return nil, fmt.Errorf("screen and tail are mutually exclusive")
	}
	argv := []string{"peek", terminal}
	if screenPresent && screen {
		argv = append(argv, "--screen")
	}
	if tailPresent {
		argv = append(argv, "--tail", strconv.Itoa(tail))
	}
	return argv, nil
}

func buildWait(a arguments) ([]string, error) {
	if err := a.allow("terminal", "idle_ms", "pattern", "human", "done", "notify", "timeout"); err != nil {
		return nil, err
	}
	terminal, err := a.requiredString("terminal")
	if err != nil {
		return nil, err
	}
	argv := []string{"wait", terminal}
	if idle, present, err := a.optionalInt("idle_ms", 1); err != nil {
		return nil, err
	} else if present {
		argv = append(argv, "--idle", strconv.Itoa(idle))
	}
	if pattern, present, err := a.optionalString("pattern"); err != nil {
		return nil, err
	} else if present {
		argv = append(argv, "--pattern", pattern)
	}
	for _, field := range []struct{ property, flag string }{{"human", "--human"}, {"done", "--done"}, {"notify", "--notify"}} {
		if value, _, err := a.optionalBool(field.property); err != nil {
			return nil, err
		} else if value {
			argv = append(argv, field.flag)
		}
	}
	if timeout, present, err := a.optionalString("timeout"); err != nil {
		return nil, err
	} else if present {
		argv = append(argv, "--timeout", timeout)
	}
	return argv, nil
}

func buildSend(a arguments) ([]string, error) {
	if err := a.allow("terminal", "text", "keys", "enter"); err != nil {
		return nil, err
	}
	terminal, err := a.requiredString("terminal")
	if err != nil {
		return nil, err
	}
	argv := []string{"send", terminal}
	actions := 0
	if text, present, err := a.optionalString("text"); err != nil {
		return nil, err
	} else if present {
		argv = append(argv, "--text", text)
		actions++
	}
	if keys, present, err := a.optionalStrings("keys"); err != nil {
		return nil, err
	} else if present {
		if len(keys) == 0 {
			return nil, fmt.Errorf("keys must contain at least one key")
		}
		for _, key := range keys {
			argv = append(argv, "--key", key)
		}
		actions++
	}
	if enter, _, err := a.optionalBool("enter"); err != nil {
		return nil, err
	} else if enter {
		argv = append(argv, "--enter")
		actions++
	}
	if actions == 0 {
		return nil, fmt.Errorf("send needs text, keys, or enter=true")
	}
	return argv, nil
}

func buildLog(a arguments) ([]string, error) {
	if err := a.allow("terminal", "command_id", "grep", "range"); err != nil {
		return nil, err
	}
	terminal, err := a.requiredString("terminal")
	if err != nil {
		return nil, err
	}
	argv := []string{"log", terminal}
	selectors := 0
	if id, present, err := a.optionalInt("command_id", 1); err != nil {
		return nil, err
	} else if present {
		argv = append(argv, "--cmd", strconv.Itoa(id))
		selectors++
	}
	for _, field := range []struct{ property, flag string }{{"grep", "--grep"}, {"range", "--range"}} {
		if value, present, err := a.optionalString(field.property); err != nil {
			return nil, err
		} else if present {
			argv = append(argv, field.flag, value)
			selectors++
		}
	}
	if selectors > 1 {
		return nil, fmt.Errorf("command_id, grep, and range are mutually exclusive")
	}
	return argv, nil
}

func buildKill(a arguments) ([]string, error) {
	if err := a.allow("terminal", "all"); err != nil {
		return nil, err
	}
	terminal, terminalPresent, err := a.optionalString("terminal")
	if err != nil {
		return nil, err
	}
	all, _, err := a.optionalBool("all")
	if err != nil {
		return nil, err
	}
	if all == terminalPresent {
		return nil, fmt.Errorf("provide exactly one of terminal or all=true")
	}
	if all {
		return []string{"kill", "--all"}, nil
	}
	return []string{"kill", terminal}, nil
}

func buildNote(a arguments) ([]string, error) {
	if err := a.allow("terminal", "text"); err != nil {
		return nil, err
	}
	terminal, err := a.requiredString("terminal")
	if err != nil {
		return nil, err
	}
	text, err := a.requiredString("text")
	if err != nil {
		return nil, err
	}
	return []string{"note", terminal, "--", text}, nil
}

func buildNoArgs(command string) func(arguments) ([]string, error) {
	return func(a arguments) ([]string, error) {
		if err := a.allow(); err != nil {
			return nil, err
		}
		return []string{command}, nil
	}
}

func (a arguments) allow(names ...string) error {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	var unknown []string
	for name := range a {
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("unknown argument %q", unknown[0])
}

func (a arguments) requiredString(name string) (string, error) {
	value, present, err := a.optionalString(name)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("missing required argument %q", name)
	}
	return value, nil
}

func (a arguments) optionalString(name string) (string, bool, error) {
	raw, present := a[name]
	if !present {
		return "", false, nil
	}
	var value string
	if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
		return "", true, fmt.Errorf("argument %q must be a string", name)
	}
	return value, true, nil
}

func (a arguments) optionalInt(name string, minimum int) (int, bool, error) {
	raw, present := a[name]
	if !present {
		return 0, false, nil
	}
	var value int
	if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
		return 0, true, fmt.Errorf("argument %q must be an integer", name)
	}
	if value < minimum {
		return 0, true, fmt.Errorf("argument %q must be at least %d", name, minimum)
	}
	return value, true, nil
}

func (a arguments) optionalBool(name string) (bool, bool, error) {
	raw, present := a[name]
	if !present {
		return false, false, nil
	}
	var value bool
	if isJSONNull(raw) || json.Unmarshal(raw, &value) != nil {
		return false, true, fmt.Errorf("argument %q must be a boolean", name)
	}
	return value, true, nil
}

func (a arguments) optionalStrings(name string) ([]string, bool, error) {
	raw, present := a[name]
	if !present {
		return nil, false, nil
	}
	if isJSONNull(raw) {
		return nil, true, fmt.Errorf("argument %q must be an array of strings", name)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, true, fmt.Errorf("argument %q must be an array of strings", name)
	}
	value := make([]string, 0, len(items))
	for index, item := range items {
		var element string
		if isJSONNull(item) || json.Unmarshal(item, &element) != nil {
			return nil, true, fmt.Errorf("argument %q element %d must be a string", name, index)
		}
		value = append(value, element)
	}
	return value, true, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
