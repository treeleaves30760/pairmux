package cli

import (
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/treeleaves30760/pairmux/internal/muxd"
)

// This file reads the TUI's input: stdin on its own goroutine, decoded into
// keys and SGR mouse reports.

// ttyReader reads stdin on its own goroutine. Reads are polled (RawPoll,
// PollRead), so park returns within about 100ms with the reader idle and no
// read pending that could take keys meant for an attached pane.
type ttyReader struct {
	out     chan []byte
	parkReq chan chan struct{}
	resumed chan struct{}
}

func newTTYReader() *ttyReader {
	return &ttyReader{out: make(chan []byte, 64), parkReq: make(chan chan struct{}), resumed: make(chan struct{})}
}

func (r *ttyReader) loop() {
	buf := make([]byte, 512)
	for {
		select {
		case ack := <-r.parkReq:
			close(ack)
			<-r.resumed
			continue
		default:
		}
		n, err := muxd.PollRead(buf)
		if n > 0 {
			b := append([]byte(nil), buf[:n]...)
			select {
			case r.out <- b:
			case ack := <-r.parkReq:
				close(ack)
				<-r.resumed
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (r *ttyReader) park() {
	ack := make(chan struct{})
	r.parkReq <- ack
	<-ack
	for len(r.out) > 0 { // keys typed before the attach belong to the TUI, not the pane
		<-r.out
	}
}

func (r *ttyReader) resume() { r.resumed <- struct{}{} }

type tuiKey struct {
	name string // "rune" for printable input, "mouse" for a mouse report
	r    rune
	// A mouse report: 0-based cell, and the wheel direction (-1 up, +1
	// down) or 0 for a left click.
	mx, my, wheel int
}

// parseKeys decodes one read's worth of terminal input into keys. A read
// carries whole escape sequences in practice; an ESC with nothing recognisable
// after it is the Escape key.
func parseKeys(b []byte) []tuiKey {
	var keys []tuiKey
	for len(b) > 0 {
		if b[0] == 0x1b {
			if len(b) == 1 {
				keys = append(keys, tuiKey{name: "esc"})
				b = b[1:]
				continue
			}
			if len(b) > 2 && b[1] == '[' && b[2] == '<' {
				k, rest, ok := parseSGRMouse(b[3:])
				b = rest
				if ok {
					keys = append(keys, k)
				}
				continue
			}
			if b[1] == '[' || b[1] == 'O' {
				i := 2
				for i < len(b) && (b[i] >= '0' && b[i] <= '9' || b[i] == ';') {
					i++
				}
				if i < len(b) {
					name := csiKeys[string(b[2:i+1])]
					b = b[i+1:]
					if name != "" {
						keys = append(keys, tuiKey{name: name})
					}
					continue
				}
				b = nil
				continue
			}
			// Alt-<key> or a lone ESC followed by typing: treat as ESC.
			keys = append(keys, tuiKey{name: "esc"})
			b = b[1:]
			continue
		}
		switch b[0] {
		case '\r', '\n':
			keys = append(keys, tuiKey{name: "enter"})
		case 0x7f, 0x08:
			keys = append(keys, tuiKey{name: "backspace"})
		case '\t':
			keys = append(keys, tuiKey{name: "tab"})
		case 0x03:
			keys = append(keys, tuiKey{name: "ctrl-c"})
		case 0x15:
			keys = append(keys, tuiKey{name: "ctrl-u"})
		default:
			if b[0] < 0x20 {
				b = b[1:]
				continue
			}
			r, size := decodeRune(b)
			keys = append(keys, tuiKey{name: "rune", r: r})
			b = b[size:]
			continue
		}
		b = b[1:]
	}
	return keys
}

// parseSGRMouse decodes the body of an SGR mouse report, "b;x;yM" (press)
// or "...m" (release). Only left-button presses and the wheel are reported;
// everything else (releases, drags, other buttons) is consumed and dropped.
func parseSGRMouse(b []byte) (tuiKey, []byte, bool) {
	i := 0
	for i < len(b) && (b[i] >= '0' && b[i] <= '9' || b[i] == ';') {
		i++
	}
	if i >= len(b) {
		return tuiKey{}, nil, false
	}
	final := b[i]
	parts := strings.Split(string(b[:i]), ";")
	rest := b[i+1:]
	if len(parts) != 3 || final != 'M' && final != 'm' {
		return tuiKey{}, rest, false
	}
	var n [3]int
	for j, p := range parts {
		v := 0
		for _, c := range p {
			v = v*10 + int(c-'0')
		}
		n[j] = v
	}
	k := tuiKey{name: "mouse", mx: n[1] - 1, my: n[2] - 1}
	switch {
	case final == 'm':
		return k, rest, false
	case n[0] == 64:
		k.wheel = -1
	case n[0] == 65:
		k.wheel = 1
	case n[0] != 0:
		return k, rest, false
	}
	return k, rest, true
}

var csiKeys = map[string]string{
	"A": "up", "B": "down", "C": "right", "D": "left", "H": "home", "F": "end",
	"1~": "home", "4~": "end", "7~": "home", "8~": "end",
	"5~": "pgup", "6~": "pgdn", "3~": "delete", "Z": "backtab",
}

func decodeRune(b []byte) (rune, int) {
	r, size := utf8.DecodeRune(b)
	if size == 0 {
		size = 1
	}
	return r, size
}
