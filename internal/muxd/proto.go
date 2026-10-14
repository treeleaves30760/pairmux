package muxd

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/treeleaves30760/pairmux/internal/mux"
)

// protoVersion is bumped when a request's meaning changes incompatibly. A
// server answers every request with its own version, so a client talking to a
// server left running by an older pairmux can tell why an operation failed.
const protoVersion = 1

// maxMessage bounds one control message. A capture with deep history is the
// largest legitimate message; anything bigger is a corrupt stream.
const maxMessage = 64 << 20

// request is one control operation. A connection carries exactly one request
// and one response, except "attach", after which it switches to frames.
type request struct {
	Op      string   `json:"op"`
	Proto   int      `json:"proto"`
	Pane    string   `json:"pane,omitempty"`
	Name    string   `json:"name,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	Journal string   `json:"journal,omitempty"`
	Text    string   `json:"text,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	History int      `json:"history,omitempty"`
	Opt     string   `json:"opt,omitempty"`
	Val     string   `json:"val,omitempty"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
}

type response struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Proto    int            `json:"proto"`
	Version  string         `json:"version,omitempty"`
	Pid      int            `json:"pid,omitempty"`
	Pane     string         `json:"pane,omitempty"`
	Text     string         `json:"text,omitempty"`
	Panes    []mux.PaneInfo `json:"panes,omitempty"`
	Attached int            `json:"attached,omitempty"`
	Active   bool           `json:"active,omitempty"`
}

func writeMsg(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > maxMessage {
		return fmt.Errorf("muxd: message too large (%d bytes)", len(body))
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

func readMsg(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxMessage {
		return fmt.Errorf("muxd: message too large (%d bytes)", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// Attach frames: one type byte, a 4-byte big-endian length, the payload.
const (
	frameInput  = 'i' // client → server: keyboard bytes
	frameResize = 'r' // client → server: cols, rows as two uint16
	frameDetach = 'd' // client → server: leaving
	frameOutput = 'o' // server → client: bytes for the client's terminal
	frameExit   = 'x' // server → client: the pane is gone; payload is a message
)

// maxFrame bounds one attach frame payload.
const maxFrame = 16 << 20

func encodeFrame(typ byte, payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("muxd: frame too large")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func resizePayload(cols, rows int) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:2], uint16(cols))
	binary.BigEndian.PutUint16(b[2:4], uint16(rows))
	return b[:]
}

func parseResize(p []byte) (cols, rows int, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(p[0:2])), int(binary.BigEndian.Uint16(p[2:4])), true
}
