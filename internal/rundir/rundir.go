// Package rundir locates the built-in multiplexer's runtime directory: where
// each server's socket, single-instance lock and log live. It is a leaf so
// that the state registry can derive a built-in endpoint's identity without
// depending on the server itself.
//
// The layout deliberately mirrors tmux's: a per-user directory under a short
// temporary root, private to its owner, holding one socket per endpoint name.
// Unix domain socket paths are limited to about 104 bytes, which is why the
// root is /tmp rather than a deep per-user cache directory, and why
// PAIRMUX_TMPDIR (or TMUX_TMPDIR, which sandboxed users of the tmux backend
// may already have set for the same reason) can move it.
package rundir

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// maxSocketPath keeps socket paths inside every platform's sun_path (104
// bytes on darwin, 108 on Linux and Windows, both counting the NUL).
const maxSocketPath = 100

// Dir returns the runtime directory without creating it.
func Dir() (string, error) { return dir() }

// SocketPath returns the socket path for endpoint name socket. A name that
// would overflow the socket path limit is replaced by a stable digest of it.
func SocketPath(socket string) (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return socketPathIn(d, socket)
}

func socketPathIn(d, socket string) (string, error) {
	p := filepath.Join(d, socket+".sock")
	if len(p) <= maxSocketPath {
		return p, nil
	}
	p = filepath.Join(d, "s-"+digest(socket)+".sock")
	if len(p) <= maxSocketPath {
		return p, nil
	}
	return "", fmt.Errorf("built-in multiplexer runtime directory %s is too deep for a socket path (limit %d bytes); set PAIRMUX_TMPDIR to a short writable directory", d, maxSocketPath)
}

// FileBase returns the name stem used for socket/lock/log files of an
// endpoint, matching SocketPath's choice.
func FileBase(socket string) (string, error) {
	p, err := SocketPath(socket)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(filepath.Base(p), ".sock"), nil
}

// Identity names the built-in endpoint socket in the current environment, for
// state namespacing: equal endpoint names under different runtime roots are
// different servers and must not share terminal state.
func Identity(socket string) string { return IdentityUnder(envRoot(), socket) }

// IdentityUnder is Identity for an explicit runtime root (the value
// PAIRMUX_TMPDIR, or on Unix TMUX_TMPDIR, would hold; "" for the default), for
// code that locates another environment's namespace.
func IdentityUnder(root, socket string) string {
	d, err := dirUnder(root)
	if err != nil {
		d = "?"
	}
	return "builtin:" + filepath.Join(d, socket)
}

func dir() (string, error) { return dirUnder(envRoot()) }

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:8])
}

// canonical resolves symlinks and makes root absolute so /tmp and
// /private/tmp (or 8.3 short names) do not split one directory into two.
func canonical(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	root = filepath.Clean(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}
