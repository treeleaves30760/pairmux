//go:build uvintegration

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/treeleaves30760/pairmux/internal/core"
)

const updateUVIntegrationHelper = "PAIRMUX_UPDATE_UV_INTEGRATION_HELPER"

// This suite is deliberately opt-in. uv and Python must already be installed;
// every package and interpreter used below is local, and all writable state is
// confined to canonical temporary directories. The 0.5.3 fixture is today's
// code with an old linker stamp, since the actual 0.5.3 release had no update.
func TestUpdateUVIntegration(t *testing.T) {
	if config := os.Getenv(updateUVIntegrationHelper); config != "" {
		runUpdateUVIntegrationHelper(t, config)
		return
	}

	uv := updateUVIntegrationTool(t, "PAIRMUX_TEST_UV")
	python := updateUVIntegrationTool(t, "PAIRMUX_TEST_PYTHON")
	goPath, err := exec.LookPath("go") // Snapshot before installing any PATH decoys.
	if err != nil {
		t.Fatalf("find native Go toolchain: %v", err)
	}
	goPath = updateUVIntegrationCanonical(t, goPath)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Clean(filepath.Join(cwd, "..", ".."))
	module, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil || !bytes.HasPrefix(module, []byte("module github.com/treeleaves30760/pairmux\n")) {
		t.Fatalf("cannot locate repository from %q: %v", cwd, err)
	}

	root := filepath.Join(updateUVIntegrationCanonical(t, t.TempDir()), "offline fixtures 'with spaces")
	env := updateUVIntegrationIsolate(t, root)
	uvVersion := updateUVIntegrationCommand(t, repo, env, uv, "--version")
	if !strings.HasPrefix(string(uvVersion), "uv ") {
		t.Fatalf("PAIRMUX_TEST_UV is not a functioning uv: %q", uvVersion)
	}
	pythonVersion := updateUVIntegrationCommand(t, repo, env, python, "-I", "-c",
		"import sys; assert sys.version_info >= (3, 9); print(sys.version)")
	t.Logf("real tools: %s; Python %s", strings.TrimSpace(string(uvVersion)), strings.TrimSpace(string(pythonVersion)))

	// Preserve the existing module cache, but give builds their own cache, HOME,
	// temp and telemetry directories. Never permit a toolchain/module download.
	cacheEnv := updateUVIntegrationReplaceEnv(os.Environ(), map[string]string{
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
		"GOTELEMETRYDIR": filepath.Join(root, "telemetry"),
	})
	moduleCache := strings.TrimSpace(string(updateUVIntegrationCommand(t, repo, cacheEnv, goPath, "env", "GOMODCACHE")))
	buildEnv := updateUVIntegrationReplaceEnv(env, map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0",
		"GOENV": "off", "GOFLAGS": "", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH,
		"GO386": "", "GOAMD64": "", "GOARM": "", "GOARM64": "", "GOMIPS": "", "GOMIPS64": "",
		"GOPPC64": "", "GORISCV64": "", "GOWASM": "", "GOEXPERIMENT": "",
		"GOMODCACHE": moduleCache, "GOCACHE": filepath.Join(root, "go build cache"),
		"GOTELEMETRYDIR": filepath.Join(root, "telemetry"),
	})
	seed := filepath.Join(root, "seed wheels 'old source")
	candidate := filepath.Join(root, "candidate wheels 'new source")
	rejected := filepath.Join(root, "ineligible wheels 'floor and prerelease")
	for _, dir := range []string{seed, candidate, rejected} {
		updateUVIntegrationMkdir(t, dir)
	}
	binaries := make(map[string]string)
	for _, v := range []string{"0.5.3", "0.6.0", "0.7.0-rc.1"} {
		binary := filepath.Join(root, "native builds", v, "pairmux")
		updateUVIntegrationMkdir(t, filepath.Dir(binary))
		updateUVIntegrationCommand(t, repo, buildEnv, goPath, "build", "-trimpath", "-ldflags",
			"-X github.com/treeleaves30760/pairmux/internal/version.Version="+v, "-o", binary, "./cmd/pairmux")
		binaries[v] = binary
		out := candidate
		if v == "0.5.3" {
			out = seed
		}
		updateUVIntegrationCommand(t, repo, env, python, "-I", filepath.Join(repo, "packaging", "pypi", "build_wheels.py"),
			"--version", v, "--binary", binary, "--platform", runtime.GOOS+"_"+runtime.GOARCH, "--out-dir", out, "--check")
	}
	updateUVIntegrationCopyWheel(t, candidate, seed, "0.6.0")
	updateUVIntegrationCopyWheel(t, seed, rejected, "0.5.3")
	updateUVIntegrationCopyWheel(t, candidate, rejected, "0.7.0rc1")

	newFixture := func(t *testing.T, v string) *updateUVIntegrationFixture {
		return newUpdateUVIntegrationFixture(t, repo, uv, python, seed, v)
	}

	t.Run("pinned_old_updates_to_stable", func(t *testing.T) {
		f := newFixture(t, "0.5.3")
		before := f.snapshot(t)
		result := f.update(t, "0.5.3", candidate, nil)
		f.assertSuccess(t, result, "0.5.3", "0.6.0", "updated", binaries["0.6.0"])
		if after := updateUVIntegrationHash(t, f.binary); after == before.hash || before.hash != updateUVIntegrationHash(t, binaries["0.5.3"]) {
			t.Fatal("update did not replace the installed old native binary with the candidate")
		}
		f.assertReceipt(t, ">=0.5.3", candidate)
	})

	t.Run("same_version_replaces_open_binary", func(t *testing.T) {
		f := newFixture(t, "0.6.0")
		old, err := os.Open(f.binary)
		if err != nil {
			t.Fatal(err)
		}
		defer old.Close()
		before, err := old.Stat()
		if err != nil {
			t.Fatal(err)
		}
		result := f.update(t, "0.6.0", candidate, nil)
		f.assertSuccess(t, result, "0.6.0", "0.6.0", "refreshed", binaries["0.6.0"])
		after, err := os.Stat(f.binary)
		if err != nil || os.SameFile(before, after) {
			t.Fatalf("refresh must create a new file/inode while the old binary is held open: %v", err)
		}
		held, err := old.Stat()
		if err != nil || !sameUpdateFile(before, held) {
			t.Fatalf("refresh mutated the held old binary: %v", err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, old); err != nil {
			t.Fatal(err)
		}
		want := updateUVIntegrationHash(t, binaries["0.6.0"])
		if !bytes.Equal(h.Sum(nil), want[:]) {
			t.Fatal("held binary contents changed during refresh")
		}
		f.assertReceipt(t, ">=0.6.0", candidate)
	})

	for _, kind := range []string{"regular_file", "victim_symlink"} {
		t.Run("manual_entrypoint_"+kind, func(t *testing.T) {
			f := newFixture(t, "0.5.3")
			victim := newFixture(t, "0.6.0")
			before, victimBefore := f.snapshot(t), victim.snapshot(t)
			if err := os.Remove(f.entrypoint); err != nil {
				t.Fatal(err)
			}
			if kind == "regular_file" {
				writeUpdateTestFile(t, f.entrypoint, []byte("manually installed pairmux; do not overwrite\n"), 0o700)
			} else if err := os.Symlink(victim.binary, f.entrypoint); err != nil {
				t.Fatal(err)
			}
			manual, err := os.Lstat(f.entrypoint)
			if err != nil {
				t.Fatal(err)
			}
			result := f.update(t, "0.5.3", candidate, nil)
			e := assertUpdateError(t, result.RC, bytes.NewBuffer(result.Output))
			if e.Error.Hint != updateOwnershipHint || !strings.Contains(e.Error.Message, "cannot verify uv ownership") {
				t.Fatalf("manual replacement must get the ownership repair hint: %+v", e.Error)
			}
			f.assertCalls(t, result, "list") // No install, force or rollback is allowed.
			f.assertUnchanged(t, before)
			victim.assertUnchanged(t, victimBefore)
			after, err := os.Lstat(f.entrypoint)
			if err != nil || !sameUpdateFile(manual, after) {
				t.Fatalf("manual entrypoint changed: %v", err)
			}
			if kind == "regular_file" {
				b, err := os.ReadFile(f.entrypoint)
				if err != nil || string(b) != "manually installed pairmux; do not overwrite\n" {
					t.Fatalf("manual file was overwritten: %q, %v", b, err)
				}
			} else if link, err := os.Readlink(f.entrypoint); err != nil || link != victim.binary {
				t.Fatalf("victim symlink was overwritten: %q, %v", link, err)
			}
		})
	}

	t.Run("hostile_config_and_old_receipt_sources_are_ignored", func(t *testing.T) {
		f := newFixture(t, "0.5.3")
		victim := newFixture(t, "0.6.0")
		victimBefore := victim.snapshot(t)
		f.assertReceipt(t, "==0.5.3", seed)
		constraint := filepath.Join(f.root, "hostile constraints.txt")
		writeUpdateTestFile(t, constraint, []byte("pairmux==0.5.3\n"), 0o600)
		config := filepath.Join(f.root, "hostile uv.toml")
		configBody := []byte(fmt.Sprintf("no-index = true\nfind-links = [%q]\nconstraint-dependencies = [\"pairmux==0.5.3\"]\n", seed))
		writeUpdateTestFile(t, config, configBody, 0o600)
		configHome := filepath.Join(f.root, "config", "uv")
		updateUVIntegrationMkdir(t, configHome)
		writeUpdateTestFile(t, filepath.Join(configHome, "uv.toml"), configBody, 0o600)
		pollution := map[string]string{
			"UV_TOOL_DIR": victim.toolRoot, "UV_TOOL_BIN_DIR": victim.binDir, "UV_CACHE_DIR": filepath.Join(victim.root, "poison cache"),
			"UV_CONFIG_FILE": config, "UV_FIND_LINKS": seed, "UV_CONSTRAINT": constraint, "UV_OVERRIDE": constraint,
			"UV_INDEX": "https://hostile.invalid/simple", "UV_DEFAULT_INDEX": "https://hostile.invalid/simple",
			"UV_INDEX_URL": "https://hostile.invalid/simple", "UV_EXTRA_INDEX_URL": "https://extra.invalid/simple",
			"UV_PYTHON": filepath.Join(victim.root, "missing python"), "UV_PYTHON_DOWNLOADS": "automatic",
			"UV_PYTHON_INSTALL_MIRROR": "https://python.invalid", "UV_OFFLINE": "false", "UV_NO_CONFIG": "false",
			"UV_PRERELEASE": "allow", "UV_NO_BUILD": "false", "UV_FUTURE_UNRECOGNIZED_OPTION": "hostile",
			"CARGO_DIST_MIRROR": "https://cargo.invalid", "CARGO_DIST_FUTURE_OPTION": "hostile",
			"INSTALLER_MIRROR": "https://installer.invalid", "INSTALLER_FUTURE_OPTION": "hostile", "CARGO_HOME": victim.root,
		}
		result := f.update(t, "0.5.3", candidate, pollution)
		f.assertSuccess(t, result, "0.5.3", "0.6.0", "updated", binaries["0.6.0"])
		f.assertReceipt(t, ">=0.5.3", candidate)
		victim.assertUnchanged(t, victimBefore)
		if _, err := os.Stat(pollution["UV_CACHE_DIR"]); !os.IsNotExist(err) {
			t.Fatalf("hostile cache directory was used: %v", err)
		}
	})

	t.Run("lower_stable_and_higher_prerelease_cannot_satisfy_floor", func(t *testing.T) {
		f := newFixture(t, "0.6.0")
		before := f.snapshot(t)
		result := f.update(t, "0.6.0", rejected, nil)
		e := assertUpdateError(t, result.RC, bytes.NewBuffer(result.Output))
		if e.Error.Hint != updateRepairHint || !strings.Contains(e.Error.Message, "uv tool install failed:") ||
			!strings.Contains(e.Error.Hint, "does not perform automatic rollback") {
			t.Fatalf("resolution failure lost diagnostic/manual repair guidance: %+v", e.Error)
		}
		f.assertCalls(t, result, "list", "install")
		if result.Calls[1].Error == "" || len(result.Calls[1].Stderr) == 0 {
			t.Fatal("ineligible wheelhouse did not produce a real uv resolution failure")
		}
		f.assertUnchanged(t, before)
		f.assertLink(t)
	})
}

type updateUVIntegrationConfig struct {
	Repo, UV, Python, Binary, ToolRoot, BinDir, Wheelhouse, CacheDir, Current, Trace string
}

type updateUVIntegrationCall struct {
	Path                             string
	Args, Env, ActualArgs, ActualEnv []string
	Stdout, Stderr                   []byte
	StdoutTruncated, StderrTruncated bool
	Error                            string
}

type updateUVIntegrationResult struct {
	RC     int
	Output []byte
	Calls  []updateUVIntegrationCall
}

// Only the install's source and Python are redirected. Production filesystem
// checks, real uv listing, and the absolute native version command all run.
func runUpdateUVIntegrationHelper(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config updateUVIntegrationConfig
	if err := json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	result := updateUVIntegrationResult{RC: -1}
	defer func() {
		result.Output = buf.Bytes()
		b, err := json.Marshal(result)
		if err == nil {
			err = os.WriteFile(config.Trace, b, 0o600)
		}
		if err != nil {
			t.Errorf("write helper trace: %v", err)
		}
	}()
	original := os.Environ()
	ops := updateOps{
		executable: func() (string, error) { return config.Binary, nil },
		lookPath:   exec.LookPath,
		run: func(path string, args, env []string) (updateRunResult, error) {
			phase := len(result.Calls)
			result.Calls = append(result.Calls, updateUVIntegrationCall{
				Path: path, Args: slices.Clone(args), Env: slices.Clone(env),
			})
			wantEnv := updateUVIntegrationCleanEnv(original)
			wantEnv = append(wantEnv, "UV_TOOL_DIR="+config.ToolRoot)
			if phase > 0 {
				wantEnv = append(wantEnv, "UV_TOOL_BIN_DIR="+config.BinDir)
			}
			if !slices.Equal(env, wantEnv) {
				t.Fatalf("call %d: production environment was not scrubbed and binary-derived", phase)
			}
			actualArgs := slices.Clone(args)
			switch phase {
			case 0, 2:
				if path != config.UV || !slices.Equal(args, []string{"tool", "list", "--no-config", "--show-paths", "--color", "never"}) {
					t.Fatalf("call %d: unexpected ownership command %q %q", phase, path, args)
				}
			case 1:
				want := updateUVIntegrationInstallArgs(config.Current)
				if path != config.UV || !slices.Equal(args, want) {
					t.Fatalf("install command %q %q, want real uv %q", path, args, want)
				}
				actualArgs = []string{"tool", "install", "--no-config", "--no-index", "--find-links", config.Wheelhouse,
					"--offline", "--no-python-downloads", "--no-sources", "--no-build", "--python", config.Python,
					"--upgrade", "--reinstall", "--no-cache", "--prerelease", "disallow", want[len(want)-1]}
			case 3:
				if path != config.Binary || !filepath.IsAbs(path) || !slices.Equal(args, []string{"--json", "version"}) {
					t.Fatalf("replacement check was not the absolute native binary: %q %q", path, args)
				}
			default:
				t.Fatalf("unexpected extra subprocess (force/rollback): %q %q", path, args)
			}
			// These are test-only settings, added AFTER checking the untouched
			// production environment. None can come from hostile ambient UV_*.
			actualEnv := updateUVIntegrationReplaceEnv(env, map[string]string{
				"UV_CACHE_DIR": config.CacheDir, "UV_OFFLINE": "true", "UV_PYTHON_DOWNLOADS": "never",
			})
			result.Calls[phase].ActualArgs = actualArgs
			result.Calls[phase].ActualEnv = actualEnv
			run, err := runUpdateCommand(path, actualArgs, actualEnv)
			call := &result.Calls[phase]
			call.Stdout, call.Stderr = run.stdout, run.stderr
			call.StdoutTruncated, call.StderrTruncated = run.stdoutTruncated, run.stderrTruncated
			if err != nil {
				call.Error = err.Error()
			}
			return run, err
		},
	}
	c := &Ctx{JSON: true, Stdout: &buf} // No tmux client or state directory.
	result.RC = c.cmdUpdateWith(nil, config.Current, ops)
}

func updateUVIntegrationInstallArgs(current string) []string {
	return []string{"tool", "install", "--no-config", "--default-index", "https://pypi.org/simple", "--no-sources", "--no-build",
		"--python", ">=3.9", "--upgrade", "--reinstall", "--no-cache", "--prerelease", "disallow", "pairmux>=" + current}
}

type updateUVIntegrationFixture struct {
	root, repo, uv, python, toolRoot, binDir, binary, entrypoint, seed, decoyMarker string
	env                                                                             []string
}

func newUpdateUVIntegrationFixture(t *testing.T, repo, uv, python, seed, v string) *updateUVIntegrationFixture {
	t.Helper()
	f := &updateUVIntegrationFixture{
		root: filepath.Join(updateUVIntegrationCanonical(t, t.TempDir()), "custom roots 'and spaces"),
		repo: repo, uv: uv, python: python, seed: seed,
	}
	f.env = updateUVIntegrationIsolate(t, f.root)
	f.toolRoot, f.binDir = filepath.Join(f.root, "tools 'custom"), filepath.Join(f.root, "bin 'custom")
	f.binary = filepath.Join(f.toolRoot, "pairmux", "bin", "pairmux")
	f.entrypoint = filepath.Join(f.binDir, "pairmux")
	// Persist an obsolete source and exact pin in a receipt written by uv,
	// without ever accessing that index. update must reset both.
	updateUVIntegrationCommand(t, repo, f.env, uv, "tool", "install", "--no-config", "--no-index", "--find-links", seed,
		"--offline", "--no-python-downloads", "--python", python, "--default-index", "https://receipt.invalid/simple", "pairmux=="+v)
	f.assertLink(t)
	f.assertReceipt(t, "=="+v, seed)
	version, err := runUpdateCommand(f.binary, []string{"--json", "version"}, f.env)
	if err != nil {
		t.Fatalf("seed native binary failed: %v; stderr=%q", err, version.stderr)
	}
	if got, err := parseUpdateVersionReply(version); err != nil || got != v {
		t.Fatalf("seed binary version=%q, want %q: %v", got, v, err)
	}
	decoy := filepath.Join(f.root, "PATH decoys 'first")
	updateUVIntegrationMkdir(t, decoy)
	f.decoyMarker = filepath.Join(f.root, "unexpected PATH command")
	for _, name := range []string{"pairmux", "tmux"} {
		writeUpdateTestFile(t, filepath.Join(decoy, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$0\" >> \"$PAIRMUX_TEST_UV_DECOY_MARKER\"\nexit 97\n"), 0o700)
	}
	if err := os.Symlink(uv, filepath.Join(decoy, "uv")); err != nil {
		t.Fatal(err)
	}
	f.env = updateUVIntegrationReplaceEnv(f.env, map[string]string{
		"PATH":                         decoy + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PAIRMUX_TEST_UV_DECOY_MARKER": f.decoyMarker, "PAIRMUX_SOCKET": filepath.Join(f.root, "nonexistent tmux socket"),
	})
	return f
}

func (f *updateUVIntegrationFixture) update(t *testing.T, current, wheelhouse string, pollution map[string]string) updateUVIntegrationResult {
	t.Helper()
	config := updateUVIntegrationConfig{
		Repo: f.repo, UV: f.uv, Python: f.python, Binary: f.binary, ToolRoot: f.toolRoot, BinDir: f.binDir,
		Wheelhouse: wheelhouse, CacheDir: filepath.Join(f.root, "offline runner cache"), Current: current,
		Trace: filepath.Join(f.root, "update trace.json"),
	}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "helper config.json")
	writeUpdateTestFile(t, path, b, 0o600)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv := updateUVIntegrationReplaceEnv(f.env, pollution)
	childEnv = updateUVIntegrationReplaceEnv(childEnv, map[string]string{updateUVIntegrationHelper: path})
	cmd := exec.Command(testBinary, "-test.run=^TestUpdateUVIntegration$", "-test.v")
	cmd.Dir, cmd.Env = f.repo, childEnv
	out, err := cmd.CombinedOutput()
	trace, traceErr := os.ReadFile(config.Trace)
	if err != nil || traceErr != nil {
		t.Fatalf("update helper failed: %v; trace error=%v\n%s\ntrace=%s", err, traceErr, out, trace)
	}
	var result updateUVIntegrationResult
	if err := json.Unmarshal(trace, &result); err != nil {
		t.Fatalf("invalid helper trace: %v\n%s", err, out)
	}
	if _, err := os.Stat(f.decoyMarker); !os.IsNotExist(err) {
		t.Fatalf("updater used a PATH pairmux/tmux decoy: %v", err)
	}
	// Independently inspect the pre-adapter environment preserved in the trace.
	for i, call := range result.Calls {
		seen := make(map[string]bool)
		for _, kv := range call.Env {
			name, value, _ := strings.Cut(kv, "=")
			if seen[name] {
				t.Fatalf("call %d has duplicate environment key %s", i, name)
			}
			seen[name] = true
			switch {
			case name == "UV_TOOL_DIR":
				if value != f.toolRoot {
					t.Fatalf("call %d trusted ambient tool directory", i)
				}
			case name == "UV_TOOL_BIN_DIR":
				if i == 0 || value != f.binDir {
					t.Fatalf("call %d trusted an unproven executable directory", i)
				}
			case updateUVIntegrationPollutedKey(name):
				t.Fatalf("call %d leaked malicious/ambient environment key %s", i, name)
			}
		}
		if !seen["UV_TOOL_DIR"] || i > 0 && !seen["UV_TOOL_BIN_DIR"] {
			t.Fatalf("call %d did not use the verified installation directories", i)
		}
	}
	return result
}

func (f *updateUVIntegrationFixture) assertCalls(t *testing.T, result updateUVIntegrationResult, phases ...string) {
	t.Helper()
	if len(result.Calls) != len(phases) {
		t.Fatalf("got %d subprocesses, want %v; rc=%d output=%s", len(result.Calls), phases, result.RC, result.Output)
	}
	for i, phase := range phases {
		call := result.Calls[i]
		wantPath := f.uv
		var wantArgs []string
		switch phase {
		case "list":
			wantArgs = []string{"tool", "list", "--no-config", "--show-paths", "--color", "never"}
		case "install":
			wantArgs = updateUVIntegrationInstallArgs(strings.TrimPrefix(call.Args[len(call.Args)-1], "pairmux>="))
		case "version":
			wantPath, wantArgs = f.binary, []string{"--json", "version"}
		}
		if call.Path != wantPath || !slices.Equal(call.Args, wantArgs) || call.StdoutTruncated || call.StderrTruncated {
			t.Fatalf("call %d (%s): unexpected path/argv or truncated output: %q %q", i, phase, call.Path, call.Args)
		}
		if phase != "install" && !slices.Equal(call.ActualArgs, call.Args) {
			t.Fatalf("%s was redirected rather than executed unmodified", phase)
		}
	}
}

func (f *updateUVIntegrationFixture) assertSuccess(t *testing.T, result updateUVIntegrationResult, old, next, status, native string) {
	t.Helper()
	if result.RC != 0 {
		t.Fatalf("update rc=%d: %s", result.RC, result.Output)
	}
	buf := bytes.NewBuffer(result.Output)
	e := decode(t, buf)
	want := fmt.Sprintf("pairmux %s -> %s\nsource: https://pypi.org/simple\ninstalled: %s", old, next, f.entrypoint)
	if e.Schema != core.SchemaID || !e.OK || e.Status != status || e.Output != want || e.Error != nil ||
		strings.Count(string(result.Output), "\n") != 1 {
		t.Fatalf("success must be one clean pairmux.v1 envelope with the fixed PyPI source: %s", result.Output)
	}
	f.assertCalls(t, result, "list", "install", "list", "version")
	if !slices.Equal(result.Calls[1].Args, updateUVIntegrationInstallArgs(old)) {
		t.Fatalf("pin was not replaced with the running version's floor: %q", result.Calls[1].Args)
	}
	for _, call := range result.Calls {
		if call.Error != "" {
			t.Fatalf("successful update had a subprocess failure: %s", call.Error)
		}
	}
	if len(result.Calls[1].Stderr)+len(result.Calls[1].Stdout) == 0 {
		t.Fatal("real installer produced no captured diagnostic output")
	}
	if got, err := parseUpdateVersionReply(updateRunResult{stdout: result.Calls[3].Stdout}); err != nil || got != next {
		t.Fatalf("absolute replacement version command returned %q: %v", got, err)
	}
	if updateUVIntegrationHash(t, f.binary) != updateUVIntegrationHash(t, native) {
		t.Fatal("installed native binary does not match the locally built candidate")
	}
	f.assertLink(t)
}

func (f *updateUVIntegrationFixture) assertLink(t *testing.T) {
	t.Helper()
	info, err := os.Lstat(f.entrypoint)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("uv did not create an entrypoint symlink: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(f.entrypoint)
	if err != nil || resolved != f.binary {
		t.Fatalf("uv entrypoint resolved to %q, want %q: %v", resolved, f.binary, err)
	}
	info, err = os.Lstat(f.binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed pairmux is not a regular native executable: %v", err)
	}
}

func (f *updateUVIntegrationFixture) assertReceipt(t *testing.T, requirement, wheelhouse string) {
	t.Helper()
	b, err := readUpdateReceipt(filepath.Join(f.toolRoot, "pairmux", "uv-receipt.toml"))
	if err != nil {
		t.Fatalf("real uv receipt violates the updater's filesystem policy: %v", err)
	}
	// uv persists find-links as file URLs, percent-encoding spaces. Decode
	// those for comparison while keeping the untouched TOML for FS checks.
	decoded, err := url.PathUnescape(string(b))
	if err != nil {
		t.Fatalf("decode uv's persisted file URL: %v", err)
	}
	for _, fragment := range []string{"[tool]", `name = "pairmux"`, `specifier = "` + requirement + `"`,
		"install-path = " + fmt.Sprintf("%q", f.entrypoint), "file://" + wheelhouse} {
		if !strings.Contains(decoded, fragment) {
			t.Fatalf("real receipt missing %q:\n%s", fragment, b)
		}
	}
	if strings.HasPrefix(requirement, ">=") && (strings.Contains(decoded, "https://receipt.invalid/simple") || strings.Contains(decoded, f.seed)) {
		t.Fatalf("old receipt source settings survived fresh tool install:\n%s", b)
	}
	if strings.HasPrefix(requirement, "==") && !strings.Contains(decoded, "https://receipt.invalid/simple") {
		t.Fatalf("seed receipt did not persist its obsolete default index:\n%s", b)
	}
}

type updateUVIntegrationSnapshot struct {
	hash        [sha256.Size]byte
	binaryInfo  os.FileInfo
	receipt     []byte
	receiptInfo os.FileInfo
}

func (f *updateUVIntegrationFixture) snapshot(t *testing.T) updateUVIntegrationSnapshot {
	t.Helper()
	s := updateUVIntegrationSnapshot{hash: updateUVIntegrationHash(t, f.binary)}
	var err error
	s.binaryInfo, err = os.Stat(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.toolRoot, "pairmux", "uv-receipt.toml")
	s.receipt, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s.receiptInfo, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *updateUVIntegrationFixture) assertUnchanged(t *testing.T, before updateUVIntegrationSnapshot) {
	t.Helper()
	after := f.snapshot(t)
	if before.hash != after.hash || !sameUpdateFile(before.binaryInfo, after.binaryInfo) ||
		!bytes.Equal(before.receipt, after.receipt) || !sameUpdateFile(before.receiptInfo, after.receiptInfo) {
		t.Fatal("protected installation binary or real uv receipt changed")
	}
}

func updateUVIntegrationTool(t *testing.T, key string) string {
	t.Helper()
	path := os.Getenv(key)
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must name an absolute, functioning executable; the tagged suite never skips", key)
	}
	path = updateUVIntegrationCanonical(t, path)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not an executable file: %v", key, err)
	}
	return path
}

func updateUVIntegrationCanonical(t *testing.T, path string) string {
	t.Helper()
	path, err := filepath.Abs(path)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		t.Fatalf("canonicalize fixture/tool path: %v", err)
	}
	return path
}

func updateUVIntegrationMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func updateUVIntegrationIsolate(t *testing.T, root string) []string {
	t.Helper()
	values := map[string]string{
		"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_DATA_HOME": filepath.Join(root, "data"),
		"TMPDIR": filepath.Join(root, "tmp"), "UV_TOOL_DIR": filepath.Join(root, "tools 'custom"),
		"UV_TOOL_BIN_DIR": filepath.Join(root, "bin 'custom"), "UV_CACHE_DIR": filepath.Join(root, "uv cache"),
	}
	for _, dir := range values {
		updateUVIntegrationMkdir(t, dir)
	}
	values["UV_OFFLINE"], values["UV_PYTHON_DOWNLOADS"], values["UV_NO_CONFIG"] = "true", "never", "true"
	return updateUVIntegrationReplaceEnv(updateUVIntegrationCleanEnv(os.Environ()), values)
}

func updateUVIntegrationPollutedKey(name string) bool {
	return strings.HasPrefix(name, "UV_") || strings.HasPrefix(name, "CARGO_DIST_") || strings.HasPrefix(name, "INSTALLER_") || name == "CARGO_HOME"
}

func updateUVIntegrationCleanEnv(base []string) []string {
	env := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !updateUVIntegrationPollutedKey(name) {
			env = append(env, kv)
		}
	}
	return env
}

func updateUVIntegrationReplaceEnv(base []string, values map[string]string) []string {
	env := make([]string, 0, len(base)+len(values))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, replaced := values[name]; !replaced {
			env = append(env, kv)
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		env = append(env, name+"="+values[name])
	}
	return env
}

func updateUVIntegrationCommand(t *testing.T, repo string, env []string, path string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env = repo, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture command %q %q failed: %v\n%s", path, args, err, out)
	}
	return out
}

func updateUVIntegrationHash(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func updateUVIntegrationCopyWheel(t *testing.T, from, to, version string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(from, "pairmux-"+version+"-*.whl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("find locally generated wheel for %s: %v, %v", version, matches, err)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	writeUpdateTestFile(t, filepath.Join(to, filepath.Base(matches[0])), b, 0o600)
}
