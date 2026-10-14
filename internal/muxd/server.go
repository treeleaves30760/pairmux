// Package muxd is pairmux's built-in terminal multiplexer: a small background
// server that owns pseudo terminals (package pty) on behalf of pairmux
// commands, the way a tmux server owns panes. It exists so pairmux works with
// no tmux installed and on native Windows, where tmux does not run.
//
// One server serves one endpoint name (the same name tmux -L would take). It
// is started on demand by the first command that needs it, detaches from that
// command's session, and exits on its own once it has had no terminals and no
// clients for a while. Commands talk to it over a Unix domain socket in a
// private per-user directory (package rundir) — available on Windows 10 1803+
// as well — one request per connection, except for a human's attach, which
// turns its connection into a duplex stream of terminal bytes.
//
// What the server keeps per terminal is exactly what pairmux needed from tmux:
// the program's raw output appended to the terminal's journal from the very
// first byte (so no start gate is needed), a virtual screen (package vt) for
// peek --screen and for redrawing a newly attached human, an input queue for
// agent and human keystrokes, and the @pairmux_name option.
package muxd

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/flock"
	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/pty"
	"github.com/treeleaves30760/pairmux/internal/rundir"
	"github.com/treeleaves30760/pairmux/internal/version"
	"github.com/treeleaves30760/pairmux/internal/vt"
)

const (
	// DefaultCols and DefaultRows match the size pairmux gives its tmux
	// session: wide enough that ordinary command output does not wrap.
	DefaultCols = 220
	DefaultRows = 50

	// historyLimit bounds each screen's scrollback. The journal keeps
	// everything; this only serves capture with history.
	historyLimit = 10000

	// idleExit is how long a server with no terminals and no connections
	// lingers before exiting, so a create that follows an EnsureSession, or a
	// quick kill-then-new, finds it still there.
	idleExit = 30 * time.Second

	// inputQueueBytes bounds keystrokes queued for a program that is not
	// reading its input; past it, sends fail instead of growing without limit.
	inputQueueBytes = 8 << 20

	// clientBacklog bounds output queued for one attached human. A terminal
	// that cannot keep up has its backlog replaced by one redraw of the
	// current screen, the way tmux treats a slow client.
	clientBacklog = 4 << 20

	// killWait bounds how long kill waits for a terminal's output to drain
	// into its journal and its files to close.
	killWait = 5 * time.Second

	// lockWait bounds how long a starting server waits for a previous one,
	// still draining its terminals, to release the endpoint.
	lockWait = 15 * time.Second

	// errShuttingDown is the reply of a server that has begun to exit; clients
	// treat it like no server at all.
	errShuttingDown = "server is shutting down"

	// BuiltinPaneEnv is set in every built-in pane to "<socket>/<pane id>", so
	// an attach issued from inside a pane can refuse to attach to itself.
	BuiltinPaneEnv = "PAIRMUX_BUILTIN_PANE"
)

// Server is one running endpoint.
type Server struct {
	socket string
	ln     net.Listener

	mu         sync.Mutex
	panes      map[string]*pane
	nextID     int
	conns      int
	lastActive time.Time
	closing    bool

	done    chan struct{} // closed when shutdown begins
	stopped chan struct{} // closed when shutdown has finished
}

// pane is one terminal. Its screen, journal and clients are guarded by mu;
// output is processed in arrival order under mu so the journal, the screen
// and every attached client see the same byte stream.
type pane struct {
	srv   *Server
	id    string
	name  string
	order int
	argv0 string
	pty   pty.Pty

	mu      sync.Mutex
	scr     *vt.Screen
	journal *os.File
	jerr    bool // a journal write already failed (logged once)
	dead    bool // finish has run: no new clients, no resizes
	opts    map[string]string
	clients map[*client]struct{}
	cols    int
	rows    int

	inMu    sync.Mutex
	inCond  *sync.Cond
	inQueue [][]byte
	inBytes int

	gone chan struct{} // closed once output is drained and files are closed
}

// Serve runs the server for socket until it is told to stop or goes idle. It
// returns nil when another server already owns the endpoint.
func Serve(socket string) error {
	if socket == "" {
		socket = core.DefaultSocket
	}
	if !mux.ValidSocketName(socket) {
		return fmt.Errorf("muxd: invalid socket name %q", socket)
	}
	dir, err := rundir.Ensure()
	if err != nil {
		return err
	}
	base, err := rundir.FileBase(socket)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, base+".sock")

	// One server per endpoint: the lock is held for the server's lifetime and
	// vanishes with it, so a crashed server never blocks its successor and two
	// racing starters cannot both listen.
	lockPath := filepath.Join(dir, base+".lock")
	lockFile, err := acquireEndpointLock(lockPath, socket)
	if err != nil || lockFile == nil {
		return err // nil: another server owns the endpoint
	}
	defer lockFile.Close()
	truncateLog()
	_ = os.Remove(path) // a stale socket from a server that died
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("muxd: listen %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)

	s := &Server{
		socket:     socket,
		ln:         ln,
		panes:      map[string]*pane{},
		lastActive: time.Now(),
		done:       make(chan struct{}),
		stopped:    make(chan struct{}),
	}
	stopSignals := s.handleSignals()
	defer stopSignals()
	go s.idleLoop()
	log.Printf("pairmux built-in multiplexer %s serving %q on %s (pid %d)", version.Version, socket, path, os.Getpid())
	s.acceptLoop()
	// However the loop ended, every terminal is killed and drained before the
	// process may exit; shutdown is idempotent and waits for a concurrent one.
	s.shutdown()
	_ = os.Remove(path)
	// A clean exit leaves nothing behind, so endpoints that come and go —
	// nested layers, doctor probes, tests — do not accumulate files. The lock
	// file is removed while still held; acquireEndpointLock re-checks that the
	// path still names the file it locked, so this cannot admit two servers.
	releaseLog()
	_ = os.Remove(filepath.Join(dir, base+".log"))
	removeHeldLock(lockFile, lockPath)
	return nil
}

// acquireEndpointLock opens and locks lockPath, waiting for a server that is
// still shutting down (draining its terminals) to hand the endpoint over —
// so a create that races a kill-server gets a fresh server instead of none —
// and yielding at once to a healthy one (nil file, nil error).
//
// A lock file can be unlinked by its exiting owner (see Serve). Holding a lock
// on an unlinked file proves nothing, so after locking, the path must still
// name the same file; otherwise the attempt starts over on the new one.
func acquireEndpointLock(lockPath, socket string) (*os.File, error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("muxd: open lock: %w", err)
		}
		err = flock.TryLock(f, true)
		if err == nil {
			held, serr := f.Stat()
			now, perr := os.Stat(lockPath)
			if serr == nil && perr == nil && os.SameFile(held, now) {
				return f, nil
			}
			f.Close() // locked a file its owner already unlinked
			continue
		}
		f.Close()
		if !errors.Is(err, flock.ErrWouldBlock) {
			return nil, fmt.Errorf("muxd: lock: %w", err)
		}
		if Running(socket) || !time.Now().Before(deadline) {
			return nil, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("accept: %v", err)
			return
		}
		s.mu.Lock()
		s.conns++
		s.lastActive = time.Now()
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				s.conns--
				s.lastActive = time.Now()
				s.mu.Unlock()
			}()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) idleLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if s.shutdownIfIdle() {
			return
		}
	}
}

// shutdownIfIdle shuts the server down when it has had no terminals and no
// connections for idleExit. The decision and the closing flag are taken under
// one lock, so a connection that arrives meanwhile either counts against it or
// is refused as shutting down — never accepted into a server that then exits.
func (s *Server) shutdownIfIdle() bool {
	s.mu.Lock()
	if s.closing || len(s.panes) != 0 || s.conns != 0 || time.Since(s.lastActive) <= idleExit {
		s.mu.Unlock()
		return false
	}
	s.closing = true
	s.mu.Unlock()
	log.Printf("idle for %s with no terminals; exiting", idleExit)
	s.finishShutdown(nil)
	return true
}

// shutdown stops accepting, kills every terminal and waits for each to drain.
// Concurrent callers all return once the first has finished.
func (s *Server) shutdown() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		<-s.stopped
		return
	}
	s.closing = true
	panes := make([]*pane, 0, len(s.panes))
	for _, p := range s.panes {
		panes = append(panes, p)
	}
	s.panes = map[string]*pane{}
	s.mu.Unlock()
	s.finishShutdown(panes)
}

func (s *Server) finishShutdown(panes []*pane) {
	close(s.done)
	_ = s.ln.Close()
	for _, p := range panes {
		_ = p.pty.Kill()
	}
	for _, p := range panes {
		select {
		case <-p.gone:
		case <-time.After(killWait):
		}
	}
	close(s.stopped)
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var req request
	if err := readMsg(conn, &req); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if req.Op == "attach" {
		s.attach(conn, req)
		return
	}
	resp := s.handle(req)
	resp.Proto = protoVersion
	_ = writeMsg(conn, resp)
	if req.Op == "kill-server" && resp.OK {
		go s.shutdown()
	}
}

func fail(format string, args ...any) response {
	return response{Error: fmt.Sprintf(format, args...)}
}

func (s *Server) lookup(id string) (*pane, response, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.panes[id]; ok {
		return p, response{}, true
	}
	return nil, fail("can't find pane: %s", id), false
}

func (s *Server) handle(req request) response {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		// Including ping: a client must not mistake a draining server for a
		// usable one. It spawns a successor, which waits for this one's lock.
		return fail(errShuttingDown)
	}
	switch req.Op {
	case "ping":
		return response{OK: true, Version: version.Version, Pid: os.Getpid()}
	case "kill-server":
		return response{OK: true}
	case "new-window":
		return s.newWindow(req)
	case "list":
		return s.list()
	case "select-window":
		for _, pi := range s.list().Panes {
			if pi.Window == req.Name {
				return response{OK: true}
			}
		}
		return fail("can't find window: %s", req.Name)
	}

	p, r, ok := s.lookup(req.Pane)
	if !ok {
		return r
	}
	switch req.Op {
	case "pipe":
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.journal != nil {
			return response{OK: true} // -o: already piped
		}
		f, err := openJournal(req.Journal)
		if err != nil {
			return fail("%v", err)
		}
		p.journal = f
		return response{OK: true}
	case "send-text":
		if err := p.enqueue([]byte(req.Text)); err != nil {
			return fail("%v", err)
		}
		return response{OK: true}
	case "send-keys":
		p.mu.Lock()
		app := p.scr.Modes().AppCursor
		p.mu.Unlock()
		var b []byte
		for _, k := range req.Keys {
			kb, err := keyBytes(k, app)
			if err != nil {
				return fail("%v", err)
			}
			b = append(b, kb...)
		}
		if err := p.enqueue(b); err != nil {
			return fail("%v", err)
		}
		return response{OK: true}
	case "capture":
		p.mu.Lock()
		text := p.scr.Capture(req.History)
		p.mu.Unlock()
		return response{OK: true, Text: text}
	case "set-option":
		p.mu.Lock()
		p.opts[req.Opt] = req.Val
		p.mu.Unlock()
		return response{OK: true}
	case "kill-window":
		s.kill(p)
		return response{OK: true}
	case "pane-tty":
		return response{OK: true, Text: p.pty.TTY()}
	case "audience":
		p.mu.Lock()
		n := len(p.clients)
		p.mu.Unlock()
		return response{OK: true, Attached: n, Active: n > 0}
	}
	return fail("unknown operation %q", req.Op)
}

func openJournal(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	return f, nil
}

func (s *Server) newWindow(req request) response {
	if len(req.Argv) == 0 {
		return fail("new-window: no command")
	}
	cols, rows := req.Cols, req.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = DefaultCols, DefaultRows
	}
	journal, err := openJournal(req.Journal)
	if err != nil {
		return fail("%v", err)
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		if journal != nil {
			journal.Close()
		}
		return fail(errShuttingDown)
	}
	id := "%" + strconv.Itoa(s.nextID)
	order := s.nextID
	s.nextID++
	s.mu.Unlock()

	env := append(append([]string(nil), req.Env...), BuiltinPaneEnv+"="+s.socket+"/"+id)
	t, err := pty.Start(pty.Options{Argv: req.Argv, Dir: req.Dir, Env: env, Cols: cols, Rows: rows})
	if err != nil {
		if journal != nil {
			journal.Close()
		}
		return fail("%v", err)
	}
	p := &pane{
		srv:     s,
		id:      id,
		name:    req.Name,
		order:   order,
		argv0:   strings.TrimSuffix(filepath.Base(req.Argv[0]), ".exe"),
		pty:     t,
		scr:     vt.New(cols, rows, historyLimit),
		journal: journal,
		opts:    map[string]string{},
		clients: map[*client]struct{}{},
		cols:    cols,
		rows:    rows,
		gone:    make(chan struct{}),
	}
	p.inCond = sync.NewCond(&p.inMu)

	// Register before the reader can run, so a program that exits at once is
	// removed by its own finish rather than listed forever. If shutdown began
	// while the program was starting it has already collected the panes it
	// kills, so this one is killed here instead (its reader still cleans up).
	s.mu.Lock()
	closing := s.closing
	if !closing {
		s.panes[id] = p
		s.lastActive = time.Now()
	}
	s.mu.Unlock()
	go p.writeLoop()
	go p.readLoop()
	if closing {
		_ = t.Kill()
		return fail(errShuttingDown)
	}
	return response{OK: true, Pane: id}
}

func (s *Server) list() response {
	s.mu.Lock()
	panes := make([]*pane, 0, len(s.panes))
	for _, p := range s.panes {
		panes = append(panes, p)
	}
	s.mu.Unlock()
	sort.Slice(panes, func(i, j int) bool { return panes[i].order < panes[j].order })
	infos := make([]mux.PaneInfo, 0, len(panes))
	for _, p := range panes {
		p.mu.Lock()
		name := p.opts[core.PaneOptName]
		p.mu.Unlock()
		infos = append(infos, mux.PaneInfo{PaneID: p.id, Window: p.name, Name: name, CurrentCmd: p.argv0})
	}
	return response{OK: true, Panes: infos}
}

// kill removes a pane at once — like tmux's kill-window, a later list no longer
// shows it — then waits for its program to die and its output to finish
// draining into the journal, so a create that reuses the name never races the
// old pane's files.
func (s *Server) kill(p *pane) {
	s.mu.Lock()
	delete(s.panes, p.id)
	s.lastActive = time.Now()
	s.mu.Unlock()
	_ = p.pty.Kill()
	select {
	case <-p.gone:
	case <-time.After(killWait):
	}
}

func (p *pane) readLoop() {
	buf := make([]byte, 64<<10)
	for {
		n, err := p.pty.Read(buf)
		if n > 0 {
			p.output(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("pane %s: read: %v", p.id, err)
			}
			break
		}
	}
	// Read ends at EOF only after the program exited; on an unexpected read
	// error, make sure it has.
	select {
	case <-p.pty.Exited():
	default:
		_ = p.pty.Kill()
	}
	code, _ := p.pty.Wait()
	p.finish(code)
}

// output records one chunk: journal first (it is the record agents read), then
// the screen, then every attached human. Replies to terminal queries go back
// to the program whether or not anyone is attached — programs like fish or
// PSReadLine wait for them.
func (p *pane) output(b []byte) {
	p.mu.Lock()
	if p.journal != nil {
		if _, err := p.journal.Write(b); err != nil && !p.jerr {
			p.jerr = true
			log.Printf("pane %s: journal write: %v (further errors not logged)", p.id, err)
		}
	}
	p.scr.SetForwarding(len(p.clients) > 0)
	fwd, reply := p.scr.Feed(b)
	if len(fwd) > 0 {
		frame := encodeFrame(frameOutput, fwd) // shared: clients only write it
		for c := range p.clients {
			c.push(frame)
		}
	}
	p.mu.Unlock()
	if len(reply) > 0 {
		_ = p.enqueue(reply)
	}
}

// finish is the pane's death: it leaves the server, its humans are told and
// disconnected, and its files are closed.
func (p *pane) finish(code int) {
	s := p.srv
	s.mu.Lock()
	delete(s.panes, p.id)
	s.lastActive = time.Now()
	s.mu.Unlock()

	p.mu.Lock()
	p.dead = true
	msg := fmt.Sprintf("terminal %s exited with status %d", p.displayName(), code)
	for c := range p.clients {
		c.finish(encodeFrame(frameExit, []byte(msg)))
	}
	p.clients = map[*client]struct{}{}
	if p.journal != nil {
		_ = p.journal.Close()
		p.journal = nil
	}
	p.mu.Unlock()

	_ = p.pty.Close()
	// gone is closed under inMu so the input writer, which checks it under the
	// same lock before waiting, cannot miss the wakeup.
	p.inMu.Lock()
	p.inQueue = nil
	close(p.gone)
	p.inMu.Unlock()
	p.inCond.Broadcast()
}

func (p *pane) displayName() string {
	if n := p.opts[core.PaneOptName]; n != "" {
		return n
	}
	return p.name
}

func (p *pane) isGone() bool {
	select {
	case <-p.gone:
		return true
	default:
		return false
	}
}

// enqueue queues input for the program. A dedicated writer delivers it, so a
// program that is not reading its input stalls only its own queue — never the
// output reader that would otherwise deadlock against it, and never the
// caller beyond inputQueueBytes.
func (p *pane) enqueue(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	p.inMu.Lock()
	defer p.inMu.Unlock()
	if p.isGone() {
		return errors.New("pane is gone")
	}
	if p.inBytes+len(b) > inputQueueBytes {
		return errors.New("input queue full: the program is not reading its input")
	}
	p.inQueue = append(p.inQueue, append([]byte(nil), b...))
	p.inBytes += len(b)
	p.inCond.Signal()
	return nil
}

func (p *pane) writeLoop() {
	for {
		p.inMu.Lock()
		for len(p.inQueue) == 0 && !p.isGone() {
			p.inCond.Wait()
		}
		if p.isGone() {
			p.inMu.Unlock()
			return
		}
		chunk := p.inQueue[0]
		p.inQueue = p.inQueue[1:]
		p.inBytes -= len(chunk)
		p.inMu.Unlock()
		if _, err := p.pty.Write(chunk); err != nil {
			log.Printf("pane %s: write: %v", p.id, err)
		}
	}
}

// resizeLocked changes the pane's size; p.mu must be held. Every attached
// client is redrawn, since a size change invalidates what each one shows.
func (p *pane) resizeLocked(cols, rows int) {
	if p.dead || cols <= 0 || rows <= 0 || (cols == p.cols && rows == p.rows) {
		return
	}
	if err := p.pty.Resize(cols, rows); err != nil {
		log.Printf("pane %s: resize: %v", p.id, err)
		return
	}
	p.scr.Resize(cols, rows)
	p.cols, p.rows = cols, rows
	for c := range p.clients {
		c.requestRedraw()
	}
}
