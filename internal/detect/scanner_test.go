package detect

import "testing"

// buildOSC assembles ESC ] <payload> <terminator>. term is "bel" (BEL) or
// "st" (ESC \).
func buildOSC(payload, term string) []byte {
	b := append([]byte{esc, ']'}, []byte(payload)...)
	switch term {
	case "bel":
		b = append(b, bel)
	case "st":
		b = append(b, esc, '\\')
	default:
		panic("bad term")
	}
	return b
}

func TestScannerRecognize(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		term     string
		wantKind MarkKind
		wantExit int
	}{
		{"A", "133;A", "bel", MarkA, -1},
		{"B", "133;B", "bel", MarkB, -1},
		{"C", "133;C", "bel", MarkC, -1},
		{"D no code", "133;D", "bel", MarkD, -1},
		{"D exit 0", "133;D;0", "bel", MarkD, 0},
		{"D exit 1", "133;D;1", "bel", MarkD, 1},
		{"D exit 42", "133;D;42", "bel", MarkD, 42},
		{"D via ST", "133;D;0", "st", MarkD, 0},
		{"A via ST", "133;A", "st", MarkA, -1},
		{"fish A extension via ST", "133;A;click_events=1", "st", MarkA, -1},
		{"fish C command line via ST", "133;C;cmdline_url=echo%20hi", "st", MarkC, -1},
		{"fish D exit via ST", "133;D;17", "st", MarkD, 17},
		{"sentinel 0", "7779;p;0", "bel", MarkSentinel, 0},
		{"sentinel 137", "7779;p;137", "bel", MarkSentinel, 137},
		{"sentinel via ST", "7779;p;2", "st", MarkSentinel, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := []byte("prefix output ")
			seq := buildOSC(tt.payload, tt.term)
			stream := append(append([]byte{}, prefix...), seq...)

			sc := NewScanner(0)
			marks := sc.Feed(stream)
			if len(marks) != 1 {
				t.Fatalf("got %d marks, want 1: %+v", len(marks), marks)
			}
			m := marks[0]
			if m.Kind != tt.wantKind || m.ExitCode != tt.wantExit {
				t.Fatalf("mark = %+v, want kind=%v exit=%d", m, tt.wantKind, tt.wantExit)
			}
			wantStart := int64(len(prefix))
			wantEnd := wantStart + int64(len(seq))
			if m.Start != wantStart || m.End != wantEnd {
				t.Fatalf("offsets = (%d,%d), want (%d,%d)", m.Start, m.End, wantStart, wantEnd)
			}
		})
	}
}

// TestScannerByteByByteD is the mandated split test: ESC ] 1 3 3 ; D ; 1 BEL
// fed one byte per Feed.
func TestScannerByteByByteD(t *testing.T) {
	seq := []byte{esc, ']', '1', '3', '3', ';', 'D', ';', '1', bel}
	sc := NewScanner(0)
	var marks []Mark
	for _, b := range seq {
		marks = append(marks, sc.Feed([]byte{b})...)
	}
	if len(marks) != 1 {
		t.Fatalf("got %d marks, want 1", len(marks))
	}
	m := marks[0]
	if m.Kind != MarkD || m.ExitCode != 1 {
		t.Fatalf("mark = %+v, want MarkD exit 1", m)
	}
	if m.Start != 0 || m.End != int64(len(seq)) {
		t.Fatalf("offsets = (%d,%d), want (0,%d)", m.Start, m.End, len(seq))
	}
}

// TestScannerByteByByteSentinelST is the mandated split test for the sentinel
// with the ST terminator: ESC ] 7 7 7 9 ; p ; 0 ESC \ fed one byte per Feed.
func TestScannerByteByByteSentinelST(t *testing.T) {
	seq := []byte{esc, ']', '7', '7', '7', '9', ';', 'p', ';', '0', esc, '\\'}
	sc := NewScanner(0)
	var marks []Mark
	for _, b := range seq {
		marks = append(marks, sc.Feed([]byte{b})...)
	}
	if len(marks) != 1 {
		t.Fatalf("got %d marks, want 1", len(marks))
	}
	m := marks[0]
	if m.Kind != MarkSentinel || m.ExitCode != 0 {
		t.Fatalf("mark = %+v, want MarkSentinel exit 0", m)
	}
	if m.Start != 0 || m.End != int64(len(seq)) {
		t.Fatalf("offsets = (%d,%d), want (0,%d)", m.Start, m.End, len(seq))
	}
}

// TestScannerChunkInvariance feeds a multi-mark stream at every possible chunk
// size; the recognized marks and their absolute offsets must be identical no
// matter where Feed boundaries fall (covers byte-by-byte and every split).
func TestScannerChunkInvariance(t *testing.T) {
	var stream []byte
	aStart := int64(len(stream))
	stream = append(stream, buildOSC("133;A", "bel")...)
	stream = append(stream, []byte("$ run seq 3\n")...)
	cStart := int64(len(stream))
	stream = append(stream, buildOSC("133;C", "bel")...)
	stream = append(stream, []byte("1\n2\n3\n")...)
	dStart := int64(len(stream))
	stream = append(stream, buildOSC("133;D;0", "st")...) // ST terminator here

	type want struct {
		kind  MarkKind
		exit  int
		start int64
	}
	wants := []want{
		{MarkA, -1, aStart},
		{MarkC, -1, cStart},
		{MarkD, 0, dStart},
	}

	for chunk := 1; chunk <= len(stream); chunk++ {
		sc := NewScanner(0)
		var got []Mark
		for i := 0; i < len(stream); i += chunk {
			end := i + chunk
			if end > len(stream) {
				end = len(stream)
			}
			got = append(got, sc.Feed(stream[i:end])...)
		}
		if len(got) != len(wants) {
			t.Fatalf("chunk=%d: got %d marks, want %d (%+v)", chunk, len(got), len(wants), got)
		}
		for k := range wants {
			if got[k].Kind != wants[k].kind || got[k].ExitCode != wants[k].exit || got[k].Start != wants[k].start {
				t.Fatalf("chunk=%d mark %d = %+v, want kind=%v exit=%d start=%d",
					chunk, k, got[k], wants[k].kind, wants[k].exit, wants[k].start)
			}
		}
	}
}

// TestScannerPayloadCapAbandonment verifies an unterminated OSC introducer does
// not swallow the rest of the stream: a later valid mark is still detected.
func TestScannerPayloadCapAbandonment(t *testing.T) {
	var buf []byte
	buf = append(buf, esc, ']')
	for i := 0; i < 200; i++ { // 200 payload bytes, no terminator
		buf = append(buf, 'x')
	}
	dStart := int64(len(buf))
	buf = append(buf, buildOSC("133;D;0", "bel")...)

	// Feed at both extremes: all-at-once and byte-by-byte.
	for _, name := range []string{"whole", "bytewise"} {
		t.Run(name, func(t *testing.T) {
			sc := NewScanner(0)
			var marks []Mark
			if name == "whole" {
				marks = sc.Feed(buf)
			} else {
				for _, b := range buf {
					marks = append(marks, sc.Feed([]byte{b})...)
				}
			}
			if len(marks) != 1 {
				t.Fatalf("got %d marks, want 1 (cap must not swallow stream): %+v", len(marks), marks)
			}
			if marks[0].Kind != MarkD || marks[0].ExitCode != 0 || marks[0].Start != dStart {
				t.Fatalf("mark = %+v, want MarkD exit 0 at %d", marks[0], dStart)
			}
		})
	}
}

// TestScannerEmbeddedRestart verifies that an ESC ] appearing inside an
// in-progress (unterminated) OSC restarts scanning at the inner introducer.
func TestScannerEmbeddedRestart(t *testing.T) {
	var buf []byte
	buf = append(buf, esc, ']')
	buf = append(buf, []byte("0;a window title that never terminates ")...)
	cStart := int64(len(buf))
	buf = append(buf, buildOSC("133;C", "bel")...)

	sc := NewScanner(0)
	marks := sc.Feed(buf)
	if len(marks) != 1 || marks[0].Kind != MarkC || marks[0].Start != cStart {
		t.Fatalf("marks = %+v, want single MarkC at %d", marks, cStart)
	}
}

func TestScannerIgnoresUnrecognized(t *testing.T) {
	tests := []struct {
		name   string
		stream []byte
	}{
		{"osc set title", buildOSC("0;my title", "bel")},
		{"osc 133 unknown subtype", buildOSC("133;Z", "bel")},
		{"osc 133 empty subtype", buildOSC("133;", "bel")},
		{"osc 133 subtype too long", buildOSC("133;DD", "bel")},
		{"osc 7779 wrong subtype", buildOSC("7779;q;0", "bel")},
		{"osc 7779 missing exit", buildOSC("7779;p", "bel")},
		{"csi sgr", []byte{esc, '[', '0', 'm'}},
		{"csi erase", []byte{esc, '[', '2', 'J'}},
		{"lone st", []byte{esc, '\\'}},
		{"bare bel", []byte{bel}},
		{"plain text", []byte("just regular output\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewScanner(0)
			if marks := sc.Feed(tt.stream); len(marks) != 0 {
				t.Fatalf("got marks %+v, want none", marks)
			}
		})
	}
}

func TestScannerBaseOffset(t *testing.T) {
	seq := buildOSC("133;D;7", "bel")
	const base = 1000
	sc := NewScanner(base)
	marks := sc.Feed(seq)
	if len(marks) != 1 {
		t.Fatalf("got %d marks, want 1", len(marks))
	}
	if marks[0].Start != base || marks[0].End != base+int64(len(seq)) {
		t.Fatalf("offsets = (%d,%d), want (%d,%d)", marks[0].Start, marks[0].End, base, base+int64(len(seq)))
	}
	if marks[0].ExitCode != 7 {
		t.Fatalf("exit = %d, want 7", marks[0].ExitCode)
	}
}

// TestScannerBackToBackMarks covers two marks with no bytes between them.
func TestScannerBackToBackMarks(t *testing.T) {
	var stream []byte
	d1 := int64(len(stream))
	stream = append(stream, buildOSC("133;D;0", "bel")...)
	a1 := int64(len(stream))
	stream = append(stream, buildOSC("133;A", "bel")...)

	sc := NewScanner(0)
	marks := sc.Feed(stream)
	if len(marks) != 2 {
		t.Fatalf("got %d marks, want 2: %+v", len(marks), marks)
	}
	if marks[0].Kind != MarkD || marks[0].Start != d1 {
		t.Fatalf("mark0 = %+v, want MarkD at %d", marks[0], d1)
	}
	if marks[1].Kind != MarkA || marks[1].Start != a1 {
		t.Fatalf("mark1 = %+v, want MarkA at %d", marks[1], a1)
	}
}

func TestMarkKindString(t *testing.T) {
	cases := map[MarkKind]string{
		MarkA: "A", MarkB: "B", MarkC: "C", MarkD: "D", MarkSentinel: "sentinel",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("MarkKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
