//go:build windows

package muxd

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/treeleaves30760/pairmux/internal/rundir"
)

const (
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// spawnServer starts `pairmux __muxd` detached from this console and, where
// allowed, from this process's job, so closing the terminal window or an agent
// harness tearing down its job does not take the server along.
//
// The server runs from a private copy of the executable. Windows refuses to
// replace a running .exe, so a long-lived server running the installed binary
// would make `pairmux update`, `uv tool upgrade` and `pip install -U` fail for
// as long as any terminal is open.
func spawnServer(socket string) error {
	exe, err := serverExecutable()
	if err != nil {
		return err
	}
	dir, err := rundir.Ensure()
	if err != nil {
		return err
	}
	if copyPath, err := privateCopy(exe, dir); err == nil {
		exe = copyPath
	}
	base, err := rundir.FileBase(socket)
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, base+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	start := func(flags uint32) error {
		cmd := exec.Command(exe, ServeCommand, "--socket", socket)
		cmd.Dir = dir
		cmd.Stdout = logf
		cmd.Stderr = logf
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	if err := start(detachedProcess | createNewProcessGroup | createBreakawayFromJob); err != nil {
		// A job that forbids breakaway rejects the flag outright; run inside it.
		return start(detachedProcess | createNewProcessGroup)
	}
	return nil
}

// privateCopy copies exe into dir under a content-addressed name, reusing an
// existing copy, and removes stale copies that are no longer running.
func privateCopy(exe, dir string) (string, error) {
	src, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer src.Close()
	h := sha256.New()
	if _, err := io.Copy(h, src); err != nil {
		return "", err
	}
	name := fmt.Sprintf("pairmux-muxd-%x.exe", h.Sum(nil)[:8])
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "pairmux-muxd-*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		if _, serr := os.Stat(dst); serr == nil {
			return dst, nil // a concurrent starter won the race
		}
		return "", err
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if n != name && strings.HasPrefix(n, "pairmux-muxd-") && strings.HasSuffix(n, ".exe") {
				_ = os.Remove(filepath.Join(dir, n)) // fails harmlessly while in use
			}
		}
	}
	return dst, nil
}
