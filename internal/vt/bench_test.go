package vt

import (
	"fmt"
	"strings"
	"testing"
)

func benchStream(lines int, mixed bool) []byte {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		if mixed {
			fmt.Fprintf(&b, "\x1b[1;3%dm%5d\x1b[0m 中文測試 the quick brown fox 😀 jumps over the lazy dog \x1b[38;2;1;2;3m%d\x1b[m\r\n", i%8, i, i*7)
		} else {
			fmt.Fprintf(&b, "%5d the quick brown fox jumps over the lazy dog; lorem ipsum dolor sit amet\r\n", i)
		}
	}
	return []byte(b.String())
}

func benchFeed(b *testing.B, data []byte) {
	s := New(120, 40, 2000)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for off := 0; off < len(data); off += 4096 {
			s.Feed(data[off:min(off+4096, len(data))])
		}
	}
}

func BenchmarkFeedASCII(b *testing.B) { benchFeed(b, benchStream(5000, false)) }
func BenchmarkFeedMixed(b *testing.B) { benchFeed(b, benchStream(5000, true)) }

func BenchmarkCapture(b *testing.B) {
	s := New(120, 40, 2000)
	s.Feed(benchStream(3000, true))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Capture(2000)
	}
}

func BenchmarkRender(b *testing.B) {
	s := New(120, 40, 2000)
	s.Feed(benchStream(100, true))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Render()
	}
}
