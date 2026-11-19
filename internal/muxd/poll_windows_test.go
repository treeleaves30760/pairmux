//go:build windows

package muxd

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestInputRecordLayout(t *testing.T) {
	// INPUT_RECORD is 20 bytes on every Windows architecture: WORD type,
	// padding to 4, then the 16-byte event union.
	if got := unsafe.Sizeof(inputRecord{}); got != 20 {
		t.Fatalf("sizeof(inputRecord) = %d, want 20", got)
	}
	if got := unsafe.Offsetof(inputRecord{}.Event); got != 4 {
		t.Fatalf("offsetof(Event) = %d, want 4", got)
	}
}

func TestDecodeKeys(t *testing.T) {
	var d recordDecoder
	recs := []inputRecord{
		keyRecord(true, 1, 0, 'q'),
		keyRecord(false, 1, 0, 'q'), // key-up: dropped
		keyRecord(true, 1, 0x10, 0), // Shift down, no character: dropped
		// An arrow in VT input mode: the sequence's characters as key-downs.
		keyRecord(true, 1, 0, 0x1b), keyRecord(true, 1, 0, '['), keyRecord(true, 1, 0, 'A'),
		keyRecord(true, 3, 0, 'j'),  // auto-repeat
		keyRecord(true, 1, 0, 0x03), // Ctrl-C is a key, not a signal
		keyRecord(true, 1, 0, 0x4e2d),
		keyRecord(false, 1, vkMenu, 0x00e9), // Alt+numpad text arrives on Alt's key-up
	}
	got := string(d.decode(nil, recs, 0, 0))
	if want := "q\x1b[Ajjj\x03中é"; got != want {
		t.Fatalf("decode = %q, want %q", got, want)
	}
}

func TestDecodeSurrogatesAcrossReads(t *testing.T) {
	var d recordDecoder
	// U+1F600 as a surrogate pair split over two reads.
	first := d.decode(nil, []inputRecord{keyRecord(true, 1, 0, 0xd83d)}, 0, 0)
	if len(first) != 0 {
		t.Fatalf("high surrogate alone decoded to %q", first)
	}
	if got := string(d.decode(nil, []inputRecord{keyRecord(true, 1, 0, 0xde00)}, 0, 0)); got != "😀" {
		t.Fatalf("pair = %q", got)
	}
	// An unpaired low surrogate is dropped, not turned into garbage.
	if got := string(d.decode(nil, []inputRecord{keyRecord(true, 1, 0, 0xde00), keyRecord(true, 1, 0, 'x')}, 0, 0)); got != "x" {
		t.Fatalf("unpaired = %q", got)
	}
}

func TestDecodeMouseRecordsAsSGR(t *testing.T) {
	var d recordDecoder
	const wheelUp, wheelDown = uint32(120) << 16, uint32(0xff88) << 16 // +120, -120
	recs := []inputRecord{
		mouseRecord(10, 105, fromLeft1stButtonPressed, 0), // press at buffer (10,105)
		mouseRecord(11, 105, fromLeft1stButtonPressed, mouseMoved),
		mouseRecord(11, 105, 0, 0), // release
		mouseRecord(3, 102, wheelUp, mouseWheeled),
		mouseRecord(3, 102, wheelDown, mouseWheeled),
		mouseRecord(0, 50, fromLeft1stButtonPressed, 0), // above the window: dropped
		{EventType: 0x0010},                             // focus event: dropped
	}
	// The window starts at buffer row 100, so row 105 is screen row 6.
	got := string(d.decode(nil, recs, 0, 100))
	want := "\x1b[<0;11;6M\x1b[<0;12;6m\x1b[<64;4;3M\x1b[<65;4;3M"
	if got != want {
		t.Fatalf("decode = %q, want %q", got, want)
	}
}

func TestRawPollModes(t *testing.T) {
	// A typical cmd.exe console: processed, line and echo input with quick edit.
	in, out := rawPollModes(0x0001|0x0002|0x0004|enableExtendedFlags|enableQuickEditMode|enableInsertMode,
		enableProcessedOutput|0x0002|disableNewlineAutoReturn)
	if in&(0x0001|0x0002|0x0004) != 0 {
		t.Errorf("input mode %#x keeps processed/line/echo input", in)
	}
	if in&enableVirtualTerminalInput == 0 || in&enableMouseInput == 0 {
		t.Errorf("input mode %#x lacks VT or mouse input", in)
	}
	if want := uint32(enableExtendedFlags | enableQuickEditMode | enableInsertMode); in&want != want {
		t.Errorf("input mode %#x dropped the user's quick-edit/insert settings", in)
	}
	if out&enableVirtualTerminalProcessing == 0 || out&enableProcessedOutput == 0 {
		t.Errorf("output mode %#x lacks VT processing", out)
	}
	if out&disableNewlineAutoReturn != 0 {
		t.Errorf("output mode %#x: \\n would no longer return the carriage", out)
	}
	if out&0x0002 == 0 {
		t.Errorf("output mode %#x dropped wrap-at-EOL", out)
	}
}

func TestRawPollRefusesANonConsole(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	restore, err := rawPollHandles(syscall.Handle(r.Fd()), syscall.Handle(w.Fd()))
	if !errors.Is(err, ErrNoTUI) {
		if restore != nil {
			restore()
		}
		t.Fatalf("rawPollHandles(pipe) err = %v, want ErrNoTUI", err)
	}
}
