package tmux

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExtraArgv(t *testing.T) {
	tests := []struct {
		name string
		call func(c *Client) error
		want []string
	}{
		{
			name: "SelectWindow",
			call: func(c *Client) error { return c.SelectWindow("t1") },
			want: []string{"tmux", "-L", "pairmux", "select-window", "-t", "pairmux:t1"},
		},
		{
			name: "SelectWindow name with dash and underscore",
			call: func(c *Client) error { return c.SelectWindow("build_2-x") },
			want: []string{"tmux", "-L", "pairmux", "select-window", "-t", "pairmux:build_2-x"},
		},
		{
			name: "KillServer",
			call: func(c *Client) error { return c.KillServer() },
			want: []string{"tmux", "-L", "pairmux", "kill-server"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{outs: []string{""}}
			if err := tt.call(newClient(r)); err != nil {
				t.Fatalf("call error: %v", err)
			}
			if len(r.calls) != 1 {
				t.Fatalf("got %d calls, want 1", len(r.calls))
			}
			if !reflect.DeepEqual(r.calls[0], tt.want) {
				t.Errorf("argv = %v, want %v", r.calls[0], tt.want)
			}
		})
	}
}

func TestHasSession(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"present", nil, true},
		{"absent", errors.New("no server running on /tmp/tmux-501/pairmux"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{errs: []error{tt.err}}
			if got := newClient(r).HasSession(); got != tt.want {
				t.Errorf("HasSession() = %v, want %v", got, tt.want)
			}
			want := []string{"tmux", "-L", "pairmux", "has-session", "-t", "pairmux"}
			if !reflect.DeepEqual(r.calls[0], want) {
				t.Errorf("argv = %v, want %v", r.calls[0], want)
			}
		})
	}
}

func TestExtraErrorPropagation(t *testing.T) {
	tests := []struct {
		name string
		call func(c *Client) error
	}{
		{"SelectWindow", func(c *Client) error { return c.SelectWindow("t1") }},
		{"KillServer", func(c *Client) error { return c.KillServer() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recRunner{errs: []error{errors.New("target not found")}}
			err := tt.call(newClient(r))
			if err == nil || !strings.Contains(err.Error(), "target not found") {
				t.Fatalf("err = %v, want it to carry runner error", err)
			}
		})
	}
}
