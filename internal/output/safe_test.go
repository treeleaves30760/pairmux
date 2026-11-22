package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintableLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"plain ascii",
		"tabs\tand\nnewlines\n",
		"繁體中文 ✉ ● … emoji 👩‍💻 flags 🇹🇼",
		"café — naïve",
	} {
		if got := Printable(s); got != s {
			t.Errorf("Printable(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestPrintableNeutralisesControls(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\x1b[2Kb", `a\x1b[2Kb`},
		{"\x1b]52;c;ZWNobw==\x07", `\x1b]52;c;ZWNobw==\x07`},
		{"title\x1b]0;pwned\x1b\\", `title\x1b]0;pwned\x1b\`},
		{"over\rwrite", `over\rwrite`},
		{"bell\x07 bs\x08 nul\x00 del\x7f", `bell\x07 bs\x08 nul\x00 del\x7f`},
		{"c1 csi \u009b2J", `c1 csi \x9b2J`},
		{"raw csi byte \x9b2J", `raw csi byte \x9b2J`},
		{"bad utf8 \xff\xfe", `bad utf8 \xff\xfe`},
		{"keeps\n\tlayout\x1b", "keeps\n\tlayout" + `\x1b`},
	}
	for _, c := range cases {
		got := Printable(c.in)
		if got != c.want {
			t.Errorf("Printable(%q) = %q, want %q", c.in, got, c.want)
		}
		for _, r := range got {
			if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f) {
				t.Errorf("Printable(%q) still holds control %U", c.in, r)
			}
		}
	}
}

func TestPrintableLineStaysOnOneRow(t *testing.T) {
	got := PrintableLine("one\ntwo\tthree\r\x1b[1A")
	want := `one\ntwo three\r\x1b[1A`
	if got != want {
		t.Fatalf("PrintableLine = %q, want %q", got, want)
	}
	if s := "no change here"; PrintableLine(s) != s {
		t.Fatalf("PrintableLine rewrote %q", s)
	}
}

// A note is written by one party and read by another. In the human block it
// must arrive as text, and in JSON it must still be the exact string.
func TestEmitHumanNeutralisesStoredText(t *testing.T) {
	hostile := "ok\x1b]52;c;cm0gLXJmIH4=\x07\x1b[1A\x1b[2K\rall tests pass"
	e := Envelope{
		OK: true, Status: "idle", Terminal: "build",
		Notes:     []string{hostile},
		Terminals: []TerminalRow{{Name: "build", Status: "running", Mode: "hooks", CurrentCmd: "make \x1b[2J"}},
		Next:      []string{"pairmux peek build"},
	}
	var human bytes.Buffer
	Emit(&human, false, e)
	if strings.ContainsAny(human.String(), "\x1b\x07\r") {
		t.Fatalf("human block carries control characters:\n%q", human.String())
	}
	for _, want := range []string{`\x1b]52;c;cm0gLXJmIH4=\x07`, `make \x1b[2J`, "notes from human:\n", "next:\n  pairmux peek build\n"} {
		if !strings.Contains(human.String(), want) {
			t.Errorf("human block lacks %q:\n%s", want, human.String())
		}
	}

	var js bytes.Buffer
	Emit(&js, true, e)
	if strings.ContainsAny(js.String(), "\x1b\x07\r") {
		t.Fatalf("JSON line carries raw control characters: %q", js.String())
	}
	if !strings.Contains(js.String(), `\u001b]52;c;cm0gLXJmIH4=\u0007`) {
		t.Errorf("JSON should keep the note's content, escaped: %s", js.String())
	}
}
