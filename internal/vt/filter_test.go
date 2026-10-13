package vt

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

func TestFilterQueries(t *testing.T) {
	cases := []struct {
		name       string
		setup      string
		in         string
		fwd, reply string
	}{
		{"DSR", "", "a\x1b[5nb", "ab", "\x1b[0n"},
		{"CPR", "", "\x1b[3;7H\x1b[6n", "\x1b[3;7H", "\x1b[3;7R"},
		{"CPR at pending wrap", "", "\x1b[1;80Hx\x1b[6n", "\x1b[1;80Hx", "\x1b[1;80R"},
		{"CPR origin mode", "\x1b[5;10r\x1b[?6h", "\x1b[2;3H\x1b[6n", "\x1b[2;3H", "\x1b[2;3R"},
		{"DECXCPR", "", "\x1b[4;2H\x1b[?6n", "\x1b[4;2H", "\x1b[?4;2R"},
		{"other DSR removed", "", "\x1b[15n\x1b[?996n\x1b[?15n", "", ""},
		{"XTMODKEYS reset passes", "", "\x1b[>4n", "\x1b[>4n", ""},
		{"DA1", "", "\x1b[c\x1b[0c", "", "\x1b[?1;2c\x1b[?1;2c"},
		{"DECID", "", "\x1bZ", "", "\x1b[?1;2c"},
		{"DA2", "", "\x1b[>c\x1b[>0c", "", "\x1b[>0;10;1c\x1b[>0;10;1c"},
		{"DA3", "", "\x1b[=c\x1b[=0c", "", ""},
		{"XTVERSION", "", "\x1b[>q\x1b[>0q", "", "\x1bP>|pairmux\x1b\\\x1bP>|pairmux\x1b\\"},
		{"DECSCUSR passes", "", "\x1b[2 q", "\x1b[2 q", ""},
		{"DECRQM set", "\x1b[?2004h", "\x1b[?2004$p", "", "\x1b[?2004;1$y"},
		{"DECRQM reset", "", "\x1b[?1$p\x1b[?25$p", "", "\x1b[?1;2$y\x1b[?25;1$y"},
		{"DECRQM unknown", "", "\x1b[?2026$p", "", "\x1b[?2026;0$y"},
		{"DECRQM alt", "\x1b[?1049h", "\x1b[?1049$p\x1b[?47$p", "", "\x1b[?1049;1$y\x1b[?47;1$y"},
		{"DECRQM mouse", "\x1b[?1002h\x1b[?1006h", "\x1b[?1002$p\x1b[?1000$p\x1b[?1006$p", "", "\x1b[?1002;1$y\x1b[?1000;2$y\x1b[?1006;1$y"},
		{"DECRQM ANSI", "\x1b[4h", "\x1b[4$p\x1b[20$p", "", "\x1b[4;1$y\x1b[20;0$y"},
		{"window reports", "", "\x1b[18t\x1b[14t\x1b[16t\x1b[19t\x1b[11t\x1b[13t\x1b[15t", "",
			"\x1b[8;24;80t\x1b[4;384;640t\x1b[6;16;8t\x1b[9;24;80t\x1b[1t\x1b[3;0;0t\x1b[5;384;640t"},
		{"window ops removed", "", "\x1b[8;50;100t\x1b[3;10;10t\x1b[2t\x1b[9;1t\x1b[21t\x1b[20t\x1b[30t", "", ""},
		{"title stack passes", "", "\x1b[22;0t\x1b[23;0t", "\x1b[22;0t\x1b[23;0t", ""},
		{"XTSMTITLE passes", "", "\x1b[>2t", "\x1b[>2t", ""},
		{"OSC 10 BEL", "", "\x1b]10;?\x07", "", "\x1b]10;rgb:c0c0/c0c0/c0c0\x07"},
		{"OSC 11 ST", "", "\x1b]11;?\x1b\\", "", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
		{"OSC 12", "", "\x1b]12;?\x07", "", "\x1b]12;rgb:c0c0/c0c0/c0c0\x07"},
		{"OSC 10 chained", "", "\x1b]10;?;?\x07", "", "\x1b]10;rgb:c0c0/c0c0/c0c0\x07\x1b]11;rgb:0000/0000/0000\x07"},
		{"OSC 17 query unanswered", "", "\x1b]17;?\x07", "", ""},
		{"OSC 10 set passes", "", "\x1b]10;#ffffff\x07", "\x1b]10;#ffffff\x07", ""},
		{"OSC 4 query", "", "\x1b]4;1;?\x07", "", ""},
		{"OSC 4 set passes", "", "\x1b]4;1;rgb:ff/00/00\x1b\\", "\x1b]4;1;rgb:ff/00/00\x1b\\", ""},
		{"OSC 52 query", "", "\x1b]52;c;?\x07", "", ""},
		{"OSC 52 write passes", "", "\x1b]52;c;aGk=\x07", "\x1b]52;c;aGk=\x07", ""},
		{"OSC 133 passes", "", "\x1b]133;A\x07$ \x1b]133;B\x07", "\x1b]133;A\x07$ \x1b]133;B\x07", ""},
		{"OSC 8 passes", "", "\x1b]8;;http://x\x1b\\l\x1b]8;;\x1b\\", "\x1b]8;;http://x\x1b\\l\x1b]8;;\x1b\\", ""},
		{"DECRQSS", "", "\x1bP$qm\x1b\\", "", "\x1bP0$r\x1b\\"},
		{"XTGETTCAP", "", "\x1bP+q544e\x1b\\", "", "\x1bP0+r\x1b\\"},
		{"sixel passes", "", "\x1bPq#0~-\x1b\\", "\x1bPq#0~-\x1b\\", ""},
		{"ENQ", "", "a\x05b", "ab", ""},
		{"kitty keyboard", "", "\x1b[?u\x1b[>1u\x1b[<u\x1b[<2u\x1b[=1;1u", "", ""},
		{"restore cursor passes", "", "\x1b[s\x1b[u", "\x1b[s\x1b[u", ""},
		{"win32 input mode", "", "\x1b[?9001h\x1b[?9001l", "", ""},
		{"DECCOLM", "", "\x1b[?3h\x1b[?3l", "", ""},
		{"DECCOLM mixed", "", "\x1b[?1;3;25l", "\x1b[?1;25l", ""},
		{"win32 mixed", "", "\x1b[?9001;2004h", "\x1b[?2004h", ""},
		{"other modes pass", "", "\x1b[?1h\x1b[?2004h\x1b[?1000;1006h\x1b[?2026h", "\x1b[?1h\x1b[?2004h\x1b[?1000;1006h\x1b[?2026h", ""},
		{"XTQMODKEYS", "", "\x1b[?4m", "", ""},
		{"XTSMGRAPHICS", "", "\x1b[?1;1S", "", ""},
		{"DECREQTPARM", "", "\x1b[x", "", ""},
		{"kitty graphics query", "", "\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\", "", ""},
		{"kitty graphics passes", "", "\x1b_Gi=31,a=T;AAAA\x1b\\", "\x1b_Gi=31,a=T;AAAA\x1b\\", ""},
		{"SGR passes", "", "\x1b[1;38:2::1:2:3mx\x1b[m", "\x1b[1;38:2::1:2:3mx\x1b[m", ""},
		{"malformed CSI passes", "", "\x1b[1?2h\x1b[1$!p", "\x1b[1?2h\x1b[1$!p", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(80, 24, 100)
			feedString(s, c.setup)
			fwd, reply := feedString(s, c.in)
			if fwd != c.fwd {
				t.Errorf("forward = %q, want %q", fwd, c.fwd)
			}
			if reply != c.reply {
				t.Errorf("reply = %q, want %q", reply, c.reply)
			}
		})
	}
}

// feedAll feeds chunks and returns the concatenated outputs.
func feedAll(s *Screen, chunks [][]byte) (fwd, reply []byte) {
	for _, c := range chunks {
		f, r := s.Feed(c)
		fwd = append(fwd, f...)
		reply = append(reply, r...)
	}
	return fwd, reply
}

func randomChunks(data []byte, rng *rand.Rand, maxLen int) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := min(1+rng.Intn(maxLen), len(data))
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func TestFilterSplitAcrossFeeds(t *testing.T) {
	inputs := []string{
		"pre\x1b[6npost\x1b]10;?\x1b\\x\x1bP$qm\x1b\\y\x1b[?1;9001hz中文\x1b[?1049hALT\x1b[?1049lend",
		"\x1b]0;title\x07\x1b[38;2;1;2;3m😀\x1b[c\x1b[>q\x05\x1b[?u\x1b]52;c;?\x07é\u0301",
		"\x1b[?47h\x1b[2J\x1b[H\x1b[18t\x1b[?47l\x1bc\x1b[?2004$p\x1b_Ga=q;\x1b\\",
	}
	for _, in := range inputs {
		whole := New(80, 24, 100)
		wantFwd, wantReply := whole.Feed([]byte(in))
		for _, maxLen := range []int{1, 2, 3, 7} {
			s := New(80, 24, 100)
			fwd, reply := feedAll(s, randomChunks([]byte(in), rand.New(rand.NewSource(int64(maxLen))), maxLen))
			if !bytes.Equal(fwd, wantFwd) {
				t.Errorf("chunk %d: forward\n got %q\nwant %q", maxLen, fwd, wantFwd)
			}
			if !bytes.Equal(reply, wantReply) {
				t.Errorf("chunk %d: reply\n got %q\nwant %q", maxLen, reply, wantReply)
			}
			if s.Capture(100) != whole.Capture(100) {
				t.Errorf("chunk %d: screen differs", maxLen)
			}
		}
	}
}

func TestFilterHoldsIncompleteSequences(t *testing.T) {
	s := New(80, 24, 100)
	steps := []struct{ in, fwd string }{
		{"a\x1b", "a"},
		{"[", ""},
		{"3", ""},
		{"1mX", "\x1b[31mX"},
		{"\xe4", ""},
		{"\xb8", ""},
		{"\xad!", "中!"},
		{"\x1b]0;ti", ""},
		{"tle\x1b", ""},
		{"\\Z", "\x1b]0;title\x1b\\Z"},
		{"\xf0\x9f", ""},
		{"x", "\xf0\x9fx"}, // invalid: the held bytes are forwarded as they came
	}
	for i, st := range steps {
		fwd, _ := feedString(s, st.in)
		if fwd != st.fwd {
			t.Fatalf("step %d (%q): forward = %q, want %q", i, st.in, fwd, st.fwd)
		}
	}
	if s.Title() != "title" {
		t.Errorf("title = %q", s.Title())
	}
	if got := screenLines(s)[0]; got != "aX中!Z��x" {
		t.Errorf("row 0 = %q", got)
	}
}

// TestPassthroughIdentity checks that a stream without filtered sequences is
// forwarded byte for byte however it is chunked.
func TestPassthroughIdentity(t *testing.T) {
	vocab := []string{
		"hello ", "中文", "😀", "e\u0301", "\r\n", "\n", "\r", "\t", "\b", "\x07", "\x00",
		"\x1b[31m", "\x1b[0m", "\x1b[38;2;1;2;3m", "\x1b[H", "\x1b[5;10H", "\x1b[2J", "\x1b[K", "\x1b[3L",
		"\x1b[2M", "\x1b[4@", "\x1b[2P", "\x1b[1;20r", "\x1b[r", "\x1bM", "\x1b7", "\x1b8", "\x1b(0", "\x1b(B",
		"\x0e", "\x0f", "\x1b[?1h", "\x1b[?25l", "\x1b[?2004h", "\x1b[?1000;1006h", "\x1b]0;t\x07",
		"\x1b]133;A\x1b\\", "\x1b]8;;u\x07", "\x1bPq#0~\x1b\\", "\x1b[?2026h", "\x1b[s", "\x1b[u",
		"\x1b[2 q", "\x1b[>4;1m", "\x1b[22;0t", "\xff", "\xe4\xb8", "\x7f", "\x1b[1;2;3;4;5;6;7m",
		"\x1b=", "\x1b>", "\x1b#8", "\x1bD", "\x1bE", "\x1bH", "\x1b[g", "\x1b[3g", "\x1b[?7l", "\x1b[?7h",
		"\x1b[4h", "\x1b[4l", "\x1b[S", "\x1b[T", "\x1b[3b", "\x1b[X", "\x1b[Z", "\x1b[I", "\x1b[?5W",
	}
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var b strings.Builder
		for i := 0; i < 200; i++ {
			b.WriteString(vocab[rng.Intn(len(vocab))])
		}
		in := []byte(b.String())
		s := New(1+rng.Intn(100), 1+rng.Intn(40), 50)
		fwd, reply := feedAll(s, randomChunks(in, rng, 1+rng.Intn(40)))
		// A trailing incomplete UTF-8 sequence is legitimately held back.
		if !bytes.Equal(fwd, in[:len(fwd)]) || len(in)-len(fwd) > 3 {
			t.Fatalf("seed %d: forward differs from input\n got %q\nwant %q", seed, fwd, in)
		}
		if len(reply) != 0 {
			t.Fatalf("seed %d: unexpected reply %q", seed, reply)
		}
	}
}

func TestAltScreenFiltering(t *testing.T) {
	render := func(setup string) string {
		s := New(20, 5, 100)
		feedString(s, setup)
		return string(s.Render())
	}
	cases := []struct {
		name, setup, in, fwd string
	}{
		{"enter", "a", "\x1b[?1049hb", "" + render("a\x1b[?1049h") + "b"},
		{"leave", "a\x1b[?1049hx", "\x1b[?1049lb", render("a\x1b[?1049hx\x1b[?1049l") + "b"},
		{"enter twice", "\x1b[?1049h", "\x1b[?1049h", ""},
		{"47", "a", "\x1b[?47h", render("a\x1b[?47h")},
		{"1047 leave", "a\x1b[?1047hb", "\x1b[?1047l", render("a\x1b[?1047hb\x1b[?1047l")},
		{"1047 leave when on main", "a", "\x1b[?1047l", ""},
		{"combined, switch first", "a", "\x1b[?1049;1h", render("a\x1b[?1049h") + "\x1b[?1h"},
		{"combined, switch last", "a", "\x1b[?1;1049h", "\x1b[?1h" + render("a\x1b[?1h\x1b[?1049h")},
		{"combined, middle", "a", "\x1b[?1;1049;2004h", "\x1b[?1h" + render("a\x1b[?1h\x1b[?1049h") + "\x1b[?2004h"},
		{"combined with win32", "a\x1b[?1049h", "\x1b[?9001;1049;25l", render("a\x1b[?1049h\x1b[?1049l") + "\x1b[?25l"},
		{"1049l without save homes", "a", "\x1b[?1049l", render("a\x1b[?1049l")},
		{"1049l restores cursor on main", "\x1b[3;3H\x1b[?1049h\x1b[?1049l\x1b[H", "\x1b[?1049l",
			render("\x1b[3;3H\x1b[?1049h\x1b[?1049l")},
		{"RIS", "a\x1b[?1h", "\x1bcz", render("a\x1bc") + "z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(20, 5, 100)
			feedString(s, c.setup)
			fwd, _ := feedString(s, c.in)
			if fwd != c.fwd {
				t.Errorf("forward\n got %q\nwant %q", fwd, c.fwd)
			}
		})
	}
}

func TestStringCap(t *testing.T) {
	big := strings.Repeat("x", maxString+100)
	s := New(80, 24, 100)
	feedString(s, "\x1b]2;old\x07")
	in := "\x1b]2;" + big + "\x07after"
	fwd, _ := feedAll(s, randomChunks([]byte(in), rand.New(rand.NewSource(1)), 5000))
	if string(fwd) != in {
		t.Fatalf("oversized OSC not passed through intact (%d vs %d bytes)", len(fwd), len(in))
	}
	if s.Title() != "old" {
		t.Errorf("oversized title applied")
	}
	if got := screenLines(s)[0]; got != "after" {
		t.Errorf("row 0 = %q", got)
	}

	// An oversized query is passed through rather than answered; the
	// terminator is still found when it arrives split, ESC then '\'.
	s = New(80, 24, 100)
	in = "\x1b]10;?" + big + "\x1b"
	fwd1, reply1 := s.Feed([]byte(in))
	fwd2, reply2 := s.Feed([]byte("\\z"))
	if got := string(fwd1) + string(fwd2); got != in+"\\z" {
		t.Fatalf("oversized OSC with split ST mangled")
	}
	if len(reply1)+len(reply2) != 0 {
		t.Errorf("oversized query answered")
	}
	if got := screenLines(s)[0]; got != "z" {
		t.Errorf("row 0 = %q", got)
	}

	// An oversized string aborted by a new escape sequence.
	s = New(80, 24, 100)
	in = "\x1bP" + big + "\x1b[31mq"
	fwd, _ = feedAll(s, randomChunks([]byte(in), rand.New(rand.NewSource(2)), 7000))
	if string(fwd) != in {
		t.Fatalf("aborted oversized DCS mangled")
	}
	if s.pen.fg != colorIndexed|1 || screenLines(s)[0] != "q" {
		t.Errorf("sequence after aborted string not applied")
	}
}

func TestStringTermination(t *testing.T) {
	cases := []struct{ name, in, title, row0, fwd string }{
		{"BEL", "\x1b]2;a\x07x", "a", "x", ""},
		{"ST", "\x1b]2;b\x1b\\x", "b", "x", ""},
		{"CAN aborts", "\x1b]2;c\x18x", "", "x", ""},
		{"ESC aborts", "\x1b]2;d\x1b[1mx", "", "x", ""},
		{"DCS ignores BEL", "\x1bPzz\x07x\x1b\\y", "", "y", ""},
		{"APC", "\x1b_hello\x1b\\y", "", "y", ""},
		{"PM SOS", "\x1b^p\x1b\\\x1bXs\x1b\\y", "", "y", ""},
		{"title controls stripped", "\x1b]0;a\x01b\xc2\x9bc\x1b\\", "abc", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(20, 2, 0)
			fwd, _ := feedString(s, c.in)
			if fwd != c.in {
				t.Errorf("forward = %q, want input", fwd)
			}
			if s.Title() != c.title {
				t.Errorf("title = %q, want %q", s.Title(), c.title)
			}
			if got := screenLines(s)[0]; got != c.row0 {
				t.Errorf("row 0 = %q, want %q", got, c.row0)
			}
		})
	}
}
