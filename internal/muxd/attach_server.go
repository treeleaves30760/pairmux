package muxd

import (
	"log"
	"net"
	"sync"
)

// client is one human attached to a pane. Output is queued per client and
// written by the client's own goroutine, so a slow terminal never stalls the
// pane or the other humans watching it.
type client struct {
	conn net.Conn

	mu      sync.Mutex
	cond    *sync.Cond
	pending [][]byte
	bytes   int
	redraw  bool // backlog discarded (or size changed): send a fresh screen
	ending  bool // write what is pending, then close
	closed  bool
}

func newClient(conn net.Conn) *client {
	c := &client{conn: conn}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// push queues one frame. Called with the pane's mu held.
func (c *client) push(frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ending || c.redraw {
		return // a pending redraw will show this output anyway
	}
	if c.bytes+len(frame) > clientBacklog {
		c.pending, c.bytes, c.redraw = nil, 0, true
	} else {
		c.pending = append(c.pending, frame)
		c.bytes += len(frame)
	}
	c.cond.Signal()
}

// requestRedraw replaces the backlog with a fresh screen. Called with the
// pane's mu held.
func (c *client) requestRedraw() {
	c.mu.Lock()
	if !c.closed && !c.ending {
		c.pending, c.bytes, c.redraw = nil, 0, true
		c.cond.Signal()
	}
	c.mu.Unlock()
}

// finish queues a last frame and lets the writer close the connection once it
// is delivered. Called with the pane's mu held.
func (c *client) finish(frame []byte) {
	c.mu.Lock()
	if !c.closed && !c.ending {
		c.pending = append(c.pending, frame)
		c.ending = true
		c.cond.Signal()
	}
	c.mu.Unlock()
}

func (c *client) close() {
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
	_ = c.conn.Close()
}

// writeLoop delivers queued frames. A redraw is rendered under the pane's lock
// (then the client's, the same order output uses), so the screen it shows and
// the output queued after it are exactly contiguous.
func (c *client) writeLoop(p *pane) {
	defer c.conn.Close()
	for {
		c.mu.Lock()
		for len(c.pending) == 0 && !c.redraw && !c.closed && !c.ending {
			c.cond.Wait()
		}
		if c.closed {
			c.mu.Unlock()
			return
		}
		redraw := c.redraw
		c.mu.Unlock()

		if redraw {
			p.mu.Lock()
			c.mu.Lock()
			if c.redraw {
				r := encodeFrame(frameOutput, p.scr.Render())
				c.pending, c.bytes, c.redraw = [][]byte{r}, len(r), false
			}
			c.mu.Unlock()
			p.mu.Unlock()
		}

		c.mu.Lock()
		batch := c.pending
		c.pending, c.bytes = nil, 0
		ending := c.ending
		c.mu.Unlock()
		for _, f := range batch {
			if _, err := c.conn.Write(f); err != nil {
				c.close()
				return
			}
		}
		if ending {
			c.mu.Lock()
			done := len(c.pending) == 0
			c.mu.Unlock()
			if done {
				return
			}
		}
	}
}

// attach turns conn into a live view of one pane. The pane takes the human's
// terminal size while they are attached (the most recent attach or resize
// wins) and returns to the default size once nobody is watching, so agents
// keep their wide screen.
func (s *Server) attach(conn net.Conn, req request) {
	p, resp, ok := s.lookup(req.Pane)
	if !ok {
		resp.Proto = protoVersion
		_ = writeMsg(conn, resp)
		return
	}
	if err := writeMsg(conn, response{OK: true, Proto: protoVersion, Pane: p.id}); err != nil {
		return
	}
	c := newClient(conn)

	p.mu.Lock()
	if p.isGone() {
		p.mu.Unlock()
		return
	}
	p.clients[c] = struct{}{}
	p.resizeLocked(req.Cols, req.Rows)
	c.requestRedraw() // the first thing a new client sees is the whole screen
	p.mu.Unlock()

	go c.writeLoop(p)

loop:
	for {
		typ, payload, err := readFrame(conn)
		if err != nil {
			break
		}
		switch typ {
		case frameInput:
			if err := p.enqueue(payload); err != nil {
				log.Printf("pane %s: attach input: %v", p.id, err)
			}
		case frameResize:
			if cols, rows, ok := parseResize(payload); ok {
				p.mu.Lock()
				p.resizeLocked(cols, rows)
				p.mu.Unlock()
			}
		case frameDetach:
			break loop
		}
	}
	p.mu.Lock()
	delete(p.clients, c)
	if len(p.clients) == 0 && !p.isGone() {
		p.resizeLocked(DefaultCols, DefaultRows)
	}
	p.mu.Unlock()
	c.close()
}
