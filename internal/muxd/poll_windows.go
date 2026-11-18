//go:build windows

package muxd

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// This file is the Windows side of RawPoll/PollRead, the polled raw mode
// pairmux tui reads the console in.
//
// Unix gets polling from VTIME. A Windows console has no such mode, and a
// ReadConsoleW blocks until a key arrives: a reader parked mid-read for an
// attach would swallow the first key typed into the attached pane. So input is
// read as console records instead: WaitForSingleObject on the input handle
// with a timeout says whether any records are queued, and ReadConsoleInputW
// then returns only those (it never waits when records are queued). In VT
// input mode each key arrives as key-down records carrying the characters of
// the escape sequence a Unix terminal would send, so the UI's key parser is
// the same everywhere. Mouse reports arrive the same way when the console
// translates them (Windows Terminal); a console that delivers raw mouse
// records instead has them re-encoded here as the SGR reports the UI parses.

var (
	procReadConsoleInputW             = modkernel32.NewProc("ReadConsoleInputW")
	procGetNumberOfConsoleInputEvents = modkernel32.NewProc("GetNumberOfConsoleInputEvents")
)

const (
	enableMouseInput         = 0x0010
	pollTimeoutMs            = 100
	maxRecordsPerRead        = 128
	keyEventType             = 0x0001
	mouseEventType           = 0x0002
	vkMenu                   = 0x12 // Alt: Alt+numpad text arrives on its key-up
	fromLeft1stButtonPressed = 0x0001
	mouseMoved               = 0x0001
	mouseWheeled             = 0x0004
)

// inputRecord is the Win32 INPUT_RECORD: an event type, then (aligned to 4)
// a 16-byte union whose layout depends on the type.
type inputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

// keyRecord builds a KEY_EVENT inputRecord (used by tests).
func keyRecord(down bool, repeat, vk, char uint16) inputRecord {
	r := inputRecord{EventType: keyEventType}
	if down {
		binary.LittleEndian.PutUint32(r.Event[0:], 1)
	}
	binary.LittleEndian.PutUint16(r.Event[4:], repeat)
	binary.LittleEndian.PutUint16(r.Event[6:], vk)
	binary.LittleEndian.PutUint16(r.Event[10:], char)
	return r
}

// mouseRecord builds a MOUSE_EVENT inputRecord (used by tests).
func mouseRecord(x, y int16, buttons, flags uint32) inputRecord {
	r := inputRecord{EventType: mouseEventType}
	binary.LittleEndian.PutUint16(r.Event[0:], uint16(x))
	binary.LittleEndian.PutUint16(r.Event[2:], uint16(y))
	binary.LittleEndian.PutUint32(r.Event[4:], buttons)
	binary.LittleEndian.PutUint32(r.Event[12:], flags)
	return r
}

// recordDecoder turns console input records into the bytes a Unix terminal
// would have sent. It keeps a UTF-16 high surrogate and the mouse button state
// between reads.
type recordDecoder struct {
	highSurrogate uint16
	buttons       uint32
}

// decode appends what recs carry to dst. left and top are the console window's
// origin in screen-buffer coordinates, which mouse records are reported in.
func (d *recordDecoder) decode(dst []byte, recs []inputRecord, left, top int) []byte {
	for i := range recs {
		ev := recs[i].Event[:]
		switch recs[i].EventType {
		case keyEventType:
			down := binary.LittleEndian.Uint32(ev[0:]) != 0
			repeat := int(binary.LittleEndian.Uint16(ev[4:]))
			vk := binary.LittleEndian.Uint16(ev[6:])
			ch := binary.LittleEndian.Uint16(ev[10:])
			if ch == 0 || !down && vk != vkMenu {
				continue
			}
			if repeat < 1 || utf16.IsSurrogate(rune(ch)) {
				repeat = 1
			}
			for ; repeat > 0; repeat-- {
				dst = d.unit(dst, ch)
			}
		case mouseEventType:
			x := int(int16(binary.LittleEndian.Uint16(ev[0:]))) - left + 1
			y := int(int16(binary.LittleEndian.Uint16(ev[2:]))) - top + 1
			buttons := binary.LittleEndian.Uint32(ev[4:])
			flags := binary.LittleEndian.Uint32(ev[12:])
			if x < 1 || y < 1 {
				continue
			}
			switch {
			case flags&mouseWheeled != 0:
				code := 65 // toward the user: scroll down
				if int16(buttons>>16) > 0 {
					code = 64
				}
				dst = sgrMouse(dst, code, x, y, 'M')
			case flags&mouseMoved == 0:
				was, is := d.buttons&fromLeft1stButtonPressed != 0, buttons&fromLeft1stButtonPressed != 0
				if is && !was {
					dst = sgrMouse(dst, 0, x, y, 'M')
				} else if was && !is {
					dst = sgrMouse(dst, 0, x, y, 'm')
				}
				d.buttons = buttons & 0xffff
			}
		}
	}
	return dst
}

// unit appends one UTF-16 code unit, pairing surrogates across records and
// reads. An unpaired half is dropped.
func (d *recordDecoder) unit(dst []byte, u uint16) []byte {
	switch {
	case u >= 0xd800 && u < 0xdc00:
		d.highSurrogate = u
		return dst
	case u >= 0xdc00 && u < 0xe000:
		hi := d.highSurrogate
		d.highSurrogate = 0
		if hi == 0 {
			return dst
		}
		return utf8.AppendRune(dst, utf16.DecodeRune(rune(hi), rune(u)))
	}
	d.highSurrogate = 0
	return utf8.AppendRune(dst, rune(u))
}

func sgrMouse(dst []byte, code, x, y int, final byte) []byte {
	dst = append(dst, "\x1b[<"...)
	dst = strconv.AppendInt(dst, int64(code), 10)
	dst = append(dst, ';')
	dst = strconv.AppendInt(dst, int64(x), 10)
	dst = append(dst, ';')
	dst = strconv.AppendInt(dst, int64(y), 10)
	return append(dst, final)
}

// poll is the state PollRead keeps between calls. Only the UI's reader
// goroutine reads; RawPoll resets it.
var poll struct {
	mu    sync.Mutex
	dec   recordDecoder
	carry []byte
}

// rawPollModes is the console input and output modes RawPoll asks for, given
// the modes the console had. Input: VT key sequences and mouse records, no
// line editing, echo or Ctrl-C processing (Ctrl-C is a key to the UI); the
// user's quick-edit and insert settings are kept, so a console with quick edit
// on keeps mouse selection and simply sends the UI no clicks. Output: VT
// processing, with "\n" still returning the carriage.
func rawPollModes(inMode, outMode uint32) (uint32, uint32) {
	in := uint32(enableVirtualTerminalInput|enableMouseInput) | inMode&(enableExtendedFlags|enableQuickEditMode|enableInsertMode)
	out := (outMode | enableProcessedOutput | enableVirtualTerminalProcessing) &^ disableNewlineAutoReturn
	return in, out
}

// RawPoll switches the console to VT input and output for an interactive UI
// (pairmux tui) and returns the function that restores the modes and code
// pages it found. Input is then read with PollRead, which never leaves a read
// pending, so the UI can hand the console whole to a child process (an
// attach) between reads. A console without VT input answers ErrNoTUI.
func RawPoll() (restore func(), err error) {
	in, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	out, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	return rawPollHandles(in, out)
}

func rawPollHandles(in, out syscall.Handle) (func(), error) {
	var inMode, outMode uint32
	if err := syscall.GetConsoleMode(in, &inMode); err != nil {
		return nil, fmt.Errorf("%w (stdin is not a console: %v)", ErrNoTUI, err)
	}
	if err := syscall.GetConsoleMode(out, &outMode); err != nil {
		return nil, fmt.Errorf("%w (stdout is not a console: %v)", ErrNoTUI, err)
	}
	inCP, _, _ := procGetConsoleCP.Call()
	outCP, _, _ := procGetConsoleOutputCP.Call()
	newIn, newOut := rawPollModes(inMode, outMode)
	if err := setConsoleMode(in, newIn); err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNoTUI, err)
	}
	if err := setConsoleMode(out, newOut); err != nil {
		_ = setConsoleMode(in, inMode)
		return nil, fmt.Errorf("%w (%v)", ErrNoTUI, err)
	}
	procSetConsoleCP.Call(utf8CodePage)
	procSetConsoleOutputCP.Call(utf8CodePage)

	poll.mu.Lock()
	poll.dec, poll.carry = recordDecoder{}, nil
	poll.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = setConsoleMode(in, inMode)
			_ = setConsoleMode(out, outMode)
			if inCP != 0 {
				procSetConsoleCP.Call(inCP)
			}
			if outCP != 0 {
				procSetConsoleOutputCP.Call(outCP)
			}
		})
	}, nil
}

// PollRead reads console input after RawPoll as UTF-8 bytes. It waits at most
// about 100ms and returns 0, nil when nothing arrived, and it only reads
// records already queued, so no read is left pending when the caller stops
// calling it.
func PollRead(p []byte) (int, error) {
	poll.mu.Lock()
	defer poll.mu.Unlock()
	if len(poll.carry) == 0 {
		in, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
		if err != nil {
			return 0, err
		}
		ev, err := syscall.WaitForSingleObject(in, pollTimeoutMs)
		if ev == syscall.WAIT_TIMEOUT {
			return 0, nil
		}
		if ev != syscall.WAIT_OBJECT_0 {
			return 0, err
		}
		var queued uint32
		if r, _, err := procGetNumberOfConsoleInputEvents.Call(uintptr(in), uintptr(unsafe.Pointer(&queued))); r == 0 {
			return 0, err
		}
		if queued == 0 {
			return 0, nil
		}
		recs := make([]inputRecord, min(queued, maxRecordsPerRead))
		var got uint32
		if r, _, err := procReadConsoleInputW.Call(uintptr(in), uintptr(unsafe.Pointer(&recs[0])),
			uintptr(len(recs)), uintptr(unsafe.Pointer(&got))); r == 0 {
			return 0, err
		}
		left, top := windowOrigin()
		poll.carry = poll.dec.decode(poll.carry, recs[:got], left, top)
	}
	n := copy(p, poll.carry)
	poll.carry = poll.carry[n:]
	return n, nil
}

// windowOrigin is the console window's top-left cell in screen-buffer
// coordinates, which mouse records are reported in.
func windowOrigin() (left, top int) {
	out, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0
	}
	var info consoleScreenBufferInfo
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(out), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, 0
	}
	return int(info.Window.Left), int(info.Window.Top)
}
