//go:build windows

package cli

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// updateRenameNoReplace renames with MoveFileW, which (unlike os.Rename's
// MOVEFILE_REPLACE_EXISTING) fails when the destination exists, and which
// Windows permits for an executable image that is currently running.
//
// A scanner (Defender, an indexer) may briefly hold a freshly written
// executable open without FILE_SHARE_DELETE; such sharing failures are retried
// for about a second. An existing destination is never retried.
func updateRenameNoReplace(from, to string) error {
	fromp, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	top, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	for attempt := 1; ; attempt++ {
		err = syscall.MoveFile(fromp, top)
		if err == nil {
			return nil
		}
		if attempt == 8 || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32)) { // ERROR_SHARING_VIOLATION
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
		}
		time.Sleep(time.Duration(attempt) * 30 * time.Millisecond)
	}
}
