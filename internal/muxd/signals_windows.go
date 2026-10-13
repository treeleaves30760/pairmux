//go:build windows

package muxd

import (
	"os"
	"os/signal"
)

// handleSignals shuts the server down cleanly on an interrupt. A detached
// server normally receives none; this covers running it in the foreground for
// diagnosis.
func (s *Server) handleSignals() (stop func()) {
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
