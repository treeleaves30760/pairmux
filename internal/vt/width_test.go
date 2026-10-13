package vt

import (
	"testing"
	"unicode"
)

func TestRuneWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{0, 0}, {'\n', 0}, {0x1b, 0}, {0x7f, 0}, {0x85, 0}, {0x9f, 0},
		{' ', 1}, {'a', 1}, {'~', 1}, {0xa0, 1}, {0xad, 1}, {'é', 1}, {'Ω', 1},
		{0x0301, 0}, {0x0483, 0}, {0x0488, 0}, // Mn, Me
		{0x093e, 1},                                                     // Mc stays narrow (glibc, xterm)
		{0x200b, 0}, {0x200d, 0}, {0x200e, 0}, {0x2060, 0}, {0xfeff, 0}, // Cf
		{0xfe00, 0}, {0xfe0f, 0}, {0xe0100, 0}, {0xe0001, 0}, {0xe0041, 0}, // variation selectors, tags
		{0x0600, 0}, // prepended concatenation mark (Cf), as tmux
		{0x1100, 2}, {0x115f, 2}, {0x1160, 0}, {0x11a8, 0}, {0x11ff, 0}, {0xd7b0, 0}, {0xd7fb, 0},
		{'中', 2}, {'文', 2}, {0x3000, 2}, {0x303f, 1}, {0x3041, 2}, {0x3099, 0}, {0x30a2, 2},
		{0xac00, 2}, {0xd7a3, 2}, {0xff01, 2}, {0xff61, 1}, {0xffe0, 2},
		{0x4dc0, 2}, {0x2630, 2}, {0x1d300, 2}, // Unicode 16 wide additions
		{0x20000, 2}, {0x2fffd, 2}, {0x30000, 2}, {0x3fffd, 2},
		{0x1f600, 2}, {0x1f44d, 2}, {0x1f3fd, 2}, {0x231a, 2}, {0x2705, 2}, {0x1fae9, 2},
		{0x2764, 1}, {0x2600, 1}, {0x261d, 1}, {0x1f1f9, 1}, {0x1f130, 1},
		{0x2500, 1}, {0x25c6, 1}, {0xfffd, 1}, {0xe000, 1}, {0xf0000, 1},
		{-1, 1}, {0x110000, 1},
	}
	for _, c := range cases {
		if got := RuneWidth(c.r); got != c.want {
			t.Errorf("RuneWidth(%U) = %d, want %d", c.r, got, c.want)
		}
	}
}

func TestWidthTablesSorted(t *testing.T) {
	for name, tab := range map[string][]runeRange{"wide": wideRanges, "zeroWidthExtra": zeroWidthExtra} {
		for i, r := range tab {
			if r.lo > r.hi {
				t.Errorf("%s[%d] = %X..%X is inverted", name, i, r.lo, r.hi)
			}
			if i > 0 && tab[i-1].hi >= r.lo {
				t.Errorf("%s[%d] = %X..%X overlaps or precedes %X..%X", name, i, r.lo, r.hi, tab[i-1].lo, tab[i-1].hi)
			}
		}
	}
	// wideRanges is EastAsianWidth W/F verbatim, so it contains a few
	// nonspacing marks (the combining kana voicing marks U+3099/309A, the
	// ideographic tone marks U+302A–302D, KHITAN SMALL SCRIPT FILLER U+16FE4).
	// As in wcwidth, zero width takes precedence for those; any other overlap
	// with the zero-width classes would be a table error.
	for _, r := range wideRanges {
		for c := r.lo; c <= r.hi; c++ {
			if !isZeroWidth(c) {
				continue
			}
			if !unicode.Is(unicode.Mn, c) {
				t.Errorf("%U is in a wide range and zero-width without being a nonspacing mark", c)
			}
			if RuneWidth(c) != 0 {
				t.Errorf("RuneWidth(%U) = %d, want 0 (nonspacing mark wins)", c, RuneWidth(c))
			}
		}
	}
}
