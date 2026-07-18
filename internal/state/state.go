// Package state is the terminal registry: it composes the tmux client and the
// on-disk journal into a single view of which terminals exist, where their
// state lives, and whether their panes are still alive. It owns naming rules
// (validation, auto-naming) and the base state directory resolution.
package state

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

// ErrNotFound is returned by Resolve when neither a managed pane nor a state
// directory exists for the requested name.
var ErrNotFound = errors.New("state: terminal not found")

// ErrInvalidName is returned before a tmux or filesystem lookup when a caller
// asks Resolve for a name outside pairmux's terminal-name grammar.
var ErrInvalidName = errors.New("state: invalid terminal name")

// Terminal is the composed view of one pairmux terminal.
type Terminal struct {
	Name   string
	PaneID string
	Mode   core.Mode
	Dir    string
	Alive  bool
	Meta   core.Meta
}

// nameRe is the permitted terminal-name shape: a lowercase alphanumeric first
// character then up to 31 more of lowercase alphanumerics, underscore or dash.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// autoNameRe matches an auto-generated name ("t" followed by a positive number)
// so NextAutoName can find the current maximum.
var autoNameRe = regexp.MustCompile(`^t([0-9]+)$`)

// BaseDir resolves the state root: $PAIRMUX_STATE_DIR, else
// $XDG_STATE_HOME/pairmux, else ~/.local/state/pairmux. SocketDir derives the
// effective per-socket namespace below this root.
func BaseDir() string {
	if d := os.Getenv("PAIRMUX_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "pairmux")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "pairmux")
	}
	return filepath.Join(".local", "state", "pairmux")
}

// SocketDir returns the effective state namespace for socket below root.
//
// A tmux endpoint is not just its -L name: TMUX_TMPDIR and uid select the
// socket directory too. Hashing SocketIdentity under an invalid-terminal-name
// directory prevents traversal/length problems and isolates equal -L names in
// different tmux roots. ListAt and ResolveAt retain a conservative,
// non-migrating lookup fallback for the historical conventional default
// endpoint only; existing live terminals keep reading and writing their legacy
// journals, but files are never moved implicitly.
func SocketDir(root, socket string) string {
	return EndpointDir(root, os.Getenv("TMUX_TMPDIR"), os.Getuid(), socket)
}

// EndpointDir is SocketDir with every endpoint input explicit. It is the
// stable helper for code that locates another process's namespace rather than
// the current environment's namespace.
func EndpointDir(root, tmuxTmpDir string, uid int, socket string) string {
	sum := sha256.Sum256([]byte(EndpointIdentity(tmuxTmpDir, uid, socket)))
	return filepath.Join(root, ".sockets", fmt.Sprintf("%x", sum[:]))
}

// SocketIdentity returns the stable identity of the tmux endpoint targeted by
// -L socket in the current environment. TMUX_TMPDIR's real/absolute path is
// used when possible so /tmp and /private/tmp aliases do not split state.
func SocketIdentity(socket string) string {
	return EndpointIdentity(os.Getenv("TMUX_TMPDIR"), os.Getuid(), socket)
}

// EndpointIdentity is SocketIdentity with every endpoint input explicit.
func EndpointIdentity(tmuxTmpDir string, uid int, socket string) string {
	return filepath.Join(canonicalTmuxRoot(tmuxTmpDir), "tmux-"+strconv.Itoa(uid), normalizedSocket(socket))
}

// Dir is the state directory for a single default-socket terminal. New code
// that already has a tmux client should use ResolveAt/ListAt with BaseDir. It
// intentionally denotes the historical default layout for compatibility.
func Dir(name string) string { return filepath.Join(BaseDir(), name) }

// ValidName reports whether name is a legal terminal name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// NextAutoName returns the next "t<N>" name that does not collide with any
// existing auto name: one past the current maximum (it does not fill gaps).
func NextAutoName(existing []string) string {
	max := 0
	for _, n := range existing {
		if m := autoNameRe.FindStringSubmatch(n); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil && v > max {
				max = v
			}
		}
	}
	return "t" + strconv.Itoa(max+1)
}

// IsNoServer reports whether err is tmux's "no running server" condition, which
// pairmux treats as "zero managed panes" rather than a failure (a fresh machine
// has no server until the first `new`).
func IsNoServer(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "no server running") {
		return true
	}
	// tmux reports a missing or stale socket as "error connecting". Do not
	// collapse every connection error into an empty server: permission and
	// path-length failures need to reach the CLI with their recovery hint.
	return strings.Contains(s, "error connecting") &&
		(strings.Contains(s, "no such file or directory") || strings.Contains(s, "connection refused"))
}

// paneLister is the slice of tmux.Client that state needs; declaring it locally
// keeps List/Resolve unit-testable with a fake.
type paneLister interface {
	ListManaged() ([]tmux.PaneInfo, error)
}

// managedPanes lists managed panes, mapping tmux's no-server condition to an
// empty slice so callers on a fresh machine still succeed.
func managedPanes(c paneLister) ([]tmux.PaneInfo, error) {
	panes, err := c.ListManaged()
	if err != nil {
		if IsNoServer(err) {
			return nil, nil
		}
		return nil, err
	}
	return panes, nil
}

// Resolve returns the composed view of one terminal in c's socket namespace.
// It errors with ErrNotFound when neither a managed pane nor compatible state
// directory exists for name.
func Resolve(c *tmux.Client, name string) (*Terminal, error) {
	return ResolveAt(c, BaseDir(), name)
}

// ResolveAt is Resolve rooted at root. root is the un-namespaced state root;
// the client's socket selects the effective namespace.
func ResolveAt(c *tmux.Client, root, name string) (*Terminal, error) {
	return resolveAt(c, root, c.Socket, name)
}

func resolve(c paneLister, name string) (*Terminal, error) {
	return resolveAt(c, BaseDir(), core.DefaultSocket, name)
}

func resolveAt(c paneLister, root, socket, name string) (*Terminal, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	panes, err := managedPanes(c)
	if err != nil {
		return nil, err
	}
	var pane *tmux.PaneInfo
	for i := range panes {
		if panes[i].Name == name {
			pane = &panes[i]
			break
		}
	}

	dir, dirExists := stateDirForName(root, socket, name)
	if pane == nil && !dirExists {
		return nil, ErrNotFound
	}

	t := &Terminal{Name: name, Dir: dir}
	// meta.json is best effort: a pane may exist a moment before its meta is
	// written, and a resolve should still succeed.
	if meta, err := readMeta(dir); err == nil {
		t.Meta = meta
		t.Mode = meta.Mode
		t.PaneID = meta.PaneID
	}
	if pane != nil {
		t.PaneID = pane.PaneID
		t.Alive = !pane.Dead
	}
	return t, nil
}

// List returns the union of managed panes and compatible state directories in
// c's socket namespace. A directory with no live pane appears with Alive=false.
func List(c *tmux.Client) ([]Terminal, error) { return ListAt(c, BaseDir()) }

// ListAt is List rooted at root. root is the un-namespaced state root; the
// client's socket selects the effective namespace.
func ListAt(c *tmux.Client, root string) ([]Terminal, error) {
	return listAt(c, root, c.Socket)
}

func list(c paneLister) ([]Terminal, error) {
	return listAt(c, BaseDir(), core.DefaultSocket)
}

func listAt(c paneLister, root, socket string) ([]Terminal, error) {
	panes, err := managedPanes(c)
	if err != nil {
		return nil, err
	}

	byName := make(map[string]*Terminal)
	order := []string{}
	add := func(name string) *Terminal {
		if t, ok := byName[name]; ok {
			return t
		}
		dir, _ := stateDirForName(root, socket, name)
		t := &Terminal{Name: name, Dir: dir}
		if meta, err := readMeta(t.Dir); err == nil {
			t.Meta = meta
			t.Mode = meta.Mode
			t.PaneID = meta.PaneID
		}
		byName[name] = t
		order = append(order, name)
		return t
	}

	for i := range panes {
		if !ValidName(panes[i].Name) {
			continue
		}
		t := add(panes[i].Name)
		t.PaneID = panes[i].PaneID
		t.Alive = !panes[i].Dead
	}

	// Directories that carry a meta.json but have no live pane are dead
	// terminals whose journals are retained; include them (Alive stays false).
	for _, name := range stateDirsAt(SocketDir(root, socket), socket) {
		add(name)
	}
	// Existing releases stored the conventional default endpoint directly in
	// root. Keep that state readable, but never move it automatically. Legacy
	// custom-socket metadata lacks TMUX_TMPDIR/uid and therefore cannot be
	// attributed to an endpoint safely; it is deliberately not claimed.
	if usesLegacyDefaultFallback(socket) {
		for _, name := range stateDirsAt(root, socket) {
			add(name)
		}
	}

	sort.Strings(order)
	out := make([]Terminal, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out, nil
}

// stateDirs returns the names of terminal directories under BaseDir: entries
// that are directories, pass ValidName, and contain a meta.json (which excludes
// the shim dirs and archived "<name>.prev" directories).
func stateDirs() []string {
	return stateDirsAt(BaseDir(), core.DefaultSocket)
}

func stateDirsAt(base, socket string) []string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !ValidName(name) {
			continue
		}
		meta, err := readMeta(filepath.Join(base, name))
		if err != nil || !MetaMatchesSocket(meta, socket) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// MetaMatchesSocket reports whether metadata belongs to socket. Empty socket
// metadata is accepted only for the default socket for compatibility with the
// earliest state files, before Meta.Socket was populated.
func MetaMatchesSocket(meta core.Meta, socket string) bool {
	want := normalizedSocket(socket)
	if meta.Socket == "" {
		return want == core.DefaultSocket
	}
	return normalizedSocket(meta.Socket) == want
}

func normalizedSocket(socket string) string {
	if socket == "" {
		return core.DefaultSocket
	}
	return socket
}

func canonicalTmuxRoot(root string) string {
	if root == "" {
		root = "/tmp"
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	root = filepath.Clean(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return root
}

func effectiveTmuxRoot() string { return canonicalTmuxRoot(os.Getenv("TMUX_TMPDIR")) }

func usesLegacyDefaultFallback(socket string) bool {
	if normalizedSocket(socket) != core.DefaultSocket {
		return false
	}
	legacyRoot := "/tmp"
	if real, err := filepath.EvalSymlinks(legacyRoot); err == nil {
		legacyRoot = real
	}
	return effectiveTmuxRoot() == filepath.Clean(legacyRoot)
}

// stateDirForName returns the canonical directory, or the conventional
// default endpoint's legacy directory. A canonical directory with an explicit
// foreign-socket meta.json is not claimed.
func stateDirForName(root, socket, name string) (string, bool) {
	canonical := filepath.Join(SocketDir(root, socket), name)
	if meta, err := readMeta(canonical); err == nil {
		if MetaMatchesSocket(meta, socket) {
			return canonical, true
		}
		return canonical, false
	}
	if isDir(canonical) {
		return canonical, true // creation may be between mkdir and meta.json
	}

	if usesLegacyDefaultFallback(socket) {
		legacy := filepath.Join(root, name)
		if meta, err := readMeta(legacy); err == nil && MetaMatchesSocket(meta, socket) {
			return legacy, true
		}
	}
	return canonical, false
}

// readMeta reads meta.json without creating the directory (unlike journal.Open).
func readMeta(dir string) (core.Meta, error) {
	j := &journal.Journal{Dir: dir}
	return j.ReadMeta()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
