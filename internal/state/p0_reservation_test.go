//go:build unix

package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNameReservationMutualExclusionAndRelease(t *testing.T) {
	namespace := t.TempDir()
	release1, err := AcquireNameReservation(namespace, "build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireNameReservation(namespace, "build"); !errors.Is(err, ErrNameReserved) {
		t.Fatalf("second reservation err = %v, want ErrNameReserved", err)
	}
	release1()
	release1() // idempotent

	release2, err := AcquireNameReservation(namespace, "build")
	if err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
	release2()
	if _, err := os.Stat(filepath.Join(namespace, ".locks", "build.lock")); err != nil {
		t.Fatalf("stable lock inode missing: %v", err)
	}
}

func TestNameReservationRejectsTraversalBeforeFilesystem(t *testing.T) {
	namespace := t.TempDir()
	if _, err := AcquireNameReservation(namespace, "../escape"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("err = %v, want ErrInvalidName", err)
	}
	if _, err := os.Stat(filepath.Join(namespace, ".locks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid reservation created lock state: %v", err)
	}
}
