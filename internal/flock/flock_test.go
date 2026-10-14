package flock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockConflictsAcrossOpenFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := TryLock(a, true); err != nil {
		t.Fatalf("first exclusive lock: %v", err)
	}
	if err := TryLock(b, true); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("second exclusive lock = %v, want ErrWouldBlock", err)
	}
	if err := TryLock(b, false); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("shared lock under exclusive = %v, want ErrWouldBlock", err)
	}
	// The locked byte lies past the data, so a holder's recorded pid stays
	// readable by everyone (Windows range locks are mandatory).
	if _, err := a.WriteAt([]byte("1234"), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "1234" {
		t.Fatalf("read under lock = %q, %v", got, err)
	}
	if err := Unlock(a); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := TryLock(b, false); err != nil {
		t.Fatalf("shared lock after unlock: %v", err)
	}
	if err := TryLock(a, false); err != nil {
		t.Fatalf("second shared lock: %v", err)
	}
}
