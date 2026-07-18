package tmux

import (
	"errors"
	"strings"
	"testing"
)

func TestValidSocketName(t *testing.T) {
	for _, name := range []string{"pairmux", "build-01", "CI.sock_2", "a", "x.y"} {
		if !ValidSocketName(name) {
			t.Errorf("ValidSocketName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "../escape", "/absolute", "a/b", ".", "-leading", "has space", strings.Repeat("x", 65)} {
		if ValidSocketName(name) {
			t.Errorf("ValidSocketName(%q) = true", name)
		}
	}
}

func TestInvalidSocketNeverReachesRunner(t *testing.T) {
	r := &recRunner{}
	c := &Client{Socket: "../escape", runner: r.fn}
	if _, err := c.Version(); !errors.Is(err, ErrInvalidSocket) {
		t.Fatalf("Version err = %v, want ErrInvalidSocket", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("invalid socket reached runner: %v", r.calls)
	}
}
