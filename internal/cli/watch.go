package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/state"
)

// cmdWatch renders a live, self-refreshing dashboard of every terminal until the
// human interrupts it with Ctrl-C. It is a human-only view (stdout must be a
// tty); running it from inside tmux is fine, so — unlike attach — it does not
// refuse on $TMUX.
func (c *Ctx) cmdWatch(args []string) int {
	var intervalS string
	if _, err := parseFlags(args, flagSpec{vals: map[string]*string{"interval": &intervalS}}); err != nil {
		return c.usage("pairmux watch [--interval 2s]", err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}
	interval, err := parseInterval(intervalS)
	if err != nil {
		return c.usage("pairmux watch [--interval 2s]", "bad --interval: "+err.Error())
	}
	if !isTTY() {
		return c.fail(output.CodeBadArgs,
			"watch needs an interactive terminal, but stdout is not a tty",
			"run pairmux watch from an interactive shell")
	}

	// Catch SIGINT so our deferred cleanup runs and the terminal is left on a
	// clean line (the default SIGINT handler would exit before any defer).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	defer fmt.Fprintln(c.Stdout)

	for {
		c.renderWatchFrame()
		select {
		case <-sig:
			return 0
		case <-time.After(interval):
		}
	}
}

// parseInterval parses watch's --interval the same way run parses --timeout
// (Go durations like 2s / 500ms). An empty value defaults to 2s; a non-positive
// value is rejected.
func parseInterval(s string) (time.Duration, error) {
	if s == "" {
		return 2 * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("interval must be positive")
	}
	return d, nil
}

// watchRow is one dashboard line's derived data.
type watchRow struct {
	name   string
	status core.Status
	mode   string
	lock   int // holder pid; 0 = not held, -1 = held but pid unreadable
	cmd    string
	age    string
}

// probeLockHolder reports the pid actually holding a terminal's write lock, 0
// when it is free, -1 when it is held but the recorded pid is unreadable. It
// replicates commands.go's unexported lockHolderPID (file-ownership rules keep
// watch.go self-contained): journal.LockHolder alone would be a stale display,
// because the pid TEXT persists in write.lock after the flock is released, so
// heldness is probed with a non-blocking shared flock on a read-only descriptor
// (nothing is written; an instant success is released immediately).
func probeLockHolder(dir string) int {
	f, err := os.Open(filepath.Join(dir, "write.lock"))
	if err != nil {
		return 0 // never locked (or unreadable): free as far as we can tell
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return 0
	}
	if pid, ok := journal.LockHolder(dir); ok {
		return pid
	}
	return -1
}

// renderWatchFrame draws a single dashboard frame: it clears the screen, prints
// a header, and lists every terminal with its status, lock holder, pending
// command and last-activity age. It writes the whole frame in one call so the
// redraw does not flicker.
func (c *Ctx) renderWatchFrame() {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H") // clear screen + cursor home
	fmt.Fprintf(&b, "pairmux watch — %s — socket %s  (Ctrl-C to quit)\n\n",
		time.Now().Format("15:04:05"), c.Tmux.Socket)

	terms, err := state.ListAt(c.Tmux, c.StateDir)
	if err != nil {
		fmt.Fprintf(&b, "  error listing terminals: %v\n", err)
		fmt.Fprint(c.Stdout, b.String())
		return
	}
	if len(terms) == 0 {
		b.WriteString("  no terminals\n\n  next: pairmux new\n")
		fmt.Fprint(c.Stdout, b.String())
		return
	}

	rows := make([]watchRow, 0, len(terms))
	for _, t := range terms {
		j := &journal.Journal{Dir: t.Dir}
		st := detect.DeriveStatus(j, t.Alive, t.Mode)
		// Refine upgrades a quiet running command to awaiting-input when its last
		// line looks like an interactive prompt; the "!! " highlight keys off it.
		st, _ = detect.Refine(j, st, t.Mode)
		row := watchRow{name: t.Name, status: st, mode: string(t.Mode), lock: probeLockHolder(t.Dir)}
		if pending, ok, err := j.PendingCmd(); err == nil && ok {
			row.cmd = pending.Text
		}
		if mt, ok := j.LastModified(); ok {
			row.age = humanizeAge(time.Since(mt))
		}
		rows = append(rows, row)
	}
	writeWatchTable(&b, rows)
	fmt.Fprint(c.Stdout, b.String())
}

// writeWatchTable renders the terminal rows as an aligned table. Awaiting-input
// rows are flagged with "!! " and dead rows with "xx "; the leading three
// columns keep every row aligned regardless of flag.
func writeWatchTable(b *strings.Builder, rows []watchRow) {
	lockStr := func(r watchRow) string {
		if r.lock != 0 { // -1 renders literally: held, holder pid unreadable
			return strconv.Itoa(r.lock)
		}
		return "-"
	}
	nameW, statusW, modeW, lockW, ageW := len("NAME"), len("STATUS"), len("MODE"), len("LOCK"), len("AGE")
	for _, r := range rows {
		nameW = max(nameW, len(r.name))
		statusW = max(statusW, len(string(r.status)))
		modeW = max(modeW, len(r.mode))
		lockW = max(lockW, len(lockStr(r)))
		ageW = max(ageW, len(r.age))
	}

	fmt.Fprintf(b, "   %-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		nameW, "NAME", statusW, "STATUS", modeW, "MODE", lockW, "LOCK", ageW, "AGE", "CMD")
	for _, r := range rows {
		prefix := "   "
		switch r.status {
		case core.StatusAwaitingInput:
			prefix = "!! "
		case core.StatusDead:
			prefix = "xx "
		}
		cmd := r.cmd
		if cmd == "" {
			cmd = "-"
		}
		age := r.age
		if age == "" {
			age = "-"
		}
		fmt.Fprintf(b, "%s%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			prefix, nameW, r.name, statusW, string(r.status), modeW, r.mode, lockW, lockStr(r), ageW, age, cmd)
	}
}

// humanizeAge renders a duration as a short "3s" / "2m" / "1h" / "4d" token. A
// negative duration (clock skew) is clamped to zero.
func humanizeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
}
