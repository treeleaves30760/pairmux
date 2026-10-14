package muxd

import "testing"

func TestKeyBytes(t *testing.T) {
	cases := []struct {
		name string
		app  bool
		want string
	}{
		{"Enter", false, "\r"},
		{"C-c", false, "\x03"},
		{"C-a", false, "\x01"},
		{"C-z", false, "\x1a"},
		{"M-x", false, "\x1bx"},
		{"Up", false, "\x1b[A"},
		{"Up", true, "\x1bOA"},
		{"Home", true, "\x1bOH"},
		{"End", false, "\x1b[F"},
		{"PPage", false, "\x1b[5~"},
		{"DC", false, "\x1b[3~"},
		{"BSpace", false, "\x7f"},
		{"F1", false, "\x1bOP"},
		{"F12", false, "\x1b[24~"},
		{"Escape", false, "\x1b"},
		{"Space", false, " "},
		{"Tab", false, "\t"},
	}
	for _, c := range cases {
		got, err := keyBytes(c.name, c.app)
		if err != nil || string(got) != c.want {
			t.Errorf("keyBytes(%q, %v) = %q, %v; want %q", c.name, c.app, got, err, c.want)
		}
	}
	if _, err := keyBytes("Nope", false); err == nil {
		t.Error("unknown key accepted")
	}
}
