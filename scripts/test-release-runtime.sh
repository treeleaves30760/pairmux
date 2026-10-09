#!/usr/bin/env bash
# Exercise an installed public binary, never a binary built by this test.
# Usage: test-release-runtime.sh /absolute/pairmux X.Y.Z /absolute/log-dir [--reject-update]
set -euo pipefail

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  printf 'usage: %s EXE VERSION LOG_DIR [--reject-update]\n' "$0" >&2
  exit 2
fi
exe=$1
expected=${2#v}
logs=$3
case "$exe:$logs" in /*:/*) ;; *) printf 'executable and log directory must be absolute\n' >&2; exit 2 ;; esac
[ -x "$exe" ] || { printf 'installed executable is missing: %s\n' "$exe" >&2; exit 1; }
[[ "$expected" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || exit 2
[ "$#" -eq 3 ] || [ "$4" = --reject-update ] || exit 2
python=$(command -v python3)
tmux=$(command -v tmux)
# A short, private socket root avoids macOS's Unix socket path-length limit.
work=$(mktemp -d /tmp/pmx-a.XXXXXX)
socket="acceptance-$$"
mkdir -p "$logs" "$work/home" "$work/state" "$work/sockets"
runtime=(env -i PATH="$PATH" HOME="$work/home" SHELL=/bin/bash TERM=xterm-256color
  LC_ALL=C LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-}" TMPDIR="$work"
  TMUX_TMPDIR="$work/sockets" PAIRMUX_SOCKET="$socket" PAIRMUX_STATE_DIR="$work/state")
cleanup() {
  "${runtime[@]}" "$tmux" -L "$socket" kill-server >"$logs/cleanup.txt" 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

check_json() {
  "$python" -I -c '
import json, sys
reply = json.load(sys.stdin)
status, terminal, marker = sys.argv[1:]
if not isinstance(reply, dict) or reply.get("schema") != "pairmux.v1" or reply.get("ok") is not True:
    raise SystemExit("invalid or unsuccessful pairmux.v1 response")
if reply.get("status") != status or "error" in reply:
    raise SystemExit("unexpected status: " + repr(reply.get("status")))
if terminal and reply.get("terminal") != terminal:
    raise SystemExit("wrong terminal")
if marker and (type(reply.get("exit_code")) is not int or reply["exit_code"] != 0 or reply.get("output", "").splitlines().count(marker) != 1):
    raise SystemExit("command did not complete successfully with the unique output marker")
' "$@"
}

"${runtime[@]}" "$exe" version >"$logs/version.txt"
actual=$(<"$logs/version.txt")
[ "$actual" = "$expected" ] || { printf 'version %s; expected %s\n' "$actual" "$expected" >&2; exit 1; }
printf 'executable=%s\nexpected_version=%s\nsocket=%s\n' "$exe" "$expected" "$socket" >"$logs/runtime.txt"
"${runtime[@]}" "$tmux" -V >>"$logs/runtime.txt"
"${runtime[@]}" "$exe" --json doctor >"$logs/doctor.json"
# doctor exits zero even when its report contains issues. All three fields matter.
check_json ok '' '' <"$logs/doctor.json"
name="acceptance-$$"
marker="pairmux-public-runtime-${work##*/}-$$"
"${runtime[@]}" "$exe" --json new --name "$name" --cwd "$work" >"$logs/new.json"
check_json created "$name" '' <"$logs/new.json"
"${runtime[@]}" "$exe" --json run "$name" "printf '%s\\n' '$marker'" --timeout 30s >"$logs/run.json"
check_json 'done' "$name" "$marker" <"$logs/run.json"

if [ "$#" -eq 4 ]; then
  before=$("$python" -I -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$exe")
  rc=0
  "${runtime[@]}" "$exe" --json update >"$logs/update-rejected.json" 2>"$logs/update-rejected.stderr" || rc=$?
  [ "$rc" -ne 0 ] || { printf 'non-uv update unexpectedly succeeded\n' >&2; exit 1; }
  "$python" -I -c '
import hashlib, json, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    reply = json.load(stream)
if reply.get("schema") != "pairmux.v1" or reply.get("ok") is not False or reply.get("status") != "error" or reply.get("error", {}).get("code") != "E_UPDATE":
    raise SystemExit("non-uv installation did not reject update with E_UPDATE")
if hashlib.sha256(open(sys.argv[2], "rb").read()).hexdigest() != sys.argv[3]:
    raise SystemExit("rejected update changed the installed binary")
' "$logs/update-rejected.json" "$exe" "$before"
fi
printf 'runtime acceptance passed for installed pairmux %s\n' "$expected"
