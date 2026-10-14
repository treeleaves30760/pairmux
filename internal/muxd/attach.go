package muxd

import (
	"errors"
	"fmt"
	"sync"
)

// PrefixKey is the attach prefix, Ctrl-b, the same key tmux uses: Ctrl-b d
// detaches and Ctrl-b Ctrl-b types a literal Ctrl-b. Keeping tmux's binding
// means the handoff instructions pairmux already gives stay correct.
const PrefixKey = 0x02

// enterSeq puts the human's terminal on its alternate screen for the attach,
// so their own scrollback is untouched; the server's first frame redraws the
// pane onto it.
const enterSeq = "\x1b[?1049h\x1b[H\x1b[2J"

// leaveSeq undoes every mode a program in the pane may have switched on in
// the human's terminal — cursor and keypad modes, bracketed paste, mouse and
// focus reporting, scroll region, attributes, charset — then returns to the
// normal screen.
const leaveSeq = "\x1b[0m\x1b[r\x1b[?25h\x1b[?1l\x1b>\x1b[?2004l" +
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?7h\x1b[4l\x1b[0 q\x1b(B\x1b[?1049l"

// AttachResult says how an attach ended.
type AttachResult struct {
	Detached bool   // the human pressed the detach key
	Message  string // why the attach ended otherwise (e.g. the program exited)
}

// terminal is the human's terminal in raw mode.
type terminal interface {
	read(p []byte) (int, error)
	write(p []byte)
	size() (cols, rows int)
	watchResize(func(cols, rows int)) (stop func())
	restore()
}

// Attach connects this process's terminal to paneID until the human detaches
// or the pane's program exits. The pane takes the terminal's size while
// attached.
func Attach(socket, paneID string) (AttachResult, error) {
	conn, err := New(socket).dial()
	if err != nil {
		return AttachResult{}, err
	}
	defer conn.Close()

	t, err := openTerminal()
	if err != nil {
		return AttachResult{}, fmt.Errorf("attach: %w", err)
	}
	// Every write to the human's terminal goes through out, and restore closes
	// it: frames still arriving from the server after a detach must not land
	// on the human's normal screen after its modes were reset.
	var outMu sync.Mutex
	outClosed, entered := false, false
	out := func(b []byte) {
		outMu.Lock()
		if !outClosed {
			t.write(b)
		}
		outMu.Unlock()
	}
	restore := func() {
		outMu.Lock()
		defer outMu.Unlock()
		if outClosed {
			return
		}
		outClosed = true
		if entered {
			t.write([]byte(leaveSeq))
		}
		t.restore()
	}
	defer restore()

	cols, rows := t.size()
	if err := writeMsg(conn, request{Op: "attach", Proto: protoVersion, Pane: paneID, Cols: cols, Rows: rows}); err != nil {
		return AttachResult{}, fmt.Errorf("attach: %w", err)
	}
	var resp response
	if err := readMsg(conn, &resp); err != nil {
		return AttachResult{}, fmt.Errorf("attach: no reply: %w", err)
	}
	if !resp.OK {
		return AttachResult{}, errors.New("attach: " + resp.Error)
	}
	outMu.Lock()
	entered = true
	t.write([]byte(enterSeq))
	outMu.Unlock()

	var wmu sync.Mutex
	send := func(typ byte, payload []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := conn.Write(encodeFrame(typ, payload))
		return err
	}
	done := make(chan AttachResult, 2)

	go func() { // server → terminal
		for {
			typ, payload, err := readFrame(conn)
			if err != nil {
				done <- AttachResult{Message: "lost the connection to the built-in multiplexer"}
				return
			}
			switch typ {
			case frameOutput:
				out(payload)
			case frameExit:
				done <- AttachResult{Message: string(payload)}
				return
			}
		}
	}()

	go func() { // terminal → server
		buf := make([]byte, 4096)
		prefix := false
		for {
			n, err := t.read(buf)
			if err != nil {
				done <- AttachResult{Message: "terminal input closed"}
				return
			}
			out := make([]byte, 0, n)
			for _, b := range buf[:n] {
				if prefix {
					prefix = false
					switch b {
					case 'd', 'D':
						if len(out) > 0 {
							_ = send(frameInput, out)
						}
						_ = send(frameDetach, nil)
						done <- AttachResult{Detached: true}
						return
					case PrefixKey:
						out = append(out, PrefixKey)
					}
					continue // like tmux, an unbound key after the prefix is dropped
				}
				if b == PrefixKey {
					prefix = true
					continue
				}
				out = append(out, b)
			}
			if len(out) > 0 {
				if err := send(frameInput, out); err != nil {
					done <- AttachResult{Message: "lost the connection to the built-in multiplexer"}
					return
				}
			}
		}
	}()

	stop := t.watchResize(func(c, r int) { _ = send(frameResize, resizePayload(c, r)) })
	res := <-done
	stop()
	restore()
	_ = conn.Close() // ends the server-to-terminal goroutine
	return res, nil
}
