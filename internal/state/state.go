// Package state is the terminal registry: it composes the tmux client and the
// on-disk journal into a single view of which terminals exist, where their
// state lives, and whether their panes are still alive. It owns naming rules
// (validation, auto-naming) and the base state directory resolution.
package state

import (
	"errors"
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
// $XDG_STATE_HOME/pairmux, else ~/.local/state/pairmux.
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

// Dir is the state directory for a single terminal.
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
	s := err.Error()
	return strings.Contains(s, "no server running") || strings.Contains(s, "error connecting")
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

// Resolve returns the composed view of one terminal. It errors with ErrNotFound
// when neither a managed pane nor a state directory exists for name.
func Resolve(c *tmux.Client, name string) (*Terminal, error) { return resolve(c, name) }

func resolve(c paneLister, name string) (*Terminal, error) {
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

	dir := Dir(name)
	dirExists := isDir(dir)
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

// List returns the union of managed panes and state directories. A directory
// with no live pane appears with Alive=false.
func List(c *tmux.Client) ([]Terminal, error) { return list(c) }

func list(c paneLister) ([]Terminal, error) {
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
		t := &Terminal{Name: name, Dir: Dir(name)}
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
		t := add(panes[i].Name)
		t.PaneID = panes[i].PaneID
		t.Alive = !panes[i].Dead
	}

	// Directories that carry a meta.json but have no live pane are dead
	// terminals whose journals are retained; include them (Alive stays false).
	for _, name := range stateDirs() {
		add(name)
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
	entries, err := os.ReadDir(BaseDir())
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
		if _, err := os.Stat(filepath.Join(BaseDir(), name, "meta.json")); err != nil {
			continue
		}
		names = append(names, name)
	}
	return names
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
