package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/output"
)

func TestDiffFleet(t *testing.T) {
	prev := map[string]output.TerminalRow{
		"a":    {Name: "a", Status: "running", CurrentCmd: "make", LastActivity: "t1"},
		"a/b":  {Name: "b", Status: "idle"},
		"gone": {Name: "gone", Status: "idle"},
	}
	cur := map[string]output.TerminalRow{
		"a":   {Name: "a", Status: "awaiting-input", CurrentCmd: "make", LastActivity: "t2"},
		"a/b": {Name: "b", Status: "idle", LastActivity: "t9"}, // output alone is not a change
		"new": {Name: "new", Status: "idle"},
	}
	evs := diffFleet(prev, cur, []string{"a", "a/b", "new"})
	var got []string
	for _, e := range evs {
		got = append(got, e.Type+" "+e.Path)
	}
	if want := []string{"changed a", "added new", "removed gone"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if e := evs[0]; e.From != "running" || !reflect.DeepEqual(e.Changes, []string{"status"}) || e.Terminal.Status != "awaiting-input" {
		t.Fatalf("changed a = %+v", e)
	}
	if evs[2].Terminal != nil {
		t.Fatal("a removal carries no row")
	}
}

func TestParseStatusFilter(t *testing.T) {
	only, canon, ok := parseStatusFilter(" awaiting-input, dead,dead ,")
	if !ok || canon != "awaiting-input,dead" || len(only) != 2 || !only["dead"] {
		t.Fatalf("got %v %q %v", only, canon, ok)
	}
	if _, _, ok := parseStatusFilter("busy"); ok {
		t.Fatal("unknown status accepted")
	}
	if only, canon, ok := parseStatusFilter(""); !ok || canon != "" || len(only) != 0 {
		t.Fatalf("empty filter: %v %q %v", only, canon, ok)
	}
}

// How the reply depends on why events --wait stopped.
func TestEmitFleetChangesStatus(t *testing.T) {
	changed := []output.Event{{Type: "changed", Path: "w", Terminal: &output.TerminalRow{Name: "w", Status: "idle"}}}
	for _, tc := range []struct {
		reason   string
		evs      []output.Event
		status   string
		nextHas  string
		nextLack string
	}{
		{"timeout", nil, "timeout", "--timeout 20s", ""},
		{"interrupted", nil, "interrupted", "events --wait --tree", "--timeout"},
		{"parent-exited", nil, "interrupted", "process that started it exited", "--timeout"},
		{"interrupted", changed, "changed", "keep watching", "--timeout"},
		{"max", changed, "changed", "keep watching", "--timeout"},
	} {
		var buf bytes.Buffer
		c := newTestCtx(&buf, true)
		if rc := c.emitFleetChanges(tc.evs, true, "", 10*time.Second, tc.reason); rc != 0 {
			t.Fatalf("%s: rc = %d", tc.reason, rc)
		}
		e := decode(t, &buf)
		next := strings.Join(e.Next, "\n")
		if e.Status != tc.status || !strings.Contains(next, tc.nextHas) || tc.nextLack != "" && strings.Contains(next, tc.nextLack) {
			t.Errorf("%s with %d events: status %q next %q", tc.reason, len(tc.evs), e.Status, e.Next)
		}
	}
}
