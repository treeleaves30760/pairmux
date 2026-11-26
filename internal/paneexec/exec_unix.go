//go:build unix

package paneexec

import "syscall"

// execve replaces this process with path; it returns only on failure.
func execve(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }
