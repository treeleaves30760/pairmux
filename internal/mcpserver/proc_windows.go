//go:build windows

package mcpserver

import (
	"os"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// childSysProcAttr gives each pairmux child its own process group, so a
// console Ctrl-C aimed at the MCP client does not also hit in-flight children.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// terminateProcessGroup ends the child. Windows has no signal for a process
// group; a pairmux child starts nothing that must die with it (the built-in
// multiplexer server it may spawn is deliberately detached), so terminating the
// child itself is the whole job.
func terminateProcessGroup(pid int, _ syscall.Signal) {
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
		_ = p.Release()
	}
}
