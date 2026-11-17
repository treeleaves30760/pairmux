package cli

import (
	"reflect"
	"testing"

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
