package backend

import (
	"testing"

	"github.com/treeleaves30760/pairmux/internal/mux"
)

func TestChoose(t *testing.T) {
	defer func(l func() bool, r, tr func(string) bool, tv func(string) (string, error), g string) {
		lookTmux, builtinRunning, tmuxRunning, tmuxVersion, goos = l, r, tr, tv, g
	}(lookTmux, builtinRunning, tmuxRunning, tmuxVersion, goos)
	cases := []struct {
		name        string
		env         string
		goos        string
		tmux        bool
		running     bool
		tmuxRunning bool
		version     string
		want        string
		wantErr     bool
	}{
		{"auto prefers installed tmux", "", "linux", true, false, false, "3.4", mux.BackendTmux, false},
		{"auto without tmux uses builtin", "", "darwin", false, false, false, "", mux.BackendBuiltin, false},
		{"running builtin server sticks", "", "linux", true, true, false, "3.4", mux.BackendBuiltin, false},
		{"both servers running keeps tmux", "", "linux", true, true, true, "3.4", mux.BackendTmux, false},
		{"tmux older than 3.2 uses builtin", "", "linux", true, false, false, "3.1c", mux.BackendBuiltin, false},
		{"tmux 2.x uses builtin", "", "linux", true, false, false, "2.7", mux.BackendBuiltin, false},
		{"next build is new enough", "", "linux", true, false, false, "next-3.4", mux.BackendTmux, false},
		{"unreadable version keeps tmux", "", "linux", true, false, false, "master", mux.BackendTmux, false},
		{"windows is always builtin", "", "windows", true, false, false, "3.4", mux.BackendBuiltin, false},
		{"explicit tmux", "tmux", "linux", false, true, false, "", mux.BackendTmux, false},
		{"explicit builtin", "BUILTIN", "linux", true, false, false, "3.4", mux.BackendBuiltin, false},
		{"auto keyword", "auto", "linux", true, false, false, "3.4", mux.BackendTmux, false},
		{"tmux refused on windows", "tmux", "windows", true, false, false, "", "", true},
		{"unknown value", "screen", "linux", true, false, false, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(Env, c.env)
			goos = c.goos
			tm, run, trun, ver := c.tmux, c.running, c.tmuxRunning, c.version
			lookTmux = func() bool { return tm }
			builtinRunning = func(string) bool { return run }
			tmuxRunning = func(string) bool { return trun }
			tmuxVersion = func(string) (string, error) { return ver, nil }
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
