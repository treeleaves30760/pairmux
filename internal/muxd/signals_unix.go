//go:build unix

package muxd

import (
	"os"
	"os/signal"
	"syscall"
)

// handleSignals shuts the server down cleanly on SIGTERM/SIGINT. SIGHUP is
// caught and discarded: the server has detached from the session that started
// it, and the terminal that session belonged to closing must not take the
// agents' terminals with it.
//
// It is caught rather than ignored on purpose. An ignored disposition survives
// exec, so every program in every pane would start with SIGHUP ignored —
// killing a terminal (which hangs it up) would then leave its programs running,
// and nohup semantics would silently apply to everything. A caught signal is
// reset to its default in each child, and Notify also undoes an ignore this
// process may itself have inherited (say, from a parent run under nohup).
func (s *Server) handleSignals() (stop func()) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for {
			select {
			case <-hup:
			case <-ch:
				s.shutdown()
				return
			case <-s.done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		signal.Stop(hup)
	}
}
