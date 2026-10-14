//go:build windows

package muxd

import (
	"log"
	"os"
	"os/signal"
)

var procSetConsoleCtrlHandler = modkernel32.NewProc("SetConsoleCtrlHandler")

// handleSignals shuts the server down cleanly on an interrupt. A detached
// server normally receives none; this covers running it in the foreground for
// diagnosis.
//
// It first clears the process's "ignore Ctrl+C" flag, before any terminal is
// spawned. The flag is inherited by every child, and this process usually
// starts with it set: CREATE_NEW_PROCESS_GROUP, which spawnServer passes and
// the MCP server passes for the CLI children that may spawn us, implicitly
// calls SetConsoleCtrlHandler(NULL, TRUE) in the new process. Left set, the
// Ctrl+C key event ConPTY makes of a 0x03 written to a pane never becomes a
// CTRL_C_EVENT for cmd.exe or the programs in it (Git Bash's MSYS runtime
// delivers SIGINT itself, which is why not every shell showed it). Clearing it is the
// Windows counterpart of catching rather than ignoring SIGHUP on Unix. It
// cannot expose the server itself: it runs DETACHED_PROCESS, with no console
// to deliver it a Ctrl+C.
func (s *Server) handleSignals() (stop func()) {
	if r, _, err := procSetConsoleCtrlHandler.Call(0, 0); r == 0 {
		log.Printf("muxd: enable Ctrl+C for terminals: %v", err)
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		select {
		case <-ch:
			s.shutdown()
		case <-s.done:
		}
	}()
	return func() { signal.Stop(ch) }
}
