//go:build windows

package cli

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests need real Windows semantics: a running executable image cannot
// be overwritten or deleted, but can be renamed within its volume.

const updateWindowsChildEnv = "PAIRMUX_UPDATE_WINDOWS_CHILD"

// TestUpdateWindowsChildHelper is the process the tests below run from a
// copied executable: it reports readiness and runs until stdin closes.
func TestUpdateWindowsChildHelper(t *testing.T) {
	if os.Getenv(updateWindowsChildEnv) != "wait" {
		return
	}
	_, _ = os.Stdout.WriteString("ready\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func updateWindowsTestExecutable(t *testing.T) []byte {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// startUpdateWindowsChild runs path (a copy of the test binary) until the
// returned stop function closes its stdin.
func startUpdateWindowsChild(t *testing.T, path string) (stop func()) {
	t.Helper()
	cmd := exec.Command(path, "-test.run=^TestUpdateWindowsChildHelper$")
	cmd.Env = append(os.Environ(), updateWindowsChildEnv+"=wait")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		_ = cmd.Process.Kill()
		t.Fatalf("child did not start: %q %v", line, err)
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = stdin.Close()
		_ = cmd.Wait()
	}
	t.Cleanup(stop)
	return stop
}

func TestUpdateWindowsRenamesRunningExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pairmux.exe")
	aside := exe + ".old-1"
	writeUpdateTestFile(t, exe, updateWindowsTestExecutable(t), 0o700)
	stop := startUpdateWindowsChild(t, exe)

	// The premise: uv could neither delete nor overwrite the running image.
	if err := os.Remove(exe); err == nil {
		t.Fatal("a running executable was deleted; the move-aside premise does not hold")
	}
	if err := os.WriteFile(exe, []byte("new"), 0o700); err == nil {
		t.Fatal("a running executable was overwritten; the move-aside premise does not hold")
	}
	// But it can be renamed, and its name is then free for uv's copy.
	if err := updateRenameNoReplace(exe, aside); err != nil {
		t.Fatalf("rename of a running executable: %v", err)
	}
	writeUpdateTestFile(t, exe, []byte("new"), 0o700)
	if err := updateRenameNoReplace(aside, exe); err == nil {
		t.Fatal("restore replaced the file uv created")
	}
	cleanUpdateAside(updateFS{}.orDefault(), updateLayout{windows: true, name: "pairmux.exe", scripts: "Scripts"}, dir)
	if _, err := os.Stat(aside); err != nil {
		t.Fatalf("cleanup should keep (fail to delete) a running image: %v", err)
	}
	stop()
	cleanUpdateAside(updateFS{}.orDefault(), updateLayout{windows: true, name: "pairmux.exe", scripts: "Scripts"}, dir)
	assertUpdateMissing(t, aside)
	assertUpdateFileBytes(t, exe, []byte("new"))
}

// Both the PATH copy and the environment's own executable are running (for
// example a daemon from each). The update must still succeed, keep the running
// images aside, and a later update must remove them once they have exited.
func TestUpdateWindowsUpdatesWhileCopiesRun(t *testing.T) {
	f := newUpdateWinFixture(t, updateWindowsTestExecutable(t))
	stopEntrypoint := startUpdateWindowsChild(t, f.entrypoint)
	stopBinary := startUpdateWindowsChild(t, f.binary)
	if err := os.Remove(f.entrypoint); err == nil {
		t.Fatal("running entrypoint was deletable; test premise does not hold")
	}
	var buf bytes.Buffer
	c := &Ctx{JSON: true, Stdout: &buf}
	if rc := c.cmdUpdateWith(nil, "0.5.3", f.ops(t)); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, buf.String())
	}
	assertUpdateFileBytes(t, f.entrypoint, f.newBytes)
	assertUpdateFileBytes(t, f.binary, f.newBytes)
	for _, path := range []string{f.aside(f.binDir), f.aside(f.toolRoot)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("running image %s should remain aside: %v", path, err)
		}
	}

	stopEntrypoint()
	stopBinary()
	f.calls = nil
	buf.Reset()
	if rc := c.cmdUpdateWith(nil, "0.6.0", f.ops(t)); rc != 0 {
		t.Fatalf("second update rc=%d: %s", rc, buf.String())
	}
	assertUpdateMissing(t, f.aside(f.binDir))
	assertUpdateMissing(t, f.aside(f.toolRoot))
}

func TestUpdateWindowsEnvironmentScrubIgnoresCase(t *testing.T) {
	env := updateEnvironment([]string{"uv_tool_dir=C:\\untrusted", "Uv_Index_Url=https://private.invalid", "cargo_home=C:\\x",
		"Installer_Mirror=https://m.invalid", "Path=C:\\Windows"}, `C:\tools`, "")
	if strings.Join(env, "\n") != "Path=C:\\Windows\nUV_TOOL_DIR=C:\\tools" {
		t.Fatalf("env=%q", env)
	}
}

// uv lists Windows paths natively; only clean, volume-qualified backslash
// paths are accepted.
func TestUpdateWindowsCanonicalPathsAndListing(t *testing.T) {
	f := newUpdateWinFixture(t, []byte("old binary"))
	if !strings.Contains(f.makeListing("0.5.3"), `\`) {
		t.Fatal("listing should use native backslash paths")
	}
	env, entry, err := parseUpdateListingIn([]byte(f.listing), "")
	if err != nil || env != f.environment || entry != f.entrypoint {
		t.Fatalf("discovery parse=%q %q %v", env, entry, err)
	}
	for _, bad := range []string{
		strings.Replace(f.listing, f.entrypoint, filepath.ToSlash(f.entrypoint), 1),                                      // mixed separators
		strings.Replace(f.listing, f.entrypoint, strings.Replace(f.entrypoint, `\`, `\\`, 1), 1),                         // unclean
		strings.Replace(f.listing, f.entrypoint, strings.TrimPrefix(f.entrypoint, filepath.VolumeName(f.entrypoint)), 1), // no volume
	} {
		if _, _, err := parseUpdateListingIn([]byte(bad), f.environment); err == nil {
			t.Fatalf("unsafe Windows listing accepted: %q", bad)
		}
	}
}
