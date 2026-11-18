//go:build windows

package cli

import (
	"os"
	"testing"
)

// NUL is a character device but not a console: on Windows it must not count
// as a terminal, or ls would open the TUI with its output going nowhere.
func TestIsTerminalRejectsNUL(t *testing.T) {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("NUL was taken for a console")
	}
}
