//go:build unix

package cli

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

// execTmuxAttach replaces this process with a tmux client on the pairmux
// session; on success nothing after it runs and the human sees a full-screen
// tmux (so nothing is printed first).
func (c *Ctx) execTmuxAttach() int {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return c.fail(output.CodeTmux, "tmux not found on PATH: "+err.Error(), "install tmux >= 3.2, or use PAIRMUX_BACKEND=builtin")
	}
	argv := tmux.New(c.Mux.SocketName()).AttachArgv()
	if err := syscall.Exec(tmuxPath, argv, os.Environ()); err != nil {
		return c.fail(output.CodeTmux, "exec tmux attach: "+err.Error(), "pairmux ls")
	}
	return 0 // unreachable: a successful Exec has replaced this process
}
