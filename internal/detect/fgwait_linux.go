//go:build linux

package detect

import (
	"os"
	"syscall"
)

// Linux answers both halves of readFgWait from /proc: which processes hold the
// terminal (each one's stat records its controlling device) and whether any of
// them is on the CPU (the state letter).
//
// The whole table is walked rather than resolved from the foreground process
// group, for the same two reasons as on Darwin: TIOCGPGRP is refused on a
// terminal that is not the caller's own, and a pipeline's group can outlive the
// pid that names it. The walk costs one readdir plus a small read per process,
// and Classify only reaches it once the pane has been quiet for refineQuiet —
// never while output is flowing.
func readFgWait(path string) FgWait {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return FgUnknown
	}
	want := uint32(st.Rdev)
	if want == 0 {
		return FgUnknown
	}

	proc, err := os.Open("/proc")
	if err != nil {
		return FgUnknown
	}
	defer proc.Close()
	names, err := proc.Readdirnames(-1)
	if err != nil {
		return FgUnknown
	}

	seen := false
	for _, name := range names {
		if name[0] < '0' || name[0] > '9' {
			continue
		}
		data, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue // exited between readdir and read, or not ours to read
		}
		ps, ok := parseProcStat(data)
		if !ok || ps.TTYNr != want {
			continue
		}
		if isProcRunning(ps.State) {
			return FgWorking
		}
		seen = true
	}
	if !seen {
		return FgUnknown
	}
	return FgParked
}
