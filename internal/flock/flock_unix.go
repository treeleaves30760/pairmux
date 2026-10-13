//go:build unix

package flock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// TryLock takes a non-blocking shared or exclusive lock on f. A conflicting
// holder yields an error wrapping ErrWouldBlock.
func TryLock(f *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrWouldBlock
		}
		return fmt.Errorf("flock: %w", err)
	}
	return nil
}

// Unlock releases a lock taken with TryLock. Closing f releases it as well.
func Unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
