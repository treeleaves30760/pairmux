#!/bin/sh
# pairmux installer — POSIX sh, safe for `curl -fsSL .../install.sh | sh`.
#
#   Usage:
#     curl -fsSL https://raw.githubusercontent.com/treeleaves30760/pairmux/main/install.sh | sh
#     ./install.sh [--version vX.Y.Z] [--dry-run] [--help]
#
#   Environment:
#     PAIRMUX_INSTALL_DIR   install target (default: ~/.local/bin)
#
# It detects OS/arch, downloads the matching GitHub release archive, verifies
# its sha256 against checksums.txt, installs the binary without sudo, warns if
# tmux is missing or older than 3.2, and confirms with `pairmux version`.
set -eu

REPO="treeleaves30760/pairmux"

# --- output helpers -------------------------------------------------------
info() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
err()  { printf 'error: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat <<'EOF'
pairmux installer

usage: install.sh [--version vX.Y.Z] [--dry-run] [--help]

options:
  --version vX.Y.Z   install a specific tagged release (default: latest)
  --dry-run          print the resolved download URL and install dir, then exit
                     (no network, nothing written)
  -h, --help         show this help

environment:
  PAIRMUX_INSTALL_DIR   install target directory (default: ~/.local/bin)
EOF
}

# --- download primitives (curl or wget) -----------------------------------
download() { # download URL OUTFILE
	if have curl; then
		curl -fsSL "$1" -o "$2"
	elif have wget; then
		wget -qO "$2" "$1"
	else
		err "need curl or wget to download files"
	fi
}

fetch() { # fetch URL to stdout
	if have curl; then
		curl -fsSL "$1"
	elif have wget; then
		wget -qO- "$1"
	else
		err "need curl or wget to download files"
	fi
}

sha256_file() { # print the sha256 hex digest of FILE
	if have sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		err "need sha256sum or shasum to verify the download"
	fi
}

strip_v() { printf '%s\n' "${1#v}"; }

# --- platform detection ---------------------------------------------------
detect_platform() {
	os_raw=$(uname -s)
	case "$os_raw" in
	Darwin) OS=darwin ;;
	Linux) OS=linux ;;
	*) err "unsupported OS: ${os_raw} (pairmux supports darwin and linux; on Windows use WSL)" ;;
	esac

	arch_raw=$(uname -m)
	case "$arch_raw" in
	arm64 | aarch64) ARCH=arm64 ;;
	x86_64 | amd64) ARCH=amd64 ;;
	*) err "unsupported architecture: ${arch_raw}" ;;
	esac
}

resolve_install_dir() {
	# Prefer a user-writable directory so no sudo prompt is ever needed.
	if [ -n "${PAIRMUX_INSTALL_DIR:-}" ]; then
		INSTALL_DIR=$PAIRMUX_INSTALL_DIR
	else
		INSTALL_DIR="${HOME}/.local/bin"
	fi
}

resolve_latest() { # print the latest release tag (e.g. v0.1.0)
	rl_tag=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" |
		grep '"tag_name"' | head -n1 |
		sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
	[ -n "$rl_tag" ] || err "could not determine the latest release tag from the GitHub API"
	printf '%s\n' "$rl_tag"
}

# --- tmux dependency check (non-fatal) ------------------------------------
tmux_hint() {
	if [ "$OS" = darwin ]; then
		printf 'brew install tmux'
	else
		printf 'sudo apt install tmux   (or your distro package manager)'
	fi
}

check_tmux() {
	if ! have tmux; then
		warn "tmux is not installed. pairmux requires tmux >= 3.2 to do anything."
		printf '         install it with: %s\n' "$(tmux_hint)" >&2
		return
	fi
	tv=$(tmux -V 2>/dev/null | grep -oE '[0-9]+\.[0-9]+' | head -n1)
	if [ -z "$tv" ]; then
		warn "could not parse the tmux version; ensure it is >= 3.2"
		return
	fi
	tmaj=${tv%%.*}
	tmin=${tv#*.}
	if [ "$tmaj" -gt 3 ] || { [ "$tmaj" -eq 3 ] && [ "$tmin" -ge 2 ]; }; then
		info "tmux ${tv} detected (>= 3.2)"
	else
		warn "tmux ${tv} is older than the required 3.2."
		printf '         upgrade it with: %s\n' "$(tmux_hint)" >&2
	fi
}

path_hint() {
	case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) : ;;
	*)
		warn "${INSTALL_DIR} is not on your PATH."
		printf '         add this line to your shell profile:\n' >&2
		printf "           export PATH=\"%s:\$PATH\"\n" "$INSTALL_DIR" >&2
		;;
	esac
}

# --- install --------------------------------------------------------------
verify_checksum() { # verify_checksum ARCHIVE SUMSFILE
	vc_expected=$(awk -v f="$ASSET" '$2 == f {print $1}' "$2" | head -n1)
	[ -n "$vc_expected" ] || err "no checksum for ${ASSET} in checksums.txt"
	vc_actual=$(sha256_file "$1")
	if [ "$vc_expected" != "$vc_actual" ]; then
		err "checksum mismatch for ${ASSET}: expected ${vc_expected}, got ${vc_actual}"
	fi
	info "checksum verified (sha256)"
}

do_install() {
	TMPDIR_PM=$(mktemp -d 2>/dev/null || mktemp -d -t pairmux) || err "could not create a temp directory"
	archive="${TMPDIR_PM}/${ASSET}"
	sums="${TMPDIR_PM}/checksums.txt"

	info "downloading ${ASSET}"
	download "$ASSET_URL" "$archive" || err "download failed: ${ASSET_URL}"
	info "downloading checksums.txt"
	download "$CHECKSUMS_URL" "$sums" || err "download failed: ${CHECKSUMS_URL}"

	verify_checksum "$archive" "$sums"

	info "extracting"
	tar -xzf "$archive" -C "$TMPDIR_PM" || err "failed to extract ${archive}"
	[ -f "${TMPDIR_PM}/pairmux" ] || err "archive did not contain a pairmux binary"

	mkdir -p "$INSTALL_DIR" || err "could not create ${INSTALL_DIR}"
	cp "${TMPDIR_PM}/pairmux" "${INSTALL_DIR}/pairmux" || err "could not write to ${INSTALL_DIR} (set PAIRMUX_INSTALL_DIR to a writable dir)"
	chmod 0755 "${INSTALL_DIR}/pairmux"
	info "installed to ${INSTALL_DIR}/pairmux"
}

print_dry_run() {
	printf 'pairmux install.sh — dry run (no network, nothing written)\n\n'
	printf '  os:           %s\n' "$OS"
	printf '  arch:         %s\n' "$ARCH"
	printf '  version:      %s\n' "$TAG"
	printf '  asset:        %s\n' "$ASSET"
	printf '  download url: %s\n' "$ASSET_URL"
	printf '  checksums:    %s\n' "$CHECKSUMS_URL"
	printf '  install dir:  %s\n' "$INSTALL_DIR"
}

print_quickstart() {
	printf '\nQuickstart:\n'
	printf '  pairmux new --name build           # create a managed terminal\n'
	printf '  pairmux run build "make -j4"       # run a command, block until it finishes\n'
	printf '  pairmux ls                         # list terminals and their status\n'
	printf '\nDocs: https://github.com/%s\n' "$REPO"
}

cleanup() {
	if [ -n "${TMPDIR_PM:-}" ] && [ -d "${TMPDIR_PM:-}" ]; then
		rm -rf "$TMPDIR_PM"
	fi
}

main() {
	PINNED_TAG=""
	DRY_RUN=0
	TMPDIR_PM=""

	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			shift
			[ $# -gt 0 ] || err "--version requires an argument (e.g. --version v0.1.0)"
			PINNED_TAG=$1
			;;
		--version=*) PINNED_TAG=${1#*=} ;;
		--dry-run) DRY_RUN=1 ;;
		-h | --help)
			usage
			exit 0
			;;
		*) err "unknown argument: $1 (try --help)" ;;
		esac
		shift
	done

	detect_platform
	resolve_install_dir

	if [ -n "$PINNED_TAG" ]; then
		TAG=$PINNED_TAG
		VERSION_NUM=$(strip_v "$TAG")
	elif [ "$DRY_RUN" -eq 1 ]; then
		# Dry run must not touch the network, so leave the version symbolic.
		TAG="<latest>"
		VERSION_NUM="<latest>"
	else
		info "resolving the latest release"
		TAG=$(resolve_latest)
		VERSION_NUM=$(strip_v "$TAG")
	fi

	ASSET="pairmux_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
	BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"
	ASSET_URL="${BASE_URL}/${ASSET}"
	CHECKSUMS_URL="${BASE_URL}/checksums.txt"

	if [ "$DRY_RUN" -eq 1 ]; then
		print_dry_run
		exit 0
	fi

	trap cleanup EXIT INT TERM

	do_install

	info "verifying: pairmux version"
	installed_ver=$("${INSTALL_DIR}/pairmux" version 2>/dev/null) || err "the installed binary failed to run"
	info "pairmux ${installed_ver} is ready"

	check_tmux
	path_hint
	print_quickstart
}

main "$@"
