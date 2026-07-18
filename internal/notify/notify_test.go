package notify

import (
	"reflect"
	"testing"
)

func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"double quote", `say "hi"`, `say \"hi\"`},
		{"backslash", `a\b`, `a\\b`},
		{"backslash then quote", `\"`, `\\\"`},
		{"quote then backslash", `"\`, `\"\\`},
		{"many quotes", `"""`, `\"\"\"`},
		{"already escaped-looking", `a\"b`, `a\\\"b`},
		{"unicode untouched", "héllo — ✓", "héllo — ✓"},
		{"single quotes untouched", "it's 'fine'", "it's 'fine'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeAppleScript(tt.in); got != tt.want {
				t.Fatalf("escapeAppleScript(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFlattenNewlines(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"none", "abc", "abc"},
		{"lf", "a\nb", "a b"},
		{"crlf", "a\r\nb", "a b"},
		{"cr", "a\rb", "a b"},
		{"mixed", "a\nb\r\nc\rd", "a b c d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flattenNewlines(tt.in); got != tt.want {
				t.Fatalf("flattenNewlines(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestArgvFor(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		title   string
		body    string
		want    []string
		wantErr bool
	}{
		{
			name: "darwin plain", goos: "darwin",
			title: "pairmux", body: "terminal t1 needs your attention",
			want: []string{"osascript", "-e",
				`display notification "terminal t1 needs your attention" with title "pairmux"`},
		},
		{
			name: "darwin escapes quotes and backslashes", goos: "darwin",
			title: `ti"tle`, body: `bo\dy "x"`,
			want: []string{"osascript", "-e",
				`display notification "bo\\dy \"x\"" with title "ti\"tle"`},
		},
		{
			name: "darwin flattens newlines", goos: "darwin",
			title: "pairmux", body: "line1\nline2",
			want: []string{"osascript", "-e",
				`display notification "line1 line2" with title "pairmux"`},
		},
		{
			name: "linux argv untouched", goos: "linux",
			title: `ti"tle`, body: `bo\dy`,
			want: []string{"notify-send", `ti"tle`, `bo\dy`},
		},
		{name: "windows unsupported", goos: "windows", wantErr: true},
		{name: "plan9 unsupported", goos: "plan9", wantErr: true},
		{name: "empty goos unsupported", goos: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := argvFor(tt.goos, tt.title, tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("argvFor(%q) err = nil, want error", tt.goos)
				}
				return
			}
			if err != nil {
				t.Fatalf("argvFor(%q): %v", tt.goos, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("argvFor(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}
