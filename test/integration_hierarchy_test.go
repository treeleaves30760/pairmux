//go:build integration && !windows

package integration

import (
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/output"
)

// TestAgentHierarchyScopePathsAndReport drives a three-level tree the way
// agents build one — each level runs pairmux inside its own pane — and checks
// the three rules a coordinator relies on: paths reach any descendant from
// above, nothing below can address its owner's layer, and report is the one
// upward channel.
func TestAgentHierarchyScopePathsAndReport(t *testing.T) {
	e := newEnv(t, bashShell)
	pmx1(t, e, "new", "--name", "boss")
	defer pmx(t, e, "kill", "boss")

	inner := func(term, cmd string) output.Envelope {
		t.Helper()
		env, code := pmx(t, e, "run", term, cmd, "--timeout", "60s")
		if code != 0 || env.Status != "done" {
			t.Fatalf("run %s %q: code=%d env=%+v", term, cmd, code, env)
		}
		return env
	}
	bin := binPath + " --json"

	if out := inner("boss", bin+" new --name worker").Output; !strings.Contains(out, `"status":"created"`) {
		t.Fatalf("boss could not create worker: %s", out)
	}
	if out := inner("boss", bin+" run worker '"+bin+" new --name sub' --timeout 60s").Output; !strings.Contains(out, `\"status\":\"created\"`) && !strings.Contains(out, `"created"`) {
		t.Fatalf("worker could not create sub: %s", out)
	}

	// From the top: the whole tree, and a path straight to the grandchild.
	ls := pmx1(t, e, "ls", "--tree")
	var paths []string
	for _, r := range ls.Terminals {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, ",") != "boss,boss/worker,boss/worker/sub" {
		t.Fatalf("ls --tree paths = %v", paths)
	}
	if env := pmx1(t, e, "peek", "boss/worker/sub"); env.Terminal != "boss/worker/sub" {
		t.Fatalf("peek by path: terminal=%q", env.Terminal)
	}

	// From the middle: down is fine, up is refused.
	if out := inner("boss", bin+" peek worker/sub").Output; !strings.Contains(out, `"terminal":"worker/sub"`) {
		t.Fatalf("boss could not reach worker/sub: %s", out)
	}
	out := inner("boss", bin+" run worker '"+bin+" --socket "+e.socket+" ls' --timeout 60s").Output
	if !strings.Contains(out, "E_SCOPE") {
		t.Fatalf("worker reached the top layer: %s", out)
	}
	if out := inner("boss", bin+" --socket "+e.socket+" send boss --text x").Output; !strings.Contains(out, "E_SCOPE") {
		t.Fatalf("boss could drive its own row in the top layer: %s", out)
	}

	// Report is the upward channel: a note on the reporter's own row.
	rep := inner("boss", bin+" report boss-finished")
	if !strings.Contains(rep.Output, `"status":"reported"`) {
		t.Fatalf("report: %s", rep.Output)
	}
	notes := append([]string{}, rep.Notes...)
	notes = append(notes, pmx1(t, e, "peek", "boss").Notes...)
	if !containsLine(notes, "boss-finished") {
		t.Fatalf("report did not reach the owner: notes=%v", notes)
	}

	kill := pmx1(t, e, "kill", "boss")
	if !strings.Contains(strings.Join(kill.Next, "\n"), "nested layer") {
		t.Fatalf("kill did not tear down the tree: %+v", kill.Next)
	}
}
