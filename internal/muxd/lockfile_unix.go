//go:build unix

package muxd

import "os"

// removeHeldLock unlinks the lock file while it is still locked, so no other
// server can lock it between the unlink and this process's exit; a starter
// that opened it earlier sees, once it gets the lock, that the path no longer
// names that file and retries.
func removeHeldLock(_ *os.File, path string) { _ = os.Remove(path) }

// releaseLog is a no-op: Unix can unlink the log while it is open as stderr.
func releaseLog() {}
