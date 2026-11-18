package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// These tests run uv's Windows tool layout (updateGOOS = "windows") on every
// platform, with real files in the platform's own path syntax. The fake uv
// mimics uv's Windows `tool install --reinstall`: remove the receipt's
// entrypoints (warning only), recreate the environment, copy
// Scripts\pairmux.exe into the bin directory, and refuse an existing target.

type updateWinFixture struct {
	root, toolRoot, environment, binary, binDir, entrypoint, uv string
	listing, nextVersion                                        string
	oldBytes, newBytes                                          []byte
	calls                                                       []updateTestCall
	// installHook runs at the start of the fake install; a non-nil error
	// fails the install before uv changes anything.
	installHook func() error
	// failAfterDelete makes the fake install fail once the environment and
	// entrypoint are gone and a new entrypoint was written.
	failAfterDelete bool
}

func newUpdateWinFixture(t *testing.T, oldBytes []byte) *updateWinFixture {
	t.Helper()
	withUpdateGOOS(t, "windows")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, "custom roots 'and spaces")
	f := &updateWinFixture{root: root, toolRoot: filepath.Join(root, "uv tools 'custom"), binDir: filepath.Join(root, "bin 'custom"),
		uv: filepath.Join(root, "uv.exe"), nextVersion: "0.6.0", oldBytes: oldBytes, newBytes: []byte("new binary")}
	f.environment = filepath.Join(f.toolRoot, "pairmux")
	f.binary = filepath.Join(f.environment, "Scripts", "pairmux.exe")
	f.entrypoint = filepath.Join(f.binDir, "pairmux.exe")
	for _, dir := range []string{filepath.Dir(f.binary), f.binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeUpdateTestFile(t, f.binary, oldBytes, 0o700)
	writeUpdateTestFile(t, f.entrypoint, oldBytes, 0o700) // uv copies, never links, on Windows
	writeUpdateTestFile(t, f.uv, []byte("uv fixture"), 0o700)
	f.writeReceipt(t, "0.5.3")
	f.listing = f.makeListing("0.5.3")
	return f
}

// uv writes install-path with forward slashes (PortablePath) on Windows.
func (f *updateWinFixture) writeReceipt(t *testing.T, v string) {
	t.Helper()
	b := fmt.Sprintf("[tool]\nrequirements = [{ name = \"pairmux\", specifier = \"==%s\" }]\nentrypoints = [\n    { name = \"pairmux.exe\", install-path = %q, from = \"pairmux\" },\n]\n",
		v, filepath.ToSlash(f.entrypoint))
	writeUpdateTestFile(t, filepath.Join(f.environment, "uv-receipt.toml"), []byte(b), 0o600)
}

// uv lists both paths natively and names the entrypoint after its file.
func (f *updateWinFixture) makeListing(v string) string {
	return fmt.Sprintf("other-tool v1.2.3 (%s)\n- other-tool.exe (%s)\npairmux v%s (%s)\n- pairmux.exe (%s)\n",
		filepath.Join(f.toolRoot, "other-tool"), filepath.Join(f.binDir, "other-tool.exe"), v, f.environment, f.entrypoint)
}

func (f *updateWinFixture) ops(t *testing.T) updateOps {
	t.Helper()
	return updateOps{
		executable: func() (string, error) { return f.entrypoint, nil },
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
				return f.install(t)
			case path == f.binary && reflect.DeepEqual(args, []string{"--json", "version"}):
				return updateTestVersionReply(t, f.nextVersion), nil
			default:
				t.Fatalf("unexpected subprocess %q %q", path, args)
				return updateRunResult{}, errors.New("unexpected subprocess")
			}
		},
	}
}

func (f *updateWinFixture) install(t *testing.T) (updateRunResult, error) {
	t.Helper()
	if f.installHook != nil {
		if err := f.installHook(); err != nil {
			return updateRunResult{stderr: []byte(err.Error())}, errors.New("exit status 2")
		}
	}
	_ = os.Remove(f.entrypoint) // uv only warns when this fails
	if err := os.RemoveAll(f.environment); err != nil {
		return updateRunResult{stderr: []byte("remove environment: " + err.Error())}, errors.New("exit status 2")
	}
	if err := os.MkdirAll(filepath.Dir(f.binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.binary, f.newBytes, 0o700); err != nil {
		return updateRunResult{stderr: []byte(err.Error())}, errors.New("exit status 2")
	}
	if _, err := os.Lstat(f.entrypoint); err == nil {
		return updateRunResult{stderr: []byte("Executable already exists: pairmux.exe (use `--force` to overwrite)")}, errors.New("exit status 2")
	}
	if err := os.WriteFile(f.entrypoint, f.newBytes, 0o700); err != nil {
		return updateRunResult{stderr: []byte("Failed to install entrypoint: " + err.Error())}, errors.New("exit status 2")
	}
	if f.failAfterDelete {
		_ = os.RemoveAll(f.environment)
		return updateRunResult{stderr: []byte("failed late")}, errors.New("exit status 2")
	}
	f.writeReceipt(t, f.nextVersion)
	f.listing = f.makeListing(f.nextVersion)
	return updateRunResult{stderr: []byte("Installed 1 executable: pairmux.exe\n")}, nil
}

func (f *updateWinFixture) aside(dir string) string {
	return filepath.Join(dir, "pairmux.exe.old-"+strconv.Itoa(os.Getpid()))
}

func updateTestEnv(env []string) map[string]string {
	values := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		values[k] = v
	}
	return values
}

func assertUpdateFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("%s = %q, %v; want %q", path, b, err, want)
	}
}

func assertUpdateMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist: %v", path, err)
	}
}

func TestUpdateWindowsCopiedEntrypointSuccess(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	t.Setenv("UV_TOOL_DIR", filepath.Join(f.root, "untrusted tools"))
	t.Setenv("UV_TOOL_BIN_DIR", filepath.Join(f.root, "untrusted bin"))
	t.Setenv("UV_INDEX_URL", "https://private.invalid")
	f.installHook = func() error {
		// uv must find neither original: both were moved aside, on their volumes.
		assertUpdateMissing(t, f.entrypoint)
		assertUpdateMissing(t, f.binary)
		assertUpdateFileBytes(t, f.aside(f.binDir), f.oldBytes)
		assertUpdateFileBytes(t, f.aside(f.toolRoot), f.oldBytes)
		return nil
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", f.ops(t)); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	e := decode(t, &buf)
	want := fmt.Sprintf("pairmux 0.5.3 -> 0.6.0\nsource: https://pypi.org/simple\ninstalled: %s", f.entrypoint)
	if !e.OK || e.Status != "updated" || e.Output != want {
		t.Fatalf("envelope=%+v", e)
	}
	if len(f.calls) != 5 {
		t.Fatalf("calls=%+v, want discovery/list/install/list/version", f.calls)
	}
	for i, call := range f.calls {
		values := updateTestEnv(call.env)
		if values["UV_INDEX_URL"] != "" {
			t.Fatalf("call %d inherited ambient uv source settings", i)
		}
		switch i {
		case 0: // Discovery: uv's default tool directory, nothing ambient.
			if _, ok := values["UV_TOOL_DIR"]; ok {
				t.Fatalf("discovery used a tool directory: %v", values["UV_TOOL_DIR"])
			}
			if _, ok := values["UV_TOOL_BIN_DIR"]; ok {
				t.Fatal("discovery used a bin directory")
			}
		default:
			if values["UV_TOOL_DIR"] != f.toolRoot || i > 1 && values["UV_TOOL_BIN_DIR"] != f.binDir || i == 1 && values["UV_TOOL_BIN_DIR"] != "" {
				t.Fatalf("call %d directories=%v", i, values)
			}
		}
	}
	if f.calls[4].path != f.binary {
		t.Fatalf("version check ran %q, want the environment's executable", f.calls[4].path)
	}
	assertUpdateFileBytes(t, f.entrypoint, f.newBytes)
	assertUpdateFileBytes(t, f.binary, f.newBytes)
	// Nothing is running here, so both moved-aside files were deleted.
	assertUpdateMissing(t, f.aside(f.binDir))
	assertUpdateMissing(t, f.aside(f.toolRoot))
}

func TestUpdateWindowsRunningFromToolEnvironment(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	ops := f.ops(t)
	ops.executable = func() (string, error) { return f.binary, nil }
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.6.0", ops); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	if e := decode(t, &buf); e.Status != "refreshed" {
		t.Fatalf("envelope=%+v", e)
	}
	// Binary-derived: no discovery listing, exactly as on Unix.
	if len(f.calls) != 4 || updateTestEnv(f.calls[0].env)["UV_TOOL_DIR"] != f.toolRoot {
		t.Fatalf("calls=%+v", f.calls)
	}
}

func TestUpdateWindowsInstallFailureRestoresExecutables(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	before := map[string]os.FileInfo{}
	for _, path := range []string{f.entrypoint, f.binary} {
		info, err := updateLstat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	f.installHook = func() error { return errors.New("No solution found when resolving: pairmux>=0.5.3") }
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", f.ops(t)), &buf)
	if e.Error.Hint != updateRepairHint || !strings.Contains(e.Error.Message, "No solution found") ||
		!strings.Contains(e.Error.Message, "moved back into place") {
		t.Fatalf("error=%+v", e.Error)
	}
	for path, info := range before {
		after, err := updateLstat(path)
		if err != nil || !sameUpdateFile(info, after) {
			t.Fatalf("%s was not restored as the same file: %v", path, err)
		}
	}
	assertUpdateMissing(t, f.aside(f.binDir))
	assertUpdateMissing(t, f.aside(f.toolRoot))
}

func TestUpdateWindowsLateInstallFailureKeepsUVFiles(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	f.failAfterDelete = true
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", f.ops(t)), &buf)
	// uv's new entrypoint is never replaced; the deleted environment cannot
	// take its executable back. Both old copies are named for manual repair.
	if !strings.Contains(e.Error.Message, "uv left a new "+f.entrypoint) || !strings.Contains(e.Error.Message, "could not restore "+f.binary) ||
		!strings.Contains(e.Error.Message, f.aside(f.binDir)) || !strings.Contains(e.Error.Message, f.aside(f.toolRoot)) {
		t.Fatalf("error=%+v", e.Error)
	}
	assertUpdateFileBytes(t, f.entrypoint, f.newBytes)
	assertUpdateFileBytes(t, f.aside(f.binDir), f.oldBytes)
	assertUpdateFileBytes(t, f.aside(f.toolRoot), f.oldBytes)
}

func TestUpdateWindowsMoveAsideFailureRestoresAndSkipsInstall(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	ops := f.ops(t)
	renames := 0
	ops.fs.rename = func(from, to string) error {
		renames++
		if renames == 2 {
			return errors.New("access denied")
		}
		return updateRenameNoReplace(from, to)
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
	if !strings.Contains(e.Error.Message, "cannot move the installed executables aside") || !strings.Contains(e.Error.Message, "access denied") ||
		!strings.Contains(e.Error.Message, "moved back into place") {
		t.Fatalf("error=%+v", e.Error)
	}
	for _, call := range f.calls {
		if call.args[1] == "install" {
			t.Fatal("install ran after a failed move-aside")
		}
	}
	assertUpdateFileBytes(t, f.entrypoint, f.oldBytes)
	assertUpdateFileBytes(t, f.binary, f.oldBytes)
	assertUpdateMissing(t, f.aside(f.binDir))
}

func TestUpdateWindowsRejectsUnverifiedEntrypoints(t *testing.T) {
	for _, kind := range []string{"different bytes", "same size different bytes", "missing", "directory", "symlink", "listed as environment executable", "unsuffixed name", "running copy elsewhere"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateWinFixture(t, []byte("old binary"))
			ops := f.ops(t)
			switch kind {
			case "different bytes":
				writeUpdateTestFile(t, f.entrypoint, []byte("manually installed pairmux"), 0o700)
			case "same size different bytes":
				writeUpdateTestFile(t, f.entrypoint, []byte("old binarx"), 0o700)
			case "missing":
				if err := os.Remove(f.entrypoint); err != nil {
					t.Fatal(err)
				}
				ops.executable = func() (string, error) { return f.binary, nil }
			case "directory":
				if err := os.Remove(f.entrypoint); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.entrypoint, 0o700); err != nil {
					t.Fatal(err)
				}
				ops.executable = func() (string, error) { return f.binary, nil }
			case "symlink":
				if err := os.Remove(f.entrypoint); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.binary, f.entrypoint); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				ops.executable = func() (string, error) { return f.binary, nil }
			case "listed as environment executable":
				f.listing = strings.Replace(f.listing, "- pairmux.exe ("+f.entrypoint, "- pairmux.exe ("+f.binary, 1)
			case "unsuffixed name":
				f.listing = strings.Replace(f.listing, "- pairmux.exe (", "- pairmux (", 1)
				ops.executable = func() (string, error) { return f.binary, nil } // past discovery
			case "running copy elsewhere":
				other := filepath.Join(f.root, "elsewhere", "pairmux.exe")
				if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
					t.Fatal(err)
				}
				writeUpdateTestFile(t, other, f.oldBytes, 0o700)
				ops.executable = func() (string, error) { return other, nil }
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if e.Error.Hint != updateOwnershipHint {
				t.Fatalf("error=%+v", e.Error)
			}
			for _, call := range f.calls {
				if call.args[1] == "install" {
					t.Fatalf("unverified entrypoint reached install: %+v", f.calls)
				}
			}
			assertUpdateFileBytes(t, f.binary, f.oldBytes)
			assertUpdateMissing(t, f.aside(f.binDir))
			assertUpdateMissing(t, f.aside(f.toolRoot))
		})
	}
}

func TestUpdateWindowsDiscoveryFailures(t *testing.T) {
	for _, kind := range []string{"not listed", "environment not named pairmux", "listed environment missing"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateWinFixture(t, []byte("old binary"))
			switch kind {
			case "not listed":
				f.listing = fmt.Sprintf("other-tool v1.2.3 (%s)\n- other-tool.exe (%s)\n", filepath.Join(f.toolRoot, "other-tool"), filepath.Join(f.binDir, "other-tool.exe"))
			case "environment not named pairmux":
				f.listing = strings.Replace(f.listing, f.environment+")", f.toolRoot+")", 1)
			case "listed environment missing":
				f.listing = strings.Replace(f.listing, f.environment+")", filepath.Join(f.root, "gone", "pairmux")+")", 1)
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", f.ops(t)), &buf)
			if e.Error.Hint != updateWindowsDiscoveryHint || len(f.calls) != 1 {
				t.Fatalf("error=%+v calls=%+v", e.Error, f.calls)
			}
		})
	}
}

func TestUpdateWindowsRechecksCopyAfterListing(t *testing.T) {
	for _, kind := range []string{"binary rewritten with same bytes", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			f := newUpdateWinFixture(t, []byte("old binary"))
			ops := f.ops(t)
			run := ops.run
			ops.run = func(path string, args, env []string) (updateRunResult, error) {
				result, err := run(path, args, env)
				if len(f.calls) == 2 {
					switch kind {
					case "binary rewritten with same bytes":
						replaceUpdateTestFile(t, f.binary, f.oldBytes)
					case "receipt":
						f.writeReceipt(t, "0.4.0")
					}
				}
				return result, err
			}
			var buf bytes.Buffer
			c := &Ctx{JSON: true, Stdout: &buf}
			assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
			if len(f.calls) != 2 {
				t.Fatalf("changed installation reached install: %+v", f.calls)
			}
		})
	}
}

// After inspect() compared the bytes, the copy is held to its file identity:
// even an identical new file at the entrypoint is a change.
func TestUpdateWindowsRecheckRefusesReplacedCopy(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	installation, err := locateUpdateInstallation(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := installation.inspect(f.uv, nil, f.ops(t).run); err != nil {
		t.Fatal(err)
	}
	replaceUpdateTestFile(t, f.entrypoint, f.oldBytes)
	if err := installation.recheck(); err == nil || !strings.Contains(err.Error(), "entrypoint changed") {
		t.Fatalf("replaced copy was accepted: %v", err)
	}
}

// replaceUpdateTestFile creates a new file (new identity) with the given bytes.
func replaceUpdateTestFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeUpdateTestFile(t, path, b, 0o700)
}

func TestUpdateWindowsPostInstallCopyMismatch(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	ops := f.ops(t)
	run := ops.run
	ops.run = func(path string, args, env []string) (updateRunResult, error) {
		result, err := run(path, args, env)
		if len(f.calls) == 3 { // after install: a different file at the entrypoint
			writeUpdateTestFile(t, f.entrypoint, []byte("tampered binary"), 0o700)
		}
		return result, err
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	e := assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", ops), &buf)
	if e.Error.Hint != updateRepairHint || !strings.Contains(e.Error.Message, "not a copy") ||
		!strings.Contains(e.Error.Message, "previous executables are kept at") {
		t.Fatalf("error=%+v", e.Error)
	}
	assertUpdateFileBytes(t, f.aside(f.binDir), f.oldBytes)
}

func TestUpdateWindowsCleansStaleAsideFiles(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	stale := []string{filepath.Join(f.binDir, "pairmux.exe.old-123"), filepath.Join(f.toolRoot, "pairmux.exe.old-456")}
	keep := []string{filepath.Join(f.binDir, "pairmux.exe.old-abc"), filepath.Join(f.binDir, "pairmux.exe.old-0"),
		filepath.Join(f.binDir, "other.exe.old-1"), filepath.Join(f.binDir, "pairmux.exe.old-1.exe"), filepath.Join(f.root, "pairmux.exe.old-1")}
	for _, path := range append(append([]string(nil), stale...), keep...) {
		writeUpdateTestFile(t, path, []byte("stale"), 0o700)
	}
	dir := filepath.Join(f.toolRoot, "pairmux.exe.old-789")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keep = append(keep, dir)
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", f.ops(t)); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	for _, path := range stale {
		assertUpdateMissing(t, path)
	}
	for _, path := range keep {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("cleanup removed unrelated %s: %v", path, err)
		}
	}
}

func TestUpdateWindowsCleanupOnlyAfterOwnershipIsVerified(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	stale := filepath.Join(f.binDir, "pairmux.exe.old-123")
	writeUpdateTestFile(t, stale, []byte("stale"), 0o700)
	writeUpdateTestFile(t, f.entrypoint, []byte("manual"), 0o700)
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	assertUpdateError(t, c.cmdUpdateWith(nil, "0.5.3", f.ops(t)), &buf)
	assertUpdateFileBytes(t, stale, []byte("stale"))
}

func TestUpdateSameContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		path := filepath.Join(dir, name)
		writeUpdateTestFile(t, path, b, 0o600)
		return path
	}
	big := bytes.Repeat([]byte("0123456789abcdef"), 20000) // spans several read chunks
	changed := append([]byte(nil), big...)
	changed[len(changed)-1] ^= 1
	a := write("a", big)
	for _, tt := range []struct {
		name string
		b    []byte
		want bool
	}{{"same", big, true}, {"last byte", changed, false}, {"shorter", big[:len(big)-1], false}, {"empty", nil, false}} {
		other := write(tt.name, tt.b)
		info, err := updateLstat(a)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := sameUpdateContent(a, info, other); err != nil || got != tt.want {
			t.Fatalf("%s: got %v, %v", tt.name, got, err)
		}
	}
	info, err := updateLstat(a)
	if err != nil {
		t.Fatal(err)
	}
	replaceUpdateTestFile(t, a, big)
	if _, err := sameUpdateContent(a, info, write("again", big)); err == nil {
		t.Fatal("a replaced file was compared under its old identity")
	}
}

func TestUpdateRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	writeUpdateTestFile(t, from, []byte("from"), 0o600)
	writeUpdateTestFile(t, to, []byte("to"), 0o600)
	if err := updateRenameNoReplace(from, to); err == nil {
		t.Fatal("rename replaced an existing destination")
	}
	assertUpdateFileBytes(t, to, []byte("to"))
	assertUpdateFileBytes(t, from, []byte("from"))
	if err := os.Remove(to); err != nil {
		t.Fatal(err)
	}
	if err := updateRenameNoReplace(from, to); err != nil {
		t.Fatal(err)
	}
	assertUpdateMissing(t, from)
	assertUpdateFileBytes(t, to, []byte("from"))
}
