package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/state"
)

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("j\x1b[A\x1b[B\r\x7f\x1b\x1b[5~\x03é\x1b[Z\t"))
	var names []string
	for _, k := range got {
		if k.name == "rune" {
			names = append(names, string(k.r))
		} else {
			names = append(names, k.name)
		}
	}
	want := []string{"j", "up", "down", "enter", "backspace", "esc", "pgup", "ctrl-c", "é", "backtab", "tab"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("keys = %v\nwant   %v", names, want)
	}
	if k := parseKeys([]byte{0x1b}); len(k) != 1 || k[0].name != "esc" {
		t.Fatalf("lone ESC = %v", k)
	}
}

func node(path string, depth int, last bool, child bool) treeNode {
	n := treeNode{Path: path, Depth: depth, Last: last, Status: core.StatusIdle}
	n.Term = state.Terminal{Name: path[strings.LastIndex(path, "/")+1:], Alive: true}
	if child {
		n.Child = "sock-" + path
	}
	return n
}

func TestVisibleDrawsTheTreeAndFolds(t *testing.T) {
	a := &tuiApp{collapsed: map[string]bool{}, nodes: []treeNode{
		node("boss", 0, false, true),
		node("boss/a", 1, false, true),
		node("boss/a/x", 2, true, false),
		node("boss/b", 1, true, false),
		node("solo", 0, true, false),
	}}
	var got []string
	for _, r := range a.visible() {
		got = append(got, r.prefix+r.node.Term.Name)
	}
	want := []string{"boss", "├─ a", "│  └─ x", "└─ b", "solo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tree = %q\nwant   %q", got, want)
	}
	a.collapsed["boss/a"] = true
	if n := len(a.visible()); n != 4 {
		t.Fatalf("folded boss/a: %d rows, want 4", n)
	}
	a.collapsed["boss"] = true
	if n := len(a.visible()); n != 2 {
		t.Fatalf("folded boss: %d rows, want 2", n)
	}
	a.filter = "x"
	if v := a.visible(); len(v) != 1 || v[0].node.Path != "boss/a/x" {
		t.Fatalf("filter x = %+v", v)
	}
}

func TestClipAndPadCountCells(t *testing.T) {
	if got := clip("hello world", 6); got != "hello…" {
		t.Fatalf("clip = %q", got)
	}
	if got := clip("中文字", 5); strWidth(got) > 5 || !strings.HasSuffix(got, "…") {
		t.Fatalf("wide clip = %q (%d cells)", got, strWidth(got))
	}
	if got := pad("ab", 4); got != "ab  " {
		t.Fatalf("pad = %q", got)
	}
}

func TestLayerSocket(t *testing.T) {
	a := &tuiApp{c: newTestCtx(nil, true)}
	root := a.c.Mux.SocketName()
	if s, _ := a.layerSocket(""); s != root {
		t.Fatalf("top = %q", s)
	}
	if s, _ := a.layerSocket("boss/w"); s != state.ChildSocket(state.ChildSocket(root, "boss"), "w") {
		t.Fatalf("boss/w = %q", s)
	}
	if _, err := a.layerSocket("../x"); err == nil {
		t.Fatal("../x accepted")
	}
}
