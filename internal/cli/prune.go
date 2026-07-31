package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// cmdPrune removes retained journals whose terminals are gone: state
// directories of dead terminals plus ".prev" archives left by name reuse. It
// is the documented exit for kill's retain-the-journal default. A live
// terminal's journal is never touched (by name, only its ".prev" archive is
// reclaimed), and a directory whose write lock is held is skipped.
func (c *Ctx) cmdPrune(args []string) int {
	const usageLine = `pairmux prune [name] [--older-than 7d] [--dry-run]`
	var olderS string
	var dryRun bool
	pos, err := parseFlags(args, flagSpec{
		bools: map[string]*bool{"dry-run": &dryRun},
		vals:  map[string]*string{"older-than": &olderS},
	})
	if err != nil {
		return c.usage(usageLine, err.Error())
	}
	if len(pos) > 1 {
		return c.usage(usageLine, "unexpected argument "+pos[1])
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	var olderThan time.Duration // zero: no age filter
	if olderS != "" {
		d, err := parseOlderThan(olderS)
		if err != nil {
			return c.usage(usageLine, "bad --older-than: "+err.Error())
		}
		olderThan = d
	}
	if len(pos) == 1 {
		return c.pruneOne(pos[0], olderThan, dryRun)
	}
	return c.pruneSweep(olderThan, dryRun)
}

// pruneOne prunes a single terminal's retained state. A dead terminal loses
// its journal and any archive; a live terminal only its archive — pruning the
// journal under a live pane would break log/peek, so that asks for kill first.
func (c *Ctx) pruneOne(name string, olderThan time.Duration, dryRun bool) int {
	if rc, rejected := c.rejectInvalidTerminalName(name); rejected {
		return rc
	}
	term, err := state.ResolveAt(c.Tmux, c.StateDir, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return c.noTerminal(name)
		}
		return c.tmuxErr(err)
	}
	var cands []pruneCandidate
	if prev := term.Dir + ".prev"; isDir(prev) {
		cands = append(cands, pruneCandidate{name: name + ".prev", dir: prev})
	}
	if term.Alive && len(cands) == 0 {
		return c.fail(output.CodeBusy, fmt.Sprintf("terminal %q is alive", name),
			fmt.Sprintf("pairmux kill %s first, or run pairmux prune without a name to sweep dead terminals", name))
	}
	if !term.Alive && isDir(term.Dir) {
		cands = append(cands, pruneCandidate{name: name, dir: term.Dir})
	}
	return c.finishPrune(filterPruneCandidates(cands, olderThan), dryRun)
}

// pruneSweep prunes every dead terminal and every ".prev" archive in the
// current endpoint namespace, including archives whose base name no longer
// resolves (they are invisible to ls).
func (c *Ctx) pruneSweep(olderThan time.Duration, dryRun bool) int {
	terms, err := state.ListAt(c.Tmux, c.StateDir)
	if err != nil {
		return c.tmuxErr(err)
	}
	var cands []pruneCandidate
	seen := map[string]bool{}
	add := func(name, dir string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		cands = append(cands, pruneCandidate{name: name, dir: dir})
	}
	for _, t := range terms {
		if prev := t.Dir + ".prev"; isDir(prev) {
			add(t.Name+".prev", prev)
		}
		if !t.Alive && isDir(t.Dir) {
			add(t.Name, t.Dir)
		}
	}
	if ents, err := os.ReadDir(c.namespaceDir()); err == nil {
		for _, ent := range ents {
			if ent.IsDir() && strings.HasSuffix(ent.Name(), ".prev") {
				add(ent.Name(), filepath.Join(c.namespaceDir(), ent.Name()))
			}
		}
	}
	return c.finishPrune(filterPruneCandidates(cands, olderThan), dryRun)
}

// pruneCandidate is one directory prune considered. A non-empty skip is the
// reason it was (or would be) kept.
type pruneCandidate struct {
	name string
	dir  string
	size int64
	skip string
}

// filterPruneCandidates sizes each candidate and marks the ones that must be
// kept: a held write lock (another process may still be settling events), or
// activity newer than --older-than.
func filterPruneCandidates(cands []pruneCandidate, olderThan time.Duration) []pruneCandidate {
	out := make([]pruneCandidate, 0, len(cands))
	for _, cand := range cands {
		cand.size = dirSize(cand.dir)
		if pid := lockHolderPID(cand.dir); pid > 0 {
			cand.skip = fmt.Sprintf("write lock held by pid %d", pid)
		} else if pid < 0 {
			cand.skip = "write lock held"
		} else if olderThan > 0 {
			if at := pruneAge(cand.dir); !at.IsZero() && time.Since(at) < olderThan {
				cand.skip = "newer than --older-than"
			}
		}
		out = append(out, cand)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].name < out[k].name })
	return out
}

// pruneAge is a directory's last activity: raw.log's mtime when present, else
// the directory's own mtime. A zero time (unstattable) counts as old.
func pruneAge(dir string) time.Time {
	if mt, ok := (&journal.Journal{Dir: dir}).LastModified(); ok {
		return mt
	}
	if fi, err := os.Stat(dir); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// finishPrune removes the unskipped candidates (unless dry-run) and emits one
// line per candidate plus a freed-bytes summary. Remove failures downgrade the
// status to "issues" but never abort the sweep.
func (c *Ctx) finishPrune(cands []pruneCandidate, dryRun bool) int {
	if len(cands) == 0 {
		return c.emit(output.Envelope{Status: "ok", Output: "nothing to prune", Next: []string{"pairmux ls"}})
	}
	verb := "pruned"
	if dryRun {
		verb = "would prune"
	}
	var lines []string
	var freed int64
	pruned, failed := 0, false
	for i := range cands {
		cand := &cands[i]
		if cand.skip == "" && !dryRun {
			if err := os.RemoveAll(cand.dir); err != nil {
				cand.skip = "remove failed: " + err.Error()
				failed = true
			}
		}
		if cand.skip != "" {
			lines = append(lines, fmt.Sprintf("%s  %s  kept (%s)", cand.name, humanBytes(cand.size), cand.skip))
			continue
		}
		pruned++
		freed += cand.size
		lines = append(lines, fmt.Sprintf("%s  %s  %s", cand.name, humanBytes(cand.size), verb))
	}
	lines = append(lines, fmt.Sprintf("%s %d of %d, %s", verb, pruned, len(cands), humanBytes(freed)))
	status := "pruned"
	switch {
	case failed:
		status = "issues"
	case dryRun:
		status = "ok"
	}
	return c.emit(output.Envelope{Status: status, Output: strings.Join(lines, "\n"), Next: []string{"pairmux ls"}})
}

// dirSize sums the regular-file bytes under dir (best effort).
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// dayRe matches the "<n>d" day shorthand that time.ParseDuration rejects but
// humans and agents reach for naturally in --older-than.
var dayRe = regexp.MustCompile(`^([0-9]+)d$`)

// parseOlderThan parses a Go duration, plus whole-day shorthand ("7d").
func parseOlderThan(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if m := dayRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("bad day count %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// humanBytes renders n for the prune/doctor reports (binary units).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%dMB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
