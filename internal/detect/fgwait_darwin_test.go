//go:build darwin

package detect

import (
	"os"
	"testing"
)

// TestDarwinProcABI checks the two kernel interfaces this platform's readFgWait
// is built on against the one process whose answers are known: our own. It is
// the guard for an OS upgrade quietly moving a struct out from under the
// offsets in fgwait_darwin.go — a case that must show up as a failing test
// rather than as pairmux telling someone their terminal is idle.
func TestDarwinProcABI(t *testing.T) {
	if !kinfoOK {
		t.Fatal("kinfo_proc probe failed: sysctl kern.proc.pid did not return our own pid at the expected offset")
	}
	if kinfoStride < 400 || kinfoStride > 2048 {
		t.Fatalf("sizeof(kinfo_proc) = %d, implausible", kinfoStride)
	}

	// A test binary is by definition on the CPU while this line runs, so the
	// Mach task info must say so. This is the assertion that would fail if
	// pti_numrunning ever moved, or if proc_info stopped answering.
	if got := runningThreads(os.Getpid()); got < 1 {
		t.Fatalf("runningThreads(self) = %d, want at least 1", got)
	}

	// A pid that cannot exist must be reported as unaskable rather than as
	// zero running threads, or a terminal whose processes have all gone would
	// read as parked.
	if got := runningThreads(1 << 30); got != -1 {
		t.Fatalf("runningThreads(bogus) = %d, want -1", got)
	}
}

// TestDarwinSysctlProcSizing pins the two-call sizing protocol: asking for our
// own pid must yield exactly one record, which is what kinfoStride is derived
// from in the first place.
func TestDarwinSysctlProcSizing(t *testing.T) {
	rec, err := sysctlProc(kernProcPID, int64(os.Getpid()))
	if err != nil {
		t.Fatalf("sysctlProc: %v", err)
	}
	if len(rec) != kinfoStride {
		t.Fatalf("own record is %d bytes, stride is %d", len(rec), kinfoStride)
	}

	// A pid nobody has is not an error, it is an empty table.
	empty, err := sysctlProc(kernProcPID, 1<<30)
	if err != nil {
		t.Fatalf("sysctlProc(bogus): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("bogus pid returned %d bytes, want none", len(empty))
	}
}
