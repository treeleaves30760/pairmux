//go:build unix

// Package journal owns the on-disk state of a single pairmux terminal: the
// append-only raw.log byte stream, the index.jsonl event log, meta.json, and a
// flock-based write lock. All I/O here is best effort against a file that a
// separate tmux pipe-pane process is concurrently appending to.
//
// Unix only: the write lock uses syscall.Flock, which pairmux supports on
// darwin and linux (no windows build).
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

const (
	rawName      = "raw.log"
	indexName    = "index.jsonl"
	metaName     = "meta.json"
	lockName     = "write.lock"
	sendLockName = "send.lock"

	// maxEventLine bounds a single index.jsonl line so a corrupt/huge line
	// cannot exhaust memory while scanning.
	maxEventLine = 16 * 1024 * 1024
)

// ErrLocked is returned by AcquireWriteLock when another writer already holds
// the flock. Callers detect it with errors.Is; the holder pid (best effort) is
// available separately via LockHolder.
var ErrLocked = errors.New("journal: write lock held by another process")

// Journal is the set of files under one terminal's state directory.
type Journal struct{ Dir string }

// Open ensures the terminal directory exists and returns a Journal for it.
func Open(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("journal: mkdir %q: %w", dir, err)
	}
	return &Journal{Dir: dir}, nil
}

// RawPath is the absolute path of the raw.log byte stream.
func (j *Journal) RawPath() string { return filepath.Join(j.Dir, rawName) }

func (j *Journal) indexPath() string    { return filepath.Join(j.Dir, indexName) }
func (j *Journal) metaPath() string     { return filepath.Join(j.Dir, metaName) }
func (j *Journal) lockPath() string     { return filepath.Join(j.Dir, lockName) }
func (j *Journal) sendLockPath() string { return filepath.Join(j.Dir, sendLockName) }

// Size reports the current length of raw.log, or 0 when it is missing.
func (j *Journal) Size() int64 {
	fi, err := os.Stat(j.RawPath())
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ReadRange returns raw.log bytes in [from, to). to == -1 (or any negative)
// means EOF. Offsets are clamped to the current file size and this never errors
// on a shrunk/missing file — the byte stream is written by another process, so
// best effort is the contract.
func (j *Journal) ReadRange(from, to int64) ([]byte, error) {
	f, err := os.Open(j.RawPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte{}, nil
		}
		return nil, fmt.Errorf("journal: open raw.log: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("journal: stat raw.log: %w", err)
	}
	size := fi.Size()

	if from < 0 {
		from = 0
	}
	if to < 0 || to > size {
		to = size
	}
	if from > size {
		from = size
	}
	if from >= to {
		return []byte{}, nil
	}

	// Best effort: the writer may truncate/rotate between stat and read, so a
	// short read (io.EOF or a transient error) yields whatever we got rather
	// than surfacing an error.
	buf := make([]byte, to-from)
	n, _ := f.ReadAt(buf, from)
	return buf[:n], nil
}

// TailBytes returns up to the last max bytes of raw.log and the absolute offset
// at which the returned slice starts. max <= 0 returns the whole file.
func (j *Journal) TailBytes(max int64) (data []byte, startOff int64, err error) {
	size := j.Size()
	from := int64(0)
	if max > 0 && size > max {
		from = size - max
	}
	data, err = j.ReadRange(from, size)
	if err != nil {
		return nil, 0, err
	}
	return data, from, nil
}

// LastModified returns raw.log's modification time. ok is false when raw.log
// does not exist yet.
func (j *Journal) LastModified() (t time.Time, ok bool) {
	fi, err := os.Stat(j.RawPath())
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// AppendEvent appends one JSON-encoded event as a single line to index.jsonl.
// A single Write call keeps the line intact under concurrent O_APPEND writers.
// The event timestamp defaults to now (UTC) when the caller left it zero.
func (j *Journal) AppendEvent(ev core.Event) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("journal: marshal event: %w", err)
	}
	line = append(line, '\n')

	f, err := os.OpenFile(j.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("journal: open index.jsonl: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("journal: append event: %w", err)
	}
	return nil
}

// Events reads all parseable events from index.jsonl in order. Unparseable
// lines (corrupt or a torn final append) are skipped silently.
func (j *Journal) Events() ([]core.Event, error) {
	f, err := os.Open(j.indexPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("journal: open index.jsonl: %w", err)
	}
	defer f.Close()

	var events []core.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev core.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return events, fmt.Errorf("journal: scan index.jsonl: %w", err)
	}
	return events, nil
}

// NextCmdID returns 1 + the highest CmdID seen in the event log (1 when empty).
func (j *Journal) NextCmdID() (int, error) {
	events, err := j.Events()
	if err != nil {
		return 0, err
	}
	max := 0
	for _, ev := range events {
		if ev.CmdID > max {
			max = ev.CmdID
		}
	}
	return max + 1, nil
}

// PendingCmd returns the most recent EvCmdStart that has no later EvCmdEnd with
// the same CmdID — i.e. a command whose completion has not been recorded.
func (j *Journal) PendingCmd() (*core.Event, bool, error) {
	events, err := j.Events()
	if err != nil {
		return nil, false, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != core.EvCmdStart {
			continue
		}
		ended := false
		for k := i + 1; k < len(events); k++ {
			if events[k].Type == core.EvCmdEnd && events[k].CmdID == events[i].CmdID {
				ended = true
				break
			}
		}
		if !ended {
			ev := events[i]
			return &ev, true, nil
		}
	}
	return nil, false, nil
}

// ReadMeta reads and decodes meta.json.
func (j *Journal) ReadMeta() (core.Meta, error) {
	var m core.Meta
	b, err := os.ReadFile(j.metaPath())
	if err != nil {
		return m, fmt.Errorf("journal: read meta.json: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("journal: parse meta.json: %w", err)
	}
	return m, nil
}

// WriteMeta writes meta.json atomically via a temp file + rename in the same
// directory.
func (j *Journal) WriteMeta(m core.Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("journal: marshal meta: %w", err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(j.Dir, metaName+".*.tmp")
	if err != nil {
		return fmt.Errorf("journal: create temp meta: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has consumed it

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("journal: write temp meta: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("journal: close temp meta: %w", err)
	}
	if err := os.Rename(tmpName, j.metaPath()); err != nil {
		return fmt.Errorf("journal: rename meta: %w", err)
	}
	return nil
}

// AcquireWriteLock takes an exclusive, non-blocking flock on write.lock. On
// success it records the caller pid in the file and returns a release func that
// unlocks and closes the descriptor. When another writer holds the lock it
// returns an error that wraps ErrLocked (message carries the holder pid when
// readable). The flock is tied to this open file description, so a second
// AcquireWriteLock in the same process — using a distinct descriptor — still
// conflicts.
func (j *Journal) AcquireWriteLock() (release func(), err error) {
	f, err := os.OpenFile(j.lockPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("journal: open write.lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			if pid, ok := LockHolder(j.Dir); ok {
				return nil, fmt.Errorf("journal: write.lock held by pid %d: %w", pid, ErrLocked)
			}
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("journal: flock write.lock: %w", err)
	}

	// Record our pid so a blocked writer can report the holder. Best effort:
	// failure to write the pid never fails the acquisition.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	}

	var once bool
	release = func() {
		if once {
			return
		}
		once = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

// LockHolder reads the pid recorded in a terminal's write.lock. ok is false
// when the file is absent or does not contain a pid. This never touches the
// flock, so it is safe to call while the lock is held elsewhere.
func LockHolder(dir string) (pid int, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, false
	}
	pid, err = strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return pid, true
}

// sendLockWait bounds how long AcquireSendLock will keep trying. A send is a
// handful of tmux calls, so a holder that has not finished in this long is
// wedged rather than busy, and reporting that beats blocking a caller forever.
// A var so the contention test does not have to spend it.
var sendLockWait = 2 * time.Second

// sendLockPoll is the retry interval inside sendLockWait. flock has no timed
// form, so the wait is a bounded retry of the non-blocking one.
const sendLockPoll = 10 * time.Millisecond

// AcquireSendLock serializes input delivery to one terminal.
//
// This is deliberately NOT the write lock. The write lock says "a command is
// running here", which is exactly when an interactive answer most needs to get
// through — send has to work against a held write lock or a handoff could never
// be answered. What send must not do is interleave with another send: two
// writers pushing keystrokes at one pane produce a line that is neither's, and
// an agent driving another agent's terminal UI while a human types into the
// same pane is now an ordinary situation rather than a pathological one.
//
// It waits rather than failing fast, because the thing it waits for is always
// short. A caller that cannot get it inside sendLockWait gets ErrLocked and the
// holder's pid.
func (j *Journal) AcquireSendLock() (release func(), err error) {
	f, err := os.OpenFile(j.sendLockPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("journal: open send.lock: %w", err)
	}
	deadline := time.Now().Add(sendLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("journal: flock send.lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			if pid, ok := sendLockHolder(j.Dir); ok {
				return nil, fmt.Errorf("journal: send.lock held by pid %d: %w", pid, ErrLocked)
			}
			return nil, ErrLocked
		}
		time.Sleep(sendLockPoll)
	}

	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	}
	var once bool
	release = func() {
		if once {
			return
		}
		once = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return release, nil
}

// sendLockHolder reads the pid recorded in a terminal's send.lock, for the
// error message only. Like LockHolder it never touches the flock.
func sendLockHolder(dir string) (pid int, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, sendLockName))
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, err == nil && pid > 0
}
