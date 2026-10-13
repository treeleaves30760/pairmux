package backend

import (
	"testing"

	"github.com/treeleaves30760/pairmux/internal/mux"
)

func TestChoose(t *testing.T) {
	defer func(l func() bool, r func(string) bool, g string) { lookTmux, builtinRunning, goos = l, r, g }(lookTmux, builtinRunning, goos)
	cases := []struct {
		name    string
		env     string
		goos    string
		tmux    bool
		running bool
		want    string
		wantErr bool
	}{
		{"auto prefers installed tmux", "", "linux", true, false, mux.BackendTmux, false},
		{"auto without tmux uses builtin", "", "darwin", false, false, mux.BackendBuiltin, false},
		{"running builtin server sticks", "", "linux", true, true, mux.BackendBuiltin, false},
		{"windows is always builtin", "", "windows", true, false, mux.BackendBuiltin, false},
		{"explicit tmux", "tmux", "linux", false, true, mux.BackendTmux, false},
		{"explicit builtin", "BUILTIN", "linux", true, false, mux.BackendBuiltin, false},
		{"auto keyword", "auto", "linux", true, false, mux.BackendTmux, false},
		{"tmux refused on windows", "tmux", "windows", true, false, "", true},
		{"unknown value", "screen", "linux", true, false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(Env, c.env)
			goos = c.goos
			tm, run := c.tmux, c.running
			lookTmux = func() bool { return tm }
			builtinRunning = func(string) bool { return run }
			got, err := Choose("pairmux")
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got.Kind != c.want {
				t.Fatalf("kind = %q, want %q (reason %q)", got.Kind, c.want, got.Reason)
			}
		})
	}
}

func TestOpenKind(t *testing.T) {
	if b := OpenKind(mux.BackendBuiltin, "x").Backend(); b != mux.BackendBuiltin {
		t.Fatalf("builtin kind opened %q", b)
	}
	if b := OpenKind(mux.BackendTmux, "x").Backend(); b != mux.BackendTmux {
		t.Fatalf("tmux kind opened %q", b)
	}
}
