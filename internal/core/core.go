// Package core holds pairmux's shared primitive types and constants.
// This file is architect-owned: implementing agents import it, never edit it.
package core

import "time"

const (
	SessionName   = "pairmux"
	DefaultSocket = "pairmux"
	PaneOptName   = "@pairmux_name"
	SchemaID      = "pairmux.v1"
)

// SentinelSuffix is appended to run commands in sentinel mode. The shell substitutes the
// exit code; the resulting OSC sequence reaches the journal via pipe-pane but renders as
// nothing on screen (verified in P0 spike).
const SentinelSuffix = `; printf '\033]7779;p;%d\007' $?`

type Mode string

const (
	ModeHooks    Mode = "hooks"
	ModeSentinel Mode = "sentinel"
)

type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusDead    Status = "dead"
	StatusUnknown Status = "unknown"
	// StatusAwaitingInput refines StatusRunning: the pending command has gone
	// quiet and its last line looks like an interactive prompt (P2).
	StatusAwaitingInput Status = "awaiting-input"
)

type EventType string

const (
	EvCreated  EventType = "created"
	EvCmdStart EventType = "cmd_start"
	EvCmdEnd   EventType = "cmd_end"
	EvNote     EventType = "note"
)

// Event is one line of index.jsonl.
type Event struct {
	TS       time.Time `json:"ts"`
	Type     EventType `json:"type"`
	CmdID    int       `json:"cmd_id,omitempty"`
	Offset   int64     `json:"offset,omitempty"` // byte offset into raw.log
	ExitCode *int      `json:"exit_code,omitempty"`
	Text     string    `json:"text,omitempty"` // command line, or note body
}

// Meta is meta.json for one terminal.
type Meta struct {
	Name   string `json:"name"`
	PaneID string `json:"pane_id"`
	// Tty is the pane's terminal device, recorded once at creation because it
	// never changes for the life of a pane. Prompt classification reads its line
	// discipline; keeping it here means that costs no tmux call per poll, which
	// matters when several agents watch one terminal. Empty for terminals
	// created before this was recorded — classification then uses wording alone.
	Tty       string    `json:"tty,omitempty"`
	Shell     string    `json:"shell"`
	Mode      Mode      `json:"mode"`
	Socket    string    `json:"socket"`
	CreatedAt time.Time `json:"created_at"`
}
