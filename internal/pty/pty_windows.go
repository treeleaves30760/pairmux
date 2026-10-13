//go:build windows

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreatePseudoConsole               = modkernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole               = modkernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole                = modkernel32.NewProc("ClosePseudoConsole")
	procInitializeProcThreadAttributeList = modkernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = modkernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = modkernel32.NewProc("DeleteProcThreadAttributeList")
	procCreateJobObjectW                  = modkernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject           = modkernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject          = modkernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject                = modkernel32.NewProc("TerminateJobObject")
	procResumeThread                      = modkernel32.NewProc("ResumeThread")
)

const (
	procThreadAttributePseudoconsole = 0x00020016

	createSuspended            = 0x00000004
	createUnicodeEnvironment   = 0x00000400
	extendedStartupinfoPresent = 0x00080000
	startfUseStdHandles        = 0x00000100

	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000

	// closeSettle gives ConPTY one render frame after the program exits to
	// emit the program's final output before the pseudo console is closed.
	closeSettle = 100 * time.Millisecond
)

// startupInfoEx is STARTUPINFOEXW: STARTUPINFOW followed by the attribute list
// that carries the pseudo console handle.
type startupInfoEx struct {
	syscall.StartupInfo
	attributeList *byte
}

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount, WriteOperationCount, OtherOperationCount uint64
	ReadTransferCount, WriteTransferCount, OtherTransferCount    uint64
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// conPty is a program attached to a pseudo console. ConPTY owns the console
// the program draws into and translates it to VT on out; VT written to in
// becomes the program's keyboard input.
type conPty struct {
	hpc     uintptr
	process syscall.Handle
	job     syscall.Handle
	pid     int
	in      *os.File
	out     *os.File

	exited   chan struct{}
	code     int
	waitErr  error
	hpcOnce  sync.Once
	killOnce sync.Once
	closeMu  sync.Mutex
	closed   bool
}

func coord(cols, rows int) uintptr {
	return uintptr(uint32(uint16(cols)) | uint32(uint16(rows))<<16)
}

func start(o Options) (Pty, error) {
	if err := procCreatePseudoConsole.Find(); err != nil {
		return nil, errors.New("pty: ConPTY is unavailable; pairmux needs Windows 10 version 1809 or later")
	}
	path, err := lookPath(o.Argv[0], o.Env)
	if err != nil {
		return nil, err
	}

	// Two anonymous pipes: ConPTY reads keyboard input from ptyIn and writes
	// rendered output to ptyOut; this process holds the other ends.
	var ptyIn, inW, outR, ptyOut syscall.Handle
	if err := syscall.CreatePipe(&ptyIn, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("pty: input pipe: %w", err)
	}
	if err := syscall.CreatePipe(&outR, &ptyOut, nil, 0); err != nil {
		syscall.CloseHandle(ptyIn)
		syscall.CloseHandle(inW)
		return nil, fmt.Errorf("pty: output pipe: %w", err)
	}
	var hpc uintptr
	hr, _, _ := procCreatePseudoConsole.Call(coord(o.Cols, o.Rows), uintptr(ptyIn), uintptr(ptyOut), 0, uintptr(unsafe.Pointer(&hpc)))
	// The pseudo console duplicated its ends; ours are no longer needed.
	syscall.CloseHandle(ptyIn)
	syscall.CloseHandle(ptyOut)
	if hr != 0 {
		syscall.CloseHandle(inW)
		syscall.CloseHandle(outR)
		return nil, fmt.Errorf("pty: CreatePseudoConsole: HRESULT 0x%08x", uint32(hr))
	}
	p := &conPty{hpc: hpc, exited: make(chan struct{})}
	p.in = os.NewFile(uintptr(inW), "conpty-input")
	p.out = os.NewFile(uintptr(outR), "conpty-output")

	if err := p.spawn(path, o); err != nil {
		p.closePseudoConsole()
		p.in.Close()
		p.out.Close()
		return nil, err
	}
	go p.reap()
	return p, nil
}

func (p *conPty) spawn(path string, o Options) error {
	var size uintptr
	procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return errors.New("pty: InitializeProcThreadAttributeList: no size")
	}
	attrs := make([]byte, size)
	if r, _, err := procInitializeProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrs[0])), 1, 0, uintptr(unsafe.Pointer(&size))); r == 0 {
		return fmt.Errorf("pty: InitializeProcThreadAttributeList: %w", err)
	}
	defer procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrs[0])))
	// The pseudo console attribute's value is the HPCON itself, not a pointer
	// to it.
	if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(&attrs[0])), 0,
		procThreadAttributePseudoconsole, p.hpc, unsafe.Sizeof(p.hpc), 0, 0); r == 0 {
		return fmt.Errorf("pty: UpdateProcThreadAttribute: %w", err)
	}

	var si startupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	// STARTF_USESTDHANDLES with null handles: the program must talk to the
	// pseudo console, never inherit whatever standard handles this (detached)
	// server happens to have.
	si.Flags = startfUseStdHandles
	si.attributeList = &attrs[0]

	cmdline := composeCommandLine(path, o.Argv[1:])
	cmdlineW, err := syscall.UTF16PtrFromString(cmdline)
	if err != nil {
		return fmt.Errorf("pty: command line: %w", err)
	}
	var dirW *uint16
	if o.Dir != "" {
		if dirW, err = syscall.UTF16PtrFromString(o.Dir); err != nil {
			return fmt.Errorf("pty: directory: %w", err)
		}
	}
	envBlock := environmentBlock(o.Env)

	var pi syscall.ProcessInformation
	flags := uint32(extendedStartupinfoPresent | createUnicodeEnvironment | createSuspended)
	if err := syscall.CreateProcess(nil, cmdlineW, nil, nil, false, flags,
		&envBlock[0], dirW, &si.StartupInfo, &pi); err != nil {
		return fmt.Errorf("pty: start %s: %w", o.Argv[0], err)
	}
	p.process = pi.Process
	p.pid = int(pi.ProcessId)

	// A kill-on-close job holds the whole tree the program starts, so killing
	// a terminal — or this server dying — takes its children with it, the way
	// a hung-up Unix session does. Failure only loses that guarantee.
	if job, _, _ := procCreateJobObjectW.Call(0, 0); job != 0 {
		var info jobExtendedLimitInformation
		info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
		r1, _, _ := procSetInformationJobObject.Call(job, jobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		r2, _, _ := procAssignProcessToJobObject.Call(job, uintptr(pi.Process))
		if r1 != 0 && r2 != 0 {
			p.job = syscall.Handle(job)
		} else {
			syscall.CloseHandle(syscall.Handle(job))
		}
	}
	procResumeThread.Call(uintptr(pi.Thread))
	syscall.CloseHandle(pi.Thread)
	return nil
}

func (p *conPty) reap() {
	_, err := syscall.WaitForSingleObject(p.process, syscall.INFINITE)
	var code uint32
	if err == nil {
		err = syscall.GetExitCodeProcess(p.process, &code)
	}
	p.code, p.waitErr = int(code), err
	close(p.exited)
	// Closing the pseudo console flushes its last frame and then closes the
	// output pipe, which is what ends Read with EOF. It can block until that
	// output is consumed, so it runs here, concurrently with the reader.
	time.Sleep(closeSettle)
	p.closePseudoConsole()
}

func (p *conPty) closePseudoConsole() {
	p.hpcOnce.Do(func() { procClosePseudoConsole.Call(p.hpc) })
}

func (p *conPty) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err != nil && n == 0 {
		// A broken pipe is the pseudo console closing: end of output.
		if errors.Is(err, io.EOF) || errors.Is(err, syscall.ERROR_BROKEN_PIPE) || errors.Is(err, os.ErrClosed) {
			return 0, io.EOF
		}
	}
	return n, err
}

func (p *conPty) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *conPty) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 0x7fff || rows > 0x7fff {
		return fmt.Errorf("pty: invalid size %dx%d", cols, rows)
	}
	if hr, _, _ := procResizePseudoConsole.Call(p.hpc, coord(cols, rows)); hr != 0 {
		return fmt.Errorf("pty: ResizePseudoConsole: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

func (p *conPty) Pid() int { return p.pid }

func (p *conPty) TTY() string { return "" }

func (p *conPty) Wait() (int, error) {
	<-p.exited
	return p.code, p.waitErr
}

func (p *conPty) Exited() <-chan struct{} { return p.exited }

// Kill terminates the program's whole job (or just the program when it could
// not be placed in one); reap then closes the pseudo console, which ends any
// other client still attached to it.
func (p *conPty) Kill() error {
	var err error
	p.killOnce.Do(func() {
		if p.job != 0 {
			if r, _, _ := procTerminateJobObject.Call(uintptr(p.job), 1); r != 0 {
				return
			}
		}
		err = syscall.TerminateProcess(p.process, 1)
	})
	return err
}

func (p *conPty) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.closePseudoConsole()
	p.in.Close()
	p.out.Close()
	if p.job != 0 {
		syscall.CloseHandle(p.job) // kill-on-close: anything left behind goes too
	}
	return syscall.CloseHandle(p.process)
}

// composeCommandLine quotes each argument by the rules CommandLineToArgvW
// (and so every C runtime and PowerShell) uses to split it again.
func composeCommandLine(path string, args []string) string {
	var b strings.Builder
	b.WriteString(syscall.EscapeArg(path))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(syscall.EscapeArg(a))
	}
	return b.String()
}

// environmentBlock builds CreateProcess's UTF-16 environment: deduplicated
// case-insensitively (the last value wins, as for exec.Cmd), sorted the way
// Windows expects, and always carrying SYSTEMROOT, without which many
// programs (notably anything using Winsock) fail in confusing ways.
func environmentBlock(env []string) []uint16 {
	byKey := map[string]string{}
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			// "=C:=C:\\" style per-drive entries start with '=' and are kept.
			if strings.HasPrefix(kv, "=") {
				k2, _, _ := strings.Cut(kv[1:], "=")
				k = "=" + k2
			} else {
				continue
			}
		}
		byKey[strings.ToUpper(k)] = kv
	}
	if _, ok := byKey["SYSTEMROOT"]; !ok {
		if v := os.Getenv("SYSTEMROOT"); v != "" {
			byKey["SYSTEMROOT"] = "SYSTEMROOT=" + v
		}
	}
	order := make([]string, 0, len(byKey))
	for u := range byKey {
		order = append(order, u)
	}
	sort.Strings(order)
	var block []uint16
	for _, u := range order {
		block = append(block, utf16.Encode([]rune(byKey[u]))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0)
}
