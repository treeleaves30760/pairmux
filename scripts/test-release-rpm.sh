#!/usr/bin/env bash
# Runs only inside the official Fedora container on an ephemeral hosted runner.
set -euo pipefail
[ "$#" -eq 4 ] || { printf 'usage: %s RPM VERSION BINARY_SHA256 LOG_DIR\n' "$0" >&2; exit 2; }
package=$1
version=$2
binary_hash=$3
logs=$4
trusted=$(cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$logs"
cat /etc/os-release >"$logs/fedora-os-release.txt"
uname -a >>"$logs/fedora-os-release.txt"
[ "$(uname -m)" = x86_64 ]
if rpm -q pairmux >"$logs/pairmux-before.txt" 2>&1; then
  printf 'pairmux was already installed in the Fedora image\n' >&2; exit 1
fi
if rpm -q tmux >"$logs/tmux-before.txt" 2>&1; then
  printf 'tmux was already installed; dependency installation cannot be proved\n' >&2; exit 1
fi
rpm -qp --requires "$package" >"$logs/rpm-requires.txt"
grep -Eq '^tmux >= 3\.2$' "$logs/rpm-requires.txt"
# The release RPM is unsigned; public SHA256 was checked on the host first.
# Do not install tmux explicitly: dnf must satisfy pairmux's real dependency.
dnf -y --setopt=install_weak_deps=False --setopt=localpkg_gpgcheck=0 \
  install "$package" python3 >"$logs/dnf-install.txt" 2>&1
rpm -q --qf '%{NAME} %{VERSION} %{ARCH}\n' pairmux tmux >"$logs/installed-packages.txt"
[ "$(rpm -q --qf '%{VERSION}' pairmux)" = "$version" ]
[ "$(rpm -q --qf '%{ARCH}' pairmux)" = x86_64 ]
[ "$(rpm -qf --qf '%{NAME}' /usr/bin/pairmux)" = pairmux ]
rpm -V pairmux >"$logs/rpm-verify.txt"
python3 -I -c 'import hashlib,sys; sys.exit(0 if hashlib.sha256(open("/usr/bin/pairmux", "rb").read()).hexdigest() == sys.argv[1] else "installed RPM binary differs from the checksummed native archive")' "$binary_hash"
bash "$trusted/test-release-runtime.sh" /usr/bin/pairmux "$version" "$logs/runtime" --reject-update
printf 'dnf installation, dependency, ownership, and runtime acceptance passed\n'
