package detect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Windows has no terminal device and no termios, but it has the same two facts
// under other names, and this file is the platform-neutral half of reading
// them (console_windows.go is the system-call half).
//
//   - The line discipline is the console input mode of the pseudo console's
//     input buffer: ENABLE_LINE_INPUT is ICANON, ENABLE_ECHO_INPUT is ECHO, and
//     a program that wants a hidden line clears the second and keeps the first
//     exactly as a Unix getpass does.
//   - The processes holding the terminal are the processes attached to the
//     console (GetConsoleProcessList), and whether any of them is on the CPU is
//     the scheduler state of their threads (NtQuerySystemInformation).
//
// The awkward part is the first one. A console's mode can only be read by a
// process attached to that console, and a process has exactly one console —
// AttachConsole and FreeConsole are process-wide. A pairmux command already
// has a console (the agent's), so it cannot attach to a pane's without losing
// its own, and the multiplexer server must not either: while attached it would
// receive the pane's Ctrl+C. So the read happens in a short-lived helper, this
// same executable started with ConsoleProbeCommand, detached from every
// console, which attaches, reads, and exits. It changes no mode and reads no
// input. Its answer is cached (probeTTL) in this process and in a file shared
// by every pairmux process of the user, so N agents polling one terminal cost
// about one helper per second, not N per poll.
//
// Everything that can be decided without Windows — the identifier, the helper's
// wire format, the cache, the parse of the kernel's process snapshot and the
// verdict drawn from it — lives here so it is tested on every platform.

// ConsoleProbeCommand is the hidden subcommand that runs the helper. It is
// dispatched before anything else in cli.Main, like the multiplexer server.
const ConsoleProbeCommand = "__console-probe"

// ConsoleProbeEnv disables the helper when set to "0", "off" or "false":
// classification on Windows then falls back to wording alone, which is how it
// behaved before the helper existed.
const ConsoleProbeEnv = "PAIRMUX_CONSOLE_PROBE"

// Console input mode flags (wincon.h).
const (
	enableLineInput = 0x0002
	enableEchoInput = 0x0004
)

// probeTTL bounds how stale a console reading may be. A second is below every
// gate Classify applies (refineQuiet aside, which only delays the first look),
// and a stale answer is bounded in what it can do: the secret case it might
// keep reporting just after a prompt was answered is the safe direction, and
// output that follows an answer resets the quiet gates anyway.
const probeTTL = time.Second

// consoleProbe is what the helper reports: the console's input mode and the
// pids attached to the console, excluding the helper itself.
type consoleProbe struct {
	Mode uint32
	Pids []uint32
}

// probeWireTag starts every helper reply, versioned so a helper and a caller
// from different releases (an update between the two) cannot misread each
// other: an unrecognised reply is an unknown terminal, never a guess.
const probeWireTag = "pairmux-console-probe/1"

// formatProbeReply is the helper's single line of output.
func formatProbeReply(p consoleProbe, err error) string {
	if err != nil {
		msg := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, err.Error())
		return probeWireTag + " error=" + msg
	}
	ids := make([]string, len(p.Pids))
	for i, pid := range p.Pids {
		ids[i] = strconv.FormatUint(uint64(pid), 10)
	}
	return fmt.Sprintf("%s mode=%#x pids=%s", probeWireTag, p.Mode, strings.Join(ids, ","))
}

// parseProbeReply reads formatProbeReply back. A reply that is not exactly
// that shape is an error, so a helper that crashed half-way, printed a panic,
// or is some other program entirely yields an unknown terminal.
func parseProbeReply(s string) (consoleProbe, error) {
	s = strings.TrimRight(s, "\r\n")
	rest, ok := strings.CutPrefix(s, probeWireTag+" ")
	if !ok {
		return consoleProbe{}, errors.New("console probe: unrecognised reply")
	}
	if msg, isErr := strings.CutPrefix(rest, "error="); isErr {
		return consoleProbe{}, errors.New("console probe: " + msg)
	}
	modeField, pidsField, ok := strings.Cut(rest, " ")
	if !ok {
		return consoleProbe{}, errors.New("console probe: malformed reply")
	}
	modeHex, ok := strings.CutPrefix(modeField, "mode=0x")
	if !ok {
		return consoleProbe{}, errors.New("console probe: malformed mode")
	}
	mode, err := strconv.ParseUint(modeHex, 16, 32)
	if err != nil {
		return consoleProbe{}, errors.New("console probe: malformed mode")
	}
	list, ok := strings.CutPrefix(pidsField, "pids=")
	if !ok || strings.Contains(list, " ") {
		return consoleProbe{}, errors.New("console probe: malformed pids")
	}
	p := consoleProbe{Mode: uint32(mode)}
	if list != "" {
		for _, f := range strings.Split(list, ",") {
			pid, err := strconv.ParseUint(f, 10, 32)
			if err != nil || pid == 0 {
				return consoleProbe{}, errors.New("console probe: malformed pids")
			}
			p.Pids = append(p.Pids, uint32(pid))
		}
	}
	return p, nil
}

// consoleModeState maps a console input mode onto the line discipline it is
// the Windows form of. ECHO without LINE is not a mode Windows honours (echo
// only applies to a cooked read), which TTYState already reads as raw input.
func consoleModeState(mode uint32) TTYState {
	return TTYState{
		Echo:      mode&enableEchoInput != 0,
		Canonical: mode&enableLineInput != 0,
		Known:     true,
	}
}

// probeRecord is one cached reading: when the probe started, and what it found
// or why it failed. The start time, not the finish, is kept, because readFgWait
// uses it to recognise processes that joined the console after the list was
// taken — anything created after the start may have been missed.
type probeRecord struct {
	At    time.Time
	Probe consoleProbe
	Err   string
}

func (r probeRecord) ok() bool { return r.Err == "" }

func (r probeRecord) encode() string {
	var err error
	if r.Err != "" {
		err = errors.New(r.Err)
	}
	return strconv.FormatInt(r.At.UnixNano(), 10) + "\n" + formatProbeReply(r.Probe, err) + "\n"
}

func decodeProbeRecord(s string) (probeRecord, bool) {
	at, reply, ok := strings.Cut(s, "\n")
	if !ok {
		return probeRecord{}, false
	}
	ns, err := strconv.ParseInt(at, 10, 64)
	if err != nil || ns <= 0 {
		return probeRecord{}, false
	}
	r := probeRecord{At: time.Unix(0, ns)}
	if !strings.HasPrefix(reply, probeWireTag+" ") {
		return probeRecord{}, false
	}
	if p, err := parseProbeReply(reply); err != nil {
		r.Err = err.Error()
	} else {
		r.Probe = p
	}
	return r, true
}

// probeCache throttles the helper. A reading younger than ttl is reused, first
// from memory and then from a file in dir that every pairmux process of this
// user shares — a `wait` per agent is a process per agent, so an in-process
// cache alone would still let N agents spawn N helpers a second. A failed
// probe is cached too: a console that cannot be read now will not be readable
// a few milliseconds later, and retrying it every poll is exactly the storm
// this exists to prevent.
//
// The shared file is advisory. Two processes that miss at the same instant
// both probe and the later rename wins; no lock is taken, because a lock that
// a killed process left behind would cost more than an occasional duplicate.
type probeCache struct {
	mu  sync.Mutex
	mem map[string]probeRecord
	ttl time.Duration
	dir string // "" keeps the cache in memory only
	now func() time.Time
	run func(id string) (consoleProbe, error)
}

func (c *probeCache) fresh(r probeRecord, now time.Time) bool {
	age := now.Sub(r.At)
	// A record from the future (a clock stepped back) is not trusted for
	// longer than one ttl either way.
	return age < c.ttl && age > -c.ttl
}

func (c *probeCache) get(id string) probeRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if r, ok := c.mem[id]; ok && c.fresh(r, now) {
		return r
	}
	if r, ok := c.readShared(id); ok && c.fresh(r, now) {
		c.remember(id, r)
		return r
	}
	r := probeRecord{At: now}
	if p, err := c.run(id); err != nil {
		r.Err = err.Error()
		if r.Err == "" {
			r.Err = "console probe failed"
		}
	} else {
		r.Probe = p
	}
	c.remember(id, r)
	c.writeShared(id, r)
	return r
}

func (c *probeCache) remember(id string, r probeRecord) {
	if c.mem == nil {
		c.mem = map[string]probeRecord{}
	}
	c.mem[id] = r
}

// sharedPath is the cache file for id. The identifier is already a safe file
// name apart from its colons, and it is only ever one this package parsed.
func (c *probeCache) sharedPath(id string) string {
	return filepath.Join(c.dir, strings.ReplaceAll(id, ":", "-"))
}

func (c *probeCache) readShared(id string) (probeRecord, bool) {
	if c.dir == "" {
		return probeRecord{}, false
	}
	data, err := os.ReadFile(c.sharedPath(id))
	if err != nil || len(data) > 4096 {
		return probeRecord{}, false
	}
	return decodeProbeRecord(string(data))
}

func (c *probeCache) writeShared(id string, r probeRecord) {
	if c.dir == "" {
		return
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return
	}
	final := c.sharedPath(id)
	tmp := final + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, []byte(r.encode()), 0o600); err != nil {
		return
	}
	// Rename can lose to a reader holding the old file open on Windows; the
	// next miss simply probes again.
	if os.Rename(tmp, final) != nil {
		os.Remove(tmp)
	}
}

// consoleProbeEnabled is set by the CLI entry point. Only a process that is
// the pairmux executable may run its own path as the helper: a library user,
// and in particular a `go test` binary (which would run its whole test suite
// when re-executed), must not.
var consoleProbeEnabled atomic.Bool

// probeAllowTestBinary lets this package's own tests, whose TestMain does
// dispatch ConsoleProbeCommand, use the test binary as the helper.
var probeAllowTestBinary bool

// EnableConsoleProbe declares that this process's executable dispatches
// ConsoleProbeCommand, so prompt detection may run it to read a Windows
// console. Called once from the CLI entry point; a no-op off Windows.
func EnableConsoleProbe() { consoleProbeEnabled.Store(true) }

// probeExecutable returns the program to run as the helper, or false when no
// helper may be run at all.
func probeExecutable() (string, bool) {
	if !consoleProbeEnabled.Load() || consoleProbeDisabled(os.Getenv(ConsoleProbeEnv)) {
		return "", false
	}
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if isTestBinary(exe) && !probeAllowTestBinary {
		return "", false
	}
	return exe, true
}

func consoleProbeDisabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

func isTestBinary(exe string) bool {
	b := strings.ToLower(filepath.Base(exe))
	return strings.HasSuffix(b, ".test") || strings.HasSuffix(b, ".test.exe")
}

// --- the kernel's process snapshot ---------------------------------------
//
// NtQuerySystemInformation(SystemProcessInformation) returns every process
// with every thread and each thread's scheduler state, in one call and without
// opening a single process — the Windows counterpart of walking /proc. The
// layouts below are those of 64-bit Windows, identical on amd64 and arm64
// (both little-endian), and are checked against a known answer before use: the
// thread making the call must appear in the snapshot as Running. A layout that
// fails that check yields FgUnknown, never a verdict.

const (
	spiSize        = 0x100 // sizeof(SYSTEM_PROCESS_INFORMATION), 64-bit
	stiSize        = 0x50  // sizeof(SYSTEM_THREAD_INFORMATION), 64-bit
	spiNextOff     = 0x00  // NextEntryOffset ULONG
	spiThreadsOff  = 0x04  // NumberOfThreads ULONG
	spiCreateOff   = 0x20  // CreateTime LARGE_INTEGER
	spiPidOff      = 0x50  // UniqueProcessId HANDLE
	spiParentOff   = 0x58  // InheritedFromUniqueProcessId HANDLE
	stiTidOff      = 0x30  // ClientId.UniqueThread HANDLE
	stiStateOff    = 0x44  // ThreadState ULONG (KTHREAD_STATE)
	threadRunning  = 2
	maxSnapshotLen = 64 << 20
)

// sysThread and sysProc are the parts of the snapshot readFgWait needs.
type sysThread struct {
	Tid   uint32
	State uint32
}

type sysProc struct {
	Pid, Parent uint32
	Created     uint64 // FILETIME
	Threads     []sysThread
}

// parseProcessSnapshot walks the chain of SYSTEM_PROCESS_INFORMATION records.
// Any record that would read past the buffer, or a chain that loops, rejects
// the whole snapshot.
func parseProcessSnapshot(buf []byte) ([]sysProc, bool) {
	if len(buf) < spiSize || len(buf) > maxSnapshotLen {
		return nil, false
	}
	le := binary.LittleEndian
	var procs []sysProc
	off := 0
	for {
		if off+spiSize > len(buf) {
			return nil, false
		}
		rec := buf[off:]
		next := int(le.Uint32(rec[spiNextOff:]))
		n := int(le.Uint32(rec[spiThreadsOff:]))
		if n < 0 || n > (len(buf)-off-spiSize)/stiSize {
			return nil, false
		}
		p := sysProc{
			Pid:     uint32(le.Uint64(rec[spiPidOff:])),
			Parent:  uint32(le.Uint64(rec[spiParentOff:])),
			Created: le.Uint64(rec[spiCreateOff:]),
			Threads: make([]sysThread, n),
		}
		for i := range n {
			t := rec[spiSize+i*stiSize:]
			p.Threads[i] = sysThread{Tid: uint32(le.Uint64(t[stiTidOff:])), State: le.Uint32(t[stiStateOff:])}
		}
		procs = append(procs, p)
		if next == 0 {
			return procs, true
		}
		// Records only move forward and never overlap their own threads.
		if next < spiSize+n*stiSize {
			return nil, false
		}
		off += next
	}
}

// verifySnapshot is the known-answer check on the layout: the calling thread,
// which is executing the query, must be in the snapshot and Running.
func verifySnapshot(procs []sysProc, selfPid, selfTid uint32) bool {
	for _, p := range procs {
		if p.Pid != selfPid {
			continue
		}
		for _, t := range p.Threads {
			if t.Tid == selfTid {
				return t.State == threadRunning
			}
		}
		return false
	}
	return false
}

// threadOnCPU reports whether a KTHREAD_STATE means the thread is running or
// about to: Ready, Running, Standby, DeferredReady, and the two states of a
// runnable thread waiting for memory (Transition, WaitingForProcessInSwap).
// Initialized is counted too — a process starting threads is busy. Only
// Waiting (5), Terminated (4) and the obsolete gate wait (8) are idle; a
// suspended thread is Waiting, as a stopped job is not running on Unix. An
// unknown state counts as on the CPU: being wrong in that direction only
// withholds an inference.
func threadOnCPU(state uint32) bool {
	switch state {
	case 4, 5, 8:
		return false
	}
	return true
}

// filetime converts a wall-clock time to a FILETIME.
func filetime(t time.Time) uint64 {
	const epochDelta = 116444736000000000 // 1601-01-01 to 1970-01-01 in 100ns
	ns := t.UnixNano()
	if ns < 0 {
		return 0
	}
	return uint64(ns/100) + epochDelta
}

// fgWaitFromSnapshot draws the verdict for a console whose attached processes
// were listed at probedAt (a FILETIME). The list may be up to probeTTL old, so
// processes started since are found through their parents: a process whose
// parent is on the console and which was created after the list was taken is
// counted as on the console too — the compiler a shell started a moment ago
// is the one most likely to be busy. Creation times guard both links against
// pid reuse.
//
// As on Unix, any thread on the CPU means working; at least one process found
// and none running means parked; nothing found means unknown.
func fgWaitFromSnapshot(procs []sysProc, consolePids []uint32, probedAt uint64) FgWait {
	byPid := make(map[uint32]*sysProc, len(procs))
	for i := range procs {
		byPid[procs[i].Pid] = &procs[i]
	}
	members := map[uint32]bool{}
	for _, pid := range consolePids {
		if pid != 0 && byPid[pid] != nil {
			members[pid] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for i := range procs {
			p := &procs[i]
			if members[p.Pid] || p.Pid == 0 || !members[p.Parent] {
				continue
			}
			parent := byPid[p.Parent]
			if p.Created > probedAt && p.Created >= parent.Created {
				members[p.Pid] = true
				grew = true
			}
		}
	}
	if len(members) == 0 {
		return FgUnknown
	}
	for pid := range members {
		for _, t := range byPid[pid].Threads {
			if threadOnCPU(t.State) {
				return FgWorking
			}
		}
	}
	return FgParked
}
