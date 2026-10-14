//go:build darwin || linux

package pty

import (
	"os"
	"testing"
	"time"
)

func TestDrainEndsDespiteAChattyLeftoverJob(t *testing.T) {
	// The shell exits at once, leaving a background job that ignores SIGHUP
	// and keeps writing for ~4s. EOF must not wait for the job. Both darwin
	// and Linux hang up the terminal when its session leader exits, so the
	// job's writes fail and EOF comes promptly; drainMax is the backstop for
	// output that would otherwise keep arriving.
	script := `(trap '' HUP; i=0; while [ $i -lt 400 ]; do echo spam; sleep 0.01; i=$((i+1)); done) & exit 0`
	p, err := Start(Options{Argv: shellArgv(script), Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	start := time.Now()
	readAll(t, p, 10*time.Second)
	if took := time.Since(start); took > drainMax+time.Second {
		t.Fatalf("drain took %s; want it bounded near %s", took, drainMax)
	}
}
