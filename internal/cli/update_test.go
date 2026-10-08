package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/version"
)

type updateTestCall struct {
	path      string
	args, env []string
}

type updateFixture struct {
	root, toolRoot, environment, binary, binDir, entrypoint, uv string
	listing                                                     string
	nextVersion                                                 string
	calls                                                       []updateTestCall
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	return newUpdateFixtureAt(t, filepath.Join(t.TempDir(), "custom roots 'and spaces"))
}

func newUpdateFixtureAt(t *testing.T, root string) *updateFixture {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &updateFixture{root: root, toolRoot: filepath.Join(root, "uv tools 'custom"),
		binDir: filepath.Join(root, "bin 'custom"), uv: filepath.Join(root, "uv"), nextVersion: "0.6.0"}
	f.environment = filepath.Join(f.toolRoot, "pairmux")
	f.binary = filepath.Join(f.environment, "bin", "pairmux")
	f.entrypoint = filepath.Join(f.binDir, "pairmux")
	for _, dir := range []string{filepath.Dir(f.binary), f.binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeUpdateTestFile(t, f.binary, []byte("old binary"), 0o700)
	writeUpdateTestFile(t, f.uv, []byte("uv fixture"), 0o700)
	if err := os.Symlink(f.binary, f.entrypoint); err != nil {
		t.Fatal(err)
	}
	f.writeReceipt(t, "0.5.3")
	f.listing = f.makeListing("0.5.3")
	return f
}

func writeUpdateTestFile(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatal(err)
	}
}

// This is real uv TOML, not the install.sh mock's plain-path "receipt". It
// deliberately records the old pin and sources that install must not inherit.
func (f *updateFixture) writeReceipt(t *testing.T, v string) {
	t.Helper()
	b := fmt.Sprintf(`[tool]
requirements = [{ name = "pairmux", specifier = "==%s" }]
constraints = [{ name = "pairmux", specifier = "==%s" }]
entrypoints = [
    { name = "pairmux", install-path = %q, from = "pairmux" },
]

[tool.options]
index-url = "https://old.invalid/simple"
extra-index-url = ["https://extra.invalid/simple"]
find-links = ["/old/wheels"]
`, v, v, f.entrypoint)
	writeUpdateTestFile(t, filepath.Join(f.environment, "uv-receipt.toml"), []byte(b), 0o600)
}

func (f *updateFixture) makeListing(v string) string {
	return fmt.Sprintf("other-tool v1.2.3 (%s)\n- other-tool (%s)\npairmux v%s (%s)\n- pairmux (%s)\nthird.tool v2024.10 (%s)\n- third-cli (%s)\n",
		filepath.Join(f.toolRoot, "other-tool"), filepath.Join(f.binDir, "other-tool"), v, f.environment, f.entrypoint,
		filepath.Join(f.toolRoot, "third.tool"), filepath.Join(f.binDir, "third-cli"))
}

func (f *updateFixture) ops(t *testing.T) updateOps {
	t.Helper()
	return updateOps{
		executable: func() (string, error) { return f.binary, nil },
		lookPath: func(name string) (string, error) {
			if name != "uv" {
				t.Fatalf("unexpected PATH discovery %q", name)
			}
			return f.uv, nil
		},
		run: func(path string, args, env []string) (updateRunResult, error) {
			f.calls = append(f.calls, updateTestCall{path, append([]string(nil), args...), append([]string(nil), env...)})
			switch {
			case path == f.uv && len(args) >= 2 && args[0] == "tool" && args[1] == "list":
				return updateRunResult{stdout: []byte(f.listing)}, nil
			case path == f.uv && len(args) >= 2 && args[0] == "tool" && args[1] == "install":
				// uv replaces the tool environment; the old in-memory stamp is
				// intentionally not changed by this fake installer.
				if err := os.Remove(f.binary); err != nil {
					t.Fatal(err)
				}
				writeUpdateTestFile(t, f.binary, []byte("new binary"), 0o700)
				f.writeReceipt(t, f.nextVersion)
				f.listing = f.makeListing(f.nextVersion)
				return updateRunResult{stdout: []byte("installer stdout\n"), stderr: []byte("installer stderr\n")}, nil
			case path == f.binary && reflect.DeepEqual(args, []string{"--json", "version"}):
				return updateTestVersionReply(t, f.nextVersion), nil
			default:
				t.Fatalf("unexpected subprocess %q %q", path, args)
				return updateRunResult{}, errors.New("unexpected subprocess")
			}
		},
	}
}

func updateTestVersionReply(t *testing.T, v string) updateRunResult {
	t.Helper()
	b, err := json.Marshal(output.Envelope{Schema: core.SchemaID, OK: true, Status: "ok", Output: v})
	if err != nil {
		t.Fatal(err)
	}
	return updateRunResult{stdout: append(b, '\n')}
}

func assertUpdateError(t *testing.T, rc int, buf *bytes.Buffer) output.Envelope {
	t.Helper()
	if rc != 1 {
		t.Fatalf("rc=%d, want 1: %s", rc, buf.String())
	}
	e := decode(t, buf)
	if e.OK || e.Status != "error" || e.Error == nil || e.Error.Code != output.CodeUpdate || e.Error.Hint == "" || len(e.Next) != 1 || e.Next[0] != e.Error.Hint {
		t.Fatalf("invalid E_UPDATE envelope: %+v", e)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("JSON must be a single envelope: %q", buf.String())
	}
	return e
}

func TestUpdateSuccessVersions(t *testing.T) {
	for _, tt := range []struct {
		old, next, status string
	}{
		{"0.5.3", "0.6.0", "updated"},
		{"0.6.0", "0.6.0", "refreshed"},
		{"0.6.0-alpha.0", "0.6.0", "updated"},
		{"0.6.0-beta.9", "0.6.0", "updated"},
		{"0.6.0-rc.12", "0.7.0", "updated"},
		{"v0.6.0", "0.6.0", "refreshed"},
		{"999999999999999999999999.9.9", "1000000000000000000000000.0.0", "updated"},
	} {
		t.Run(tt.old+" to "+tt.next, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.nextVersion = tt.next
			beforeStamp := version.Version
			var buf bytes.Buffer
			// No tmux client or state directory exists: update must not consult them.
			c := &Ctx{JSON: true, Stdout: &buf}
			if rc := c.cmdUpdateWith(nil, tt.old, f.ops(t)); rc != 0 {
				t.Fatalf("rc=%d: %s", rc, buf.String())
			}
			e := decode(t, &buf)
			wantOutput := fmt.Sprintf("pairmux %s -> %s\nsource: https://pypi.org/simple\ninstalled: %s", tt.old, tt.next, f.entrypoint)
			if !e.OK || e.Schema != core.SchemaID || e.Status != tt.status || e.Output != wantOutput || e.Error != nil {
				t.Fatalf("envelope=%+v; want %q %q", e, tt.status, wantOutput)
			}
			if strings.Count(buf.String(), "\n") != 1 || strings.Contains(buf.String(), "installer stdout") || strings.Contains(buf.String(), "installer stderr") {
				t.Fatalf("subprocess output leaked into JSON: %q", buf.String())
			}
			if version.Version != beforeStamp {
				t.Fatal("update changed the old process's linker stamp")
			}
			if len(f.calls) != 4 {
				t.Fatalf("calls=%+v, want list/install/list/version", f.calls)
			}
			if !reflect.DeepEqual(f.calls[0].args, []string{"tool", "list", "--no-config", "--show-paths", "--color", "never"}) ||
				!reflect.DeepEqual(f.calls[2].args, f.calls[0].args) || f.calls[3].path != f.binary ||
				!reflect.DeepEqual(f.calls[3].args, []string{"--json", "version"}) {
				t.Fatalf("unexpected discovery/version calls: %+v", f.calls)
			}
			old, _ := parseUpdateVersion(tt.old)
			wantInstall := []string{"tool", "install", "--no-config", "--default-index", "https://pypi.org/simple", "--no-sources", "--no-build",
				"--python", ">=3.9", "--upgrade", "--reinstall", "--no-cache", "--prerelease", "disallow", "pairmux>=" + old.core()}
			if !reflect.DeepEqual(f.calls[1].args, wantInstall) {
				t.Fatalf("install argv=%q, want %q", f.calls[1].args, wantInstall)
			}
		})
	}
}

func TestUpdateHumanOutput(t *testing.T) {
	f := newUpdateFixture(t)
	var buf bytes.Buffer
	c := &Ctx{Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", f.ops(t)); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	want := fmt.Sprintf("updated\npairmux 0.5.3 -> 0.6.0\nsource: https://pypi.org/simple\ninstalled: %s\n", f.entrypoint)
	if buf.String() != want {
		t.Fatalf("human output=%q, want %q", buf.String(), want)
	}
}

func TestUpdateRejectsArgumentsBeforeAnyOps(t *testing.T) {
	for _, args := range [][]string{
		{"pairmux"}, {"other"}, {"--version", "0.6.0"}, {"--all"}, {"--dry-run"}, {"--help"}, {"--force"}, {"--index", "https://bad.invalid"},
		{"--"}, {"--", "--json"}, {"-"}, {"--unknown=value"}, {"update"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			// Nil operations would panic if usage reached discovery.
			if rc := c.cmdUpdateWith(args, "0.5.3", updateOps{}); rc != 2 {
				t.Fatalf("rc=%d: %s", rc, buf.String())
			}
			e := decode(t, &buf)
			if e.OK || e.Error == nil || e.Error.Code != output.CodeBadArgs || strings.Count(buf.String(), "\n") != 1 {
				t.Fatalf("usage envelope=%+v", e)
			}
		})
	}
}

func TestUpdateRejectsUnsupportedRunningVersionsBeforeOps(t *testing.T) {
	for _, v := range []string{"", "dev", "0.6.0-dev", "0.6.1-snapshot-deadbeef", "0.6.0+build.1", "01.2.3", "1.02.3", "1.2.03", "1.2.3-rc.01", "1.2.3-preview.1", "1.2", "1.2.3.4", "1.2.3\n", "vv1.2.3", "1.2.3-rc.-1"} {
		t.Run(v, func(t *testing.T) {
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, v, updateOps{}), &buf)
			if !strings.Contains(e.Error.Message, "not a canonical release") {
				t.Fatalf("message=%q", e.Error.Message)
			}
		})
	}
}

func TestUpdateVersionComparisonNoOverflow(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		cmp  int
	}{
		{"1.2.3", "1.2.3", 0}, {"1.10.0", "1.9.100", 1}, {"0.6.0-rc.1", "0.5.99", 1},
		{"99999999999999999999999999999.0.0", "100000000000000000000000000000.0.0", -1},
		{"1.2.99999999999999999999999999999", "1.2.100000000000000000000000000000", -1},
	} {
		a, err := parseUpdateVersion(tt.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := parseUpdateVersion(tt.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := compareUpdateCore(a, b); got != tt.cmp {
			t.Fatalf("compare(%s,%s)=%d, want %d", tt.a, tt.b, got, tt.cmp)
		}
	}
}

func TestUpdateNoDowngradeOrUnstableReplacement(t *testing.T) {
	for _, tt := range []struct{ old, next string }{
		{"0.6.0", "0.5.99"}, {"0.6.0-rc.1", "0.5.99"}, {"1.10.0", "1.9.999999999999999999999999999"},
		{"0.5.3", "0.6.0-rc.1"}, {"0.5.3", "0.6.0-alpha.1"}, {"0.5.3", "0.6.0-dev"},
		{"0.5.3", "0.6.0+build.1"}, {"0.5.3", "00.6.0"}, {"0.5.3", "unexpected"},
	} {
		t.Run(tt.old+" to "+tt.next, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.nextVersion = tt.next
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, tt.old, f.ops(t)), &buf)
			if !strings.Contains(e.Error.Message, "not a stable release at or above") || !strings.Contains(e.Error.Hint, "does not perform automatic rollback") {
				t.Fatalf("error=%+v", e.Error)
			}
			if len(f.calls) != 4 {
				t.Fatalf("calls=%+v, unexpected recovery action", f.calls)
			}
			b, err := os.ReadFile(f.binary)
			if err != nil || string(b) != "new binary" {
				t.Fatal("post-failure must not claim or attempt rollback")
			}
		})
	}
}

func TestUpdateEnvironmentAndSourcePollution(t *testing.T) {
	f := newUpdateFixture(t)
	pollution := map[string]string{
		"UV_INDEX": "https://private.invalid", "UV_DEFAULT_INDEX": "https://private.invalid", "UV_INDEX_URL": "https://private.invalid",
		"UV_EXTRA_INDEX_URL": "https://extra.invalid", "UV_FIND_LINKS": "/untrusted/wheels", "UV_CONSTRAINT": "pairmux==0.5.3",
		"UV_OVERRIDE": "pairmux==0.5.3", "UV_CONFIG_FILE": "/untrusted/uv.toml", "UV_OFFLINE": "1", "UV_NATIVE_TLS": "1",
		"UV_INSECURE_HOST": "private.invalid", "UV_PYTHON_INSTALL_MIRROR": "https://mirror.invalid", "UV_PYTHON": "/untrusted/python",
		"UV_TOOL_DIR": "/untrusted/tools", "UV_TOOL_BIN_DIR": "/untrusted/bin", "UV_FUTURE_UNRECOGNIZED_OPTION": "bad",
		"CARGO_DIST_MIRROR": "https://mirror.invalid", "CARGO_DIST_NEW": "bad", "INSTALLER_MIRROR": "https://mirror.invalid", "CARGO_HOME": "/untrusted/cargo",
	}
	for k, v := range pollution {
		t.Setenv(k, v)
	}
	t.Setenv("HOME", f.root)
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:8080")
	t.Setenv("NO_PROXY", "localhost")
	t.Setenv("PAIRMUX_SOCKET", "invalid/unusable socket")
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", f.ops(t)); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	for i, call := range f.calls {
		values := map[string]string{}
		for _, kv := range call.env {
			k, v, _ := strings.Cut(kv, "=")
			if k != "UV_TOOL_DIR" && k != "UV_TOOL_BIN_DIR" {
				if _, polluted := pollution[k]; polluted || strings.HasPrefix(k, "UV_") || strings.HasPrefix(k, "INSTALLER_") || strings.HasPrefix(k, "CARGO_DIST_") {
					t.Fatalf("call %d inherited pollution %q", i, kv)
				}
			}
			values[k] = v
		}
		if values["UV_TOOL_DIR"] != f.toolRoot || i > 0 && values["UV_TOOL_BIN_DIR"] != f.binDir || i == 0 && values["UV_TOOL_BIN_DIR"] != "" {
			t.Fatalf("call %d directories=%v", i, values)
		}
		if values["HOME"] != f.root || values["PATH"] != os.Getenv("PATH") || values["HTTPS_PROXY"] != "http://proxy.invalid:8080" || values["NO_PROXY"] != "localhost" {
			t.Fatalf("call %d did not preserve generic environment", i)
		}
	}
}

func TestUpdateNonUVOwnershipFailsBeforeUVDiscovery(t *testing.T) {
	for _, kind := range []string{"manual", "homebrew", "rpm", "pipx", "uvx", "wrong binary basename", "missing binary"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, kind, "bin", "pairmux")
			if kind == "uvx" {
				path = filepath.Join(root, "uv", "cache", "archive-v0", "random", "bin", "pairmux")
			}
			if kind == "wrong binary basename" {
				path = filepath.Join(root, "pairmux", "bin", "renamed")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if kind != "missing binary" {
				writeUpdateTestFile(t, path, []byte("manual binary"), 0o700)
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			ops := updateOps{executable: func() (string, error) { return path, nil }}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if !strings.Contains(e.Error.Message, "cannot verify a persistent uv-owned") || e.Error.Hint != updateOwnershipHint {
				t.Fatalf("error=%+v", e.Error)
			}
			if strings.Contains(strings.ToLower(e.Error.Hint), kind+" install") {
				t.Fatal("hint guessed the package manager")
			}
		})
	}
}

func TestUpdateExecutableDiscoveryFailure(t *testing.T) {
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	ops := updateOps{executable: func() (string, error) { return "", errors.New("no executable") }}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
	if !strings.Contains(e.Error.Message, "cannot locate the running executable") {
		t.Fatalf("message=%q", e.Error.Message)
	}
}

func TestUpdateReceiptRefusalsBeforeUVDiscovery(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "directory", "empty", "large", "backslash", "triple basic", "triple literal", "nul", "bare carriage return", "escape", "invalid UTF8", "bidi", "unicode line separator"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateFixture(t)
			path := filepath.Join(f.environment, "uv-receipt.toml")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			var b []byte
			switch kind {
			case "missing":
			case "symlink":
				target := filepath.Join(f.root, "real-receipt")
				writeUpdateTestFile(t, target, []byte("[tool]\n"), 0o600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "empty":
				b = []byte{}
			case "large":
				b = bytes.Repeat([]byte("x"), updateReceiptLimit+1)
			case "backslash":
				b = []byte("[tool]\n# escaped \\n\n")
			case "triple basic":
				b = []byte("[tool]\nvalue = \"\"\"multiline\nvalue\"\"\"\n")
			case "triple literal":
				b = []byte("[tool]\nvalue = '''multiline\nvalue'''\n")
			case "nul":
				b = []byte("[tool]\n#\x00")
			case "bare carriage return":
				b = []byte("[tool]\rvalue=1\n")
			case "escape":
				b = []byte("[tool]\n# \x1b[31m")
			case "invalid UTF8":
				b = []byte{'[', 't', 'o', 'o', 'l', ']', '\n', 0xff}
			case "bidi":
				b = []byte("[tool]\n# \u202eunsafe\n")
			case "unicode line separator":
				b = []byte("[tool]\n# \u2028unsafe\n")
			}
			if b != nil {
				writeUpdateTestFile(t, path, b, 0o600)
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			ops := updateOps{executable: func() (string, error) { return f.binary, nil }}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if !strings.Contains(e.Error.Message, "uv-receipt.toml") {
				t.Fatalf("message=%q", e.Error.Message)
			}
		})
	}
}

func TestUpdateReceiptAllowsOrdinaryMultilineArrays(t *testing.T) {
	f := newUpdateFixture(t)
	path := filepath.Join(f.environment, "uv-receipt.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("    {"), []byte("\t{"))
	b = bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
	writeUpdateTestFile(t, path, b, 0o600)
	got, err := readUpdateReceipt(path)
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("ordinary multiline array/CRLF refused: %v", err)
	}
}

func TestUpdateMissingUVAndFallback(t *testing.T) {
	f := newUpdateFixture(t)
	t.Setenv("PATH", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	ops := f.ops(t)
	ops.lookPath = exec.LookPath
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
	if e.Error.Message != "uv was not found on PATH or in ~/.local/bin" || e.Error.Hint != updateUVHint || len(f.calls) != 0 {
		t.Fatalf("missing uv error=%+v calls=%+v", e.Error, f.calls)
	}
	path := filepath.Join(home, ".local", "bin", "uv")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writeUpdateTestFile(t, path, []byte("uv fixture"), 0o700)
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveUpdateUV(exec.LookPath, f.environment)
	if err != nil || got != canonical {
		t.Fatalf("fallback=%q %v, want %q", got, err, canonical)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUpdateUV(exec.LookPath, f.environment); err == nil {
		t.Fatal("fallback accepted a non-executable uv")
	}
}

func TestUpdateUVRejectsPATHDotAndInsideEnvironment(t *testing.T) {
	f := newUpdateFixture(t)
	for _, kind := range []string{"dot", "inside", "symlink inside", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(f.root, "uv candidate "+kind)
			look := func(string) (string, error) { return path, nil }
			switch kind {
			case "dot":
				look = func(string) (string, error) { return "uv", &exec.Error{Name: "uv", Err: exec.ErrDot} }
			case "inside", "symlink inside":
				inside := filepath.Join(f.environment, "bin", "uv")
				writeUpdateTestFile(t, inside, []byte("uv fixture"), 0o700)
				if kind == "inside" {
					path = inside
				} else if err := os.Symlink(inside, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			ops := f.ops(t)
			ops.lookPath = look
			assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if len(f.calls) != 0 {
				t.Fatalf("rejected uv was executed: %+v", f.calls)
			}
		})
	}
}

func TestUpdateDoesNotUsePATHPairmux(t *testing.T) {
	f := newUpdateFixture(t)
	shadowDir := filepath.Join(f.root, "shadow")
	if err := os.Mkdir(shadowDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeUpdateTestFile(t, filepath.Join(shadowDir, "pairmux"), []byte("do not execute"), 0o700)
	if err := os.Symlink(f.uv, filepath.Join(shadowDir, "uv")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shadowDir)
	ops := f.ops(t)
	ops.lookPath = exec.LookPath
	ops.executable = func() (string, error) { return f.entrypoint, nil }
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", ops); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	for _, call := range f.calls {
		if call.path != f.uv && call.path != f.binary {
			t.Fatalf("PATH shadow was used: %+v", call)
		}
	}
}

func TestUpdateListingStrictGrammar(t *testing.T) {
	f := newUpdateFixture(t)
	pair := fmt.Sprintf("pairmux v0.5.3 (%s)\n- pairmux (%s)\n", f.environment, f.entrypoint)
	for _, tt := range []struct{ name, listing string }{
		{"missing block", fmt.Sprintf("other v1.0 (%s)\n- other (%s)\n", f.environment, f.entrypoint)},
		{"missing entry", fmt.Sprintf("pairmux v0.5.3 (%s)\n", f.environment)},
		{"duplicate block", pair + pair},
		{"duplicate entry", pair + fmt.Sprintf("- pairmux (%s)\n", f.entrypoint)},
		{"extra entry", pair + fmt.Sprintf("- extra (%s)\n", filepath.Join(f.binDir, "extra"))},
		{"improper command name", strings.Replace(pair, "- pairmux (", "- fake (", 1)},
		{"improper path basename", strings.Replace(pair, f.entrypoint, filepath.Join(f.binDir, "fake"), 1)},
		{"wrong block name", strings.Replace(pair, "pairmux v", "pairmux-other v", 1)},
		{"wrong block environment", strings.Replace(pair, f.environment+")", f.root+")", 1)},
		{"malformed header", strings.Replace(pair, " v0.5.3 ", " 0.5.3 ", 1)},
		{"unknown continuation", pair + "  continued receipt path\n"},
		{"unknown banner", "Installed tools:\n" + pair},
		{"orphan entry", fmt.Sprintf("- pairmux (%s)\n", f.entrypoint) + pair},
		{"blank line", pair + "\n"},
		{"malformed other header", pair + "other invalid\n"},
		{"malformed other entry", pair + fmt.Sprintf("other v1.0 (%s)\n- other\n", f.root)},
		{"duplicate other block", f.listing + fmt.Sprintf("other-tool v1.2.3 (%s)\n", f.root)},
		{"duplicate other entry", f.listing + fmt.Sprintf("- third-cli (%s)\n", filepath.Join(f.binDir, "another"))},
		{"duplicate recorded path", f.listing + fmt.Sprintf("- additional (%s)\n", f.entrypoint)},
		{"relative recorded path", strings.Replace(pair, f.entrypoint, "relative/pairmux", 1)},
		{"unclean recorded path", strings.Replace(pair, f.entrypoint, f.binDir+"/../pairmux", 1)},
		{"backslash", strings.Replace(pair, f.entrypoint, f.binDir+"/escaped\\n/pairmux", 1)},
		{"carriage return", strings.ReplaceAll(pair, "\n", "\r\n")},
		{"nul", pair + "\x00"},
		{"invalid UTF8", pair + string([]byte{0xff})},
		{"oversized", pair + strings.Repeat("x", updateOutputLimit)},
		{"empty", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseUpdateListing([]byte(tt.listing), f.environment); err == nil {
				t.Fatalf("unsafe listing accepted: %q", tt.listing)
			}
		})
	}
	if entry, err := parseUpdateListing([]byte(f.listing), f.environment); err != nil || entry != f.entrypoint {
		t.Fatalf("real-format complete listing rejected: %q %v", entry, err)
	}
	if entry, err := parseUpdateListing([]byte(strings.TrimSuffix(f.listing, "\n")), f.environment); err != nil || entry != f.entrypoint {
		t.Fatalf("listing without trailing newline rejected: %q %v", entry, err)
	}
}

func TestUpdateRejectsUnverifiedEntrypoints(t *testing.T) {
	for _, kind := range []string{"missing", "manual regular file", "wrong symlink", "broken symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateFixture(t)
			if err := os.Remove(f.entrypoint); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "manual regular file":
				writeUpdateTestFile(t, f.entrypoint, []byte("manual replacement"), 0o700)
			case "wrong symlink":
				other := filepath.Join(f.root, "other binary")
				writeUpdateTestFile(t, other, []byte("manual replacement"), 0o700)
				if err := os.Symlink(other, f.entrypoint); err != nil {
					t.Fatal(err)
				}
			case "broken symlink":
				if err := os.Symlink(filepath.Join(f.root, "missing"), f.entrypoint); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", f.ops(t)), &buf)
			if len(f.calls) != 1 || f.calls[0].args[1] != "list" {
				t.Fatalf("unsafe entrypoint reached install: %+v", f.calls)
			}
		})
	}
}

func TestUpdateRelativeSymlink(t *testing.T) {
	f := newUpdateFixture(t)
	if err := os.Remove(f.entrypoint); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(f.binDir, f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, f.entrypoint); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", f.ops(t)); rc != 0 {
		t.Fatalf("relative symlink refused: rc=%d %s", rc, buf.String())
	}
}

func TestUpdateMacOSTmpAliases(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS /tmp is an alias of /private/tmp")
	}
	root, err := os.MkdirTemp("/tmp", "pairmux-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := newUpdateFixtureAt(t, root)
	if !strings.HasPrefix(f.root, "/private/tmp/") {
		t.Skip("/tmp is not a symlink here")
	}
	alias := func(s string) string { return strings.TrimPrefix(s, "/private") }
	f.entrypoint = alias(f.entrypoint)
	f.writeReceipt(t, "0.5.3")
	f.listing = fmt.Sprintf("pairmux v0.5.3 (%s)\n- pairmux (%s)\n", alias(f.environment), f.entrypoint)
	ops := f.ops(t)
	ops.executable = func() (string, error) { return alias(f.binary), nil }
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", ops); rc != 0 {
		t.Fatalf("/tmp alias refused: rc=%d %s", rc, buf.String())
	}
	if !strings.Contains(decode(t, &buf).Output, "installed: "+f.entrypoint) {
		t.Fatal("recorded /tmp entrypoint was not retained")
	}
	if !strings.Contains(strings.Join(f.calls[1].env, "\n"), "UV_TOOL_BIN_DIR="+alias(f.binDir)) {
		t.Fatal("custom recorded alias bin directory was not retained")
	}
}

func TestUpdateRechecksReceiptAndBinaryAfterListing(t *testing.T) {
	for _, kind := range []string{"receipt bytes", "receipt symlink", "binary replacement", "entrypoint replacement", "entrypoint wrong link"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateFixture(t)
			ops := f.ops(t)
			run := ops.run
			ops.run = func(path string, args, env []string) (updateRunResult, error) {
				result, err := run(path, args, env)
				if len(f.calls) == 1 {
					receipt := filepath.Join(f.environment, "uv-receipt.toml")
					switch kind {
					case "receipt bytes":
						f.writeReceipt(t, "0.4.0")
					case "receipt symlink":
						if err := os.Remove(receipt); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(f.binary, receipt); err != nil {
							t.Fatal(err)
						}
					case "binary replacement":
						if err := os.Remove(f.binary); err != nil {
							t.Fatal(err)
						}
						writeUpdateTestFile(t, f.binary, []byte("manually replaced binary"), 0o700)
					case "entrypoint replacement", "entrypoint wrong link":
						if err := os.Remove(f.entrypoint); err != nil {
							t.Fatal(err)
						}
						if kind == "entrypoint replacement" {
							writeUpdateTestFile(t, f.entrypoint, []byte("manual replacement"), 0o700)
						} else if err := os.Symlink(f.uv, f.entrypoint); err != nil {
							t.Fatal(err)
						}
					}
				}
				return result, err
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if len(f.calls) != 1 {
				t.Fatalf("changed ownership reached install: %+v", f.calls)
			}
		})
	}
}

func TestUpdateRecheckRefusesMovedBinaryBehindSymlink(t *testing.T) {
	f := newUpdateFixture(t)
	installation, err := locateUpdateInstallation(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := installation.inspect(f.uv, nil, f.ops(t).run); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(f.root, "manually-moved-binary")
	if err := os.Rename(f.binary, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, f.binary); err != nil {
		t.Fatal(err)
	}
	if err := installation.recheck(); err == nil || !strings.Contains(err.Error(), "binary changed") {
		t.Fatalf("moved binary behind a symlink was accepted: %v", err)
	}
}

func TestUpdatePreInstallRecheckChangedLink(t *testing.T) {
	f := newUpdateFixture(t)
	installation, err := locateUpdateInstallation(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := installation.inspect(f.uv, nil, f.ops(t).run); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.entrypoint); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(f.binDir, f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, f.entrypoint); err != nil {
		t.Fatal(err)
	}
	// Even a different raw target reaching the same binary is a changed link.
	if err := installation.recheck(); err == nil || !strings.Contains(err.Error(), "symlink changed") {
		t.Fatalf("changed raw link was accepted: %v", err)
	}
}

func TestUpdateSubprocessFailuresAndOversizedListing(t *testing.T) {
	for _, kind := range []string{"TOML parse failure", "list failure", "oversized listing", "install failure", "post list failure", "version failure"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateFixture(t)
			if kind == "TOML parse failure" {
				writeUpdateTestFile(t, filepath.Join(f.environment, "uv-receipt.toml"), []byte("[tool]\nrequirements = invalid TOML\n"), 0o600)
			}
			ops := f.ops(t)
			run := ops.run
			ops.run = func(path string, args, env []string) (updateRunResult, error) {
				phase := len(f.calls)
				fail := (kind == "list failure" || kind == "TOML parse failure") && phase == 0 || kind == "install failure" && phase == 1 ||
					kind == "post list failure" && phase == 2 || kind == "version failure" && phase == 3
				if fail {
					f.calls = append(f.calls, updateTestCall{path, args, env})
					return updateRunResult{stdout: []byte("subprocess stdout\n"), stderr: []byte("actionable failure: disk full\n")}, errors.New("exit status 1")
				}
				if kind == "oversized listing" && phase == 0 {
					f.calls = append(f.calls, updateTestCall{path, args, env})
					return updateRunResult{stdout: []byte(f.listing), stdoutTruncated: true}, nil
				}
				return run(path, args, env)
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if kind != "oversized listing" && (!strings.Contains(e.Error.Message, "actionable failure: disk full") || !strings.Contains(e.Error.Message, "exit status 1")) {
				t.Fatalf("failure diagnostic missing: %+v", e.Error)
			}
			wantCalls := map[string]int{"TOML parse failure": 1, "list failure": 1, "oversized listing": 1, "install failure": 2, "post list failure": 3, "version failure": 4}[kind]
			if len(f.calls) != wantCalls {
				t.Fatalf("calls=%+v, want %d; no recovery subprocess is allowed", f.calls, wantCalls)
			}
		})
	}
}

func TestUpdatePostInstallOwnershipFailures(t *testing.T) {
	for _, kind := range []string{"missing receipt", "unsafe receipt", "extra entrypoint", "wrong environment", "environment moved behind symlink", "manual entrypoint", "different bin directory", "receipt changes during post list", "binary changes during version", "link changes during version"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateFixture(t)
			ops := f.ops(t)
			run := ops.run
			ops.run = func(path string, args, env []string) (updateRunResult, error) {
				result, err := run(path, args, env)
				if len(f.calls) == 2 {
					receipt := filepath.Join(f.environment, "uv-receipt.toml")
					switch kind {
					case "missing receipt":
						if err := os.Remove(receipt); err != nil {
							t.Fatal(err)
						}
					case "unsafe receipt":
						writeUpdateTestFile(t, receipt, []byte("[tool]\n# escaped \\n\n"), 0o600)
					case "extra entrypoint":
						f.listing = fmt.Sprintf("pairmux v0.6.0 (%s)\n- pairmux (%s)\n- extra (%s)\n", f.environment, f.entrypoint, filepath.Join(f.binDir, "extra"))
					case "wrong environment":
						f.listing = strings.Replace(f.listing, f.environment+")", f.root+")", 1)
					case "environment moved behind symlink":
						movedRoot := filepath.Join(f.root, "moved-tools")
						if err := os.Mkdir(movedRoot, 0o700); err != nil {
							t.Fatal(err)
						}
						movedEnv := filepath.Join(movedRoot, "pairmux")
						if err := os.Rename(f.environment, movedEnv); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(movedEnv, f.environment); err != nil {
							t.Fatal(err)
						}
					case "manual entrypoint":
						if err := os.Remove(f.entrypoint); err != nil {
							t.Fatal(err)
						}
						writeUpdateTestFile(t, f.entrypoint, []byte("manual replacement"), 0o700)
					case "different bin directory":
						otherDir := filepath.Join(f.root, "other-bin")
						if err := os.Mkdir(otherDir, 0o700); err != nil {
							t.Fatal(err)
						}
						other := filepath.Join(otherDir, "pairmux")
						if err := os.Symlink(f.binary, other); err != nil {
							t.Fatal(err)
						}
						f.listing = strings.ReplaceAll(f.listing, f.entrypoint, other)
					}
				}
				if len(f.calls) == 3 && kind == "receipt changes during post list" {
					f.writeReceipt(t, "0.4.0")
				}
				if len(f.calls) == 4 {
					switch kind {
					case "binary changes during version":
						writeUpdateTestFile(t, f.binary, []byte("changed again"), 0o700)
					case "link changes during version":
						if err := os.Remove(f.entrypoint); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(f.uv, f.entrypoint); err != nil {
							t.Fatal(err)
						}
					}
				}
				return result, err
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if e.Error.Hint != updateRepairHint {
				t.Fatalf("post-install repair hint=%q", e.Error.Hint)
			}
			installs := 0
			for _, call := range f.calls {
				if call.path == f.uv && len(call.args) > 1 && call.args[1] == "install" {
					installs++
				}
			}
			if installs != 1 {
				t.Fatalf("unexpected recovery or repeated install: %+v", f.calls)
			}
		})
	}
}

func TestUpdateVersionEnvelopeValidation(t *testing.T) {
	good := `{"schema":"pairmux.v1","ok":true,"status":"ok","output":"0.6.0"}`
	for _, reply := range []string{
		"0.6.0\n", "banner\n" + good, good + "\n" + good, good + " trailing", "{}", "[]", "null",
		strings.Replace(good, "pairmux.v1", "other.v1", 1), strings.Replace(good, "true", "false", 1), strings.Replace(good, `"status":"ok"`, `"status":"done"`, 1),
		strings.Replace(good, `"ok":true`, `"ok":"true"`, 1), strings.Replace(good, `"output":"0.6.0"`, `"output":6`, 1),
		strings.Replace(good, `"output":"0.6.0"`, `"output":"0.6.0","output":"0.7.0"`, 1),
		strings.Replace(good, `"output":"0.6.0"`, `"output":"0.6.0","unexpected":1`, 1),
		strings.Replace(good, `"status":"ok",`, "", 1), string([]byte{0xff}), strings.Repeat(" ", updateOutputLimit+1) + good,
	} {
		if v, err := parseUpdateVersionReply(updateRunResult{stdout: []byte(reply)}); err == nil {
			t.Fatalf("invalid envelope accepted as %q: %q", v, reply)
		}
	}
	if _, err := parseUpdateVersionReply(updateRunResult{stdout: []byte(good), stdoutTruncated: true}); err == nil {
		t.Fatal("truncated JSON output accepted")
	}
	if v, err := parseUpdateVersionReply(updateRunResult{stdout: []byte(good + "\n")}); err != nil || v != "0.6.0" {
		t.Fatalf("ordinary envelope rejected: %q %v", v, err)
	}
}

func TestUpdateInvalidVersionEnvelopeOperationalError(t *testing.T) {
	f := newUpdateFixture(t)
	ops := f.ops(t)
	run := ops.run
	ops.run = func(path string, args, env []string) (updateRunResult, error) {
		result, err := run(path, args, env)
		if len(f.calls) == 4 {
			result.stdout = []byte(`{"schema":"pairmux.v1","ok":true,"status":"ok","output":"0.6.0"}` + "\nnoise\n")
		}
		return result, err
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
}

func TestUpdateCommandCaptureDrainsBothStreams(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "drained")
	env := append(os.Environ(), "PAIRMUX_UPDATE_TEST_CHILD=drain", "PAIRMUX_UPDATE_TEST_MARKER="+marker)
	result, err := runUpdateCommand(path, []string{"-test.run=^TestUpdateCommandHelper$"}, env)
	if err != nil || len(result.stdout) != updateOutputLimit || len(result.stderr) != updateOutputLimit || !result.stdoutTruncated || !result.stderrTruncated {
		t.Fatalf("capture=%d/%d truncated=%v/%v err=%v", len(result.stdout), len(result.stderr), result.stdoutTruncated, result.stderrTruncated, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("child did not finish draining both streams: %v", err)
	}
	env = append(os.Environ(), "PAIRMUX_UPDATE_TEST_CHILD=fail")
	result, err = runUpdateCommand(path, []string{"-test.run=^TestUpdateCommandHelper$"}, env)
	if err == nil {
		t.Fatal("child failure was hidden")
	}
	message := updateCommandFailure("uv tool install", result, err)
	if !strings.Contains(message, "exit status 17") || !strings.Contains(message, "disk full") {
		t.Fatalf("failure message=%q", message)
	}
}

func TestUpdateCommandHelper(t *testing.T) {
	switch os.Getenv("PAIRMUX_UPDATE_TEST_CHILD") {
	case "drain":
		chunk := bytes.Repeat([]byte("x"), 8192)
		for i := 0; i < 128; i++ {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(20)
			}
			if _, err := os.Stderr.Write(chunk); err != nil {
				os.Exit(21)
			}
		}
		if err := os.WriteFile(os.Getenv("PAIRMUX_UPDATE_TEST_MARKER"), []byte("done"), 0o600); err != nil {
			os.Exit(22)
		}
		os.Exit(0)
	case "fail":
		_, _ = io.WriteString(os.Stderr, "disk full\n")
		os.Exit(17)
	}
}

func TestUpdateDiagnosticsTruncateWithoutLeakingRawControls(t *testing.T) {
	result := updateRunResult{stdout: append([]byte("stdout\n\x1b"), bytes.Repeat([]byte("x"), updateOutputLimit)...),
		stderr: append([]byte("stderr\n\x00"), bytes.Repeat([]byte("y"), updateOutputLimit)...), stdoutTruncated: true, stderrTruncated: true}
	message := updateCommandFailure("uv tool install", result, errors.New("exit status 1"))
	if len(message) > 2*updateDiagnosticCap+256 || !strings.Contains(message, "stdout truncated") || !strings.Contains(message, "stderr truncated") || strings.ContainsAny(message, "\n\x1b\x00") {
		t.Fatalf("diagnostic was not bounded/safe: len=%d %q", len(message), message[:min(len(message), 128)])
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	assertUpdateError(t, c.fail(output.CodeUpdate, message, updateRepairHint), &buf)
}
