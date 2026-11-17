//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// startPPID is the parent pid read as early as this process can: during
// package initialisation, before main. Reading it this early narrows the
// window in which a parent that exits right after starting pairmux goes
// unnoticed (see watchParent).
var startPPID = os.Getppid()

// getppid is os.Getppid, replaceable in tests.
var getppid = os.Getppid

// watchParent reports when the process that started this one is gone. On Unix
// an orphan is reparented — to init, or to the nearest subreaper (systemd
// --user, a container's init, tini) — so the test is "the parent pid is no
// longer the one we started with", never "the parent is pid 1".
//
// Two consequences, both documented for users:
//   - Started by pid 1 (a dashboard that is PID 1 in a container, a systemd
//     unit, a launchd agent), there is no parent death to observe — pid 1
//     outlives us — so the flag has no effect and the command keeps running.
//   - A parent that exits before startPPID is read (sh -c 'pairmux events
//     --exit-with-parent &' with a shell that exits at once) leaves us
//     already adopted by init or a subreaper, which looks exactly like a
//     legitimate parent, so that exit is not detected.
func watchParent() (gone func() bool, stop func(), err error) {
	start := startPPID
	return func() bool { return getppid() != start }, func() {}, nil
}

// tolerateClosedStdout makes a write to a stdout whose reader is gone return
// EPIPE instead of killing the process with SIGPIPE, so a command whose parent
// has exited can still say goodbye and exit 0.
func tolerateClosedStdout() { signal.Ignore(syscall.SIGPIPE) }
