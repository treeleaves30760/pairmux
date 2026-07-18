// Package detect turns pairmux's raw.log byte stream into completion signals.
// It provides a stateful, incremental scanner for OSC 133 shell-integration
// marks and the pairmux sentinel sequence, plus the waiting/status-derivation
// helpers built on top of a terminal's journal.
package detect

import (
	"strconv"
	"strings"
)

// MarkKind identifies a recognized OSC mark.
type MarkKind int

const (
	MarkA        MarkKind = iota // OSC 133;A — prompt start
	MarkB                        // OSC 133;B — prompt end / command start (input)
	MarkC                        // OSC 133;C — command output start
	MarkD                        // OSC 133;D[;exit] — command finished
	MarkSentinel                 // OSC 7779;p;exit — pairmux run sentinel
)

func (k MarkKind) String() string {
	switch k {
	case MarkA:
		return "A"
	case MarkB:
		return "B"
	case MarkC:
		return "C"
	case MarkD:
		return "D"
	case MarkSentinel:
		return "sentinel"
	default:
		return "unknown"
	}
}

// Mark is a recognized sequence located in the stream. Start and End are
// absolute stream offsets: Start is the offset of the leading ESC, End is the
// offset one past the terminator's last byte. ExitCode is -1 when absent.
type Mark struct {
	Kind     MarkKind
	ExitCode int
	Start    int64
	End      int64
}

const (
	esc = 0x1b // ESC
	bel = 0x07 // BEL, one of the two OSC terminators

	// payloadCap bounds the bytes collected after an OSC introducer. An
	// introducer that runs this long without a BEL or ST terminator is
	// abandoned so a malformed/binary stream cannot swallow later marks.
	payloadCap = 160
)

type scanState int

const (
	stGround scanState = iota // outside any escape sequence
	stEsc                     // saw ESC in ground
	stOSC                     // collecting OSC payload after ESC ]
	stOSCEsc                  // saw ESC inside an OSC (possible ST, or restart)
)

// Scanner incrementally recognizes OSC marks across arbitrarily-split chunks.
// A single Scanner must be fed the stream in order; sequences may straddle Feed
// boundaries. It is not safe for concurrent use.
type Scanner struct {
	state     scanState
	pos       int64  // absolute offset of the next byte to consume
	markStart int64  // offset of the ESC that began the current OSC
	payload   []byte // OSC payload bytes collected so far (excludes ESC ] and terminator)
}

// NewScanner returns a Scanner whose first fed byte sits at absolute offset base.
func NewScanner(base int64) *Scanner {
	return &Scanner{state: stGround, pos: base}
}

// Feed consumes chunk and returns any marks that completed within it. Marks
// whose sequence began in an earlier Feed are reported here, at their true
// absolute offsets. The input is never modified.
func (s *Scanner) Feed(chunk []byte) []Mark {
	var marks []Mark
	for _, b := range chunk {
		off := s.pos
		s.pos++

		switch s.state {
		case stGround:
			if b == esc {
				s.state = stEsc
				s.markStart = off
			}

		case stEsc:
			switch b {
			case ']':
				s.state = stOSC
				s.payload = s.payload[:0]
			case esc:
				// Consecutive ESC: restart the escape at this byte.
				s.markStart = off
			default:
				// Some other escape (CSI, charset, ...) — not ours.
				s.state = stGround
			}

		case stOSC:
			switch b {
			case bel:
				if m, ok := s.finish(off + 1); ok {
					marks = append(marks, m)
				}
				s.state = stGround
			case esc:
				// Possible ST terminator (ESC \) or a fresh sequence.
				s.state = stOSCEsc
			default:
				s.payload = append(s.payload, b)
				if len(s.payload) > payloadCap {
					// Abandon the over-long introducer. The collected bytes
					// held neither ESC nor BEL, so nothing recoverable is
					// dropped; later marks are still detected.
					s.state = stGround
					s.payload = s.payload[:0]
				}
			}

		case stOSCEsc:
			switch b {
			case '\\':
				// ST terminator (ESC \).
				if m, ok := s.finish(off + 1); ok {
					marks = append(marks, m)
				}
				s.state = stGround
			case ']':
				// The ESC started a new OSC; restart from that ESC.
				s.state = stOSC
				s.markStart = off - 1
				s.payload = s.payload[:0]
			case esc:
				// Another ESC — cancel this OSC, keep the new ESC pending.
				s.state = stEsc
				s.markStart = off
			default:
				// Stray ESC cancelled the OSC; nothing to emit.
				s.state = stGround
			}
		}
	}
	return marks
}

// finish parses the collected payload into a Mark ending at absolute offset end
// (one past the terminator). ok is false for OSC sequences we do not recognize.
func (s *Scanner) finish(end int64) (Mark, bool) {
	fields := strings.Split(string(s.payload), ";")
	switch fields[0] {
	case "133":
		if len(fields) < 2 || len(fields[1]) != 1 {
			return Mark{}, false
		}
		var kind MarkKind
		switch fields[1] {
		case "A":
			kind = MarkA
		case "B":
			kind = MarkB
		case "C":
			kind = MarkC
		case "D":
			kind = MarkD
		default:
			return Mark{}, false
		}
		return Mark{Kind: kind, ExitCode: exitFrom(fields, 2), Start: s.markStart, End: end}, true

	case "7779":
		// pairmux sentinel: 7779;p;<exit>
		if len(fields) < 3 || fields[1] != "p" {
			return Mark{}, false
		}
		return Mark{Kind: MarkSentinel, ExitCode: exitFrom(fields, 2), Start: s.markStart, End: end}, true
	}
	return Mark{}, false
}

// exitFrom parses fields[idx] as an exit code, returning -1 when absent or
// non-numeric.
func exitFrom(fields []string, idx int) int {
	if idx >= len(fields) {
		return -1
	}
	v, err := strconv.Atoi(strings.TrimSpace(fields[idx]))
	if err != nil {
		return -1
	}
	return v
}
