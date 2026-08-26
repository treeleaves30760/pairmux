package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseProcStat pins the two fields readFgWait takes from Linux, and the
// reason it cannot simply split on spaces: field 2 is the executable name, it
// is not quoted or escaped, and this project itself produces one containing
// both a space and a parenthesis.
func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   procStat
		wantOK bool
	}{
		{
			"a plain name",
			"4242 (less) S 4200 4242 4200 34816 4242 4194304 91 0 0 0",
			procStat{State: 'S', TTYNr: 34816}, true,
		},
		{
			"a name with a space",
			"77 (tmux: server) S 1 77 77 0 -1 4194304 0 0 0 0",
			procStat{State: 'S', TTYNr: 0}, true,
		},
		{
			"a name with parentheses",
			"78 (weird (name)) R 1 78 78 34817 78 4194304 0 0 0 0",
			procStat{State: 'R', TTYNr: 34817}, true,
		},
		// A kernel thread has no controlling terminal, which is tty_nr 0 rather
		// than a parse failure. readFgWait excludes it by device instead: no
		// pane's terminal is device 0.
		{"no controlling terminal", "2 (kthreadd) S 0 0 0 0 -1 69238880 0 0 0 0", procStat{State: 'S', TTYNr: 0}, true},
		{"truncated", "4242 (less) S 4200", procStat{}, false},
		{"no comm field at all", "garbage without parens", procStat{}, false},
		{"empty", "", procStat{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcStat([]byte(tc.line))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("= %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestIsProcRunning pins that only R counts. A stopped job (T) is suspended, not
// computing, and must not veto the inference that its terminal is waiting.
func TestIsProcRunning(t *testing.T) {
	for state, want := range map[byte]bool{'R': true, 'S': false, 'D': false, 'T': false, 't': false, 'Z': false, 'I': false} {
		if got := isProcRunning(state); got != want {
			t.Errorf("isProcRunning(%q) = %v, want %v", state, got, want)
		}
	}
}

// TestReadFgWaitOnNonTerminals pins the degradation contract: anything that is
// not a live pane terminal answers FgUnknown, which leaves every caller behaving
// exactly as it did before this evidence existed.
func TestReadFgWaitOnNonTerminals(t *testing.T) {
	regular := filepath.Join(t.TempDir(), "not-a-tty")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", regular, filepath.Join(t.TempDir(), "missing")} {
		if got := ReadFgWait(path); got != FgUnknown {
			t.Fatalf("ReadFgWait(%q) = %v, want %v", path, got, FgUnknown)
		}
	}
}

func TestFgWaitString(t *testing.T) {
	for w, want := range map[FgWait]string{FgUnknown: "unknown", FgParked: "parked", FgWorking: "working"} {
		if got := w.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", w, got, want)
		}
	}
}
