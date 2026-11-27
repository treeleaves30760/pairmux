//go:build unix

package cli

import (
	"errors"
	"syscall"
)

// processAlive reports whether process pid exists (EPERM: it does, as
// someone else's).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
