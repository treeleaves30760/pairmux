//go:build windows

package muxd

import "os"

// removeHeldLock closes the lock and then deletes the file. Windows cannot
// delete a file another process has open, so if a successor opened (and is
// about to lock) it in between, the delete simply fails and the file stays —
// and a successor that locked a file deleted after it was opened sees the
// path no longer names it and retries.
func removeHeldLock(f *os.File, path string) {
	_ = f.Close()
	_ = os.Remove(path)
}

// releaseLog closes stderr, the server log, so it can be deleted: Windows
// refuses to delete a file that is open. Nothing is logged after this.
func releaseLog() { _ = os.Stderr.Close() }
