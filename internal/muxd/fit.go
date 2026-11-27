package muxd

import (
	"time"

	"github.com/treeleaves30760/pairmux/internal/mux"
)

// A fit gives a pane nobody is attached to the size of a human's TUI preview
// box, so a full-screen program redraws to what the human is shown instead of
// being cut out of a 220-column screen.
//
// Attached humans always own the size: a fit is refused while anyone is
// attached, and an attach ends it (attach_server.go). Ending a fit restores
// the size the pane had before it, which with nobody attached is the agents'
// default.
//
// A fit is a lease. The TUI renews it while it keeps showing the pane; if the
// TUI dies — killed, crashed, its terminal closed — the lease lapses and the
// pane gets its size back on its own, so agents are never left with a
// preview-sized screen by a process that is no longer there.

// maxFitLease bounds a requested lease.
const maxFitLease = 5 * time.Minute

func (p *pane) screenInfo() response {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return fail("terminal has exited")
	}
	m := p.scr.Modes()
	return response{OK: true, Screen: &mux.ScreenInfo{
		Cols: p.cols, Rows: p.rows,
		AltScreen:    m.AltScreen,
		CursorHidden: !m.CursorVisible,
		Mouse:        m.MouseTracking != 0,
		Attached:     len(p.clients),
		Fitted:       p.fitted,
	}}
}

func (p *pane) fit(cols, rows, leaseMS int) response {
	if cols <= 0 || rows <= 0 {
		return fail("fit: bad size %dx%d", cols, rows)
	}
	lease := time.Duration(leaseMS) * time.Millisecond
	if lease <= 0 {
		lease = mux.FitLease
	}
	lease = min(lease, maxFitLease)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead {
		return fail("terminal has exited")
	}
	if len(p.clients) > 0 {
		return fail(errWatched)
	}
	if !p.fitted {
		p.fitted, p.baseCols, p.baseRows = true, p.cols, p.rows
	}
	p.resizeLocked(cols, rows)
	p.fitGen++
	gen := p.fitGen
	if p.fitTimer != nil {
		p.fitTimer.Stop()
	}
	p.fitTimer = time.AfterFunc(lease, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.fitGen == gen {
			p.unfitLocked()
		}
	})
	return response{OK: true}
}

// endFitLocked forgets a fit without touching the size; p.mu must be held.
func (p *pane) endFitLocked() {
	if p.fitTimer != nil {
		p.fitTimer.Stop()
		p.fitTimer = nil
	}
	p.fitGen++
	p.fitted = false
}

// unfitLocked ends a fit and, with nobody attached, restores the size the
// pane had before it; p.mu must be held.
func (p *pane) unfitLocked() {
	if !p.fitted {
		return
	}
	p.endFitLocked()
	if len(p.clients) == 0 {
		p.resizeLocked(p.baseCols, p.baseRows)
	}
}
