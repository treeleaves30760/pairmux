#!/usr/bin/env bash

set -euo pipefail

ROOT=$(unset CDPATH; cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/pairmux-install-test.XXXXXX")
cleanup() {
    local status=$?
    if [ "$status" -ne 0 ] && [ -n "${CASE_DIR:-}" ]; then
        printf 'Failed case: %s\n' "$CASE_DIR" >&2
        cat "$CASE_DIR/errors" >&2
    fi
    rm -rf "$TEST_ROOT"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
FAKE_BIN="$TEST_ROOT/bin"
FIXTURES="$TEST_ROOT/fixtures"
mkdir -p "$FAKE_BIN" "$FIXTURES"

# Keep all cases offline, including uv bootstrap. Fake uv checks the source
# contract before doing any writes; a forgotten environment override fails.
cat >"$FAKE_BIN/uv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for name in "${!UV_@}"; do
    [ "$name" = UV_TOOL_BIN_DIR ] || { printf 'unexpected UV setting: %s\n' "$name" >&2; exit 60; }
done
printf '%s\n' "$*" >>"$PM_TEST_LOG"
[ "$1" = tool ] || exit 61
TOOL_ROOT="$HOME/.test-uv-tools"
if [ "$2" = dir ]; then
    printf '%s\n' "$TOOL_ROOT"
    exit
fi
if [ "$2" = list ]; then
    if [ -f "$TOOL_ROOT/pairmux/uv-receipt.toml" ]; then
        printf 'pairmux v1.2.3 (%s/pairmux)\n' "$TOOL_ROOT"
        printf -- '- pairmux (%s)\n' "$(<"$TOOL_ROOT/pairmux/uv-receipt.toml")"
    fi
    exit
fi
[ "$2" = install ] || exit 61
shift 2
requirement=""
while [ "$#" -gt 0 ]; do
    case "$1" in
    --no-config) config=1 ;;
    --default-index) shift; [ "$1" = https://pypi.org/simple ] || exit 62; index=1 ;;
    --no-sources) sources=1 ;;
    --no-build) binary=1 ;;
    --python) shift; [ "$1" = '>=3.9' ] || exit 63; python=1 ;;
    --upgrade) upgrade=1 ;;
    --reinstall) reinstall=1 ;;
    --no-cache) cache=1 ;;
    pairmux | pairmux==*) requirement=$1 ;;
    *) printf 'unexpected argument: %s\n' "$1" >&2; exit 64 ;;
    esac
    shift
done
[ "${config:-}${index:-}${sources:-}${binary:-}${python:-}${upgrade:-}${reinstall:-}${cache:-}" = 11111111 ] || exit 65
[ -n "$requirement" ] || exit 66
[ "${PM_TEST_UV_FAIL:-0}" = 0 ] || exit 67
mkdir -p "$UV_TOOL_BIN_DIR" "$TOOL_ROOT/pairmux/bin"
if [ -f "$TOOL_ROOT/pairmux/uv-receipt.toml" ]; then
    rm -f "$(<"$TOOL_ROOT/pairmux/uv-receipt.toml")"
fi
if [ -e "$UV_TOOL_BIN_DIR/pairmux" ] || [ -L "$UV_TOOL_BIN_DIR/pairmux" ]; then
    printf 'executable conflict; use a different bin directory\n' >&2
    exit 68
fi
cat >"$TOOL_ROOT/pairmux/bin/pairmux" <<SCRIPT
#!/bin/sh
[ "\${1:-}" = version ] || exit 1
printf '%s\\n' '${PM_TEST_VERSION:-1.2.3}'
SCRIPT
chmod +x "$TOOL_ROOT/pairmux/bin/pairmux"
ln -s "$TOOL_ROOT/pairmux/bin/pairmux" "$UV_TOOL_BIN_DIR/pairmux"
printf '%s\n' "$UV_TOOL_BIN_DIR/pairmux" >"$TOOL_ROOT/pairmux/uv-receipt.toml"
EOF

cat >"$FAKE_BIN/uname" <<'EOF'
#!/bin/sh
case "$1" in
-s) printf '%s\n' "${PM_TEST_OS:-Linux}" ;;
-m) printf '%s\n' "${PM_TEST_ARCH:-x86_64}" ;;
*) exit 2 ;;
esac
EOF

cat >"$FAKE_BIN/tmux" <<'EOF'
#!/bin/sh
printf '%s\n' "tmux ${PM_TEST_TMUX:-3.2}"
EOF

cat >"$FAKE_BIN/curl" <<'EOF'
#!/bin/sh
printf 'download\n' >>"$PM_TEST_LOG"
[ "${PM_TEST_DOWNLOAD_FAIL:-0}" = 0 ] || exit 22
[ "$1" = -q ] && [ "$2" = -fsSL ] || exit 69
[ "$7" = https://astral.sh/uv/install.sh ] && [ "$8" = -o ] || exit 70
cp "$PM_TEST_FIXTURES/install-uv.sh" "$9"
EOF

cat >"$FIXTURES/install-uv.sh" <<'EOF'
#!/bin/sh
set -eu
[ "$UV_NO_MODIFY_PATH" = 1 ] || exit 71
[ -z "${UV_DOWNLOAD_URL:-}${UV_UNMANAGED_INSTALL:-}${CARGO_DIST_FORCE_INSTALL_DIR:-}${INSTALLER_DOWNLOAD_URL:-}${CARGO_HOME:-}" ] || exit 72
printf 'bootstrap\n' >>"$PM_TEST_LOG"
[ "${PM_TEST_BOOTSTRAP_FAIL:-0}" = 0 ] || exit 73
mkdir -p "$UV_INSTALL_DIR"
cp "$PM_TEST_FIXTURES/uv" "$UV_INSTALL_DIR/uv"
chmod +x "$UV_INSTALL_DIR/uv"
EOF
cp "$FAKE_BIN/uv" "$FIXTURES/uv"
chmod +x "$FAKE_BIN/"* "$FIXTURES/install-uv.sh"

new_case() {
    CASE_DIR="$TEST_ROOT/$1"
    mkdir -p "$CASE_DIR/home" "$CASE_DIR/tmp"
    export HOME="$CASE_DIR/home" TMPDIR="$CASE_DIR/tmp"
    export PAIRMUX_INSTALL_DIR="$CASE_DIR/install dir's [test]"
    export PM_TEST_LOG="$CASE_DIR/calls" PM_TEST_FIXTURES="$FIXTURES"
    : >"$PM_TEST_LOG"
    unset PM_TEST_UV_FAIL PM_TEST_DOWNLOAD_FAIL PM_TEST_BOOTSTRAP_FAIL PM_TEST_VERSION PM_TEST_OS PM_TEST_ARCH PM_TEST_TMUX
}

run_installer() {
    PATH="$FAKE_BIN:$PATH" bash "$ROOT/install.sh" "$@" >"$CASE_DIR/output" 2>"$CASE_DIR/errors"
}

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

new_case source-isolation
export UV_INDEX=https://untrusted.invalid/simple UV_EXTRA_INDEX_URL=https://untrusted.invalid/extra
export UV_INDEX_URL=https://untrusted.invalid UV_DEFAULT_INDEX=https://untrusted.invalid
export UV_FIND_LINKS=/untrusted UV_OVERRIDE=/untrusted/override UV_CONSTRAINT=/untrusted/constraints
export UV_CONFIG_FILE=/untrusted/uv.toml UV_TOOL_DIR=/untrusted/tools UV_CACHE_DIR=/untrusted/cache
export UV_TOOL_BIN_DIR=/untrusted/bin UV_OFFLINE=1 UV_INSECURE_HOST=pypi.org UV_PYTHON_DOWNLOADS=never
export UV_PYTHON_INSTALL_MIRROR=https://untrusted.invalid UV_DOWNLOAD_URL=https://untrusted.invalid
export UV_NEW_FUTURE_SOURCE=https://untrusted.invalid CARGO_DIST_FORCE_INSTALL_DIR=/untrusted/bin
export INSTALLER_DOWNLOAD_URL=https://untrusted.invalid/bootstrap CARGO_HOME=/untrusted/cargo
run_installer --version v1.2.3
[ "$("$PAIRMUX_INSTALL_DIR/pairmux" version)" = 1.2.3 ] || fail 'installed version'
grep -q 'https://pypi.org/project/pairmux/' "$CASE_DIR/output"
grep -q 'https://pypi.org/simple' "$CASE_DIR/output"
grep -q 'pairmux==1.2.3' "$PM_TEST_LOG"
[ "$UV_INDEX" = https://untrusted.invalid/simple ] || fail 'changed parent environment'
run_installer --version v1.2.3
[ "$(grep -c 'tool install' "$PM_TEST_LOG")" = 2 ] || fail 'idempotent managed reinstall'

new_case dry-run
run_installer --version v1.2.3 --dry-run
[ ! -s "$PM_TEST_LOG" ] && [ ! -e "$PAIRMUX_INSTALL_DIR" ] || fail 'dry-run side effects'
grep -q 'pairmux==1.2.3' "$CASE_DIR/output"
run_installer --help
[ ! -s "$PM_TEST_LOG" ] || fail 'help side effects'

new_case invalid-pin
if run_installer --version '1.2.3 --index evil'; then fail 'accepted injected version'; fi
[ ! -s "$PM_TEST_LOG" ] || fail 'invalid pin invoked uv'
if run_installer --version=; then fail 'accepted empty pin'; fi
if run_installer --version v01.2.3; then fail 'accepted non-canonical version'; fi
if run_installer --version 'pairmux @ https://untrusted.invalid'; then fail 'accepted URL requirement'; fi

new_case prerelease
export PM_TEST_VERSION=1.2.3-rc.1
run_installer --version v1.2.3-rc.1
grep -q 'pairmux==1.2.3rc1' "$PM_TEST_LOG"

new_case replaced-managed-entrypoint
run_installer --version v1.2.3
rm "$PAIRMUX_INSTALL_DIR/pairmux"
printf 'manual executable\n' >"$PAIRMUX_INSTALL_DIR/pairmux"
if run_installer --version v1.2.3; then fail 'deleted manual replacement of managed entrypoint'; fi
[ "$(<"$PAIRMUX_INSTALL_DIR/pairmux")" = 'manual executable' ] || fail 'clobbered manual replacement'
[ "$(grep -c 'tool install' "$PM_TEST_LOG")" = 1 ] || fail 'invoked reinstall after conflict'
# Moving the requested bin path must not hide the old receipt's conflict.
old_entry="$PAIRMUX_INSTALL_DIR/pairmux"
export PAIRMUX_INSTALL_DIR="$CASE_DIR/new target"
if run_installer --version v1.2.3; then fail 'ignored old receipt entrypoint conflict'; fi
[ "$(<"$old_entry")" = 'manual executable' ] || fail 'deleted old recorded replacement'

new_case invalid-receipt
mkdir -p "$HOME/.test-uv-tools/pairmux"
if run_installer; then fail 'accepted missing receipt'; fi
if grep -q 'tool install' "$PM_TEST_LOG"; then fail 'reinstalled corrupt receipt'; fi

new_case multiline-receipt
run_installer --version v1.2.3
printf '%s\n%s\n' "$CASE_DIR/link)" 'manual/pairmux' >"$HOME/.test-uv-tools/pairmux/uv-receipt.toml"
if run_installer; then fail 'accepted split receipt entrypoint'; fi
[ "$(grep -c 'tool install' "$PM_TEST_LOG")" = 1 ] || fail 'reinstalled malformed listing'

new_case escaped-receipt
run_installer --version v1.2.3
printf '%s\\n%s\n' "$CASE_DIR/link)" 'manual/pairmux' >"$HOME/.test-uv-tools/pairmux/uv-receipt.toml"
if run_installer; then fail 'accepted escaped receipt path'; fi
[ "$(grep -c 'tool install' "$PM_TEST_LOG")" = 1 ] || fail 'reinstalled unsupported escaped receipt'

new_case control-path
export PAIRMUX_INSTALL_DIR="$CASE_DIR/"$'bad\npath'
if run_installer --dry-run; then fail 'accepted control character target'; fi
[ ! -s "$PM_TEST_LOG" ] || fail 'ran uv for control character target'

new_case latest
run_installer
grep -q 'no-cache pairmux$' "$PM_TEST_LOG"

new_case conflict
mkdir -p "$PAIRMUX_INSTALL_DIR"
victim="$CASE_DIR/victim"
printf 'untouched\n' >"$victim"
ln -s "$victim" "$PAIRMUX_INSTALL_DIR/pairmux"
if run_installer --version v1.2.3; then fail 'overwrote unmanaged executable'; fi
[ -L "$PAIRMUX_INSTALL_DIR/pairmux" ] || fail 'changed existing symlink'
[ "$(<"$victim")" = untouched ] || fail 'modified symlink target'

new_case uv-failure
export PM_TEST_UV_FAIL=1
if run_installer; then fail 'ignored uv failure'; fi
[ ! -e "$PAIRMUX_INSTALL_DIR/pairmux" ] || fail 'uv failure installed executable'
grep -q 'PyPI installation failed' "$CASE_DIR/errors"

new_case mismatch
export PM_TEST_VERSION=9.9.9
if run_installer --version v1.2.3; then fail 'ignored mismatched binary version'; fi
grep -q 'expected 1.2.3' "$CASE_DIR/errors"

new_case tmux-old
export PM_TEST_TMUX=2.9
run_installer --version v1.2.3
grep -q 'older than the required' "$CASE_DIR/errors"

# An explicit shadow executable must not be used for install verification.
new_case shadow
cat >"$FAKE_BIN/pairmux" <<'EOF'
#!/bin/sh
exit 99
EOF
chmod +x "$FAKE_BIN/pairmux"
run_installer --version v1.2.3
grep -q 'PATH currently selects' "$CASE_DIR/errors"
rm "$FAKE_BIN/pairmux"

# Hide ambient uv/tmux without hiding basic system utilities.
NO_UV_BIN="$TEST_ROOT/no-uv-bin"
mkdir -p "$NO_UV_BIN"
for name in bash sh mkdir mktemp chmod cp rm grep head cat ln readlink; do
    ln -s "$(type -P "$name")" "$NO_UV_BIN/$name"
done
cp "$FAKE_BIN/uname" "$FAKE_BIN/curl" "$NO_UV_BIN/"
run_bootstrap() {
    PATH="$NO_UV_BIN" "$NO_UV_BIN/bash" "$ROOT/install.sh" "$@" >"$CASE_DIR/output" 2>"$CASE_DIR/errors"
}
# env is needed only for the bootstrap execution, not network downloads.
ln -s "$(type -P env)" "$NO_UV_BIN/env"

new_case bootstrap
run_bootstrap --version v1.2.3
[ -x "$HOME/.local/bin/uv" ] || fail 'uv bootstrap path'
[ "$("$PAIRMUX_INSTALL_DIR/pairmux" version)" = 1.2.3 ] || fail 'bootstrap tool install'
grep -q bootstrap "$PM_TEST_LOG"
grep -q 'tmux is not installed' "$CASE_DIR/errors"
[ -z "$(find "$TMPDIR" -mindepth 1 -print -quit)" ] || fail 'bootstrap temp leak'
[ ! -e "$HOME/.bashrc" ] && [ ! -e "$HOME/.zshrc" ] || fail 'modified shell profile'

new_case download-failure
export PM_TEST_DOWNLOAD_FAIL=1
if run_bootstrap; then fail 'ignored failed download'; fi
if grep -q bootstrap "$PM_TEST_LOG"; then fail 'executed failed download'; fi
[ -z "$(find "$TMPDIR" -mindepth 1 -print -quit)" ] || fail 'failed download temp leak'

new_case bootstrap-failure
export PM_TEST_BOOTSTRAP_FAIL=1
if run_bootstrap; then fail 'ignored bootstrap failure'; fi
[ ! -e "$PAIRMUX_INSTALL_DIR/pairmux" ] || fail 'installed after bootstrap failure'
[ -z "$(find "$TMPDIR" -mindepth 1 -print -quit)" ] || fail 'failed bootstrap temp leak'

new_case unsupported-platform
export PM_TEST_OS=Windows_NT
if run_installer --dry-run; then fail 'accepted Windows native'; fi
[ ! -s "$PM_TEST_LOG" ] || fail 'unsupported platform ran uv'

printf '%s\n' 'install.sh PyPI/uv tests passed (offline mocks)'
