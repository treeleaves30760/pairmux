//go:build unix

package muxd

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/treeleaves30760/pairmux/internal/rundir"
)

// spawnServer starts `pairmux __muxd` in a session of its own, so the
// terminal or agent harness that ran this command can hang up, or kill its
// process group, without taking the server and its terminals along.
func spawnServer(socket string) error {
	exe, err := serverExecutable()
	if err != nil {
		return err
	}
	dir, err := rundir.Ensure()
	if err != nil {
		return err
	}
	base, err := rundir.FileBase(socket)
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, base+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, ServeCommand, "--socket", socket)
	cmd.Dir = dir
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it if this process outlives it
	return nil
}
