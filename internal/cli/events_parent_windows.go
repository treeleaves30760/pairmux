//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	processSynchronize             = 0x00100000
	errorInvalidParameter          = syscall.Errno(87)
)

// watchParent reports when the process that started this one is gone. Windows
// never reparents, so os.Getppid keeps returning the dead parent's pid (which
// may even be reused); instead hold a handle to the parent process and ask
// whether it has exited. The handle is opened once, at start, and its creation
// time is checked against ours so a parent that died — and whose pid was
// reused — before the handle was opened counts as already gone.
func watchParent() (gone func() bool, stop func(), err error) {
	return watchPid(os.Getppid())
}

// watchPid is watchParent for the process ppid.
func watchPid(ppid int) (gone func() bool, stop func(), err error) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation|processSynchronize, false, uint32(ppid))
	if err != nil {
		if errors.Is(err, errorInvalidParameter) { // no such process: already gone
			return func() bool { return true }, func() {}, nil
		}
		return nil, nil, fmt.Errorf("cannot watch the parent process (pid %d): %w", ppid, err)
	}
	stop = func() { syscall.CloseHandle(h) }
	if parentNewer(h) {
		return func() bool { return true }, stop, nil
	}
	return handleExited(h), stop, nil
}

// handleExited reports, on each call, whether the process behind h has exited.
func handleExited(h syscall.Handle) func() bool {
	return func() bool {
		ev, err := syscall.WaitForSingleObject(h, 0)
		return err == nil && ev == syscall.WAIT_OBJECT_0
	}
}

// parentNewer reports whether the process behind h was created after this one,
// which means it is not our parent but a later process that reused its pid.
func parentNewer(h syscall.Handle) bool {
	var pc, pe, pk, pu, sc, se, sk, su syscall.Filetime
	if syscall.GetProcessTimes(h, &pc, &pe, &pk, &pu) != nil {
		return false
	}
	self, err := syscall.GetCurrentProcess()
	if err != nil || syscall.GetProcessTimes(self, &sc, &se, &sk, &su) != nil {
		return false
	}
	return pc.Nanoseconds() > sc.Nanoseconds()
}

// tolerateClosedStdout is a no-op on Windows: a write to a closed pipe already
// returns an error there instead of raising a signal.
func tolerateClosedStdout() {}
