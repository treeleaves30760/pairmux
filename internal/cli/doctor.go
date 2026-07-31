package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/detect"
	"github.com/treeleaves30760/pairmux/internal/journal"
	"github.com/treeleaves30760/pairmux/internal/output"
	"github.com/treeleaves30760/pairmux/internal/shellhooks"
	"github.com/treeleaves30760/pairmux/internal/tmux"
)

// doctorCheck is one environment probe result. fix carries an actionable hint;
// for a failed check (ok==false) it also feeds the envelope's top-level next.
type doctorCheck struct {
	name   string
	ok     bool
	detail string
	fix    string
}

// cmdDoctor probes the environment and prints an actionable report. It works on
// a fresh machine (missing tmux/state dir are reported, not fatal) and never
// disturbs real terminals: the live tier probe runs on an isolated throwaway
// tmux socket and temp state dir. The command always completes with ok=true;
// individual check failures surface as "issues" in the report.
func (c *Ctx) cmdDoctor(args []string) int {
	if _, err := parseFlags(args, flagSpec{}); err != nil {
		return c.usage("pairmux doctor", err.Error())
	}
	if rc, rejected := c.rejectInvalidSocket(); rejected {
		return rc
	}

	tmuxCheck, tmuxOK := c.checkTmux()
	checks := []doctorCheck{
		tmuxCheck,
		c.checkStateDir(),
		c.checkLiveProbe(tmuxOK),
		checkNotifier(),
	}

	body, issues, topFix := renderDoctor(checks)
	status := "ok"
	var next []string
	if issues {
		status = "issues"
		if topFix != "" {
			next = []string{topFix}
		}
	}
	return c.emit(output.Envelope{Status: status, Output: body, Next: next})
}

// checkTmux verifies tmux is on PATH and at least version 3.2. The bool result
// gates the live probe (which needs a working tmux).
func (c *Ctx) checkTmux() (doctorCheck, bool) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return doctorCheck{name: "tmux", detail: "not found on PATH", fix: "install tmux >= 3.2"}, false
	}
	ver, err := c.Tmux.Version()
	if err != nil {
		return doctorCheck{name: "tmux", detail: "found at " + path + " but version probe failed: " + err.Error(),
			fix: "verify your tmux install"}, false
	}
	if !versionAtLeast(ver, 3, 2) {
		return doctorCheck{name: "tmux", detail: fmt.Sprintf("%s at %s (need >= 3.2)", ver, path),
			fix: "upgrade tmux to >= 3.2"}, false
	}
	return doctorCheck{name: "tmux", ok: true, detail: fmt.Sprintf("%s at %s (>= 3.2)", ver, path)}, true
}

// checkStateDir confirms the state root can be created and written. c.StateDir
// is state.BaseDir() for a normal invocation.
func (c *Ctx) checkStateDir() doctorCheck {
	dir := c.namespaceDir()
	ck := doctorCheck{name: "state dir"}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		ck.detail = "cannot create " + dir + ": " + err.Error()
		ck.fix = "set PAIRMUX_STATE_DIR to a writable path"
		return ck
	}
	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		ck.detail = dir + " not writable: " + err.Error()
		ck.fix = "set PAIRMUX_STATE_DIR to a writable path"
		return ck
	}
	_ = os.Remove(probe)
	ck.ok = true
	ck.detail = "writable: " + dir
	if usage, total := stateUsage(dir); usage != "" {
		ck.detail += "; " + usage
		if total > journalSizeGuard {
			ck.fix = "reclaim dead-terminal disk with pairmux prune"
		}
	}
	return ck
}

// stateUsage summarizes the bytes retained under the namespace dir — total
// plus the largest few terminal directories — so a growing state root is
// observable before the filesystem notices. Empty when nothing is stored.
func stateUsage(dir string) (summary string, total int64) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	type dirUse struct {
		name string
		size int64
	}
	var uses []dirUse
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		size := dirSize(filepath.Join(dir, ent.Name()))
		uses = append(uses, dirUse{ent.Name(), size})
		total += size
	}
	if len(uses) == 0 {
		return "", 0
	}
	sort.Slice(uses, func(i, k int) bool { return uses[i].size > uses[k].size })
	top := uses
	if len(top) > 3 {
		top = top[:3]
	}
	parts := make([]string, 0, len(top))
	for _, u := range top {
		parts = append(parts, u.name+" "+humanBytes(u.size))
	}
	return fmt.Sprintf("journals %s across %d dirs (largest: %s)",
		humanBytes(total), len(uses), strings.Join(parts, ", ")), total
}

// checkLiveProbe runs the isolated per-shell tier probe and summarizes the tiers
// achieved. It is ok as long as at least one shell probe ran; degraded hooks are
// reported (with a fix hint) but do not fail the check, since sentinel mode is a
// working fallback.
func (c *Ctx) checkLiveProbe(tmuxOK bool) doctorCheck {
	ck := doctorCheck{name: "live probe"}
	if !tmuxOK {
		ck.detail = "skipped (requires tmux >= 3.2)"
		return ck
	}

	results := liveProbe()
	var parts, degraded []string
	anyOK := false
	for _, r := range results {
		label := filepath.Base(r.shell)
		if r.err != "" {
			parts = append(parts, label+": error ("+r.err+")")
			continue
		}
		anyOK = true
		parts = append(parts, label+": "+r.tier)
		if strings.HasPrefix(r.tier, "hooks-degraded") {
			degraded = append(degraded, label)
		}
	}
	ck.detail = strings.Join(parts, ";  ")
	ck.ok = anyOK
	switch {
	case !anyOK:
		ck.fix = "tmux could not run a probe terminal — check tmux health and that $TMUX is not nested"
	case len(degraded) > 0:
		ck.fix = "hooks degraded for " + strings.Join(degraded, ", ") +
			"; check your shell rc (oh-my-zsh / powerlevel10k / starship) is not clobbering the prompt hooks"
	}
	return ck
}

// checkNotifier reports which desktop-notification backend is available. It is
// informational only and never fails.
func checkNotifier() doctorCheck {
	ck := doctorCheck{name: "notifier", ok: true}
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("osascript"); err == nil {
			ck.detail = "osascript at " + p + " (macOS notifications available)"
		} else {
			ck.detail = "osascript not found; --notify will be a no-op"
		}
	case "linux":
		if p, err := exec.LookPath("notify-send"); err == nil {
			ck.detail = "notify-send at " + p + " (desktop notifications available)"
		} else {
			ck.detail = "notify-send not found; install libnotify for --notify (best-effort)"
		}
	default:
		ck.detail = "no notifier backend for GOOS=" + runtime.GOOS + " (--notify is best-effort)"
	}
	return ck
}

// shellTierResult is one shell's probe outcome.
type shellTierResult struct {
	shell string
	tier  string
	note  string
	err   string
}

// liveProbe creates a throwaway tmux server (isolated socket + temp state dir)
// and, for each candidate shell, spins up a terminal exactly the way `new` does
// and measures the completion-detection tier achieved. It always tears the
// server and temp dir down afterwards.
func liveProbe() []shellTierResult {
	shells := candidateShells()
	if len(shells) == 0 {
		return []shellTierResult{{shell: "(none)", err: "no candidate shells found"}}
	}
	tmpState, err := os.MkdirTemp("", "pmx-doctor-")
	if err != nil {
		return []shellTierResult{{shell: "(all)", err: "mktemp: " + err.Error()}}
	}
	defer os.RemoveAll(tmpState)

	sock := fmt.Sprintf("pmx-doctor-%d", os.Getpid())
	cl := tmux.New(sock)
	defer func() { _ = cl.KillServer() }()

	out := make([]shellTierResult, 0, len(shells))
	for i, sh := range shells {
		out = append(out, probeShell(cl, tmpState, i, sh))
	}
	return out
}

// probeShell mirrors cmdNew's creation primitives on the throwaway server:
// EnsureSession -> shellhooks.Prepare -> pre-create raw.log 0600 -> NewWindow ->
// PipePaneAppend -> WaitReady, then sends `true` and waits for its completion.
func probeShell(cl *tmux.Client, stateDir string, idx int, shell string) shellTierResult {
	name := fmt.Sprintf("d%d-%s", idx, sanitizeShellName(filepath.Base(shell)))
	res := shellTierResult{shell: shell}

	if err := cl.EnsureSession(); err != nil {
		res.err = "ensure session: " + err.Error()
		return res
	}
	argv, prepEnv, mode, err := shellhooks.Prepare(stateDir, shell, nil)
	if err != nil {
		res.err = "prepare shell: " + err.Error()
		return res
	}
	env := map[string]string{"PAIRMUX": "1", "PAIRMUX_NAME": name}
	for k, v := range prepEnv {
		env[k] = v
	}

	dir := filepath.Join(stateDir, name)
	j, err := journal.Open(dir)
	if err != nil {
		res.err = err.Error()
		return res
	}
	if f, err := os.OpenFile(j.RawPath(), os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}

	paneID, err := cl.NewWindow(tmux.NewWindowReq{Name: name, Dir: stateDir, Env: env, Argv: argv})
	if err != nil {
		res.err = "new-window: " + err.Error()
		return res
	}
	if err := cl.PipePaneAppend(paneID, j.RawPath()); err != nil {
		res.err = "pipe-pane: " + err.Error()
		return res
	}
	if mode == core.ModeHooks {
		_ = cl.SendKeys(paneID, "Enter") // nudge one prompt so an A mark is guaranteed after capture
	}

	gotHooks, err := detect.WaitReady(j, mode, 5*time.Second)
	if err != nil {
		res.err = "wait ready: " + err.Error()
		return res
	}

	// Send a trivial command. If a nominal hooks shell emitted no ready mark,
	// exercise the same shell-specific sentinel fallback that `new` records and
	// `run` subsequently uses.
	sent, completionMode := doctorProbeCommand(shell, mode, gotHooks)
	sendOffset := j.Size()
	_ = cl.SendLiteral(paneID, sent)
	_ = cl.SendKeys(paneID, "Enter")
	wr, err := detect.WaitCompletion(j, sendOffset, completionMode, 5*time.Second, 0)
	gotDone := err == nil && wr.Outcome == detect.OutcomeDone

	// Hooks tier fidelity: bash 3.2 emits A/D but never C (no PS0), which
	// weakens human-interleave correlation. Scan the probe journal after the
	// send offset for any output-start (C) mark to distinguish that tier.
	gotC := false
	if mode == core.ModeHooks {
		if data, rerr := j.ReadRange(sendOffset, -1); rerr == nil {
			sc := detect.NewScanner(sendOffset)
			for _, m := range sc.Feed(data) {
				if m.Kind == detect.MarkC {
					gotC = true
					break
				}
			}
		}
	}

	res.tier, res.note = deriveTier(mode, gotHooks, gotC, gotDone)
	return res
}

func doctorProbeCommand(shell string, preparedMode core.Mode, gotHooks bool) (string, core.Mode) {
	completionMode := preparedMode
	if preparedMode == core.ModeHooks && !gotHooks {
		completionMode = core.ModeSentinel
	}
	if completionMode == core.ModeSentinel {
		return "true" + shellhooks.SentinelSuffix(shell), completionMode
	}
	return "true", completionMode
}

// deriveTier maps the observed marks to a tier label:
//
//	hooks                    A + C + D — full shell integration
//	hooks-no-C               A + D but no C (expected for bash 3.2): completion
//	                         detection works, human-interleave correlation is weaker
//	hooks-degraded->sentinel hooks were attempted but A and/or D never arrived
//	sentinel                 non-hooks shell using the sentinel marker
func deriveTier(mode core.Mode, gotHooks, gotC, gotDone bool) (tier, note string) {
	if mode == core.ModeSentinel {
		if gotDone {
			return "sentinel", "completion via sentinel marker"
		}
		return "sentinel", "no sentinel completion detected"
	}
	switch {
	case gotHooks && gotDone && gotC:
		return "hooks", "OSC 133 A + C + D detected"
	case gotHooks && gotDone:
		return "hooks-no-C", "A + D but no C (expected for bash 3.2): completion detection works, human-interleave correlation is weaker"
	case !gotHooks && gotDone:
		return "hooks-degraded->sentinel", "no prompt mark (A); sentinel fallback verified"
	case !gotHooks:
		return "hooks-degraded->sentinel", "no prompt mark (A); sentinel fallback did not complete"
	default:
		return "hooks-degraded->sentinel", "prompt mark (A) seen but no completion mark (D)"
	}
}

// candidateShells returns the distinct, executable shells to probe: $SHELL,
// /bin/bash, and /bin/dash (each only when present). Deduplication is by cleaned
// path so a $SHELL that equals one of the fixed paths is probed once.
func candidateShells() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		cp := filepath.Clean(p)
		if seen[cp] || !isExecutable(cp) {
			return
		}
		seen[cp] = true
		out = append(out, cp)
	}
	add(os.Getenv("SHELL"))
	add("/bin/bash")
	add("/bin/dash")
	return out
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// sanitizeShellName reduces a shell basename to a safe window/journal name.
func sanitizeShellName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "sh"
	}
	return b.String()
}

// parseVersion extracts leading major.minor integers from a tmux version token,
// tolerating a leading label ("next-3.4") and a trailing suffix ("3.7b" -> 3,7).
// A missing minor is 0. ok is false when no leading numeric version is present.
func parseVersion(s string) (major, minor int, ok bool) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] < '0' || s[i] > '9') {
		i++
	}
	if i == len(s) {
		return 0, 0, false
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	maj, err := strconv.Atoi(s[i:j])
	if err != nil {
		return 0, 0, false
	}
	if j < len(s) && s[j] == '.' {
		k := j + 1
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j+1 {
			minor, _ = strconv.Atoi(s[j+1 : k])
		}
	}
	return maj, minor, true
}

// versionAtLeast reports whether the parsed tmux version s is >= major.minor.
func versionAtLeast(s string, major, minor int) bool {
	maj, min, ok := parseVersion(s)
	if !ok {
		return false
	}
	if maj != major {
		return maj > major
	}
	return min >= minor
}

// renderDoctor formats the checks as a ✓/✗ table with per-check detail and fix
// lines, reporting whether any check failed and the first failed check's fix.
func renderDoctor(checks []doctorCheck) (body string, issues bool, topFix string) {
	nameW := 0
	for _, ck := range checks {
		nameW = max(nameW, len(ck.name))
	}
	var b strings.Builder
	b.WriteString("pairmux doctor\n\n")
	for _, ck := range checks {
		mark := "✓"
		if !ck.ok {
			mark = "✗"
			issues = true
			if topFix == "" && ck.fix != "" {
				topFix = ck.fix
			}
		}
		fmt.Fprintf(&b, "%s  %-*s  %s\n", mark, nameW, ck.name, ck.detail)
		if ck.fix != "" {
			fmt.Fprintf(&b, "   %-*s  → %s\n", nameW, "", ck.fix)
		}
	}
	return b.String(), issues, topFix
}
