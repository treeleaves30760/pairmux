package detect

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProbeReplyRoundTrip(t *testing.T) {
	for _, p := range []consoleProbe{
		{Mode: 0x1f7, Pids: []uint32{4242, 77}},
		{Mode: 0x1f3, Pids: []uint32{1}},
		{Mode: 0, Pids: nil},
	} {
		line := formatProbeReply(p, nil)
		got, err := parseProbeReply(line + "\r\n")
		if err != nil || !reflect.DeepEqual(got, p) {
			t.Errorf("parseProbeReply(%q) = %+v, %v; want %+v", line, got, err, p)
		}
	}
	line := formatProbeReply(consoleProbe{Mode: 7}, errors.New("attach console: Access is denied.\r\nmore"))
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("error reply spans lines: %q", line)
	}
	if _, err := parseProbeReply(line); err == nil || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("error reply parsed as %v", err)
	}
}

// TestParseProbeReplyRejects pins that only the helper's exact reply is read
// as a console state: anything else — a panic, a different release's format,
// an older pairmux that does not know the subcommand and printed its usage —
// is an unknown terminal.
func TestParseProbeReplyRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"panic: runtime error",
		"pairmux-console-probe/2 mode=0x1f7 pids=1",
		"pairmux-console-probe/1",
		"pairmux-console-probe/1 mode=0x1f7",
		"pairmux-console-probe/1 mode=1f7 pids=1",
		"pairmux-console-probe/1 mode=0xzz pids=1",
		"pairmux-console-probe/1 mode=0x100000000 pids=1",
		"pairmux-console-probe/1 mode=0x1f7 pids=1,x",
		"pairmux-console-probe/1 mode=0x1f7 pids=0",
		"pairmux-console-probe/1 mode=0x1f7 pids=1, 2",
		"pairmux-console-probe/1 mode=0x1f7 procs=1",
	} {
		if p, err := parseProbeReply(s); err == nil {
			t.Errorf("parseProbeReply(%q) = %+v, nil; want an error", s, p)
		}
	}
}

// TestConsoleModeState pins the Windows console modes to the line discipline
// they stand for. 0x1f7 is the default cooked mode of a console's input
// buffer; the getpass shape clears ECHO and keeps LINE; a program reading keys
// itself clears LINE (and ECHO with it, which is meaningless without it).
func TestConsoleModeState(t *testing.T) {
	tests := []struct {
		name                string
		mode                uint32
		secret, line, rawIn bool
	}{
		{"default cooked", 0x1f7, false, true, false},
		{"echo cleared, line kept: the getpass signature", 0x1f7 &^ enableEchoInput, true, true, false},
		{"line and echo cleared: raw keys", 0x1f7 &^ (enableLineInput | enableEchoInput), false, false, true},
		{"VT input only, as a TUI sets it", 0x200, false, false, true},
		{"echo without line is still raw", enableEchoInput, false, false, true},
	}
	for _, tc := range tests {
		s := consoleModeState(tc.mode)
		if !s.Known || s.Secret() != tc.secret || s.LineOriented() != tc.line || s.RawInput() != tc.rawIn {
			t.Errorf("%s: mode %#x -> %+v (secret %v line %v raw %v)", tc.name, tc.mode, s, s.Secret(), s.LineOriented(), s.RawInput())
		}
	}
}

type fakeProbe struct {
	calls int
	probe consoleProbe
	err   error
}

func (f *fakeProbe) run(string) (consoleProbe, error) {
	f.calls++
	return f.probe, f.err
}

func newTestCache(dir string, clock *time.Time, f *fakeProbe) *probeCache {
	return &probeCache{ttl: time.Second, dir: dir, now: func() time.Time { return *clock }, run: f.run}
}

// TestProbeCacheThrottles pins the cost model: within a ttl one helper serves
// every poll of this process, and — through the shared file — of every other
// pairmux process polling the same terminal.
func TestProbeCacheThrottles(t *testing.T) {
	dir := t.TempDir()
	clock := time.Unix(1_700_000_000, 0)
	f := &fakeProbe{probe: consoleProbe{Mode: 0x1f3, Pids: []uint32{10, 11}}}
	a := newTestCache(dir, &clock, f)
	const id = "conpty:10:123"

	r := a.get(id)
	if !r.ok() || !reflect.DeepEqual(r.Probe, f.probe) || f.calls != 1 {
		t.Fatalf("first get = %+v after %d calls", r, f.calls)
	}
	clock = clock.Add(900 * time.Millisecond)
	a.get(id)
	if f.calls != 1 {
		t.Fatalf("a poll within the ttl ran the helper again (%d calls)", f.calls)
	}

	// Another process: an empty memory, the same shared directory.
	g := &fakeProbe{probe: consoleProbe{Mode: 0x1f7}}
	b := newTestCache(dir, &clock, g)
	if r := b.get(id); g.calls != 0 || !reflect.DeepEqual(r.Probe, f.probe) {
		t.Fatalf("second process probed (%d calls) or misread the shared reading: %+v", g.calls, r)
	}

	clock = clock.Add(200 * time.Millisecond) // past the ttl of the reading
	if r := b.get(id); g.calls != 1 || r.Probe.Mode != 0x1f7 {
		t.Fatalf("stale reading reused: %d calls, %+v", g.calls, r)
	}
	if r := a.get(id); f.calls != 1 || r.Probe.Mode != 0x1f7 {
		t.Fatalf("first process did not pick up the refreshed shared reading: %d calls, %+v", f.calls, r)
	}
}

// TestProbeCacheRemembersFailure pins that an unreadable console is not
// retried every poll: that would be the storm the cache exists to prevent.
func TestProbeCacheRemembersFailure(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	f := &fakeProbe{err: errors.New("attach console: denied")}
	c := newTestCache(t.TempDir(), &clock, f)
	for range 5 {
		if r := c.get("conpty:5:6"); r.ok() {
			t.Fatalf("failed probe reported ok: %+v", r)
		}
	}
	if f.calls != 1 {
		t.Fatalf("failed probe retried within the ttl: %d calls", f.calls)
	}
	g := &fakeProbe{}
	if r := newTestCache(c.dir, &clock, g).get("conpty:5:6"); r.ok() || g.calls != 0 {
		t.Fatalf("shared failure not honoured: %+v, %d calls", r, g.calls)
	}
}

func TestProbeCacheIgnoresBadSharedFiles(t *testing.T) {
	dir := t.TempDir()
	clock := time.Unix(1_700_000_000, 0)
	f := &fakeProbe{probe: consoleProbe{Mode: 0x1f7, Pids: []uint32{3}}}
	c := newTestCache(dir, &clock, f)
	const id = "conpty:3:4"
	for _, content := range []string{
		"garbage",
		"1700000000000000000\nsomething else",
		"-5\n" + formatProbeReply(consoleProbe{Mode: 1}, nil),
		// From the future by more than a ttl: a clock stepped back.
		"1700000100000000000\n" + formatProbeReply(consoleProbe{Mode: 1}, nil),
	} {
		if err := os.WriteFile(filepath.Join(dir, "conpty-3-4"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		c.mem = nil
		before := f.calls
		if r := c.get(id); f.calls != before+1 || r.Probe.Mode != 0x1f7 {
			t.Errorf("shared file %q was trusted: %+v", content, r)
		}
	}
}

func TestProbeCacheMemoryOnly(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	f := &fakeProbe{probe: consoleProbe{Mode: 2}}
	c := newTestCache("", &clock, f)
	c.get("conpty:1:1")
	c.get("conpty:1:1")
	if f.calls != 1 {
		t.Fatalf("memory cache missed: %d calls", f.calls)
	}
}

func TestProbeGates(t *testing.T) {
	for _, v := range []string{"0", "off", "OFF", "false", " no "} {
		if !consoleProbeDisabled(v) {
			t.Errorf("%s=%q does not disable the probe", ConsoleProbeEnv, v)
		}
	}
	for _, v := range []string{"", "1", "on"} {
		if consoleProbeDisabled(v) {
			t.Errorf("%s=%q disables the probe", ConsoleProbeEnv, v)
		}
	}
	for exe, want := range map[string]bool{
		`C:\Users\a\AppData\Local\Temp\go-build1\b001\cli.test.exe`: true,
		"/tmp/go-build1/b001/detect.test":                           true,
		`C:\Users\a\.local\bin\pairmux.exe`:                         false,
		"/usr/local/bin/pairmux":                                    false,
	} {
		if got := isTestBinary(exe); got != want {
			t.Errorf("isTestBinary(%q) = %v, want %v", exe, got, want)
		}
	}
}

func TestFiletime(t *testing.T) {
	if got := filetime(time.Unix(0, 0)); got != 116444736000000000 {
		t.Fatalf("filetime(unix epoch) = %d", got)
	}
	if got := filetime(time.Unix(1, 500)); got != 116444736000000000+10_000_005 {
		t.Fatalf("filetime(1s+500ns) = %d", got)
	}
}

// snapshotBuilder lays out SYSTEM_PROCESS_INFORMATION records the way the
// 64-bit kernel does, so the parser is tested against the layout it claims.
type snapshotBuilder struct{ recs [][]byte }

func (b *snapshotBuilder) add(p sysProc) {
	le := binary.LittleEndian
	rec := make([]byte, spiSize+len(p.Threads)*stiSize)
	le.PutUint32(rec[spiThreadsOff:], uint32(len(p.Threads)))
	le.PutUint64(rec[spiCreateOff:], p.Created)
	le.PutUint64(rec[spiPidOff:], uint64(p.Pid))
	le.PutUint64(rec[spiParentOff:], uint64(p.Parent))
	for i, th := range p.Threads {
		t := rec[spiSize+i*stiSize:]
		le.PutUint64(t[stiTidOff-8:], uint64(p.Pid)) // ClientId.UniqueProcess
		le.PutUint64(t[stiTidOff:], uint64(th.Tid))
		le.PutUint32(t[stiStateOff:], th.State)
		le.PutUint32(t[stiStateOff+4:], 0xAA) // WaitReason, must be ignored
	}
	b.recs = append(b.recs, rec)
}

func (b *snapshotBuilder) bytes() []byte {
	var out []byte
	for i, rec := range b.recs {
		if i < len(b.recs)-1 {
			binary.LittleEndian.PutUint32(rec[spiNextOff:], uint32(len(rec)))
		}
		out = append(out, rec...)
	}
	return out
}

func TestParseProcessSnapshot(t *testing.T) {
	want := []sysProc{
		{Pid: 0, Created: 0, Threads: []sysThread{{Tid: 0, State: threadRunning}}},
		{Pid: 4242, Parent: 100, Created: 1000, Threads: []sysThread{{Tid: 1, State: 5}, {Tid: 2, State: 1}}},
		{Pid: 77, Parent: 4242, Created: 2000, Threads: []sysThread{}},
	}
	var b snapshotBuilder
	for _, p := range want {
		b.add(p)
	}
	buf := b.bytes()
	got, ok := parseProcessSnapshot(buf)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcessSnapshot = %+v, %v\nwant %+v", got, ok, want)
	}

	if _, ok := parseProcessSnapshot(buf[:len(buf)-1]); ok {
		t.Error("truncated snapshot accepted")
	}
	if _, ok := parseProcessSnapshot(nil); ok {
		t.Error("empty snapshot accepted")
	}
	loop := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(loop[spiNextOff:], 0x10) // points back into itself
	if _, ok := parseProcessSnapshot(loop); ok {
		t.Error("self-overlapping chain accepted")
	}
	huge := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(huge[spiThreadsOff:], 1<<30)
	if _, ok := parseProcessSnapshot(huge); ok {
		t.Error("thread count past the buffer accepted")
	}
}

func TestVerifySnapshot(t *testing.T) {
	procs := []sysProc{
		{Pid: 9, Threads: []sysThread{{Tid: 90, State: 5}, {Tid: 91, State: threadRunning}}},
	}
	if !verifySnapshot(procs, 9, 91) {
		t.Error("calling thread found Running, yet rejected")
	}
	if verifySnapshot(procs, 9, 90) {
		t.Error("calling thread found Waiting — a misread layout — yet accepted")
	}
	if verifySnapshot(procs, 8, 91) || verifySnapshot(procs, 9, 92) {
		t.Error("calling process or thread missing, yet accepted")
	}
}

func TestThreadOnCPU(t *testing.T) {
	for state, want := range map[uint32]bool{
		0: true, 1: true, 2: true, 3: true, 4: false, 5: false,
		6: true, 7: true, 8: false, 9: true, 42: true,
	} {
		if got := threadOnCPU(state); got != want {
			t.Errorf("threadOnCPU(%d) = %v, want %v", state, got, want)
		}
	}
}

func waiting(tids ...uint32) []sysThread {
	out := make([]sysThread, len(tids))
	for i, tid := range tids {
		out[i] = sysThread{Tid: tid, State: 5}
	}
	return out
}

// TestFgWaitFromSnapshot covers the verdict, including the case that makes a
// cached process list safe to use: a busy child started after the list was
// taken still counts, while a process that merely shares a parent with the
// console but predates the list (a daemon the shell detached long ago) and a
// child whose "parent" pid was reused do not.
func TestFgWaitFromSnapshot(t *testing.T) {
	const probedAt = 5000
	shell := sysProc{Pid: 100, Parent: 1, Created: 1000, Threads: waiting(1000, 1001)}
	busy := []sysThread{{Tid: 2000, State: 5}, {Tid: 2001, State: threadRunning}}
	tests := []struct {
		name    string
		procs   []sysProc
		console []uint32
		want    FgWait
	}{
		{"everything waiting", []sysProc{shell}, []uint32{100}, FgParked},
		{"a listed process running", []sysProc{shell, {Pid: 200, Parent: 100, Created: 2000, Threads: busy}}, []uint32{100, 200}, FgWorking},
		{"a ready thread counts as busy", []sysProc{{Pid: 100, Created: 1, Threads: []sysThread{{Tid: 1, State: 1}}}}, []uint32{100}, FgWorking},
		{"listed processes all gone", []sysProc{shell}, []uint32{300}, FgUnknown},
		{"nothing listed", []sysProc{shell}, nil, FgUnknown},
		{"busy child started after the list", []sysProc{shell, {Pid: 200, Parent: 100, Created: 6000, Threads: busy}}, []uint32{100}, FgWorking},
		{"busy grandchild started after the list", []sysProc{shell,
			{Pid: 200, Parent: 100, Created: 6000, Threads: waiting(3)},
			{Pid: 201, Parent: 200, Created: 6001, Threads: busy}}, []uint32{100}, FgWorking},
		{"busy process detached before the list", []sysProc{shell, {Pid: 200, Parent: 100, Created: 2000, Threads: busy}}, []uint32{100}, FgParked},
		{"parent pid reused by the console", []sysProc{
			{Pid: 100, Parent: 1, Created: 7000, Threads: waiting(1)},
			{Pid: 200, Parent: 100, Created: 6000, Threads: busy}}, []uint32{100}, FgParked},
		{"the idle process is never a member", []sysProc{shell, {Pid: 0, Parent: 100, Created: 9000, Threads: busy}}, []uint32{100, 0}, FgParked},
	}
	for _, tc := range tests {
		if got := fgWaitFromSnapshot(tc.procs, tc.console, probedAt); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
