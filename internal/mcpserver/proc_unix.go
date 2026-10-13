//go:build unix

package mcpserver

import "syscall"

// childSysProcAttr puts each pairmux child in its own process group so a
// cancelled call can terminate it together with anything it started. The
// built-in multiplexer server a child may spawn starts its own session, so it
// is never part of that group.
func childSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func terminateProcessGroup(pid int, signal syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, signal)
	}
}
