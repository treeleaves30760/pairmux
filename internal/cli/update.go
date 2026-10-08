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
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/version"
)

const (
	updateIndex         = "https://pypi.org/simple"
	updateReceiptLimit  = 64 << 10
	updateOutputLimit   = 64 << 10
	updateDiagnosticCap = 4 << 10
	updateOwnershipHint = "Use the installation's existing package manager, or install pairmux as a persistent uv tool in separate directories."
	updateRepairHint    = "Inspect the diagnostic and repair or reinstall this persistent uv tool manually; update does not perform automatic rollback."
	updateUVHint        = "Install uv from https://docs.astral.sh/uv/getting-started/installation/ and retry pairmux update."
)

// Only process discovery and execution are injected. Filesystem ownership
// checks always run, including in tests. A test-only runner can redirect the
// fixed install argv to an offline wheelhouse without a production override.
type updateOps struct {
	executable func() (string, error)
	lookPath   func(string) (string, error)
	run        func(string, []string, []string) (updateRunResult, error)
}

type updateRunResult struct {
	stdout, stderr                   []byte
	stdoutTruncated, stderrTruncated bool
}

func (c *Ctx) cmdUpdate(args []string) int {
	return c.cmdUpdateWith(args, version.Version, updateOps{
		executable: os.Executable,
		lookPath:   exec.LookPath,
		run:        runUpdateCommand,
	})
}

func (c *Ctx) cmdUpdateWith(args []string, current string, ops updateOps) int {
	if _, err := parseFlags(args, flagSpec{}); err != nil {
		return c.usage("pairmux update", err.Error())
	}
	if len(args) != 0 {
		return c.usage("pairmux update", "update accepts no arguments or command-specific flags; add global --json for JSON output")
	}
	old, err := parseUpdateVersion(current)
	if err != nil {
		return c.fail(output.CodeUpdate, fmt.Sprintf("running version %q is not a canonical release", current),
			"Install a released pairmux with your existing package manager before retrying update.")
	}

	executable, err := ops.executable()
	if err != nil {
		return c.fail(output.CodeUpdate, "cannot locate the running executable: "+err.Error(), updateOwnershipHint)
	}
	installation, err := locateUpdateInstallation(executable)
	if err != nil {
		return c.fail(output.CodeUpdate, "cannot verify a persistent uv-owned pairmux installation: "+err.Error(), updateOwnershipHint)
	}
	uv, err := resolveUpdateUV(ops.lookPath, installation.env)
	if err != nil {
		return c.fail(output.CodeUpdate, err.Error(), updateUVHint)
	}

	// Before the listing verifies an entrypoint, only the binary-derived tool
	// root is trusted. Ambient uv directories and source settings are not used.
	env := updateEnvironment(os.Environ(), installation.toolRoot, "")
	if err := installation.inspect(uv, env, ops.run); err != nil {
		return c.fail(output.CodeUpdate, "cannot verify uv ownership: "+err.Error(), updateOwnershipHint)
	}
	env = updateEnvironment(os.Environ(), installation.toolRoot, filepath.Dir(installation.entrypoint))
	if err := installation.recheck(); err != nil {
		return c.fail(output.CodeUpdate, "installation changed before update: "+err.Error(), updateOwnershipHint)
	}

	// A fresh install requirement resets the old receipt's pins, constraints,
	// indexes, sources and extras. tool upgrade would retain them. Never force:
	// uv must retain its own no-clobber checks in addition to this preflight.
	args = []string{"tool", "install", "--no-config", "--default-index", updateIndex,
		"--no-sources", "--no-build", "--python", ">=3.9", "--upgrade", "--reinstall",
		"--no-cache", "--prerelease", "disallow", "pairmux>=" + old.core()}
	result, err := ops.run(uv, args, env)
	if err != nil {
		return c.fail(output.CodeUpdate, updateCommandFailure("uv tool install", result, err), updateRepairHint)
	}

	// The old linker stamp remains in this process. Check the replacement's
	// receipt and link, then query that absolute binary rather than PATH.
	updated, err := locateUpdateInstallation(installation.binary)
	if err == nil && updated.binary != installation.binary {
		err = fmt.Errorf("the canonical tool environment changed")
	}
	if err == nil {
		err = updated.inspect(uv, env, ops.run)
	}
	if err == nil && updated.binDir != installation.binDir {
		err = fmt.Errorf("the recorded executable directory changed")
	}
	if err == nil {
		err = updated.recheck()
	}
	if err != nil {
		return c.fail(output.CodeUpdate, "uv finished, but replacement ownership could not be verified: "+err.Error(), updateRepairHint)
	}
	result, err = ops.run(updated.binary, []string{"--json", "version"}, env)
	if err != nil {
		return c.fail(output.CodeUpdate, updateCommandFailure("replacement version check", result, err), updateRepairHint)
	}
	newVersion, err := parseUpdateVersionReply(result)
	if err != nil {
		return c.fail(output.CodeUpdate, "uv finished, but the replacement version could not be verified: "+err.Error(), updateRepairHint)
	}
	newRelease, err := parseUpdateVersion(newVersion)
	if err != nil || newRelease.prerelease || compareUpdateCore(newRelease, old) < 0 {
		return c.fail(output.CodeUpdate, fmt.Sprintf("replacement version %q is not a stable release at or above %s", newVersion, old.core()), updateRepairHint)
	}
	if err := updated.recheck(); err != nil {
		return c.fail(output.CodeUpdate, "replacement changed during the version check: "+err.Error(), updateRepairHint)
	}
	status := "updated"
	if !old.prerelease && compareUpdateCore(old, newRelease) == 0 {
		status = "refreshed"
	}
	return c.emit(output.Envelope{Status: status, Output: fmt.Sprintf("pairmux %s -> %s\nsource: %s\ninstalled: %s",
		current, newVersion, updateIndex, updated.entrypoint)})
}

type updateVersion struct {
	parts      [3]string
	prerelease bool
}

var updateVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$`)

func parseUpdateVersion(s string) (updateVersion, error) {
	m := updateVersionPattern.FindStringSubmatch(strings.TrimPrefix(s, "v"))
	if m == nil {
		return updateVersion{}, fmt.Errorf("unsupported release version")
	}
	return updateVersion{parts: [3]string{m[1], m[2], m[3]}, prerelease: m[4] != ""}, nil
}

func (v updateVersion) core() string { return strings.Join(v.parts[:], ".") }

// Canonical decimal strings compare by length and then lexically. No integer
// conversion (and therefore no platform-dependent overflow) is necessary.
func compareUpdateCore(a, b updateVersion) int {
	for i := range a.parts {
		if len(a.parts[i]) < len(b.parts[i]) {
			return -1
		}
		if len(a.parts[i]) > len(b.parts[i]) {
			return 1
		}
		if cmp := strings.Compare(a.parts[i], b.parts[i]); cmp != 0 {
			return cmp
		}
	}
	return 0
}

type updateInstallation struct {
	binary, env, toolRoot string
	receipt               []byte
	binaryInfo            os.FileInfo
	entrypoint, link      string
	binDir                string // canonical directory, for post-install identity
}

func locateUpdateInstallation(executable string) (*updateInstallation, error) {
	binary, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil || !safeUpdatePath(binary) {
		return nil, fmt.Errorf("the executable path is not supported")
	}
	bin := filepath.Dir(binary)
	env := filepath.Dir(bin)
	if filepath.Base(binary) != "pairmux" || filepath.Base(bin) != "bin" || filepath.Base(env) != "pairmux" {
		return nil, fmt.Errorf("the running binary is not <tool-root>/pairmux/bin/pairmux")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("the running binary is not a regular executable")
	}
	receipt, err := readUpdateReceipt(filepath.Join(env, "uv-receipt.toml"))
	if err != nil {
		return nil, err
	}
	return &updateInstallation{binary: binary, env: env, toolRoot: filepath.Dir(env), receipt: receipt, binaryInfo: info}, nil
}

func readUpdateReceipt(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("uv-receipt.toml is missing or is not a regular non-symlink file")
	}
	if before.Size() > updateReceiptLimit {
		return nil, fmt.Errorf("uv-receipt.toml exceeds %d bytes", updateReceiptLimit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read uv-receipt.toml: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("uv-receipt.toml changed while opening it")
	}
	b, err := io.ReadAll(io.LimitReader(f, updateReceiptLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read uv-receipt.toml: %w", err)
	}
	if len(b) > updateReceiptLimit {
		return nil, fmt.Errorf("uv-receipt.toml exceeds %d bytes", updateReceiptLimit)
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !sameUpdateFile(before, after) || int64(len(b)) != after.Size() {
		return nil, fmt.Errorf("uv-receipt.toml changed while reading it")
	}
	// uv, not a partial TOML parser here, interprets the fields. Refuse raw
	// representations whose public line-oriented listing could be ambiguous.
	if len(b) == 0 || !safeUpdateText(b, true) || bytes.Contains(b, []byte(`\`)) ||
		bytes.Contains(b, []byte(`"""`)) || bytes.Contains(b, []byte(`'''`)) {
		return nil, fmt.Errorf("uv-receipt.toml has an unsupported escaped, multiline-string or control-character representation")
	}
	return b, nil
}

func sameUpdateFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

func safeUpdateText(b []byte, receipt bool) bool {
	if !utf8.Valid(b) {
		return false
	}
	for i, r := range string(b) {
		if r == '\n' || receipt && r == '\t' {
			continue
		}
		if receipt && r == '\r' && i+1 < len(b) && b[i+1] == '\n' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}

func safeUpdatePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\\\n\r\t") && safeUpdateText([]byte(path), false)
}

func resolveUpdateUV(lookPath func(string) (string, error), env string) (string, error) {
	path, err := lookPath("uv")
	if err != nil {
		if !os.IsNotExist(err) && !errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("cannot safely locate uv on PATH: %w", err)
		}
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || !filepath.IsAbs(home) {
			return "", fmt.Errorf("uv was not found on PATH or in ~/.local/bin")
		}
		path = filepath.Join(home, ".local", "bin", "uv")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("uv was not found on PATH or in ~/.local/bin")
	}
	path, err = filepath.Abs(path)
	if err != nil || !safeUpdatePath(path) {
		return "", fmt.Errorf("uv does not have a supported absolute executable path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("uv is not a regular executable")
	}
	rel, err := filepath.Rel(env, path)
	if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("uv is inside the pairmux environment being replaced; install uv separately")
	}
	return path, nil
}

func updateEnvironment(original []string, toolRoot, binDir string) []string {
	env := make([]string, 0, len(original)+2)
	for _, kv := range original {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "UV_") || strings.HasPrefix(name, "CARGO_DIST_") ||
			strings.HasPrefix(name, "INSTALLER_") || name == "CARGO_HOME" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "UV_TOOL_DIR="+toolRoot)
	if binDir != "" {
		env = append(env, "UV_TOOL_BIN_DIR="+binDir)
	}
	return env
}

var (
	updateListHeader = regexp.MustCompile(`^([a-zA-Z0-9_.-]+) v([^[:space:]]+) \((/.*)\)$`)
	updateListEntry  = regexp.MustCompile(`^- ([a-zA-Z0-9_.-]+) \((/.*)\)$`)
)

func parseUpdateListing(b []byte, env string) (string, error) {
	if len(b) == 0 || len(b) > updateOutputLimit || !safeUpdateText(b, false) || bytes.Contains(b, []byte(`\`)) {
		return "", fmt.Errorf("uv tool listing is empty, oversized or has unsupported characters")
	}
	tools, entries, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	tool, entrypoint := "", ""
	pairmuxCount := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		// uv prints receipt names and paths without quoting. More than one
		// delimiter could hide a different install-path inside an entrypoint name.
		if strings.Count(line, " (") != 1 {
			return "", fmt.Errorf("uv tool listing has an ambiguous name/path delimiter")
		}
		if m := updateListHeader.FindStringSubmatch(line); m != nil {
			tool = m[1]
			if tools[tool] || !safeUpdatePath(m[3]) {
				return "", fmt.Errorf("uv tool listing has a duplicate or unsupported header")
			}
			tools[tool] = true
			if tool == "pairmux" {
				resolved, err := filepath.EvalSymlinks(m[3])
				if err != nil || resolved != env {
					return "", fmt.Errorf("the listed pairmux environment is not the running environment")
				}
			}
			continue
		}
		m := updateListEntry.FindStringSubmatch(line)
		if m == nil || tool == "" || !safeUpdatePath(m[2]) {
			return "", fmt.Errorf("uv tool listing has an unrecognized line or continuation")
		}
		key := tool + "/" + m[1]
		if entries[key] || paths[m[2]] {
			return "", fmt.Errorf("uv tool listing has duplicate entrypoints")
		}
		entries[key], paths[m[2]] = true, true
		if tool == "pairmux" {
			pairmuxCount++
			if m[1] != "pairmux" || filepath.Base(m[2]) != "pairmux" {
				return "", fmt.Errorf("pairmux has an unexpected recorded entrypoint")
			}
			entrypoint = m[2]
		}
	}
	if !tools["pairmux"] || pairmuxCount != 1 {
		return "", fmt.Errorf("uv tool listing must contain exactly one pairmux tool and one pairmux entrypoint")
	}
	return entrypoint, nil
}

func (installation *updateInstallation) inspect(uv string, env []string, run func(string, []string, []string) (updateRunResult, error)) error {
	result, err := run(uv, []string{"tool", "list", "--no-config", "--show-paths", "--color", "never"}, env)
	if err != nil {
		return fmt.Errorf("%s", updateCommandFailure("uv tool list", result, err))
	}
	if result.stdoutTruncated {
		return fmt.Errorf("uv tool listing exceeds %d bytes", updateOutputLimit)
	}
	entrypoint, err := parseUpdateListing(result.stdout, installation.env)
	if err != nil {
		return err
	}
	link, err := verifyUpdateEntrypoint(entrypoint, installation.binary)
	if err != nil {
		return err
	}
	binDir, err := filepath.EvalSymlinks(filepath.Dir(entrypoint))
	if err != nil {
		return fmt.Errorf("resolve the recorded executable directory: %w", err)
	}
	installation.entrypoint, installation.link, installation.binDir = entrypoint, link, binDir
	// The listing may acquire uv's own lock, but is not an atomic snapshot.
	// Detect receipt/link changes after it and again before the install.
	return installation.recheck()
}

func verifyUpdateEntrypoint(entrypoint, binary string) (string, error) {
	info, err := os.Lstat(entrypoint)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("the recorded pairmux entrypoint is missing or is not a symlink; refusing a manual replacement")
	}
	link, err := os.Readlink(entrypoint)
	if err != nil {
		return "", fmt.Errorf("read the recorded pairmux symlink: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(entrypoint)
	if err != nil || resolved != binary {
		return "", fmt.Errorf("the recorded pairmux symlink does not resolve to the running executable")
	}
	return link, nil
}

func (installation *updateInstallation) recheck() error {
	receipt, err := readUpdateReceipt(filepath.Join(installation.env, "uv-receipt.toml"))
	if err != nil {
		return err
	}
	if !bytes.Equal(receipt, installation.receipt) {
		return fmt.Errorf("uv-receipt.toml changed since it was inspected")
	}
	info, err := os.Lstat(installation.binary)
	resolved, resolveErr := filepath.EvalSymlinks(installation.binary)
	if err != nil || resolveErr != nil || resolved != installation.binary || !info.Mode().IsRegular() || !sameUpdateFile(installation.binaryInfo, info) {
		return fmt.Errorf("the binary changed since it was inspected")
	}
	link, err := verifyUpdateEntrypoint(installation.entrypoint, installation.binary)
	if err != nil {
		return err
	}
	if link != installation.link {
		return fmt.Errorf("the recorded pairmux symlink changed since it was inspected")
	}
	binDir, err := filepath.EvalSymlinks(filepath.Dir(installation.entrypoint))
	if err != nil || binDir != installation.binDir {
		return fmt.Errorf("the recorded executable directory changed since it was inspected")
	}
	return nil
}

// A writer that continues accepting (and discarding) bytes after the cap lets
// exec drain both pipes without accumulating unbounded Output/CombinedOutput.
type updateCapture struct {
	b         []byte
	truncated bool
}

func (capture *updateCapture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := updateOutputLimit - len(capture.b)
	if len(p) > remaining {
		capture.truncated = true
		p = p[:remaining]
	}
	capture.b = append(capture.b, p...)
	return n, nil
}

func runUpdateCommand(path string, args, env []string) (updateRunResult, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	var stdout, stderr updateCapture
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return updateRunResult{stdout: stdout.b, stderr: stderr.b,
		stdoutTruncated: stdout.truncated, stderrTruncated: stderr.truncated}, err
}

func updateCommandFailure(command string, result updateRunResult, err error) string {
	message := command + " failed: " + err.Error()
	for _, stream := range []struct {
		name      string
		b         []byte
		truncated bool
	}{{"stderr", result.stderr, result.stderrTruncated}, {"stdout", result.stdout, result.stdoutTruncated}} {
		if len(stream.b) > updateDiagnosticCap {
			stream.b, stream.truncated = stream.b[:updateDiagnosticCap], true
		}
		if len(stream.b) > 0 {
			message += "; " + stream.name + ": " + strconv.Quote(strings.ToValidUTF8(string(stream.b), "�"))
		}
		if stream.truncated {
			message += " [" + stream.name + " truncated]"
		}
	}
	return message
}

// Accept exactly the ordinary version envelope, without banners, trailing JSON,
// unknown fields or duplicate keys. The replacement's output is untrusted.
func parseUpdateVersionReply(result updateRunResult) (string, error) {
	if result.stdoutTruncated || len(result.stdout) > updateOutputLimit || !utf8.Valid(result.stdout) {
		return "", fmt.Errorf("version output is oversized or is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", fmt.Errorf("version output is not a pairmux.v1 version envelope")
	}
	seen := map[string]bool{}
	var schema, status, value string
	var ok bool
	for decoder.More() {
		token, err = decoder.Token()
		key, isString := token.(string)
		if err != nil || !isString || seen[key] {
			return "", fmt.Errorf("version envelope has invalid or duplicate keys")
		}
		seen[key] = true
		switch key {
		case "schema":
			err = decoder.Decode(&schema)
		case "status":
			err = decoder.Decode(&status)
		case "output":
			err = decoder.Decode(&value)
		case "ok":
			err = decoder.Decode(&ok)
		default:
			return "", fmt.Errorf("version envelope has an unexpected field")
		}
		if err != nil {
			return "", fmt.Errorf("version envelope has an invalid field value")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || len(seen) != 4 || schema != core.SchemaID || !ok || status != "ok" {
		return "", fmt.Errorf("version output is not a successful pairmux.v1 version envelope")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return "", fmt.Errorf("version output contains trailing data")
	}
	return value, nil
}
