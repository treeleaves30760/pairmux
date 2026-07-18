// Package shape turns the raw terminal journal byte stream into clean,
// agent-readable text.
//
// raw.log is exactly what tmux pipe-pane captured off a pane: it is full of
// carriage-return overwrites (progress bars, spinners), ANSI/VT control
// sequences (colors, cursor moves, erases), OSC marks, and the echo of the
// command the caller typed. The functions here reduce that to the text a human
// would have read off the screen.
//
// The canonical entry point is Render, which composes the individual steps in
// the one order that is correct: CollapseCR (while the \r positions are still
// intact) -> StripANSI -> trailing-blank trim -> DropEchoedCommand ->
// leading-blank trim -> Truncate. The steps are exported individually so
// callers (peek/log) can compose subsets.
package shape

import (
	"bytes"
	"regexp"
	"strings"
)

// truncMarker is the single-line placeholder Truncate inserts between the head
// and tail blocks. A lone horizontal-ellipsis rune reads as an omission marker
// and cannot be confused with the "..." that appears in real program output.
const truncMarker = "…"

// stringPayloadCap bounds how far StripANSI scans for a string-sequence
// terminator (OSC, DCS, APC, SOS, PM). Past this an unterminated sequence is
// abandoned so a single malformed sequence cannot swallow the rest of the stream.
const stringPayloadCap = 4096

// promptSPRe matches zsh's PROMPT_SP partial-line trailer: an inverse '%'
// wrapped in SGR sequences, 8+ pad spaces to force a wrap, a CR, and optionally
// a " \r" tail. zsh emits it BEFORE precmd when the last command's output had
// no trailing newline, so it lands inside a run's output region. The 8-space
// floor keeps genuine progress overwrites like "47%   \r" out of its reach.
var promptSPRe = regexp.MustCompile(`(?:\x1b\[[0-9;]*m)*%(?:\x1b\[[0-9;]*m)*[ ]{8,}\r(?: \r)?`)

// CollapseCR resolves carriage-return overwrites the way a terminal would: a \r
// returns the cursor to column zero so later bytes overwrite earlier ones. It
// first normalises CRLF (\r\n) to LF, then for every resulting line keeps only
// the content after the final remaining \r.
//
// A pre-pass rewrites each PROMPT_SP trailer (promptSPRe) to a single "\n":
// its visual meaning is "keep the partial content, continue on a fresh line",
// so `200<trailer>` becomes "200\n". Without this, the keep-after-final-\r rule
// below would wipe the whole partial line — the trailer ends in \r — and eat
// the output of any command that does not print a trailing newline.
//
// Run this BEFORE StripANSI: StripANSI drops \r as a C0 control, so the overwrite
// information would be lost if the order were reversed.
func CollapseCR(b []byte) []byte {
	b = promptSPRe.ReplaceAll(b, []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(b, []byte("\n"))
	for i, line := range lines {
		if idx := bytes.LastIndexByte(line, '\r'); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// StripANSI removes terminal control sequences and stray control bytes, leaving
// printable text plus \n and \t. It removes:
//   - CSI sequences: ESC '[' , parameter/intermediate bytes (0x20-0x3f), a final
//     byte in 0x40-0x7e. Private-parameter forms like ESC[?2004h are covered
//     because '?' is a parameter byte.
//   - String sequences: OSC (ESC ']'), DCS (ESC 'P'), APC (ESC '_'), SOS
//     (ESC 'X'), PM (ESC '^'), each skipped up to a BEL (0x07) or ST (ESC '\').
//     BEL is standard only for OSC, but emitters vary, so both terminators are
//     accepted for all five. The payload is capped at stringPayloadCap bytes so
//     a malformed sequence cannot eat the stream. tmux's DCS passthrough
//     (ESC P tmux; ... ESC \) is skipped whole.
//   - Any other ESC + single byte (e.g. ESC c full reset).
//   - C0 control bytes other than \n and \t (this includes \r, which callers who
//     care about \r must resolve with CollapseCR first).
//
// Bytes >= 0x80 pass through untouched so UTF-8 text is preserved. StripANSI only
// deletes; it never rewrites surviving bytes.
func StripANSI(b []byte) []byte {
	out := make([]byte, 0, len(b))
	n := len(b)
	i := 0
	for i < n {
		c := b[i]
		switch {
		case c == 0x1b: // ESC — start of an escape sequence
			if i+1 >= n {
				i++ // lone trailing ESC: drop it
				continue
			}
			switch b[i+1] {
			case '[': // CSI
				i = skipCSI(b, i)
			case ']', 'P', '_', 'X', '^': // OSC / DCS / APC / SOS / PM
				i = skipStringSeq(b, i)
			default: // ESC + single char
				i += 2
			}
		case c == '\n' || c == '\t':
			out = append(out, c)
			i++
		case c < 0x20:
			i++ // other C0 control (\r, BEL, ...): drop
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

// skipCSI returns the index just past a CSI sequence starting at i (b[i]=ESC,
// b[i+1]='['). If the sequence is malformed (no final byte, or an out-of-range
// byte before one), it abandons by skipping only the ESC so the '[' and any
// following bytes are reprocessed as literal text.
func skipCSI(b []byte, i int) int {
	n := len(b)
	j := i + 2
	for j < n {
		d := b[j]
		if d >= 0x40 && d <= 0x7e {
			return j + 1 // final byte consumed
		}
		if d >= 0x20 && d <= 0x3f {
			j++ // parameter / intermediate byte
			continue
		}
		break // invalid byte inside CSI: malformed
	}
	return i + 1 // abandon
}

// skipStringSeq returns the index just past a string sequence starting at i
// (b[i]=ESC, b[i+1] one of ']' 'P' '_' 'X' '^': OSC, DCS, APC, SOS, PM). It
// consumes through a BEL or ST terminator found within stringPayloadCap bytes.
// If none is found: an unterminated-but-short sequence at the end of the buffer
// is dropped whole (truncated capture), while a run past the cap is abandoned by
// skipping only the ESC so the stream is not swallowed.
func skipStringSeq(b []byte, i int) int {
	n := len(b)
	j := i + 2
	limit := i + 2 + stringPayloadCap
	if limit > n {
		limit = n
	}
	for j < limit {
		if b[j] == 0x07 { // BEL
			return j + 1
		}
		if b[j] == 0x1b && j+1 < n && b[j+1] == '\\' { // ST = ESC '\'
			return j + 2
		}
		j++
	}
	if limit == n {
		return n // unterminated at end of buffer: drop the truncated sequence
	}
	return i + 1 // ran past the cap with data still following: abandon
}

// maxEchoWrapLines caps how many leading lines a hard-wrapped command echo may
// span. At ~220 columns that admits commands over a thousand characters long.
const maxEchoWrapLines = 6

// DropEchoedCommand removes the echoed command line. If the first non-empty line
// ends with cmd (any prompt prefix is tolerated), that line — and any blank lines
// before it — are dropped. Terminals hard-wrap input longer than the pane width,
// so when the single-line match fails the first 2..maxEchoWrapLines consecutive
// non-blank lines are tried as one wrapped echo: joined raw (a terminal wrap
// inserts no bytes, so mid-token splits rejoin exactly) and joined with each line
// right-trimmed (tmux may pad captured lines with trailing spaces). If a join
// ends with cmd, all its lines plus the leading blanks are dropped. If nothing
// matches, the text is returned unchanged. An empty cmd is a no-op.
func DropEchoedCommand(text, cmd string) string {
	if cmd == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++ // leading blank lines are dropped together with a matched echo
	}
	if i == len(lines) {
		return text // all blank
	}
	if strings.HasSuffix(strings.TrimRight(lines[i], " \t"), cmd) {
		return strings.Join(lines[i+1:], "\n")
	}
	for k := 2; k <= maxEchoWrapLines && i+k <= len(lines); k++ {
		last := lines[i+k-1]
		if strings.TrimSpace(last) == "" {
			break // wrapped echo lines are consecutive; a blank ends the run
		}
		// A genuine wrap means cmd itself spans lines, so cmd must be longer
		// than the content of the candidate's last line. This rejects accidental
		// suffix matches produced by joining unrelated short output lines.
		if len(cmd) <= len(strings.TrimRight(last, " \t")) {
			continue
		}
		if wrapJoinEndsWith(lines[i:i+k], cmd) {
			return strings.Join(lines[i+k:], "\n")
		}
	}
	return text
}

// wrapJoinEndsWith reports whether the candidate wrapped-echo lines end with cmd
// under either join a wrap can produce: raw concatenation, or concatenation with
// each line right-trimmed of the padding tmux may add.
func wrapJoinEndsWith(seg []string, cmd string) bool {
	if strings.HasSuffix(strings.TrimRight(strings.Join(seg, ""), " \t"), cmd) {
		return true
	}
	trimmed := make([]string, len(seg))
	for i, l := range seg {
		trimmed[i] = strings.TrimRight(l, " \t")
	}
	return strings.HasSuffix(strings.Join(trimmed, ""), cmd)
}

// Truncate keeps the first head and last tail lines when the text has more than
// head+tail lines, joining the two blocks with a lone ellipsis marker line, and
// reports how many lines were omitted. If the text fits within head+tail lines it
// is returned unchanged with a zero count. Negative head/tail are treated as 0.
func Truncate(text string, head, tail int) (string, int) {
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	lines := strings.Split(text, "\n")
	n := len(lines)
	if n <= head+tail {
		return text, 0
	}
	omitted := n - head - tail
	kept := make([]string, 0, head+1+tail)
	kept = append(kept, lines[:head]...)
	kept = append(kept, truncMarker)
	kept = append(kept, lines[n-tail:]...)
	return strings.Join(kept, "\n"), omitted
}

// Render is the full pipeline: CollapseCR -> StripANSI -> trailing-blank trim ->
// DropEchoedCommand -> leading-blank trim -> Truncate. It returns the cleaned
// text and the number of lines Truncate omitted (0 when nothing was truncated).
//
// The leading trim runs AFTER DropEchoedCommand: dropping the echoed command
// line can expose blank echo artifacts (zsh hooks mode emits one between the
// command line and its output) that would otherwise survive as a leading "\n".
// It lives here, not in DropEchoedCommand, so peek/log composing subsets keep
// that function's contract unchanged.
func Render(raw []byte, cmd string, head, tail int) (string, int) {
	b := CollapseCR(raw)
	b = StripANSI(b)
	text := trimTrailingBlankLines(string(b))
	text = DropEchoedCommand(text, cmd)
	text = trimLeadingBlankLines(text)
	return Truncate(text, head, tail)
}

// trimLeadingBlankLines removes leading lines that are empty or whitespace-only.
func trimLeadingBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	return strings.Join(lines[start:], "\n")
}

// trimTrailingBlankLines removes trailing lines that are empty or whitespace-only,
// including the empty final element a trailing newline produces.
func trimTrailingBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}
