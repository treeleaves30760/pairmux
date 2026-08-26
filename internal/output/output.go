// Package output defines the pairmux.v1 response envelope and the two emitters
// (machine JSON and a human-readable block) every command replies through.
//
// The envelope is the single contract between pairmux and the agent driving it,
// so both shapes are deliberately terse and actionable: the JSON is one line an
// agent can parse, and every hint — in Next and in an error's Hint — is a
// concrete runnable command or one short imperative sentence, never prose.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

// Error codes carried in ErrInfo.Code. These are the stable strings agents match on.
const (
	CodeNoTerminal = "E_NO_TERMINAL"
	CodeExists     = "E_EXISTS"
	CodeBusy       = "E_BUSY"
	CodeDead       = "E_DEAD"
	CodeBadArgs    = "E_BAD_ARGS"
	CodeTmux       = "E_TMUX"
	CodeInternal   = "E_INTERNAL"
)

// TruncInfo describes an output that Render truncated and how to fetch the rest.
type TruncInfo struct {
	OmittedLines int    `json:"omitted_lines"`
	OmittedBytes int64  `json:"omitted_bytes,omitempty"`
	GetFull      string `json:"get_full"`
}

// TerminalRow is one row of the `ls` listing. The JSON tags keep the listing in
// the same snake_case shape as the rest of the envelope.
type TerminalRow struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Mode         string `json:"mode"`
	CurrentCmd   string `json:"current_cmd,omitempty"`
	LockHolder   int    `json:"lock_holder,omitempty"`   // pid holding the write lock
	Notes        int    `json:"notes,omitempty"`         // count of unseen human notes
	LastActivity string `json:"last_activity,omitempty"` // RFC3339 or ""
	ChildSocket  string `json:"child_socket,omitempty"`  // endpoint of this terminal's own nested layer, when it has one
}

// ErrInfo is the error detail for a failed command.
type ErrInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Envelope is the pairmux.v1 response. Schema and OK are always present; every
// other field is omitted when empty. Next is always populated for non-final
// states and for errors so an agent always has a concrete next step.
type Envelope struct {
	Schema     string        `json:"schema"`
	OK         bool          `json:"ok"`
	Status     string        `json:"status,omitempty"`
	Terminal   string        `json:"terminal,omitempty"`
	Mode       string        `json:"mode,omitempty"`
	ExitCode   *int          `json:"exit_code,omitempty"`
	DurationMS *int64        `json:"duration_ms,omitempty"`
	Output     string        `json:"output,omitempty"`
	Truncated  *TruncInfo    `json:"truncated,omitempty"`
	Terminals  []TerminalRow `json:"terminals,omitempty"`
	Notes      []string      `json:"notes,omitempty"` // human messages (pairmux note) the agent hasn't seen
	Next       []string      `json:"next,omitempty"`
	Error      *ErrInfo      `json:"error,omitempty"`
}

// Emit writes e to w. In JSON mode it writes one line of compact JSON followed by
// a newline; otherwise it writes a compact human-readable block. Errors are
// written to the same writer — the caller passes stdout and the process exit code
// conveys success or failure.
func Emit(w io.Writer, jsonMode bool, e Envelope) {
	if e.Schema == "" {
		e.Schema = core.SchemaID // every response is self-describing
	}
	if jsonMode {
		b, err := json.Marshal(e)
		if err != nil {
			// Our own envelope cannot realistically fail to marshal; still, keep
			// the output valid JSON rather than emitting nothing.
			fmt.Fprintf(w, `{"schema":%q,"ok":false,"error":{"code":%q,"message":%q}}`+"\n",
				core.SchemaID, CodeInternal, err.Error())
			return
		}
		b = append(b, '\n')
		_, _ = w.Write(b)
		return
	}
	_, _ = io.WriteString(w, human(e))
}

// Fail emits an error envelope (ok=false, status "error") carrying code, msg and
// hint, mirrors the hint into Next so agents that read only Next still get the
// next step, and returns process exit code 1.
func Fail(w io.Writer, jsonMode bool, code, msg, hint string) int {
	e := Envelope{
		Schema: core.SchemaID,
		OK:     false,
		Status: "error",
		Error:  &ErrInfo{Code: code, Message: msg, Hint: hint},
	}
	if hint != "" {
		e.Next = []string{hint}
	}
	Emit(w, jsonMode, e)
	return 1
}

// human renders the envelope as a scannable multi-line block: a status line
// first, then the terminals table (ls) and/or output, then any truncation note,
// then "next:" hints one per line. No ANSI colors in the MVP.
func human(e Envelope) string {
	var b strings.Builder

	// Status line.
	if e.Error != nil {
		b.WriteString("error")
		if e.Error.Code != "" {
			b.WriteString("  ")
			b.WriteString(e.Error.Code)
		}
		if e.Terminal != "" {
			b.WriteString("  terminal=")
			b.WriteString(e.Terminal)
		}
		b.WriteByte('\n')
		if e.Error.Message != "" {
			b.WriteString(e.Error.Message)
			b.WriteByte('\n')
		}
	} else {
		status := e.Status
		if status == "" {
			status = "ok"
		}
		b.WriteString(status)
		if e.Terminal != "" {
			b.WriteString("  terminal=")
			b.WriteString(e.Terminal)
		}
		if e.Mode != "" {
			b.WriteString("  mode=")
			b.WriteString(e.Mode)
		}
		if e.ExitCode != nil {
			fmt.Fprintf(&b, "  exit=%d", *e.ExitCode)
		}
		if e.DurationMS != nil {
			b.WriteString("  duration=")
			b.WriteString((time.Duration(*e.DurationMS) * time.Millisecond).String())
		}
		b.WriteByte('\n')
	}

	// Terminals table (ls).
	if len(e.Terminals) > 0 {
		writeTerminals(&b, e.Terminals)
	}

	// Output block.
	if e.Output != "" {
		b.WriteString(e.Output)
		if !strings.HasSuffix(e.Output, "\n") {
			b.WriteByte('\n')
		}
	}

	// Notes block: messages a human left on the terminal (pairmux note) that
	// the agent has not seen yet.
	if len(e.Notes) > 0 {
		b.WriteString("notes from human:\n")
		for _, n := range e.Notes {
			b.WriteString("  ")
			b.WriteString(n)
			b.WriteByte('\n')
		}
	}

	// Truncation note. Byte-capped views may not know how many shaped lines the
	// skipped prefix contains, so every non-nil descriptor must remain visible.
	if e.Truncated != nil {
		b.WriteString(truncEllipsis)
		switch {
		case e.Truncated.OmittedLines > 0 && e.Truncated.OmittedBytes > 0:
			fmt.Fprintf(&b, " %d more line(s) plus %d earlier byte(s)",
				e.Truncated.OmittedLines, e.Truncated.OmittedBytes)
		case e.Truncated.OmittedLines > 0:
			fmt.Fprintf(&b, " %d more line(s)", e.Truncated.OmittedLines)
		case e.Truncated.OmittedBytes > 0:
			fmt.Fprintf(&b, " %d earlier byte(s) omitted", e.Truncated.OmittedBytes)
		default:
			b.WriteString(" output truncated")
		}
		if e.Truncated.GetFull != "" {
			b.WriteString("; full: ")
			b.WriteString(e.Truncated.GetFull)
		}
		b.WriteByte('\n')
	}

	// Next hints. Fall back to the error's hint if Next was not populated.
	next := e.Next
	if len(next) == 0 && e.Error != nil && e.Error.Hint != "" {
		next = []string{e.Error.Hint}
	}
	if len(next) > 0 {
		b.WriteString("next:\n")
		for _, h := range next {
			b.WriteString("  ")
			b.WriteString(h)
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// truncEllipsis matches the marker shape.Truncate uses.
const truncEllipsis = "…"

// writeTerminals renders the ls listing as a left-aligned table with a header.
func writeTerminals(b *strings.Builder, rows []TerminalRow) {
	nameW, statusW, modeW := len("NAME"), len("STATUS"), len("MODE")
	for _, r := range rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(r.Status) > statusW {
			statusW = len(r.Status)
		}
		if len(r.Mode) > modeW {
			modeW = len(r.Mode)
		}
	}
	fmt.Fprintf(b, "  %-*s  %-*s  %-*s  %s\n", nameW, "NAME", statusW, "STATUS", modeW, "MODE", "CMD")
	for _, r := range rows {
		cmd := r.CurrentCmd
		if cmd == "" {
			cmd = "-"
		}
		if r.LockHolder != 0 {
			cmd += fmt.Sprintf("  [lock:%d]", r.LockHolder)
		}
		if r.Notes != 0 {
			cmd += fmt.Sprintf("  [notes:%d]", r.Notes)
		}
		if r.ChildSocket != "" {
			cmd += "  [layer:" + r.ChildSocket + "]"
		}
		if r.LastActivity != "" {
			cmd = cmd + "  (" + r.LastActivity + ")"
		}
		fmt.Fprintf(b, "  %-*s  %-*s  %-*s  %s\n", nameW, r.Name, statusW, r.Status, modeW, r.Mode, cmd)
	}
}
