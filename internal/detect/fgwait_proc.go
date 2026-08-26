package detect

import "bytes"

// procfs parsing for readFgWait, kept free of a build tag so it is exercised by
// the test suite on every platform pairmux is developed on, not only where it
// runs. The Linux implementation in fgwait_linux.go is a thin loop over these.

// procStat is the part of /proc/<pid>/stat readFgWait needs.
type procStat struct {
	State byte   // field 3: R running, S/D sleeping, T stopped, Z zombie
	TTYNr uint32 // field 7: the process's controlling terminal
}

// parseProcStat reads state and tty_nr out of a /proc/<pid>/stat line.
//
// Fields are positional but field 2 is the executable name in parentheses and
// may itself contain spaces and parentheses — "(tmux: server)" is one this
// project produces — so the split starts after the LAST ')' rather than
// tokenizing from the left.
//
// tty_nr is compared against a device path's st_rdev without conversion: both
// are what Linux calls new_encode_dev, /proc via do_task_stat and the stat
// syscall via cp_new_stat, so the two numbers are directly comparable.
func parseProcStat(data []byte) (procStat, bool) {
	close := bytes.LastIndexByte(data, ')')
	if close < 0 || close+2 >= len(data) {
		return procStat{}, false
	}
	fields := bytes.Fields(data[close+1:])
	// After the comm field: state, ppid, pgrp, session, tty_nr, ...
	const ttyNrIdx = 4
	if len(fields) <= ttyNrIdx || len(fields[0]) != 1 {
		return procStat{}, false
	}
	nr, ok := atoiBytes(fields[ttyNrIdx])
	if !ok {
		return procStat{}, false
	}
	return procStat{State: fields[0][0], TTYNr: uint32(nr)}, true
}

// atoiBytes parses a non-negative decimal without allocating. tty_nr is
// unsigned in practice; anything else is rejected rather than coerced.
func atoiBytes(b []byte) (uint64, bool) {
	if len(b) == 0 || len(b) > 20 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

// isProcRunning reports whether a state letter means the process is on the CPU.
// Only R counts: S and D are blocked, T is stopped (a suspended job is not
// computing either), Z has already exited.
func isProcRunning(state byte) bool { return state == 'R' }
