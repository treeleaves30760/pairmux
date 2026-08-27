package detect

import (
	"os"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
)

func TestIsInteractivePrompt(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		// password / passphrase / passcode class
		{"Password:", true},
		{"Password: ", true},
		{"password:", true},
		{"[sudo] password for treeleaves:", true},
		{"Enter passphrase for key '/home/u/.ssh/id_ed25519':", true},
		{"Passcode:", true},
		{"Enter PEM pass phrase:", true},
		// non-password secret prompts (smartcards, MFA, API credentials)
		{"Enter PIN:", true},
		{"PIN for token 'YubiKey':", true},
		{"MFA Token:", true},
		{"Verification code:", true},
		{"2FA code:", true},
		{"Enter your API key:", true},
		{"Client secret:", true},
		// localized sudo password prompts (the standard translations)
		{"[sudo] treeleaves 的密碼：", true},
		{"[sudo] treeleaves 的密码：", true},
		{"[sudo] Passwort für treeleaves:", true},
		{"[sudo] Mot de passe de treeleaves :", true},
		{"[sudo] treeleaves のパスワード:", true},
		{"Contraseña para treeleaves:", true},
		{"Senha para treeleaves:", true},
		{"treeleaves의 암호:", true},
		{"Пароль для treeleaves:", true},
		// confirms
		{"Do you want to continue? [Y/n]", true},
		{"Proceed [y/N]?", true},
		{"Replace existing file? [y/N] ", true},
		{"Overwrite? (y/n)", true},
		{"Delete branch? (yes/no)", true},
		{"Are you sure you want to continue connecting (yes/no)?", true},
		// multi-option confirm variants (apt, pacman, interactive rebase tools)
		{"Apply this hunk [y/N/a]?", true},
		{"Continue? (y/N/q)", true},
		// pagers
		{"--More--(56%)", true},
		{"--More--", true},
		{"(END)", true},
		{"/etc/hosts (END)", true}, // real less status line: "<filename> (END)"
		{":", true},                // less's mid-file prompt
		// press-key
		{"Press any key to continue . . .", true},
		{"press ENTER to continue", true},
		{"Press RETURN to acknowledge", true},
		// Python's primary REPL prompt. The continuation prompt (`...`) is
		// intentionally excluded because it is common ordinary output.
		{">>>", true},
		{">>> ", true},

		// negatives
		{"", false},
		{"%", false},
		{"$", false},
		{"user@host ~ %", false},
		{"/usr/local/bin:", false},
		{"/Users/u/go/bin:", false},
		{"make: entering directory '/src':", false},
		{"wrong password for user", false},
		{"password stored in keychain", false},
		{"[FAIL] test_login", false},
		{"reached (END) of section during parse", false}, // (END) mid-sentence is not a pager
		{"path/to/file:", false},                         // trailing colon alone trips neither pager nor password rule
		{"options are [yes|no]", false},
		{"1 file changed, 2 insertions(+)", false},
		{"See more at https://example.com", false},
		{"Express delivery: enter address", false},
		{"compiling main.go", false},
		{"Loading... 50%", false},
		{"...", false},
		{"result >>>", false},
		{">>> result", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := isInteractivePrompt(tt.line); got != tt.want {
				t.Fatalf("isInteractivePrompt(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestLooksSecret(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"Password:", true},
		{"Password: ", true},
		{"[sudo] password for treeleaves:", true},
		{"Enter passphrase for key '/x':", true},
		{"Passcode:", true},
		{"Enter PIN:", true},
		{"MFA Token:", true},
		{"Verification code:", true},
		{"[sudo] treeleaves 的密碼：", true},
		{"[sudo] Passwort für treeleaves:", true},
		{"[sudo] Mot de passe de treeleaves :", true},
		{"[sudo] treeleaves のパスワード:", true},
		{"Do you want to continue? [Y/n]", false},
		{"(END)", false},
		{"Press any key to continue", false},
		{"/usr/local/bin:", false},
		{"wrong password for user", false},
		{"token refresh failed, retrying", false},
		{"Building module pin_layout:", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := LooksSecret(tt.prompt); got != tt.want {
				t.Fatalf("LooksSecret(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestSecretPromptEnvExtension(t *testing.T) {
	const custom = "Autoryzacja hasłem:" // a prompt no builtin pattern knows
	if LooksSecret(custom) {
		t.Fatalf("control failed: %q already matches builtin patterns", custom)
	}

	t.Setenv(SecretPromptEnv, `(?i)hasłem.*:\s*$`)
	if !LooksSecret(custom) {
		t.Fatalf("LooksSecret(%q) = false with matching %s", custom, SecretPromptEnv)
	}
	if !isInteractivePrompt(custom) {
		t.Fatalf("isInteractivePrompt(%q) = false — the extension must also upgrade running to awaiting-input", custom)
	}
	if !LooksSecret("Password:") {
		t.Fatal("extension must extend, not replace, the builtin patterns")
	}

	// An invalid pattern is ignored: the builtin floor stays active.
	t.Setenv(SecretPromptEnv, `([unclosed`)
	if LooksSecret(custom) {
		t.Fatalf("invalid %s must be ignored", SecretPromptEnv)
	}
	if !LooksSecret("Password:") {
		t.Fatal("builtin patterns must survive an invalid extension")
	}
}

// setRawMtime moves raw.log's mtime by delta from now. A negative delta
// backdates the file (journal is quiet); a positive delta pushes the mtime into
// the future so time.Since stays below the quiet threshold no matter how slow
// the test machine is.
func setRawMtime(t *testing.T, path string, delta time.Duration) {
	t.Helper()
	ts := time.Now().Add(delta)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestRefine(t *testing.T) {
	tests := []struct {
		name       string
		st         core.Status
		mode       core.Mode
		raw        string
		noRaw      bool
		mtimeDelta time.Duration // applied to raw.log
		wantStatus core.Status
		wantLine   string
	}{
		{name: "non-running idle passthrough", st: core.StatusIdle, mode: core.ModeHooks,
			raw: "Password:", mtimeDelta: -2 * time.Second, wantStatus: core.StatusIdle},
		{name: "non-running dead passthrough", st: core.StatusDead, mode: core.ModeSentinel,
			raw: "Password:", mtimeDelta: -2 * time.Second, wantStatus: core.StatusDead},
		{name: "fresh mtime stays running", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "Password:", mtimeDelta: time.Hour, wantStatus: core.StatusRunning},
		{name: "quiet password prompt", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "$ sudo make install\nPassword:", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: "Password:"},
		{name: "quiet ANSI-wrapped confirm", st: core.StatusRunning, mode: core.ModeSentinel,
			raw: "installing\n\x1b[1mContinue? [y/N]\x1b[0m ", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: "Continue? [y/N]"},
		{name: "CR overwrite keeps final line", st: core.StatusRunning, mode: core.ModeHooks,
			raw:        "downloading 10%\rdownloading 100%\nAre you sure you want to continue connecting (yes/no)? ",
			mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: "Are you sure you want to continue connecting (yes/no)?"},
		{name: "pager END", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "line1\nline2\n(END)", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: "(END)"},
		// Exact journal tail bytes captured from a real `less /etc/hosts`
		// session: inverse-video status line "<filename> (END)".
		{name: "real less END status line", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "127.0.0.1 localhost\n\x1b[7m/etc/hosts (END)\x1b[27m\x1b[K", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: "/etc/hosts (END)"},
		{name: "less mid-file colon prompt", st: core.StatusRunning, mode: core.ModeSentinel,
			raw: "chunk of file\n:", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: ":"},
		{name: "quiet Python REPL prompt", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "$ python3\nPython 3.x\n>>> ", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: ">>>"},
		{name: "quiet non-prompt stays running", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "$ make\ncompiling main.go", mtimeDelta: -2 * time.Second, wantStatus: core.StatusRunning},
		{name: "trailing blanks skipped", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "Press any key to continue . . .\n\n", mtimeDelta: -2 * time.Second,
			wantStatus: core.StatusAwaitingInput, wantLine: "Press any key to continue . . ."},
		{name: "missing raw stays running", st: core.StatusRunning, mode: core.ModeHooks,
			noRaw: true, wantStatus: core.StatusRunning},
		{name: "empty raw stays running", st: core.StatusRunning, mode: core.ModeHooks,
			raw: "", mtimeDelta: -2 * time.Second, wantStatus: core.StatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := mustOpen(t)
			if !tt.noRaw {
				writeRawFile(t, j, []byte(tt.raw))
				setRawMtime(t, j.RawPath(), tt.mtimeDelta)
			}
			st, line := Refine(j, tt.st, tt.mode)
			if st != tt.wantStatus || line != tt.wantLine {
				t.Fatalf("Refine = (%v, %q), want (%v, %q)", st, line, tt.wantStatus, tt.wantLine)
			}
		})
	}
}

// TestClassifyPending pins the one difference between Classify and
// ClassifyPending: the settle gate, and only the settle gate. A prompt that has
// just been printed is invisible to Classify for refineQuiet — which is the
// whole life of a prompt a `wait --pattern` has just handed to its caller — and
// visible to ClassifyPending straight away. Everything the gate was not
// protecting stays where it was: a working command is still not a prompt, and
// the branches with silence thresholds of their own still have to earn them.
func TestClassifyPending(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		mtimeDelta time.Duration
		want       core.Status
		wantLine   string
	}{
		{name: "fresh recognised prompt", raw: "$ ssh host\nPassword: ", mtimeDelta: time.Hour,
			want: core.StatusAwaitingInput, wantLine: "Password:"},
		{name: "fresh confirm", raw: "Continue? [y/N] ", mtimeDelta: time.Hour,
			want: core.StatusAwaitingInput, wantLine: "Continue? [y/N]"},
		{name: "quiet recognised prompt still works", raw: "Verification code: ", mtimeDelta: -2 * time.Second,
			want: core.StatusAwaitingInput, wantLine: "Verification code:"},
		// The gate's actual job — an unrecognised part-line — is done by
		// inferQuiet, which ClassifyPending leaves alone, so a command that
		// printed "Building... " and went to work is no likelier to be called a
		// question than it was before.
		{name: "fresh unrecognised part-line is not a prompt", raw: "Building... ", mtimeDelta: time.Hour,
			want: core.StatusRunning},
		{name: "briefly quiet unrecognised part-line is not a prompt", raw: "Building... ",
			mtimeDelta: -2 * time.Second, want: core.StatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := mustOpen(t)
			writeRawFile(t, j, []byte(tt.raw))
			setRawMtime(t, j.RawPath(), tt.mtimeDelta)
			st, p := ClassifyPending(j, core.StatusRunning, core.ModeHooks, "")
			if st != tt.want || p.Line != tt.wantLine {
				t.Fatalf("ClassifyPending = (%v, %q), want (%v, %q)", st, p.Line, tt.want, tt.wantLine)
			}
			// Same input through the gated form: only the fresh prompts differ,
			// and they differ by being missed.
			gated, _ := Classify(j, core.StatusRunning, core.ModeHooks, "")
			if tt.mtimeDelta > 0 && gated != core.StatusRunning {
				t.Fatalf("Classify = %v on a fresh journal, want running (the gate is what ClassifyPending drops)", gated)
			}
		})
	}
}

// TestClassifyPendingPassesThroughSettledStatus: dropping the settle gate must
// not make a non-running terminal classifiable, or an idle shell sitting on a
// leftover prompt-shaped line would be reported as a question.
func TestClassifyPendingPassesThroughSettledStatus(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("Password:"))
	for _, st := range []core.Status{core.StatusIdle, core.StatusDead, core.StatusUnknown} {
		if got, p := ClassifyPending(j, st, core.ModeHooks, ""); got != st || p.Waiting() {
			t.Fatalf("ClassifyPending(%v) = (%v, %+v), want the status back untouched", st, got, p)
		}
	}
}

// TestRefineReadOnly guards that Refine writes nothing.
func TestRefineReadOnly(t *testing.T) {
	j := mustOpen(t)
	writeRawFile(t, j, []byte("Password:"))
	setRawMtime(t, j.RawPath(), -2*time.Second)
	if _, err := os.Stat(j.Dir); err != nil {
		t.Fatalf("stat: %v", err)
	}
	st, _ := Refine(j, core.StatusRunning, core.ModeHooks)
	if st != core.StatusAwaitingInput {
		t.Fatalf("Refine = %v, want awaiting-input", st)
	}
	if evs, err := j.Events(); err != nil || len(evs) != 0 {
		t.Fatalf("Refine wrote events: %v (err %v)", evs, err)
	}
}

// correlatedStream builds: strayD(7) "human\n" C "out\n" D(0) and returns the
// stream plus each mark's absolute start offset.
func correlatedStream() (stream []byte, strayStart, cStart, dStart int64) {
	stream = append(stream, buildOSC("133;D;7", "bel")...)
	strayStart = 0
	stream = append(stream, []byte("human ran something\n")...)
	cStart = int64(len(stream))
	stream = append(stream, buildOSC("133;C", "bel")...)
	stream = append(stream, []byte("out\n")...)
	dStart = int64(len(stream))
	stream = append(stream, buildOSC("133;D;0", "st")...)
	return
}

func TestWaitCompletionCorrelated(t *testing.T) {
	dOnly := buildOSC("133;D;5", "bel")
	cOnly := append([]byte("started\n"), buildOSC("133;C", "bel")...)

	// cdStream: plain C-then-D command cycle, no stray marks.
	var cdStream []byte
	cdStream = append(cdStream, []byte("echo hi\n")...)
	cdStream = append(cdStream, buildOSC("133;C", "bel")...)
	cdStream = append(cdStream, []byte("hi\n")...)
	cdDStart := int64(len(cdStream))
	cdStream = append(cdStream, buildOSC("133;D;0", "bel")...)

	full, strayStart, _, dStart := correlatedStream()

	tests := []struct {
		name      string
		raw       []byte
		requireC  bool
		timeout   time.Duration
		wantOut   WaitOutcome
		wantExit  int
		wantStart int64
	}{
		{"C then D", cdStream, true, 3 * time.Second, OutcomeDone, 0, cdDStart},
		{"stray D before C ignored", full, true, 3 * time.Second, OutcomeDone, 0, dStart},
		{"requireC=false takes first D", full, false, 3 * time.Second, OutcomeDone, 7, strayStart},
		{"lone D requireC=false immediate", dOnly, false, 3 * time.Second, OutcomeDone, 5, 0},
		{"lone D requireC zero timeout accepts held", dOnly, true, 0, OutcomeDone, 5, 0},
		{"C but no D times out", cOnly, true, 300 * time.Millisecond, OutcomeTimeout, -1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := mustOpen(t)
			writeRawFile(t, j, tt.raw)
			res, err := WaitCompletionCorrelated(j, 0, tt.timeout, 20*time.Millisecond, tt.requireC)
			if err != nil {
				t.Fatalf("WaitCompletionCorrelated: %v", err)
			}
			if res.Outcome != tt.wantOut || res.ExitCode != tt.wantExit || res.MarkStart != tt.wantStart {
				t.Fatalf("res = %+v, want outcome=%v exit=%d start=%d", res, tt.wantOut, tt.wantExit, tt.wantStart)
			}
		})
	}
}

// TestWaitCompletionCorrelatedGrace: a D with no C anywhere is accepted, but
// only after the grace window (bash 3.2 shells emit no C).
func TestWaitCompletionCorrelatedGrace(t *testing.T) {
	j := mustOpen(t)
	seq := buildOSC("133;D;3", "bel")
	writeRawFile(t, j, seq)

	start := time.Now()
	res, err := WaitCompletionCorrelated(j, 0, 10*time.Second, 20*time.Millisecond, true)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("WaitCompletionCorrelated: %v", err)
	}
	if res.Outcome != OutcomeDone || res.ExitCode != 3 || res.MarkStart != 0 || res.EndOffset != int64(len(seq)) {
		t.Fatalf("res = %+v, want Done exit=3 start=0 end=%d", res, len(seq))
	}
	// The 250ms grace must have been observed (lower bound only — a loaded
	// machine can stretch it, never shrink it).
	if elapsed < 200*time.Millisecond {
		t.Fatalf("returned after %v, want >= ~250ms grace before accepting C-less D", elapsed)
	}
}

// TestWaitCompletionCorrelatedGoroutine appends C, output, and a split D with
// delays while the wait is in flight; offsets must still be exact.
func TestWaitCompletionCorrelatedGoroutine(t *testing.T) {
	j := mustOpen(t)
	initial := []byte("$ ")
	from := int64(len(initial))
	writeRawFile(t, j, initial)

	pre := append([]byte("echo hi\n"), buildOSC("133;C", "bel")...)
	pre = append(pre, []byte("hi\n")...)
	dSeq := buildOSC("133;D;0", "st")

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(40 * time.Millisecond)
		appendRaw(t, j, pre[:5])
		time.Sleep(40 * time.Millisecond)
		appendRaw(t, j, pre[5:])
		time.Sleep(40 * time.Millisecond)
		appendRaw(t, j, dSeq[:3]) // completion mark split across appends
		time.Sleep(30 * time.Millisecond)
		appendRaw(t, j, dSeq[3:])
	}()

	res, err := WaitCompletionCorrelated(j, from, 10*time.Second, 15*time.Millisecond, true)
	<-done
	if err != nil {
		t.Fatalf("WaitCompletionCorrelated: %v", err)
	}
	wantStart := from + int64(len(pre))
	if res.Outcome != OutcomeDone || res.ExitCode != 0 {
		t.Fatalf("res = %+v, want Done exit=0", res)
	}
	if res.MarkStart != wantStart || res.EndOffset != wantStart+int64(len(dSeq)) {
		t.Fatalf("offsets = (%d,%d), want (%d,%d)", res.MarkStart, res.EndOffset, wantStart, wantStart+int64(len(dSeq)))
	}
}
