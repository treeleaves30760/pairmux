//go:build windows

package detect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/treeleaves30760/pairmux/internal/pty"
)

// The system-call half of console.go: the helper that reads a pseudo console's
// input mode and process list, the spawn that runs it, and the process
// snapshot that says whether those processes are on the CPU.

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	modntdll    = syscall.NewLazyDLL("ntdll.dll")

	procAttachConsole            = modkernel32.NewProc("AttachConsole")
	procFreeConsole              = modkernel32.NewProc("FreeConsole")
	procGetConsoleProcessList    = modkernel32.NewProc("GetConsoleProcessList")
	procSetConsoleCtrlHandler    = modkernel32.NewProc("SetConsoleCtrlHandler")
	procGetCurrentThreadID       = modkernel32.NewProc("GetCurrentThreadId")
	procNtQuerySystemInformation = modntdll.NewProc("NtQuerySystemInformation")
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259

	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200

	systemProcessInformation = 5
	statusInfoLengthMismatch = 0xC0000004

	// probeTimeout bounds one helper run. Attaching to a console that is
	// being torn down is the only step that could plausibly stall; a helper
	// that does is killed and the terminal reads as unknown.
	probeTimeout = 3 * time.Second
)

var probes = &probeCache{
	ttl: probeTTL,
	dir: filepath.Join(os.TempDir(), "pairmux-console"),
	now: time.Now,
	run: runProbe,
}

// probeFor returns a successful reading of the console named by id, or false.
func probeFor(id string) (probeRecord, bool) {
	if _, _, ok := pty.ParseConsoleID(id); !ok {
		return probeRecord{}, false
	}
	if _, ok := probeExecutable(); !ok {
		return probeRecord{}, false
	}
	r := probes.get(id)
	return r, r.ok()
}

func readTTYState(id string) TTYState {
	r, ok := probeFor(id)
	if !ok {
		return TTYState{}
	}
	return consoleModeState(r.Probe.Mode)
}

func readFgWait(id string) FgWait {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return FgUnknown // the snapshot layouts in console.go are 64-bit
	}
	r, ok := probeFor(id)
	if !ok || len(r.Probe.Pids) == 0 {
		return FgUnknown
	}
	procs, ok := processSnapshot()
	if !ok {
		return FgUnknown
	}
	return fgWaitFromSnapshot(procs, r.Probe.Pids, filetime(r.At))
}

// processSnapshot takes and verifies one SystemProcessInformation snapshot.
// The query runs on a locked OS thread so that thread can be looked up in the
// result as the known answer verifySnapshot checks.
func processSnapshot() ([]sysProc, bool) {
	if procNtQuerySystemInformation.Find() != nil {
		return nil, false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid, _, _ := procGetCurrentThreadID.Call()
	size := uint32(512 << 10)
	for range 4 {
		buf := make([]byte, size)
		var need uint32
		st, _, _ := procNtQuerySystemInformation.Call(systemProcessInformation,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&need)))
		if uint32(st) == statusInfoLengthMismatch {
			// The table can grow between calls; leave room for that.
			size = max(need, size) + 64<<10
			if size > maxSnapshotLen {
				return nil, false
			}
			continue
		}
		if st != 0 {
			return nil, false
		}
		if need == 0 || need > size {
			need = size
		}
		procs, ok := parseProcessSnapshot(buf[:need])
		if !ok || !verifySnapshot(procs, uint32(os.Getpid()), uint32(tid)) {
			return nil, false
		}
		return procs, true
	}
	return nil, false
}

// runProbe runs the helper against id and parses its reply.
func runProbe(id string) (consoleProbe, error) {
	exe, ok := probeExecutable()
	if !ok {
		return consoleProbe{}, errors.New("console probe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, ConsoleProbeCommand, id)
	// Detached: the helper must start with no console of its own, or
	// AttachConsole refuses. A new process group keeps the agent's Ctrl+C
	// (sent to this process's group) from reaching it.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return consoleProbe{}, fmt.Errorf("console probe: %w", err)
	}
	return parseProbeReply(out.String())
}

// ConsoleProbeMain is the helper: it attaches to the console named by args[0],
// reads its input mode and process list, and prints one line. It writes no
// mode and reads no input; the only effect on the console is this process
// appearing in its process list for the few milliseconds it is attached.
func ConsoleProbeMain(args []string) int {
	// os.Stdout is the pipe the caller reads, captured at startup; attaching
	// to a console does not replace it.
	out := os.Stdout
	var p consoleProbe
	var err error
	if len(args) != 1 {
		err = errors.New("usage: " + ConsoleProbeCommand + " <console-id>")
	} else {
		p, err = probeConsole(args[0])
	}
	fmt.Fprintln(out, formatProbeReply(p, err))
	return 0
}

func probeConsole(id string) (consoleProbe, error) {
	pid, created, ok := pty.ParseConsoleID(id)
	if !ok {
		return consoleProbe{}, errors.New("not a console id")
	}
	// The handle pins the pid for as long as it is open, so the creation
	// time checked here is the creation time of the process attached to below.
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return consoleProbe{}, fmt.Errorf("open process: %w", err)
	}
	defer syscall.CloseHandle(h)
	var c, e, k, u syscall.Filetime
	if err := syscall.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return consoleProbe{}, fmt.Errorf("process times: %w", err)
	}
	if uint64(c.HighDateTime)<<32|uint64(c.LowDateTime) != created {
		return consoleProbe{}, errors.New("process replaced")
	}
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return consoleProbe{}, errors.New("process exited")
	}

	// While attached this process receives the console's control events; a
	// Ctrl+C typed into the pane must not end the read half-way (it would only
	// cost an unknown, but there is no reason to pay it).
	procSetConsoleCtrlHandler.Call(0, 1)
	procFreeConsole.Call() // started detached; this only makes certain
	if r, _, err := procAttachConsole.Call(uintptr(pid)); r == 0 {
		return consoleProbe{}, fmt.Errorf("attach console: %w", err)
	}
	defer procFreeConsole.Call()

	name, _ := syscall.UTF16PtrFromString("CONIN$")
	// GENERIC_READ only: GetConsoleMode needs no more, and a handle that
	// cannot write cannot change anything by accident.
	conin, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return consoleProbe{}, fmt.Errorf("open CONIN$: %w", err)
	}
	defer syscall.CloseHandle(conin)
	var mode uint32
	if err := syscall.GetConsoleMode(conin, &mode); err != nil {
		return consoleProbe{}, fmt.Errorf("console mode: %w", err)
	}

	self := uint32(os.Getpid())
	list := make([]uint32, 64)
	for range 4 {
		n, _, err := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&list[0])), uintptr(len(list)))
		if n == 0 {
			return consoleProbe{}, fmt.Errorf("console process list: %w", err)
		}
		if int(n) > len(list) {
			list = make([]uint32, n+16) // grew since the last call
			continue
		}
		p := consoleProbe{Mode: mode}
		for _, pid := range list[:n] {
			if pid != self && pid != 0 {
				p.Pids = append(p.Pids, pid)
			}
		}
		return p, nil
	}
	return consoleProbe{}, errors.New("console process list kept growing")
}
