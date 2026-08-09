package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// startGate holds a program terminal's command until its journal is wired.
//
// tmux starts a pane's command as soon as the window exists, which is strictly
// before pipe-pane can be attached to it. Whatever the program prints in that
// window is not journalled and cannot be recovered, so a program whose first
// act is to prompt — ssh, a login flow, anything asking a question up front —
// can be invisible to pairmux for its whole life. A FIFO the pane blocks on
// turns that race into an ordering: the command cannot start until the caller
// says so, and the caller says so once the journal is capturing.
type startGate struct{ path string }

// newStartGate creates the FIFO inside the terminal's own directory, so it is
// cleaned up with the terminal even if this process dies before releasing it.
func newStartGate(dir string) (*startGate, error) {
	path := filepath.Join(dir, ".start-gate")
	_ = os.Remove(path) // a leftover from a crashed create would never be read
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		return nil, fmt.Errorf("start gate: %w", err)
	}
	return &startGate{path: path}, nil
}

func (g *startGate) Path() string {
	if g == nil {
		return ""
	}
	return g.path
}

// release lets the command start. Opening a FIFO for writing fails with ENXIO
// while no reader has it open, so this retries briefly: the pane is being
// spawned concurrently and may not have reached its read yet. A pane that never
// arrives — it died, or tmux failed to run the holder — is reported rather than
// waited on forever, because the alternative is a terminal that hangs silently.
func (g *startGate) release() error {
	if g == nil {
		return nil
	}
	defer os.Remove(g.path)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(g.path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_, werr := f.WriteString("go\n")
			cerr := f.Close()
			if werr != nil {
				return fmt.Errorf("start gate: %w", werr)
			}
			return cerr
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("start gate: the pane never reached it: %w", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// abandon drops the FIFO without releasing it, for a creation that failed
// before the pane was usable. The pane is killed by the caller's cleanup; the
// holder's read ends when its terminal goes.
func (g *startGate) abandon() {
	if g != nil {
		_ = os.Remove(g.path)
	}
}
