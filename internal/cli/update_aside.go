package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// uv's Windows tool layout, which update supports alongside the Unix one:
//
//	<tool-root>\pairmux\uv-receipt.toml     receipt, install-path in forward slashes
//	<tool-root>\pairmux\Scripts\pairmux.exe the wheel's script, the tool's binary
//	<bin-dir>\pairmux.exe                   a byte copy of it (fs_err::copy), on PATH
//
// `uv tool list --show-paths` prints both paths natively (backslashes) and names
// the entrypoint after its file, pairmux.exe. Ownership is the Unix proof with
// readlink replaced by content: the listing (uv's reading of the receipt)
// records exactly this entrypoint for exactly this environment, and that file
// is a regular non-link byte copy of the environment's executable.
//
// The layout is chosen by updateGOOS, so all of this runs in tests on any
// platform; path syntax, exec bits and file locking follow the real platform.
type updateLayout struct {
	windows bool
	name    string // pairmux's file name in the environment and bin directory
	scripts string // the environment's executable directory
}

func currentUpdateLayout() updateLayout {
	if updateGOOS == "windows" {
		return updateLayout{windows: true, name: "pairmux.exe", scripts: "Scripts"}
	}
	return updateLayout{name: "pairmux", scripts: "bin"}
}

const updateWindowsDiscoveryHint = "uv's default tool directory does not record this pairmux.exe. If it was installed with a custom UV_TOOL_DIR, run update through that tool environment's pairmux\\Scripts\\pairmux.exe; otherwise use the installation's existing package manager."

// discoverUpdateWindowsBinary finds the tool environment for a copied Windows
// entrypoint from uv's own listing of its default tool directory (env carries
// no UV_TOOL_DIR). The result is only a candidate: locateUpdateInstallation and
// inspect() verify it exactly as for a binary-derived environment.
func discoverUpdateWindowsBinary(uv string, env []string, run func(string, []string, []string) (updateRunResult, error)) (string, error) {
	listing, err := runUpdateListing(uv, env, run)
	if err != nil {
		return "", err
	}
	listed, _, err := parseUpdateListingIn(listing, "")
	if err != nil {
		return "", err
	}
	layout := currentUpdateLayout()
	return filepath.Join(listed, layout.scripts, layout.name), nil
}

// verifyUpdateCopiedEntrypoint is the Windows counterpart of
// verifyUpdateEntrypoint: the recorded entrypoint must be a regular file, not
// a link and not the environment's own executable, with identical bytes.
func verifyUpdateCopiedEntrypoint(entrypoint, binary string) (os.FileInfo, error) {
	info, err := updateLstat(entrypoint)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the recorded pairmux entrypoint is missing or is not a regular file; refusing a manual replacement")
	}
	if resolved, err := filepath.EvalSymlinks(entrypoint); err != nil || resolved == binary {
		return nil, fmt.Errorf("the recorded pairmux entrypoint is not a separate copy of the tool environment's executable")
	}
	same, err := sameUpdateContent(entrypoint, info, binary)
	if err != nil {
		return nil, fmt.Errorf("compare the recorded pairmux entrypoint with the tool environment's executable: %w", err)
	}
	if !same {
		return nil, fmt.Errorf("the recorded pairmux entrypoint is not a copy of the tool environment's executable; refusing a manual replacement")
	}
	return info, nil
}

func sameUpdateContent(path string, info os.FileInfo, other string) (bool, error) {
	a, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer a.Close()
	opened, err := a.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, fmt.Errorf("%s changed while opening it", filepath.Base(path))
	}
	b, err := os.Open(other)
	if err != nil {
		return false, err
	}
	defer b.Close()
	otherInfo, err := b.Stat()
	if err != nil || !otherInfo.Mode().IsRegular() {
		return false, fmt.Errorf("the tool environment's executable is not a regular file")
	}
	if opened.Size() != otherInfo.Size() {
		return false, nil
	}
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		n, errA := io.ReadFull(a, bufA)
		m, errB := io.ReadFull(b, bufB)
		if n != m || !bytes.Equal(bufA[:n], bufB[:m]) {
			return false, nil
		}
		endA := errA == io.EOF || errA == io.ErrUnexpectedEOF
		endB := errB == io.EOF || errB == io.ErrUnexpectedEOF
		if errA != nil && !endA {
			return false, errA
		}
		if errB != nil && !endB {
			return false, errB
		}
		if endA || endB {
			return endA && endB, nil
		}
	}
}

// updateFS holds the operations that move executables aside on Windows, so
// tests can inject failures. rename must refuse to replace an existing file.
type updateFS struct {
	rename  func(from, to string) error
	remove  func(string) error
	readDir func(string) ([]os.DirEntry, error)
}

func (f updateFS) orDefault() updateFS {
	if f.rename == nil {
		f.rename = updateRenameNoReplace
	}
	if f.remove == nil {
		f.remove = os.Remove
	}
	if f.readDir == nil {
		f.readDir = os.ReadDir
	}
	return f
}

// Windows refuses to overwrite or delete a mapped executable image, so uv
// could neither replace bin\pairmux.exe while this process (or a daemon started
// from it) runs nor delete the environment's Scripts\pairmux.exe if something
// runs that one. Renaming a running image is allowed within its volume. Both
// files are therefore moved aside before uv runs:
//
//	<bin-dir>\pairmux.exe                -> <bin-dir>\pairmux.exe.old-<pid>
//	<tool-root>\pairmux\Scripts\pairmux.exe -> <tool-root>\pairmux.exe.old-<pid>
//
// the second one outside the environment directory, which uv may delete and
// recreate, but on the same volume. uv only lists directories in the tool
// root and only ever writes pairmux.exe, so it ignores both. If uv fails they
// are renamed back (never over a file uv created). A later update deletes
// stale ones that are no longer running.
//
// This is preferred over a detached helper that waits for this process to
// exit and then runs uv: the result (and any failure) is reported in this
// command's envelope, nothing outlives the command, a running daemon that
// would still lock the file does not matter, and the user cannot race the
// helper by starting pairmux again before it finishes.
type updateAside struct {
	fs    updateFS
	moves []updateAsideMove
}

type updateAsideMove struct{ original, aside string }

func updateAsideName(layout updateLayout, pid int) string {
	return layout.name + ".old-" + strconv.Itoa(pid)
}

func moveUpdateAside(files updateFS, installation *updateInstallation, pid int) (*updateAside, error) {
	a := &updateAside{fs: files}
	name := updateAsideName(installation.layout, pid)
	for _, m := range []updateAsideMove{
		{installation.entrypoint, filepath.Join(filepath.Dir(installation.entrypoint), name)},
		{installation.binary, filepath.Join(installation.toolRoot, name)},
	} {
		if err := files.rename(m.original, m.aside); err != nil {
			return nil, fmt.Errorf("rename %s to %s: %v%s", m.original, m.aside, err, a.restore())
		}
		a.moves = append(a.moves, m)
	}
	return a, nil
}

// restore renames moved executables back after uv failed, and describes the
// outcome for the error message. It never replaces a file uv left behind.
func (a *updateAside) restore() string {
	if a == nil || len(a.moves) == 0 {
		return ""
	}
	var kept []string
	for i := len(a.moves) - 1; i >= 0; i-- {
		m := a.moves[i]
		if _, err := os.Lstat(m.original); err == nil {
			kept = append(kept, fmt.Sprintf("uv left a new %s; the previous executable is kept at %s", m.original, m.aside))
		} else if err := a.fs.rename(m.aside, m.original); err != nil {
			kept = append(kept, fmt.Sprintf("could not restore %s (%v); the previous executable is kept at %s", m.original, err, m.aside))
		}
	}
	a.moves = nil
	if len(kept) == 0 {
		return "; the previous executables were moved back into place"
	}
	return "; " + strings.Join(kept, "; ")
}

func (a *updateAside) kept() string {
	if a == nil || len(a.moves) == 0 {
		return ""
	}
	paths := make([]string, len(a.moves))
	for i, m := range a.moves {
		paths[i] = m.aside
	}
	return "; the previous executables are kept at " + strings.Join(paths, ", ")
}

// discard deletes the moved-aside files on success. A file that is still
// running (at least this process's) cannot be deleted; cleanUpdateAside
// removes it during a later update.
func (a *updateAside) discard() {
	if a == nil {
		return
	}
	for _, m := range a.moves {
		_ = a.fs.remove(m.aside)
	}
	a.moves = nil
}

// cleanUpdateAside removes stale <name>.old-<pid> regular files left by
// earlier updates, only in the verified bin directory and tool root. Files
// that are still running fail to delete and are silently kept.
func cleanUpdateAside(files updateFS, layout updateLayout, dirs ...string) {
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(layout.name) + `\.old-[1-9][0-9]*$`)
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := files.readDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !pattern.MatchString(entry.Name()) || !entry.Type().IsRegular() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := files.remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				continue // Still running: a later update retries.
			}
		}
	}
}
