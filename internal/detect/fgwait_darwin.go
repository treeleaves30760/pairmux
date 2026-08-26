//go:build darwin

package detect

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"
)

// Darwin has no /proc, so the two questions — which processes hold this
// terminal, and is any of them on the CPU — are asked of the kernel directly.
//
// tcgetpgrp is deliberately not used: both Darwin and Linux answer TIOCGPGRP
// with ENOTTY unless the terminal is the *caller's* controlling terminal, which
// a pairmux command's never is. Asking by device sidesteps that, and has the
// better semantics anyway — a pipeline's foreground group can outlive the
// leader whose pid names it, while the device stays fixed for the pane's life.
const (
	ctlKern      = 1
	kernProc     = 14
	kernProcPID  = 1
	kernProcTTY  = 4
	sysProcInfo  = 336 // proc_info(2)
	callPIDInfo  = 2   // PROC_INFO_CALL_PIDINFO
	flavorTask   = 4   // PROC_PIDTASKINFO
	taskInfoSize = 96  // sizeof(struct proc_taskinfo)

	// Offsets into the two structs this file reads. Both are ABI-stable public
	// layouts (sys/sysctl.h, sys/proc_info.h) and identical on amd64 and arm64,
	// but kinfoPIDOff is verified against a known pid at startup rather than
	// trusted: a layout change must degrade to FgUnknown, never to a wrong
	// verdict about someone's terminal.
	kinfoPIDOff    = 40 // kinfo_proc.kp_proc.p_pid
	taskRunningOff = 88 // proc_taskinfo.pti_numrunning
)

// kinfoStride is sizeof(struct kinfo_proc), learned from the kernel by asking
// for a record whose contents are already known (our own). ok is false when the
// answer did not look like a kinfo_proc, which disables this whole file.
var kinfoStride, kinfoOK = func() (int, bool) {
	rec, err := sysctlProc(kernProcPID, int64(os.Getpid()))
	if err != nil || len(rec) < kinfoPIDOff+4 {
		return 0, false
	}
	if int(int32(binary.LittleEndian.Uint32(rec[kinfoPIDOff:]))) != os.Getpid() {
		return 0, false
	}
	return len(rec), true
}()

// sysctlProc runs one kern.proc.<which>.<arg> query, sizing the buffer from the
// kernel's own answer. A process table that grows between the two calls is
// reported as a short read rather than being retried: the caller polls anyway.
func sysctlProc(which int, arg int64) ([]byte, error) {
	mib := [4]int32{ctlKern, kernProc, int32(which), int32(arg)}
	var n uintptr
	if _, _, e := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), 4, 0, uintptr(unsafe.Pointer(&n)), 0, 0); e != 0 {
		return nil, e
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if _, _, e := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), 4, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)), 0, 0); e != 0 {
		return nil, e
	}
	if int(n) > len(buf) {
		return nil, syscall.ENOMEM
	}
	return buf[:n], nil
}

// runningThreads returns how many of a process's threads are on the CPU, or -1
// when the process could not be asked (it exited, or it is not ours).
//
// Darwin's BSD-level process record cannot answer this: p_stat reads SRUN for a
// shell that has been blocked at its prompt for hours, and p_cpticks, p_pctcpu
// and e_wmesg all come back zeroed on current releases — verified on Darwin 25.
// The Mach-level task info is where the truth survived.
func runningThreads(pid int) int {
	var buf [taskInfoSize]byte
	r1, _, e := syscall.Syscall6(sysProcInfo, callPIDInfo, uintptr(pid), flavorTask, 0,
		uintptr(unsafe.Pointer(&buf[0])), taskInfoSize)
	if e != 0 || r1 != taskInfoSize {
		return -1
	}
	return int(int32(binary.LittleEndian.Uint32(buf[taskRunningOff:])))
}

func readFgWait(path string) FgWait {
	if !kinfoOK {
		return FgUnknown
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return FgUnknown
	}
	buf, err := sysctlProc(kernProcTTY, int64(st.Rdev))
	if err != nil || len(buf) < kinfoStride {
		return FgUnknown
	}
	seen := false
	for off := 0; off+kinfoStride <= len(buf); off += kinfoStride {
		pid := int(int32(binary.LittleEndian.Uint32(buf[off+kinfoPIDOff:])))
		if pid <= 0 {
			continue
		}
		switch n := runningThreads(pid); {
		case n > 0:
			return FgWorking
		case n == 0:
			seen = true
		}
		// n < 0: the process went away between the two calls, or is not ours to
		// inspect. It says nothing either way, so it neither counts nor vetoes.
	}
	if !seen {
		return FgUnknown
	}
	return FgParked
}
