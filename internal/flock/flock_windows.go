//go:build windows

package flock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002

	errorLockViolation = syscall.Errno(33)
	errorIOPending     = syscall.Errno(997)
)

// lockOffsetHigh places the locked byte far beyond any content. Windows byte
// range locks are mandatory: a lock over the bytes a holder's pid is written
// to would make LockHolder's plain read fail for everyone else. Locking past
// EOF is allowed, so one byte at 2^62 serves as the lock without covering data.
const lockOffsetHigh = 0x40000000

// TryLock takes a non-blocking shared or exclusive lock on f. A conflicting
// holder yields an error wrapping ErrWouldBlock.
func TryLock(f *os.File, exclusive bool) error {
	flags := uint32(lockfileFailImmediately)
	if exclusive {
		flags |= lockfileExclusiveLock
	}
	ol := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
	r, _, err := procLockFileEx.Call(f.Fd(), uintptr(flags), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r != 0 {
		return nil
	}
	if errors.Is(err, errorLockViolation) || errors.Is(err, errorIOPending) {
		return ErrWouldBlock
	}
	return fmt.Errorf("flock: LockFileEx: %w", err)
}

// Unlock releases a lock taken with TryLock. Closing f releases it as well.
func Unlock(f *os.File) error {
	ol := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
	r, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		return fmt.Errorf("flock: UnlockFileEx: %w", err)
	}
	return nil
}
