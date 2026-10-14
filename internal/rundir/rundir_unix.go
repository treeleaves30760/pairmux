//go:build unix

package rundir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func envRoot() string {
	if root := os.Getenv("PAIRMUX_TMPDIR"); root != "" {
		return root
	}
	return os.Getenv("TMUX_TMPDIR")
}

func dirUnder(root string) (string, error) {
	if root == "" {
		root = "/tmp"
	}
	return filepath.Join(canonical(root), "pairmux-"+strconv.Itoa(os.Getuid())), nil
}

// Ensure creates the runtime directory if needed and refuses one that another
// user could reach: a socket there accepts keystrokes into every terminal.
func Ensure() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create runtime directory %s: %w (set PAIRMUX_TMPDIR to a writable directory)", d, err)
	}
	return d, check(d)
}

// Verify returns the runtime directory without creating it, after the same
// checks Ensure makes; a missing directory yields an error wrapping
// os.ErrNotExist (no server can be running). Clients call it before every
// connection: like tmux, they must not trust a socket in a directory another
// user prepared, which could otherwise receive their environment and keys.
func Verify() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return d, check(d)
}

func check(d string) error {
	fi, err := os.Lstat(d)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("runtime directory %s: %w", d, os.ErrNotExist)
		}
		return fmt.Errorf("runtime directory %s: %w", d, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("runtime directory %s is not a directory", d)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("runtime directory %s is owned by uid %d, not %d", d, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("runtime directory %s has unsafe permissions %o (want 0700)", d, fi.Mode().Perm())
	}
	return nil
}
