//go:build windows

package muxd

import (
	"os"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

var (
	modkernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = modkernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = modkernel32.NewProc("GetConsoleScreenBufferInfo")
	procGetConsoleCP               = modkernel32.NewProc("GetConsoleCP")
	procSetConsoleCP               = modkernel32.NewProc("SetConsoleCP")
	procGetConsoleOutputCP         = modkernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutputCP         = modkernel32.NewProc("SetConsoleOutputCP")
)

const (
	enableProcessedOutput           = 0x0001
	enableVirtualTerminalProcessing = 0x0004
	disableNewlineAutoReturn        = 0x0008
	enableInsertMode                = 0x0020
	enableQuickEditMode             = 0x0040
	enableExtendedFlags             = 0x0080
	enableVirtualTerminalInput      = 0x0200

	utf8CodePage = 65001
)

type coord struct{ X, Y int16 }

type smallRect struct{ Left, Top, Right, Bottom int16 }

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

// winTerminal is a Windows console switched to VT mode both ways: keys arrive
// as the same escape sequences a Unix terminal sends (which ConPTY accepts as
// input), and the pane's VT output is rendered by the console itself.
type winTerminal struct {
	in, out           syscall.Handle
	inMode, outMode   uint32
	inCP, outCP       uintptr
	pendingHighSurrog uint16
	carry             []byte
}

func setConsoleMode(h syscall.Handle, mode uint32) error {
	if r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode)); r == 0 {
		return err
	}
	return nil
}

func openTerminal() (terminal, error) {
	in, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	out, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	w := &winTerminal{in: in, out: out}
	if err := syscall.GetConsoleMode(in, &w.inMode); err != nil {
		return nil, err
	}
	if err := syscall.GetConsoleMode(out, &w.outMode); err != nil {
		return nil, err
	}
	w.inCP, _, _ = procGetConsoleCP.Call()
	w.outCP, _, _ = procGetConsoleOutputCP.Call()

	// Raw VT input: no line editing, echo or ^C processing (^C belongs to the
	// program in the pane). Quick-edit/insert flags are kept as the user had
	// them.
	inMode := uint32(enableVirtualTerminalInput) | w.inMode&(enableExtendedFlags|enableQuickEditMode|enableInsertMode)
	if err := setConsoleMode(in, inMode); err != nil {
		return nil, err
	}
	outMode := w.outMode | enableProcessedOutput | enableVirtualTerminalProcessing | disableNewlineAutoReturn
	if err := setConsoleMode(out, outMode); err != nil {
		_ = setConsoleMode(in, w.inMode)
		return nil, err
	}
	procSetConsoleCP.Call(utf8CodePage)
	procSetConsoleOutputCP.Call(utf8CodePage)
	return w, nil
}

// read returns UTF-8 keyboard input. It reads UTF-16 with ReadConsoleW rather
// than bytes, which older consoles mangle for non-ASCII input under code page
// 65001, and carries surrogate halves and partial runes between calls.
func (w *winTerminal) read(p []byte) (int, error) {
	for len(w.carry) == 0 {
		var buf [512]uint16
		var n uint32
		if err := syscall.ReadConsole(w.in, &buf[0], uint32(len(buf)), &n, nil); err != nil {
			return 0, err
		}
		units := buf[:n]
		if w.pendingHighSurrog != 0 {
			units = append([]uint16{w.pendingHighSurrog}, units...)
			w.pendingHighSurrog = 0
		}
		if l := len(units); l > 0 && units[l-1] >= 0xd800 && units[l-1] < 0xdc00 {
			w.pendingHighSurrog = units[l-1]
			units = units[:l-1]
		}
		for _, r := range utf16.Decode(units) {
			w.carry = utf8.AppendRune(w.carry, r)
		}
	}
	n := copy(p, w.carry)
	w.carry = w.carry[n:]
	return n, nil
}

func (w *winTerminal) write(p []byte) {
	for len(p) > 0 {
		n, err := os.Stdout.Write(p)
		if err != nil {
			return
		}
		p = p[n:]
	}
}

func (w *winTerminal) size() (int, int) {
	var info consoleScreenBufferInfo
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(w.out), uintptr(unsafe.Pointer(&info))); r != 0 {
		cols := int(info.Window.Right-info.Window.Left) + 1
		rows := int(info.Window.Bottom-info.Window.Top) + 1
		if cols > 0 && rows > 0 {
			return cols, rows
		}
	}
	return 80, 24
}

// watchResize polls: in VT input mode the console delivers no resize events
// through ReadConsoleW.
func (w *winTerminal) watchResize(fn func(int, int)) func() {
	quit := make(chan struct{})
	go func() {
		c, r := w.size()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				if nc, nr := w.size(); nc != c || nr != r {
					c, r = nc, nr
					fn(c, r)
				}
			}
		}
	}()
	return func() { close(quit) }
}

func (w *winTerminal) restore() {
	_ = setConsoleMode(w.in, w.inMode)
	_ = setConsoleMode(w.out, w.outMode)
	if w.inCP != 0 {
		procSetConsoleCP.Call(w.inCP)
	}
	if w.outCP != 0 {
		procSetConsoleOutputCP.Call(w.outCP)
	}
}

// EnableVT turns on VT processing for this process's console output, so
// escape-sequence drawing (pairmux watch) renders in a legacy console window as
// it does in Windows Terminal. It returns a func restoring the previous mode.
func EnableVT() (restore func()) {
	out, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return func() {}
	}
	var mode uint32
	if syscall.GetConsoleMode(out, &mode) != nil {
		return func() {}
	}
	if setConsoleMode(out, mode|enableProcessedOutput|enableVirtualTerminalProcessing) != nil {
		return func() {}
	}
	return func() { _ = setConsoleMode(out, mode) }
}
