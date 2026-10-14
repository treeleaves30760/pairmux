//go:build darwin || linux

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// drainQuiet is how long output may stay silent after the program exits
// before Read reports EOF. The slave side is kept open by this process for the
// program's whole life (see unixPty), so the kernel never reports EOF by
// itself; once the program has been reaped, whatever it wrote is already in
// the pty buffer and arrives within microseconds, and a background job it left
// behind gets this long to finish talking before the terminal goes.
const drainQuiet = 150 * time.Millisecond

// drainMax bounds the drain after the program exits, however busy its output
// stays. darwin and Linux hang the terminal up when its session leader exits,
// which normally ends a leftover job's output at once; this is the backstop so
// output that keeps arriving can never keep the terminal — and its journal —
// alive forever. tmux likewise removes a pane when its program exits.
const drainMax = 2 * time.Second

// killGrace is how long a SIGHUP'd terminal has to exit before SIGKILL.
const killGrace = 2 * time.Second

// unixPty keeps the slave end open in this process as well as in the child.
// Closing it here would make the master report EOF/EIO as soon as the last
// child-side descriptor closes, and on darwin that can discard output still
// queued in the pty — the final lines a program prints before exiting are
// exactly the ones an agent most needs. Holding it means EOF is decided here,
// after the program is reaped and its output drained.
type unixPty struct {
	master *os.File
	slave  *os.File
	tty    string
	cmd    *exec.Cmd

	exited   chan struct{}
	exitedAt time.Time // set before exited is closed
	code     int
	waitErr  error
	killOnce sync.Once
	closeMu  sync.Mutex
	closed   bool
}

func start(o Options) (Pty, error) {
	master, tty, err := openMaster()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (Pty, error) {
		master.Close()
		return nil, err
	}
	slave, err := os.OpenFile(tty, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fail(fmt.Errorf("pty: open %s: %w", tty, err))
	}
	configureSlave(slave)
	// darwin rejects TIOCSWINSZ on a master whose slave has never been opened.
	if err := setSize(master, o.Cols, o.Rows); err != nil {
		slave.Close()
		return fail(err)
	}

	path, err := lookPath(o.Argv[0], o.Env)
	if err != nil {
		slave.Close()
		return fail(err)
	}
	cmd := &exec.Cmd{
		Path:   path,
		Args:   o.Argv,
		Env:    o.Env,
		Dir:    o.Dir,
		Stdin:  slave,
		Stdout: slave,
		Stderr: slave,
		// A new session whose controlling terminal is the pty: the program
		// owns job control there and receives ^C, SIGWINCH and SIGHUP from it.
		SysProcAttr: &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0},
	}
	if err := cmd.Start(); err != nil {
		slave.Close()
		return fail(fmt.Errorf("pty: start %s: %w", o.Argv[0], err))
	}
	p := &unixPty{master: master, slave: slave, tty: tty, cmd: cmd, exited: make(chan struct{})}
	go p.reap()
	return p, nil
}

func (p *unixPty) reap() {
	err := p.cmd.Wait()
	p.code, p.waitErr = exitCode(p.cmd.ProcessState, err)
	p.exitedAt = time.Now()
	close(p.exited)
	// Wake a Read blocked on a now-silent terminal so it can report EOF.
	_ = p.master.SetReadDeadline(time.Now().Add(drainQuiet))
}

func exitCode(ps *os.ProcessState, err error) (int, error) {
	if ps == nil {
		return -1, err
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), nil
	}
	return ps.ExitCode(), nil
}

func (p *unixPty) isExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (p *unixPty) Read(b []byte) (int, error) {
	for {
		n, err := p.master.Read(b)
		if n > 0 {
			if p.isExited() {
				deadline := time.Now().Add(drainQuiet)
				if limit := p.exitedAt.Add(drainMax); deadline.After(limit) {
					deadline = limit
				}
				_ = p.master.SetReadDeadline(deadline)
			}
			return n, nil
		}
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if p.isExited() {
				return 0, io.EOF
			}
			// A deadline left over from a race with reap; clear and retry.
			_ = p.master.SetReadDeadline(time.Time{})
			continue
		}
		if errors.Is(err, syscall.EIO) || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
			return 0, io.EOF
		}
		return 0, err
	}
}

func (p *unixPty) Write(b []byte) (int, error) { return p.master.Write(b) }

func (p *unixPty) Resize(cols, rows int) error { return setSize(p.master, cols, rows) }

func (p *unixPty) Pid() int { return p.cmd.Process.Pid }

func (p *unixPty) TTY() string { return p.tty }

func (p *unixPty) Wait() (int, error) {
	<-p.exited
	return p.code, p.waitErr
}

func (p *unixPty) Exited() <-chan struct{} { return p.exited }

// Kill hangs up the terminal's session the way closing a terminal window
// does: SIGHUP (with SIGCONT, so a stopped job can act on it) to the program's
// process group, then SIGKILL to whatever is left of the group after
// killGrace — including members that outlived a leader which did exit.
func (p *unixPty) Kill() error {
	p.killOnce.Do(func() {
		pid := p.cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGHUP)
		_ = syscall.Kill(-pid, syscall.SIGCONT)
		go func() {
			time.Sleep(killGrace)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}()
	})
	return nil
}

func (p *unixPty) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	serr := p.slave.Close()
	merr := p.master.Close()
	if merr != nil {
		return merr
	}
	return serr
}

// control runs fn with the master's descriptor without calling File.Fd, which
// would switch the descriptor to blocking mode and silently disable the read
// deadlines Read depends on.
func control(f *os.File, fn func(fd uintptr) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(fd) }); err != nil {
		return err
	}
	return ferr
}

// ioctl takes its argument as an unsafe.Pointer so the conversion to uintptr
// happens inside the syscall.Syscall call expression, which is what keeps the
// pointed-to value alive and in place for the duration of the call.
func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func setSize(f *os.File, cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 0xffff || rows > 0xffff {
		return fmt.Errorf("pty: invalid size %dx%d", cols, rows)
	}
	ws := winsize{Row: uint16(rows), Col: uint16(cols)}
	return control(f, func(fd uintptr) error {
		if err := ioctl(fd, syscall.TIOCSWINSZ, unsafe.Pointer(&ws)); err != nil {
			return fmt.Errorf("pty: set size: %w", err)
		}
		return nil
	})
}
