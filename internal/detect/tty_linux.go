//go:build linux

package detect

import "syscall"

const ioctlReadTermios = syscall.TCGETS
