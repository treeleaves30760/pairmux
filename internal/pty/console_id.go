package pty

import (
	"strconv"
	"strings"
)

// consolePrefix marks a Windows console identifier. No Unix device path starts
// with it, so a terminal's recorded tty is unambiguous on every platform.
const consolePrefix = "conpty:"

// FormatConsoleID names a pseudo console by a process attached to it — the
// program the terminal was started with — and that process's creation time (a
// FILETIME: 100ns units since 1601).
//
// Windows has no terminal device to name, and a console can only be inspected
// by attaching to it through one of its processes, so the process is the name.
// The creation time is what makes the name safe to keep: pids are reused once
// a process is gone, and an inspector that checks the time while holding a
// handle to the process (which pins the pid) can never end up reading an
// unrelated program's console.
func FormatConsoleID(pid uint32, created uint64) string {
	return consolePrefix + strconv.FormatUint(uint64(pid), 10) + ":" + strconv.FormatUint(created, 10)
}

// ParseConsoleID is FormatConsoleID's inverse. ok is false for anything else,
// including a Unix device path and the empty string.
func ParseConsoleID(id string) (pid uint32, created uint64, ok bool) {
	rest, found := strings.CutPrefix(id, consolePrefix)
	if !found {
		return 0, 0, false
	}
	ps, cs, found := strings.Cut(rest, ":")
	if !found {
		return 0, 0, false
	}
	p, err := strconv.ParseUint(ps, 10, 32)
	if err != nil || p == 0 {
		return 0, 0, false
	}
	c, err := strconv.ParseUint(cs, 10, 64)
	if err != nil || c == 0 {
		return 0, 0, false
	}
	return uint32(p), c, true
}
