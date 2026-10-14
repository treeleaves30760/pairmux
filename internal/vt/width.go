package vt

import "unicode"

// runeRange is an inclusive range of code points. Tables of ranges are kept
// sorted and non-overlapping so membership is a binary search.
type runeRange struct{ lo, hi rune }

// wideRanges lists the code points whose East Asian Width property is W (wide)
// or F (fullwidth) in EastAsianWidth.txt, merged into maximal contiguous runs.
// The base is Unicode 15.0, extended with the Unicode 15.1/16.0 additions
// (new ideographic description characters and CJK strokes, the Yijing
// trigrams/digrams/hexagrams and Tai Xuan Jing symbols that 16.0 made wide,
// and the 16.0 emoji), which tmux 3.7 already treats as wide. These occupy two terminal cells. Ambiguous (A) characters
// are deliberately absent: like tmux, xterm and wcwidth() in non-CJK locales we
// treat them as narrow, because that is what the programs writing to the PTY
// assume when they position the cursor.
//
// A few entries contain nonspacing marks (U+302A–302D, U+3099–309A, U+16FE4)
// because EastAsianWidth.txt classifies them W; RuneWidth checks zero width
// first, so they are zero width, as in wcwidth.
//
// The emoji entries are the Emoji_Presentation=Yes code points, which
// EastAsianWidth.txt classifies W since Unicode 9; text-presentation symbols
// such as U+2764 HEAVY BLACK HEART stay narrow unless a VS16 follows (see
// Screen.combine).
var wideRanges = []runeRange{
	{0x1100, 0x115F}, // Hangul Jamo initial consonants (choseong)
	{0x231A, 0x231B}, {0x2329, 0x232A}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0},
	{0x23F3, 0x23F3}, {0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2630, 0x2637},
	{0x2648, 0x2653}, {0x267F, 0x267F}, {0x268A, 0x268F}, {0x2693, 0x2693},
	{0x26A1, 0x26A1}, {0x26AA, 0x26AB},
	{0x26BD, 0x26BE}, {0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4},
	{0x26EA, 0x26EA}, {0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA},
	{0x26FD, 0x26FD}, {0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728},
	{0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757},
	{0x2795, 0x2797}, {0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C},
	{0x2B50, 0x2B50}, {0x2B55, 0x2B55},
	{0x2E80, 0x2E99}, // CJK Radicals Supplement
	{0x2E9B, 0x2EF3},
	{0x2F00, 0x2FD5}, // Kangxi Radicals
	{0x2FF0, 0x2FFF}, // Ideographic Description Characters
	{0x3000, 0x303E}, // CJK Symbols and Punctuation (U+303F is narrow)
	{0x3041, 0x3096}, // Hiragana
	{0x3099, 0x30FF}, // Katakana (U+3099/309A are combining; checked first)
	{0x3105, 0x312F}, // Bopomofo
	{0x3131, 0x318E}, // Hangul Compatibility Jamo
	{0x3190, 0x31E5}, // Kanbun, Bopomofo Extended, CJK Strokes
	{0x31EF, 0x321E}, // CJK Strokes, Katakana Phonetic Extensions, Enclosed CJK
	{0x3220, 0x3247},
	{0x3250, 0xA48C}, // Enclosed CJK, Ext. A, Yijing hexagrams, CJK Unified Ideographs, Yi
	{0xA490, 0xA4C6}, // Yi Radicals
	{0xA960, 0xA97C}, // Hangul Jamo Extended-A
	{0xAC00, 0xD7A3}, // Hangul Syllables
	{0xF900, 0xFAFF}, // CJK Compatibility Ideographs
	{0xFE10, 0xFE19}, // Vertical Forms
	{0xFE30, 0xFE52}, // CJK Compatibility Forms, Small Form Variants
	{0xFE54, 0xFE66}, {0xFE68, 0xFE6B},
	{0xFF01, 0xFF60}, // Fullwidth Forms
	{0xFFE0, 0xFFE6},
	{0x16FE0, 0x16FE4}, {0x16FF0, 0x16FF1}, // Ideographic Symbols
	{0x17000, 0x187F7}, // Tangut
	{0x18800, 0x18CD5}, // Tangut Components, Khitan Small Script
	{0x18D00, 0x18D08},
	{0x1AFF0, 0x1AFF3}, {0x1AFF5, 0x1AFFB}, {0x1AFFD, 0x1AFFE}, // Kana Extended-B
	{0x1B000, 0x1B122}, // Kana Supplement, Kana Extended-A
	{0x1B132, 0x1B132}, {0x1B150, 0x1B152}, {0x1B155, 0x1B155},
	{0x1B164, 0x1B167}, {0x1B170, 0x1B2FB}, // Small Kana, Nushu
	{0x1D300, 0x1D356}, {0x1D360, 0x1D376}, // Tai Xuan Jing, Counting Rods
	{0x1F004, 0x1F004}, {0x1F0CF, 0x1F0CF}, {0x1F18E, 0x1F18E},
	{0x1F191, 0x1F19A}, {0x1F200, 0x1F202}, {0x1F210, 0x1F23B},
	{0x1F240, 0x1F248}, {0x1F250, 0x1F251}, {0x1F260, 0x1F265},
	{0x1F300, 0x1F320}, {0x1F32D, 0x1F335}, {0x1F337, 0x1F37C},
	{0x1F37E, 0x1F393}, {0x1F3A0, 0x1F3CA}, {0x1F3CF, 0x1F3D3},
	{0x1F3E0, 0x1F3F0}, {0x1F3F4, 0x1F3F4}, {0x1F3F8, 0x1F43E},
	{0x1F440, 0x1F440}, {0x1F442, 0x1F4FC}, {0x1F4FF, 0x1F53D},
	{0x1F54B, 0x1F54E}, {0x1F550, 0x1F567}, {0x1F57A, 0x1F57A},
	{0x1F595, 0x1F596}, {0x1F5A4, 0x1F5A4}, {0x1F5FB, 0x1F64F},
	{0x1F680, 0x1F6C5}, {0x1F6CC, 0x1F6CC}, {0x1F6D0, 0x1F6D2},
	{0x1F6D5, 0x1F6D7}, {0x1F6DC, 0x1F6DF}, {0x1F6EB, 0x1F6EC},
	{0x1F6F4, 0x1F6FC}, {0x1F7E0, 0x1F7EB}, {0x1F7F0, 0x1F7F0},
	{0x1F90C, 0x1F93A}, {0x1F93C, 0x1F945}, {0x1F947, 0x1F9FF},
	{0x1FA70, 0x1FA7C}, {0x1FA80, 0x1FA89}, {0x1FA8F, 0x1FAC6},
	{0x1FACE, 0x1FADC}, {0x1FADF, 0x1FAE9}, {0x1FAF0, 0x1FAF8},
	{0x20000, 0x2FFFD}, // CJK Unified Ideographs Extension B..F, Supplement
	{0x30000, 0x3FFFD}, // CJK Unified Ideographs Extension G..
}

// zeroWidthExtra lists zero-width code points that are not already covered by
// the general categories Mn (nonspacing mark), Me (enclosing mark) and Cf
// (format) which isZeroWidth consults through package unicode. The Hangul
// medial vowels (jungseong) and final consonants (jongseong) belong here: a
// conjoining jamo sequence renders as one syllable whose width the initial
// consonant (choseong, which is wide) already accounts for.
var zeroWidthExtra = []runeRange{
	{0x1160, 0x11FF}, // Hangul Jamo medial vowels and final consonants
	{0xD7B0, 0xD7FF}, // Hangul Jamo Extended-B (medials and finals)
}

func inRanges(tab []runeRange, r rune) bool {
	lo, hi := 0, len(tab)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		switch {
		case r < tab[m].lo:
			hi = m
		case r > tab[m].hi:
			lo = m + 1
		default:
			return true
		}
	}
	return false
}

// isZeroWidth reports whether r is a printable code point that occupies no
// cell of its own and is instead attached to the preceding cell: nonspacing
// and enclosing marks (Mn, Me), format characters (Cf) such as ZWJ (U+200D),
// ZWSP, the bidi controls, BOM and tag characters, variation selectors
// (U+FE00–FE0F, U+E0100–E01EF; both are Mn) and conjoining Hangul jamo
// medials/finals. Spacing marks (Mc) keep width 1, as in glibc's and xterm's
// wcwidth; tmux built with utf8proc treats them as zero width.
func isZeroWidth(r rune) bool {
	if r < 0x0300 {
		// The only Cf below U+0300 is U+00AD SOFT HYPHEN, which terminals
		// and wcwidth render as a visible, narrow hyphen.
		return false
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || inRanges(zeroWidthExtra, r) {
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// RuneWidth returns the number of terminal cells r occupies: 2 for East Asian
// Wide/Fullwidth characters (CJK, Hangul syllables, fullwidth forms and emoji
// with default emoji presentation), 0 for zero-width code points (combining
// marks, ZWJ, variation selectors, other format characters, conjoining Hangul
// medials/finals) and for C0/C1 control characters and DEL, and 1 for
// everything else, including East Asian Ambiguous characters and code points
// outside the Unicode range (which a decoder would show as U+FFFD).
func RuneWidth(r rune) int {
	switch {
	case r < 0:
		return 1
	case r < 0x20:
		return 0
	case r < 0x7F:
		return 1
	case r < 0xA0:
		return 0
	case r < 0x0300:
		return 1
	case r > unicode.MaxRune:
		return 1
	}
	if isZeroWidth(r) {
		return 0
	}
	if r >= 0x1100 && inRanges(wideRanges, r) {
		return 2
	}
	return 1
}

// isRegionalIndicator reports whether r is one of the 26 regional indicator
// symbols; a pair of them forms a flag emoji.
func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

const (
	zwj  = 0x200D // ZERO WIDTH JOINER
	vs16 = 0xFE0F // VARIATION SELECTOR-16 (emoji presentation)
)
