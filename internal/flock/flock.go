// Package flock is the advisory file lock pairmux's journals and registries
// share across processes: flock(2) on Unix, LockFileEx on Windows. Both are
// tied to one open file (description/handle), so a second open of the same
// file in the same process still conflicts, and both vanish with the process
// that held them, so a crashed holder never wedges a terminal.
package flock

import "errors"

// ErrWouldBlock reports that another open file holds a conflicting lock.
var ErrWouldBlock = errors.New("flock: lock held by another open file")
