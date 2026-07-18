package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	maxChildStdoutBytes = 1 << 20
	maxChildStderrBytes = 64 << 10
	childTerminateGrace = 250 * time.Millisecond
	childPipeWaitDelay  = 250 * time.Millisecond
)

// Execution is the captured result of one pairmux child process. A non-zero
// ExitCode is not itself an execution failure: pairmux uses it alongside a
// valid pairmux.v1 error envelope.
type Execution struct {
	Stdout          []byte
	Stderr          []byte
	ExitCode        int
	StdoutTruncated bool
	StderrTruncated bool
}

// Executor runs the pairmux argv built by a tool. Implementations receive only
// arguments; the executable path is deliberately kept outside the tool layer.
type Executor interface {
	Execute(context.Context, []string) (Execution, error)
}

// SubprocessExecutor invokes Path directly with an argv array. It never uses a
// shell, so tool argument boundaries are preserved exactly.
type SubprocessExecutor struct {
	Path string
	Env  []string
}

// Execute runs the configured pairmux executable in its own process group and
// captures both streams within fixed bounds. Context cancellation terminates
// the whole group, including grandchildren, before returning.
func (e SubprocessExecutor) Execute(ctx context.Context, argv []string) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return Execution{}, err
	}
	overflow := make(chan struct{})
	var overflowOnce sync.Once
	signalOverflow := func() { overflowOnce.Do(func() { close(overflow) }) }
	stdout := newBoundedBufferWithOverflow(maxChildStdoutBytes, signalOverflow)
	stderr := newBoundedBufferWithOverflow(maxChildStderrBytes, signalOverflow)
	cmd := exec.Command(e.Path, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = childPipeWaitDelay
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if e.Env != nil {
		cmd.Env = e.Env
	} else {
		cmd.Env = os.Environ()
	}

	if err := cmd.Start(); err != nil {
		return Execution{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var (
		err        error
		terminated bool
	)
	select {
	case err = <-done:
	case <-ctx.Done():
		terminated = true
	case <-overflow:
		terminated = true
	}
	if terminated {
		err = terminateAndWait(cmd.Process.Pid, done)
	}

	result := executionFromBuffers(stdout, stderr, err)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if terminated {
		return result, nil
	}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return result, nil
	}
	return result, err
}

func terminateAndWait(pid int, done <-chan error) error {
	terminateProcessGroup(pid, syscall.SIGTERM)
	timer := time.NewTimer(childTerminateGrace)
	var err error
	select {
	case err = <-done:
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
		terminateProcessGroup(pid, syscall.SIGKILL)
		err = <-done
	}
	// The group leader may exit on SIGTERM while a grandchild ignores it.
	// A final group kill prevents that survivor from becoming an orphan.
	terminateProcessGroup(pid, syscall.SIGKILL)
	return err
}

func executionFromBuffers(stdout, stderr *boundedBuffer, err error) Execution {
	result := Execution{
		Stdout:          append([]byte(nil), stdout.Bytes()...),
		Stderr:          append([]byte(nil), stderr.Bytes()...),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result
}

func terminateProcessGroup(pid int, signal syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, signal)
	}
}

type boundedBuffer struct {
	buffer       bytes.Buffer
	limit        int
	truncated    bool
	onOverflow   func()
	overflowOnce sync.Once
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func newBoundedBufferWithOverflow(limit int, onOverflow func()) *boundedBuffer {
	return &boundedBuffer{limit: limit, onOverflow: onOverflow}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		if original > 0 {
			b.markTruncated()
		}
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.markTruncated()
	}
	_, _ = b.buffer.Write(p)
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedBuffer) String() string { return b.buffer.String() }

func (b *boundedBuffer) Truncated() bool { return b.truncated }

func (b *boundedBuffer) markTruncated() {
	b.truncated = true
	b.overflowOnce.Do(func() {
		if b.onOverflow != nil {
			b.onOverflow()
		}
	})
}
