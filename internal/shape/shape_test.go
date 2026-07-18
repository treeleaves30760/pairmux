package shape

import (
	"fmt"
	"strings"
	"testing"
)

func TestCollapseCR(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"crlf normalised", "a\r\nb\r\n", "a\nb\n"},
		{"progress overwrite one line", "P: 0%\rP: 42%\rP: 100%\n", "P: 100%\n"},
		{"leading token overwritten", "Downloading\rP: 0%\rP: 100%\n", "P: 100%\n"},
		{"no cr untouched", "plain text\nsecond\n", "plain text\nsecond\n"},
		{"cr at end of line clears it", "junk\r", ""},
		{"cr only affects its own line", "keep\nover\rwrite\n", "keep\nwrite\n"},
		{"mixed crlf and bare cr", "a\r\nb0\rb1\r\n", "a\nb1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(CollapseCR([]byte(tc.in))); got != tc.want {
				t.Fatalf("CollapseCR(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCollapseCRPromptSP covers the pre-pass for zsh's PROMPT_SP partial-line
// trailer (inverse '%' in SGRs, 8+ pad spaces, CR, optional " \r"): it must
// become a single "\n" so unterminated output like `printf 200` survives, while
// genuine \r progress overwrites keep collapsing as before.
func TestCollapseCRPromptSP(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "dump signature after partial output",
			in: "200\x1b[1m\x1b[7m%\x1b[27m\x1b[1m\x1b[0m" +
				strings.Repeat(" ", 216) + "\r \r",
			want: "200\n",
		},
		{
			name: "floor of 8 spaces, no space-cr tail",
			in:   "abc\x1b[7m%\x1b[0m" + strings.Repeat(" ", 8) + "\r",
			want: "abc\n",
		},
		{
			name: "mid-buffer signature followed by prompt line",
			in: "partial\x1b[1m\x1b[7m%\x1b[0m" + strings.Repeat(" ", 40) +
				"\r \rpmx@host ~ % next",
			want: "partial\npmx@host ~ % next",
		},
		{
			name: "negative: three spaces below floor collapses as overwrite",
			in:   "download 47%   \rdone.\n",
			want: "done.\n",
		},
		{
			name: "negative: bare 100% at line end untouched",
			in:   "progress: 100%\ndone\n",
			want: "progress: 100%\ndone\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(CollapseCR([]byte(tc.in))); got != tc.want {
				t.Fatalf("CollapseCR(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRenderPromptSPNoTrailingNewline is the acceptance repro: `printf 200`
// prints no trailing newline, so zsh emits the PROMPT_SP trailer before precmd
// — inside the output region. The 200 must survive Render.
func TestRenderPromptSPNoTrailingNewline(t *testing.T) {
	raw := []byte("pmx@host ~ % printf 200\r\n" +
		"\x1b]133;C\x07" +
		"200" +
		"\x1b[1m\x1b[7m%\x1b[27m\x1b[1m\x1b[0m" + // inverse-% marker, SGRs as captured
		strings.Repeat(" ", 216) + "\r \r") // pad to force wrap, CR, space, CR
	got, omitted := Render(raw, "printf 200", 50, 200)
	if got != "200" || omitted != 0 {
		t.Fatalf("Render printf-200 = (%q, %d), want (\"200\", 0)", got, omitted)
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain kept", "hello world", "hello world"},
		{"newline and tab kept", "a\n\tb", "a\n\tb"},
		{"sgr color removed", "\x1b[31mred\x1b[0m", "red"},
		{"erase in display", "before\x1b[Jafter", "beforeafter"},
		{"erase in line", "x\x1b[2Ky", "xy"},
		{"csi private param bracketed paste", "\x1b[?2004hfoo\x1b[?2004l", "foo"},
		{"osc bel terminated", "\x1b]133;A\x07prompt", "prompt"},
		{"osc st terminated", "\x1b]0;my title\x1b\\text", "text"},
		{"osc 8 hyperlink pair", "a\x1b]8;;http://x\x07link\x1b]8;;\x07b", "alinkb"},
		{"osc 133 d mark removed", "out\x1b]133;D;0\x07", "out"},
		// zsh partial-line marker: an inverse '%' wrapped in SGR sequences. The
		// SGR is stripped and the literal '%' survives.
		{"zsh eol mark sgr stripped", "\x1b[1m\x1b[7m%\x1b[27m\x1b[0m", "%"},
		{"esc plus single char", "a\x1bcb", "ab"},
		{"bare c0 controls dropped", "a\x00\x07\x08b", "ab"},
		{"cr dropped as c0", "a\rb", "ab"},
		{"lone trailing esc dropped", "text\x1b", "text"},
		{"utf8 preserved", "caf\xc3\xa9 \xe2\x9c\x93", "caf\xc3\xa9 \xe2\x9c\x93"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(StripANSI([]byte(tc.in))); got != tc.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripANSIOSCPayloadCap(t *testing.T) {
	// An OSC whose terminator is beyond the 4KB cap must be abandoned so the
	// real content that follows the (eventual) terminator is not swallowed.
	runaway := "\x1b]" + strings.Repeat("x", 5000) + "\x07REAL"
	got := string(StripANSI([]byte(runaway)))
	if !strings.HasSuffix(got, "REAL") {
		t.Fatalf("payload-cap abandonment lost trailing content: got suffix %q", tail(got, 8))
	}
	if !strings.Contains(got, strings.Repeat("x", 5000)) {
		t.Fatalf("expected abandoned payload to survive as literal text")
	}

	// An unterminated but short OSC at the very end is a truncated capture and
	// is dropped whole.
	got = string(StripANSI([]byte("foo\x1b]133;A")))
	if got != "foo" {
		t.Fatalf("truncated trailing OSC: got %q, want %q", got, "foo")
	}
}

// TestStripANSIStringSequences covers the non-OSC string sequences (DCS, APC,
// SOS, PM) whose payloads previously leaked as literal text: they must be
// skipped through BEL or ST just like OSC, including tmux's DCS passthrough
// wrapper form with its doubled inner ESCs.
func TestStripANSIStringSequences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"dcs terminated by st", "a\x1bP1$r0;1m\x1b\\b", "ab"},
		{"dcs terminated by bel", "a\x1bPq#0;2;0;0;0\x07b", "ab"},
		{"apc terminated by bel", "a\x1b_Ga=T,f=100\x07b", "ab"},
		{"apc terminated by st", "a\x1b_payload\x1b\\b", "ab"},
		{"sos terminated by st", "a\x1bXraw skipped text\x1b\\b", "ab"},
		{"pm terminated by bel", "a\x1b^private message\x07b", "ab"},
		{"tmux dcs passthrough wrapper", "before\x1bPtmux;\x1b\x1b]0;title\x1b\x1b\\\x1b\\after", "beforeafter"},
		{"unterminated short dcs at end dropped", "foo\x1bP12", "foo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(StripANSI([]byte(tc.in))); got != tc.want {
				t.Fatalf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripANSIDCSPayloadCap(t *testing.T) {
	// A DCS whose terminator lies beyond the 4KB cap must be abandoned so the
	// content after the (eventual) terminator survives.
	runaway := "\x1bP" + strings.Repeat("q", 5000) + "\x1b\\REAL"
	got := string(StripANSI([]byte(runaway)))
	if !strings.HasSuffix(got, "REAL") {
		t.Fatalf("DCS cap abandonment lost trailing content: got suffix %q", tail(got, 8))
	}
	if !strings.Contains(got, strings.Repeat("q", 5000)) {
		t.Fatalf("expected abandoned DCS payload to survive as literal text")
	}
}

func TestDropEchoedCommand(t *testing.T) {
	tests := []struct {
		name string
		text string
		cmd  string
		want string
	}{
		{"empty cmd is noop", "echo hi\nhi", "", "echo hi\nhi"},
		{"bare command line", "echo hi\nhi", "echo hi", "hi"},
		{"prompt prefix tolerated", "pmx@host ~ % echo hi\nhi", "echo hi", "hi"},
		{"leading blanks skipped and dropped", "\n\n$ ls\nfile", "ls", "file"},
		{"trailing space on echo tolerated", "$ echo hi  \nhi", "echo hi", "hi"},
		{"first line not the command untouched", "output only\nmore", "echo hi", "output only\nmore"},
		{"command only leaves empty", "$ echo hi", "echo hi", ""},
		{"only a substring in middle not dropped", "run echo hi now\nout", "echo hi", "run echo hi now\nout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DropEchoedCommand(tc.text, tc.cmd); got != tc.want {
				t.Fatalf("DropEchoedCommand(%q, %q) = %q, want %q", tc.text, tc.cmd, got, tc.want)
			}
		})
	}
}

// TestDropEchoedCommandWrapped covers command echoes hard-wrapped by the
// terminal across consecutive lines (pane width ~220): the wrapped lines must be
// recognised and dropped, while unrelated short output lines must never be
// joined-and-dropped.
func TestDropEchoedCommandWrapped(t *testing.T) {
	const width = 220
	prompt := "pmx@host ~ % "
	longCmd := strings.Repeat("abcdefghij", 50) // 500 chars; echo wraps mid-token
	echo := prompt + longCmd                    // 513 chars -> lines of 220, 220, 73
	var wrapped, padded []string
	for s := echo; s != ""; {
		n := width
		if len(s) < n {
			n = len(s)
		}
		wrapped = append(wrapped, s[:n])
		padded = append(padded, s[:n]+strings.Repeat(" ", width-n))
		s = s[n:]
	}
	if len(wrapped) != 3 {
		t.Fatalf("fixture: wrapped echo spans %d lines, want 3", len(wrapped))
	}

	tests := []struct {
		name string
		text string
		cmd  string
		want string
	}{
		{
			name: "500-char cmd wrapped at 220 over 3 lines",
			text: strings.Join(wrapped, "\n") + "\nreal output",
			cmd:  longCmd,
			want: "real output",
		},
		{
			name: "500-char cmd wrapped with pane-width padding",
			text: strings.Join(padded, "\n") + "\nreal output",
			cmd:  longCmd,
			want: "real output",
		},
		{
			name: "wrap splits mid-word",
			text: "$ echo supercalifragilis\nticexpialidocious\nout",
			cmd:  "echo supercalifragilisticexpialidocious",
			want: "out",
		},
		{
			name: "padded wrap needs per-line right-trim",
			text: "$ echo supercalifragilis   \nticexpialidocious\nout",
			cmd:  "echo supercalifragilisticexpialidocious",
			want: "out",
		},
		{
			name: "leading blanks dropped with wrapped echo",
			text: "\n$ echo abcdef\nghijkl\nout",
			cmd:  "echo abcdefghijkl",
			want: "out",
		},
		{
			name: "negative: two normal short lines stay",
			text: "output alpha\noutput beta\nmore",
			cmd:  "echo hello",
			want: "output alpha\noutput beta\nmore",
		},
		{
			name: "negative: accidental join suffix rejected by length guard",
			text: "gcc -O2 -c foo.c &&\nmake\ndone",
			cmd:  "make",
			want: "gcc -O2 -c foo.c &&\nmake\ndone",
		},
		{
			name: "negative: blank line ends the candidate run",
			text: "abc\n\ndef",
			cmd:  "abcdef",
			want: "abc\n\ndef",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DropEchoedCommand(tc.text, tc.cmd); got != tc.want {
				t.Fatalf("DropEchoedCommand(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		head, tail  int
		want        string
		wantOmitted int
	}{
		{"fits exactly head+tail", "a\nb\nc\nd", 2, 2, "a\nb\nc\nd", 0},
		{"fewer than head+tail", "a\nb", 2, 2, "a\nb", 0},
		{"basic truncation", "a\nb\nc\nd\ne\nf", 1, 1, "a\n" + truncMarker + "\nf", 4},
		{"head zero", "a\nb\nc\nd\ne", 0, 2, truncMarker + "\nd\ne", 3},
		{"tail zero", "a\nb\nc\nd\ne", 2, 0, "a\nb\n" + truncMarker, 3},
		{"negative clamped to zero", "a\nb\nc", -5, 1, truncMarker + "\nc", 2},
		{"single line fits", "solo", 50, 200, "solo", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, omitted := Truncate(tc.text, tc.head, tc.tail)
			if got != tc.want || omitted != tc.wantOmitted {
				t.Fatalf("Truncate(%q, %d, %d) = (%q, %d), want (%q, %d)",
					tc.text, tc.head, tc.tail, got, omitted, tc.want, tc.wantOmitted)
			}
		})
	}
}

// TestRenderZshFixture is the contract's headline case: a realistic captured zsh
// journal segment with a prompt, OSC 133 A/C/D marks, an inverse-'%' partial-line
// marker wrapped in SGR, an ESC[J erase, and CRLF line endings must render to
// clean output text with the echoed command dropped.
func TestRenderZshFixture(t *testing.T) {
	raw := []byte(
		"\x1b[J" + // erase-in-display left over from the previous prompt redraw
			"\x1b]133;A\x07" + // OSC 133 A: prompt start
			"pmx@host ~ % " + // the prompt string itself
			"echo hello\r\n" + // echoed command line, CRLF
			"\x1b]133;C\x07" + // OSC 133 C: command output start
			"hello\r\n" + // command output, CRLF
			"\x1b]133;D;0\x07" + // OSC 133 D;0: command finished exit 0
			"\x1b[1m\x1b[7m%\x1b[27m\x1b[0m" + // zsh EOL partial-line marker (inverse %)
			"      \r") // padding spaces then CR that clear the marker

	got, omitted := Render(raw, "echo hello", 50, 200)
	if got != "hello" {
		t.Fatalf("Render zsh fixture = %q, want %q", got, "hello")
	}
	if omitted != 0 {
		t.Fatalf("Render zsh fixture omitted = %d, want 0", omitted)
	}
}

func TestRenderProgressBar(t *testing.T) {
	raw := []byte("Downloading\rProgress: 0%\rProgress: 42%\rProgress: 100%\n")
	got, omitted := Render(raw, "", 50, 200)
	if got != "Progress: 100%" {
		t.Fatalf("Render progress bar = %q, want %q", got, "Progress: 100%")
	}
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
}

// TestRenderDropsLeadingBlanks reproduces the integration defect: in zsh hooks
// mode a blank echo artifact sits between the echoed command line and the real
// output, so after DropEchoedCommand the text began with "\n". Render must
// return the output with no leading newline.
func TestRenderDropsLeadingBlanks(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		cmd  string
		want string
	}{
		{
			name: "zsh artifact blank line after echoed cmd",
			raw:  "pmx@host ~ % echo hello\r\n\r\nhello\r\n",
			cmd:  "echo hello",
			want: "hello",
		},
		{
			name: "osc marks plus blank artifact",
			raw: "\x1b]133;A\x07pmx@host ~ % echo hello\r\n" +
				"\x1b]133;C\x07\r\nhello\r\n\x1b]133;D;0\x07",
			cmd:  "echo hello",
			want: "hello",
		},
		{
			name: "multiple leading blanks including whitespace-only",
			raw:  "$ ls\r\n\r\n   \r\nfile\r\n",
			cmd:  "ls",
			want: "file",
		},
		{
			name: "leading blanks trimmed even without echo drop",
			raw:  "\r\n\r\noutput\r\n",
			cmd:  "",
			want: "output",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, omitted := Render([]byte(tc.raw), tc.cmd, 50, 200)
			if got != tc.want || omitted != 0 {
				t.Fatalf("Render(%q, %q) = (%q, %d), want (%q, 0)",
					tc.raw, tc.cmd, got, omitted, tc.want)
			}
		})
	}
}

func TestRenderTrimsTrailingBlanks(t *testing.T) {
	raw := []byte("line one\nline two\n\n\n   \n")
	got, _ := Render(raw, "", 50, 200)
	if got != "line one\nline two" {
		t.Fatalf("Render = %q, want %q", got, "line one\nline two")
	}
}

func TestRenderTruncationCounts(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	got, omitted := Render([]byte(b.String()), "", 50, 200)
	if omitted != 750 {
		t.Fatalf("omitted = %d, want 750", omitted)
	}
	lines := strings.Split(got, "\n")
	// 50 head + 1 marker + 200 tail
	if len(lines) != 251 {
		t.Fatalf("rendered line count = %d, want 251", len(lines))
	}
	if lines[0] != "line-1" || lines[49] != "line-50" {
		t.Fatalf("head lines wrong: first=%q line50=%q", lines[0], lines[49])
	}
	if lines[50] != truncMarker {
		t.Fatalf("marker line = %q, want %q", lines[50], truncMarker)
	}
	if lines[51] != "line-801" || lines[250] != "line-1000" {
		t.Fatalf("tail lines wrong: first=%q last=%q", lines[51], lines[250])
	}
}

func TestRenderEmpty(t *testing.T) {
	got, omitted := Render(nil, "anything", 50, 200)
	if got != "" || omitted != 0 {
		t.Fatalf("Render(nil) = (%q, %d), want (\"\", 0)", got, omitted)
	}
}

// tail returns the last n bytes of s for readable failure messages.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
