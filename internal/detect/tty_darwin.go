//go:build darwin

package detect

import "syscall"

const ioctlReadTermios = syscall.TIOCGETA
