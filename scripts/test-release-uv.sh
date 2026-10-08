#!/usr/bin/env bash
# Public PyPI installs and the real v0.5.3 -> uv upgrade -> self-refresh path.
set -euo pipefail
[ "$#" -eq 5 ] || { printf 'usage: %s UV PYTHON VERSION PROVENANCE LOG_DIR\n' "$0" >&2; exit 2; }
uv=$1
python=$2
version=${3#v}
provenance=$4
logs=$5
trusted=$(cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d /tmp/pmx-uv-a.XXXXXX)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$logs"
"$uv" --version >"$logs/uv-version.txt"
grep -q '^uv 0\.11\.16\([[:space:]]\|$\)' "$logs/uv-version.txt"
latest=$("$python" -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["latest_pypi_version"])' "$provenance")
expected_hash=$("$python" -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["selected"]["binary_sha256"])' "$provenance")
base_path=$PATH
setup_env() {
  local root="$work/$1"
  mkdir -p "$root/home" "$root/bin" "$root/cache" "$root/config" "$root/data"
  isolated=(env -i PATH="$root/bin:$base_path" HOME="$root/home" SHELL=/bin/bash
    LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-}" XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data"
    XDG_CACHE_HOME="$root/cache" UV_TOOL_DIR="$root/tools" UV_TOOL_BIN_DIR="$root/bin" UV_CACHE_DIR="$root/cache")
  installed="$root/bin/pairmux"
}
install() {
  "${isolated[@]}" "$uv" tool install --no-config --default-index https://pypi.org/simple \
    --no-sources --no-build --no-python-downloads --python "$python" --no-cache "$@"
}
check_release_binary() {
  "$python" -I -c 'import hashlib,sys; actual=hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest(); sys.exit(0 if actual == sys.argv[2] else "installed PyPI binary differs from checksummed public release")' "$installed" "$expected_hash"
}

setup_env pinned
install "pairmux==$version" >"$logs/pinned-install.txt" 2>&1
check_release_binary
"${isolated[@]}" bash "$trusted/test-release-runtime.sh" "$installed" "$version" "$logs/pinned-runtime"

setup_env upgrade
install pairmux==0.5.3 >"$logs/bootstrap-v0.5.3.txt" 2>&1
[ "$("${isolated[@]}" "$installed" version)" = 0.5.3 ]
# Old pin is deliberately removed by an actual uv tool install --upgrade.
install --upgrade --reinstall --prerelease disallow pairmux >"$logs/uv-upgrade.txt" 2>&1
upgraded=$("${isolated[@]}" "$installed" version)
[ "$upgraded" = "$latest" ] || { printf 'public stable changed during acceptance: expected %s, got %s\n' "$latest" "$upgraded" >&2; exit 1; }
# Always verify the ACTUAL upgraded release, even if GitHub/PyPI latest differ.
# A historical requested tag keeps its pinned tests; the upgraded version gets
# its own public native/checksum/wheel provenance (or fails closed if unavailable).
"${isolated[@]}" "$python" -I "$trusted/release-acceptance.py" \
  --installed "$installed" --installed-version "$upgraded" --provenance "$provenance" \
  --out-dir "$logs/upgraded-release" >"$logs/upgraded-release-check.txt" 2>&1
# The trusted helper holds the old canonical file open throughout update,
# requires a different replacement identity, preserves held bytes, and checks
# refreshed bytes/version against that same actual release's verified hash.
"${isolated[@]}" "$python" -I "$trusted/release-acceptance.py" \
  --installed "$installed" --installed-version "$upgraded" \
  --provenance "$logs/upgraded-release/provenance.json" --refresh \
  --out-dir "$logs/refreshed-release" >"$logs/self-refresh-check.txt" 2>&1
"${isolated[@]}" bash "$trusted/test-release-runtime.sh" "$installed" "$upgraded" "$logs/upgraded-runtime"
"$python" -I -c '
import json,sys
p=json.load(open(sys.argv[1]))
print("bootstrap=https://pypi.org/project/pairmux/0.5.3/")
print("upgrade_index=https://pypi.org/simple")
print("upgraded_version="+sys.argv[2])
print("requested_tag_is_latest_on_both="+str(p["tag_is_latest_on_both"]).lower())
if p["tag_is_latest_on_both"] and sys.argv[2] != p["version"]:
    raise SystemExit("latest stable does not match the requested tag")
' "$provenance" "$upgraded" >"$logs/upgrade-provenance.txt"

# Public Bash endpoint is consumed completely in its own fresh download dir.
setup_env bash-installer
download_dir=$(mktemp -d "$work/installer-download.XXXXXX")
url=https://pairmux.treeleaves30760.com/install.sh
curl -q -fsSL --proto '=https' --proto-redir '=https' "$url" -o "$download_dir/install.sh"
"$python" -I -c 'import hashlib,sys; print("source="+sys.argv[1]); print("sha256="+hashlib.sha256(open(sys.argv[2], "rb").read()).hexdigest())' "$url" "$download_dir/install.sh" >"$logs/bash-installer-provenance.txt"
# Stay in the trusted repository, not the download directory. Do not demand
# equality to checkout bytes: the public website may deploy independently.
"${isolated[@]}" env PAIRMUX_INSTALL_DIR="$(dirname "$installed")" \
  bash "$download_dir/install.sh" --version "v$version" >"$logs/bash-install.txt" 2>&1
check_release_binary
"${isolated[@]}" bash "$trusted/test-release-runtime.sh" "$installed" "$version" "$logs/bash-runtime"
printf 'public uv, upgrade/self-refresh, and Bash installer acceptance passed\n'
