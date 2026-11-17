#!/bin/sh
# Regenerate the TUI and CLI screenshots used by README.md, the docs site and
# the homepage. Needs tmux, zsh, Go and python3; touches nothing outside a
# throwaway state directory and the "pmxdemo" endpoint.
#
#   scripts/screenshots/capture.sh [outdir]     (default: website/static/img)
#
# The fleet is staged — the output in each pane is printed by a script, not
# produced by real agents — so the shots are reproducible and show no paths,
# hostnames or user names from the machine that took them.
set -eu
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${1:-$ROOT/website/static/img}
HERE=$ROOT/scripts/screenshots
# /tmp, not $TMPDIR: the activity view prints the journal path.
WORK=$(mktemp -d /tmp/pmxshots.XXXXXX)
SOCK=pmxdemo
SHOT="tmux -L pmxshot"
cleanup() {
	$SHOT kill-server 2>/dev/null || true
	"$WORK/bin/pairmux" --socket $SOCK kill --all >/dev/null 2>&1 || true
	# The demo's tmux servers keep the environment they started with; never
	# let a later run inherit this one's HOME.
	for l in $SOCK $SOCK-lead $SOCK-lead-api; do tmux -L "$l" kill-server 2>/dev/null || true; done
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
for l in $SOCK $SOCK-lead $SOCK-lead-api; do tmux -L "$l" kill-server 2>/dev/null || true; done

mkdir -p "$WORK/bin" "$WORK/state" "$WORK/zdot" "$WORK/acme-shop" "$OUT"
(cd "$ROOT" && go build -o "$WORK/bin/pairmux" ./cmd/pairmux)
# pairmux's zsh integration may wrap ZDOTDIR and source the rc from HOME, so
# the anonymous prompt goes in both.
for rc in "$WORK/zdot/.zshrc" "$WORK/.zshrc"; do
	printf '%s\n' "PROMPT='%F{245}%1~%f %F{79}❯%f '" "RPROMPT=''" >"$rc"
done
# Stand-ins named like the real tools, so the panes and listings read
# naturally and no path from this machine appears in them.
for t in npm migrate go golangci-lint gpg-sign agent-log; do
	printf '#!/bin/sh\nexec sh "%s" %s "$@"\n' "$HERE/fake.sh" "$t" >"$WORK/bin/$t"
	chmod +x "$WORK/bin/$t"
done

# A clean, anonymous environment for every pane the demo starts.
unset TMUX CLAUDECODE PAIRMUX_SCOPE PAIRMUX_NAME PAIRMUX_OWNER_SOCKET 2>/dev/null || true
export PATH="$WORK/bin:$PATH" ZDOTDIR="$WORK/zdot" PAIRMUX_STATE_DIR="$WORK/state" \
	PAIRMUX_SHELL=/bin/zsh PAIRMUX_BACKEND=tmux HOME="$WORK" TERM=xterm-256color LANG=en_US.UTF-8
cd "$WORK/acme-shop"
P="pairmux --socket $SOCK"

# --- stage the fleet ---------------------------------------------------------
# lead is a coordinating agent; it started api and web, and api started its
# own tests and lint sub-agents. deploy stopped at a password prompt, and
# scratch is a dead terminal whose journal is retained.
$P new --name lead >/dev/null
$P run lead "pairmux new --name api && pairmux new --name web" >/dev/null
$P run lead/api "pairmux new --name tests && pairmux new --name lint" >/dev/null
for t in lead lead/api lead/web lead/api/tests lead/api/lint; do $P run $t clear >/dev/null; done
$P run lead/api/tests "go test ./..." >/dev/null
$P run lead/api/lint "golangci-lint run" >/dev/null
$P run lead/web "npm run dev" --timeout 1s >/dev/null || true
$P run lead/api "clear; agent-log api" >/dev/null
$P run lead "clear; agent-log lead" >/dev/null
$P note lead "tests are green on api — ship it once web builds" >/dev/null
$P new --name deploy >/dev/null
$P run deploy clear >/dev/null
$P run deploy "migrate up --env prod" --timeout 2s >/dev/null || true
$P new --name scratch >/dev/null
$P run scratch "echo spike: try the new cache" >/dev/null
$P kill scratch >/dev/null

# --- the TUI ----------------------------------------------------------------
$SHOT -f /dev/null new-session -d -s s -x 140 -y 34 "$WORK/bin/pairmux --socket $SOCK tui"
$SHOT set -g status off
sleep 1.5
snap() { # snap <name> <title>
	sleep 1.2
	$SHOT capture-pane -t s -p -e | python3 -I "$HERE/ansi2svg.py" --title "$2" >"$OUT/$1.svg"
	echo "wrote $OUT/$1.svg"
}
key() { $SHOT send-keys -t s "$@"; }
# Rows sort by name: deploy, lead, lead/api, ...
key Down Down
snap tui-overview "pairmux tui — the whole agent tree, one screen"
key Up Up
snap tui-handoff "pairmux tui — a worker stopped at a password prompt"
key Down 2
snap tui-activity "pairmux tui — activity: commands, exit codes, notes"
key 1 Down Down Down
key :
key -l "ls --tree"
key Enter
snap tui-command "pairmux tui — any pairmux command, read in place"
key Escape
key q

# --- the agent's side: what a supervising agent reads -------------------------
{
	printf '\033[38;5;245macme-shop\033[0m \033[38;5;79m❯\033[0m pairmux ls --tree\n'
	pairmux --socket $SOCK ls --tree --plain | sed -E 's/  \([0-9TZ:-]+\)//'
	printf '\n\033[38;5;245macme-shop\033[0m \033[38;5;79m❯\033[0m pairmux events --tree --wait --status awaiting-input,dead\n'
	(sleep 1; $P run lead/api/tests "gpg-sign release.tar.gz" --timeout 2s >/dev/null 2>&1 || true) &
	pairmux --socket $SOCK events --tree --wait --status awaiting-input,dead --interval 300ms --timeout 20s
	wait
	printf '\n\033[38;5;245macme-shop\033[0m \033[38;5;79m❯\033[0m pairmux peek lead/api/tests\n'
	pairmux --socket $SOCK peek lead/api/tests --tail 4
} >"$WORK/agent.ans" 2>&1
python3 -I "$HERE/ansi2svg.py" --title "an agent supervising its sub-agents" <"$WORK/agent.ans" >"$OUT/cli-supervise.svg"
echo "wrote $OUT/cli-supervise.svg"
