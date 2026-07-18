//go:build unix

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ErrNameReserved means another pairmux process is currently creating the
// same terminal name in this state namespace.
var ErrNameReserved = errors.New("state: terminal name is being created")

// AcquireNameReservation serializes terminal creation by name across
// processes. The lock lives outside the terminal directory because cmdNew may
// archive and replace that directory; locking a file inside it would change
// inodes during rename and let a second creator slip through.
//
// Lock files intentionally remain after release. Removing a flock file can
// split contenders across old and newly-created inodes.
func AcquireNameReservation(namespace, name string) (release func(), err error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	lockDir := filepath.Join(namespace, ".locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("state: create reservation dir: %w", err)
	}
	_ = os.Chmod(lockDir, 0o700)

	f, err := os.OpenFile(filepath.Join(lockDir, name+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state: open name reservation: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrNameReserved
		}
		return nil, fmt.Errorf("state: lock name reservation: %w", err)
	}

	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	}
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
