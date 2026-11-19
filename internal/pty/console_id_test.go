package pty

import "testing"

func TestConsoleIDRoundTrip(t *testing.T) {
	id := FormatConsoleID(4242, 133_000_000_000_000_000)
	if id != "conpty:4242:133000000000000000" {
		t.Fatalf("FormatConsoleID = %q", id)
	}
	pid, created, ok := ParseConsoleID(id)
	if !ok || pid != 4242 || created != 133_000_000_000_000_000 {
		t.Fatalf("ParseConsoleID(%q) = %d, %d, %v", id, pid, created, ok)
	}
}

// TestParseConsoleIDRejects pins that nothing but a well-formed identifier is
// taken for one: in particular a Unix device path, which is what the same
// metadata field holds on every other platform, and an identifier without a
// creation time, which could name a reused pid.
func TestParseConsoleIDRejects(t *testing.T) {
	for _, id := range []string{
		"", "/dev/ttys004", "/dev/pts/3", "conpty:", "conpty:12", "conpty:12:",
		"conpty::5", "conpty:0:5", "conpty:12:0", "conpty:-1:5", "conpty:x:5",
		"conpty:4294967296:5", "conpty:12:5:6", "CONPTY:12:5",
	} {
		if pid, created, ok := ParseConsoleID(id); ok {
			t.Errorf("ParseConsoleID(%q) = %d, %d, true; want rejection", id, pid, created)
		}
	}
}
