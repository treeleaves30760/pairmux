#!/usr/bin/env bash
# pairmux installer — installs the platform wheel from public PyPI with uv.
#
#   curl -fsSL https://pairmux.treeleaves30760.com/install.sh | bash
#   bash install.sh [--version vX.Y.Z] [--dry-run] [--help]
#
# PAIRMUX_INSTALL_DIR selects the executable directory (default: ~/.local/bin).
# uv and, if needed, Python are bootstrapped from Astral's official upstream;
# the pairmux package itself is resolved exclusively from https://pypi.org/simple.
set -euo pipefail

PYPI_INDEX="https://pypi.org/simple"
PYPI_PROJECT="https://pypi.org/project/pairmux/"
UV_INSTALLER="https://astral.sh/uv/install.sh"
DOCS="https://pairmux-docs.treeleaves30760.com"

info() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
err()  { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat <<'EOF'
pairmux PyPI installer

usage: bash install.sh [--version vX.Y.Z] [--dry-run] [--help]

options:
  --version vX.Y.Z   install a specific version (default: latest stable on PyPI)
                     canonical -alpha.N, -beta.N and -rc.N pins are also accepted
  --dry-run          print the source, command and target without network or writes
  -h, --help         show this help

environment:
  PAIRMUX_INSTALL_DIR   executable directory (default: ~/.local/bin)

Requires macOS 12+ or glibc Linux, x86-64 or ARM64. uv creates a Python >=3.9
isolated tool environment; the installed command is a native Go binary.
tmux >=3.2 is a separate runtime dependency. No sudo or shell-profile edits.
Inherited UV_* settings and uv configuration files are ignored for this install.
EOF
}

# uv's --no-config does not ignore environment variables. Scope the reset to
# child processes, including bootstrap download/mirror and constraint settings.
clean_uv() (
	local name
	for name in "${!UV_@}" "${!CARGO_DIST_@}" "${!INSTALLER_@}"; do
		[ -z "$name" ] || unset "$name"
	done
	unset CARGO_HOME
	export UV_TOOL_BIN_DIR="$INSTALL_DIR"
	"$@"
)

resolve_version() {
	REQUIREMENT="pairmux"
	VERSION_NUM=""
	if [ -z "$PINNED_TAG" ]; then
		return
	fi
	VERSION_NUM=${PINNED_TAG#v}
	local pattern='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$'
	[[ "$VERSION_NUM" =~ $pattern ]] || err "invalid version: $PINNED_TAG (expected vX.Y.Z or vX.Y.Z-rc.N)"
	local wheel_version="$VERSION_NUM" pre
	case "$VERSION_NUM" in
	*-alpha.*) pre=${VERSION_NUM##*-alpha.}; wheel_version="${VERSION_NUM%-alpha.*}a${pre}" ;;
	*-beta.*) pre=${VERSION_NUM##*-beta.}; wheel_version="${VERSION_NUM%-beta.*}b${pre}" ;;
	*-rc.*) pre=${VERSION_NUM##*-rc.}; wheel_version="${VERSION_NUM%-rc.*}rc${pre}" ;;
	esac
	REQUIREMENT="pairmux==${wheel_version}"
}

detect_platform() {
	local os_raw arch_raw
	os_raw=$(uname -s)
	case "$os_raw" in
	Darwin) OS=darwin ;;
	Linux) OS=linux ;;
	*) err "unsupported OS: $os_raw (on Windows, use WSL)" ;;
	esac
	arch_raw=$(uname -m)
	case "$arch_raw" in
	arm64 | aarch64 | x86_64 | amd64) : ;;
	*) err "unsupported architecture: $arch_raw" ;;
	esac
}

resolve_install_dir() {
	[ -n "${HOME:-}" ] || err "HOME must be set to a user-writable directory"
	INSTALL_DIR=${PAIRMUX_INSTALL_DIR:-"$HOME/.local/bin"}
	[[ ! "$INSTALL_DIR" =~ [[:cntrl:]] ]] || err "install directory must not contain control characters"
	case "$INSTALL_DIR" in
	/*) : ;;
	*) INSTALL_DIR="$PWD/$INSTALL_DIR" ;;
	esac
	[[ ! "$INSTALL_DIR$HOME" =~ [[:cntrl:]] ]] || err "HOME and install paths must not contain control characters"
	UV_INSTALL_DIR_PM="$HOME/.local/bin"
}

download() {
	if have curl; then
		curl -q -fsSL --proto '=https' --proto-redir '=https' "$1" -o "$2"
	elif have wget; then
		WGETRC=/dev/null wget --https-only -qO "$2" "$1"
	else
		err "need curl or wget to bootstrap uv"
	fi
}

ensure_uv() {
	UV_BIN=$(type -P uv || true)
	if [ -n "$UV_BIN" ]; then
		case "$UV_BIN" in /*) : ;; *) UV_BIN="$PWD/$UV_BIN" ;; esac
		return
	fi
	if [ -x "$UV_INSTALL_DIR_PM/uv" ]; then
		UV_BIN="$UV_INSTALL_DIR_PM/uv"
		return
	fi
	local executable
	for executable in uv uvx; do
		if [ -e "$UV_INSTALL_DIR_PM/$executable" ] || [ -L "$UV_INSTALL_DIR_PM/$executable" ]; then
			err "refusing to overwrite $UV_INSTALL_DIR_PM/$executable; install uv manually first"
		fi
	done
	TMPDIR_PM=$(mktemp -d "${TMPDIR:-/tmp}/pairmux-uv.XXXXXX") || err "could not create a temporary directory"
	info "uv is missing; bootstrapping from $UV_INSTALLER (not PyPI)"
	clean_uv download "$UV_INSTALLER" "$TMPDIR_PM/install-uv.sh" || err "uv installer download failed; nothing executed"
	# Execute only a fully downloaded script, not a transport pipeline. Leave the
	# caller's shell profiles alone and use the installed binary by absolute path.
	clean_uv env UV_INSTALL_DIR="$UV_INSTALL_DIR_PM" UV_NO_MODIFY_PATH=1 \
		sh "$TMPDIR_PM/install-uv.sh" || err "uv bootstrap failed"
	UV_BIN="$UV_INSTALL_DIR_PM/uv"
	[ -x "$UV_BIN" ] || err "uv bootstrap did not install $UV_BIN"
}

# uv reinstalls remove the entrypoints recorded by the old receipt, even without
# --force. Inspect that public listing before allowing uv to touch those paths.
check_entrypoints() {
	local tool_dir tool_env listing line in_pairmux=0 count=0 entry
	local header='^[a-zA-Z0-9_.-]+ v[^[:space:]]+ \(/.*\)$'
	tool_dir=$(clean_uv "$UV_BIN" tool dir --no-config) || err "could not determine uv's tool directory"
	[[ "$tool_dir" = /* && ! "$tool_dir" =~ [[:cntrl:]] ]] || err "uv tool directory is not a supported absolute path"
	tool_env="$tool_dir/pairmux"
	if [ -e "$tool_env" ] || [ -L "$tool_env" ]; then
		if [ ! -f "$tool_env/uv-receipt.toml" ] || [ -L "$tool_env/uv-receipt.toml" ]; then
			err "existing pairmux tool has no regular uv receipt; repair it manually first"
		fi
		# uv writes escaped paths to TOML but prints them unescaped in the listing.
		# Refuse unsupported escape/multiline representations rather than checking
		# a truncated path while uv subsequently removes the complete recorded one.
		if grep -Fq "\\" "$tool_env/uv-receipt.toml"; then
			err "existing pairmux receipt contains escaped paths; repair it manually first"
		fi
		listing=$(clean_uv "$UV_BIN" tool list --no-config --show-paths --color never) || \
			err "could not inspect the existing uv tool receipt"
		while IFS= read -r line; do
			[[ ! "$line" =~ [[:cntrl:]] ]] || err "existing uv tool listing contains an unsupported control character"
			case "$line" in
			pairmux\ v*)
				[[ "$line" =~ $header && "$line" = *" ($tool_env)" ]] || err "existing pairmux tool listing is malformed"
				in_pairmux=1; continue
				;;
			'- '*)
				[ "$in_pairmux" -eq 1 ] || continue
				case "$line" in
				'- pairmux ('*')') entry=${line#'- pairmux ('}; entry=${entry%')'} ;;
				*) err "unexpected entrypoint in existing pairmux receipt; repair it manually first" ;;
				esac
				[[ "$entry" = /* && ! "$entry" =~ [[:cntrl:]] ]] || err "existing receipt entrypoint is not a supported absolute path"
				count=$((count + 1))
				if [ -e "$entry" ] || [ -L "$entry" ]; then
					if [ ! -L "$entry" ] || [ "$(readlink "$entry")" != "$tool_env/bin/pairmux" ]; then
						err "refusing to replace $entry: it is not the recorded uv-managed symlink"
					fi
				fi
				;;
			*)
				[[ "$line" =~ $header ]] || err "uv tool listing is malformed; repair the existing receipt manually first"
				in_pairmux=0
				;;
			esac
		done <<<"$listing"
		[ "$count" -eq 1 ] || err "existing pairmux receipt is invalid or has unexpected entrypoints; repair it manually first"
	fi
	entry="$INSTALL_DIR/pairmux"
	if [ -e "$entry" ] || [ -L "$entry" ]; then
		if [ ! -L "$entry" ] || [ "$(readlink "$entry")" != "$tool_env/bin/pairmux" ]; then
			err "refusing to overwrite $entry: choose another PAIRMUX_INSTALL_DIR or remove the old installation manually"
		fi
	fi
}

print_command() {
	printf '  uv tool install --no-config --default-index %s --no-sources --no-build\n' "$PYPI_INDEX"
	printf '    --python ">=3.9" --upgrade --reinstall --no-cache %q\n' "$REQUIREMENT"
}

print_dry_run() {
	printf 'pairmux install.sh — dry run (no network, nothing written)\n\n'
	printf '  package:      %s\n' "$PYPI_PROJECT"
	printf '  index:        %s (exclusive)\n' "$PYPI_INDEX"
	printf '  requirement:  %s\n' "$REQUIREMENT"
	printf '  tool bin:     %s\n' "$INSTALL_DIR"
	printf '  uv bootstrap: %s (only if uv is missing)\n\n' "$UV_INSTALLER"
	print_command
}

tmux_hint() {
	if [ "$OS" = darwin ]; then
		printf 'brew install tmux'
	else
		printf 'sudo apt install tmux   (or your distro package manager)'
	fi
}

check_tmux() {
	if ! have tmux; then
		warn "tmux is not installed. pairmux requires tmux >=3.2."
		printf '         install it with: %s\n' "$(tmux_hint)" >&2
		return
	fi
	local tv tmaj tmin
	tv=$(tmux -V 2>/dev/null | grep -oE '[0-9]+\.[0-9]+' | head -n1) || true
	if [ -z "$tv" ]; then
		warn "could not parse the tmux version; ensure it is >=3.2"
		return
	fi
	tmaj=${tv%%.*}
	tmin=${tv#*.}
	if [ "$tmaj" -gt 3 ] || { [ "$tmaj" -eq 3 ] && [ "$tmin" -ge 2 ]; }; then
		info "tmux $tv detected (>=3.2)"
	else
		warn "tmux $tv is older than the required 3.2."
		printf '         upgrade it with: %s\n' "$(tmux_hint)" >&2
	fi
}

path_hint() {
	case ":$PATH:" in
	*":$INSTALL_DIR:"*) : ;;
	*)
		warn "$INSTALL_DIR is not on your PATH; a piped installer cannot change the parent shell."
		printf '         add this directory to PATH in your shell profile:\n' >&2
		printf '           %s\n' "$INSTALL_DIR" >&2
		;;
	esac
	local visible
	visible=$(type -P pairmux || true)
	if [ -n "$visible" ] && [ "$visible" != "$INSTALL_DIR/pairmux" ]; then
		warn "PATH currently selects $visible instead of $INSTALL_DIR/pairmux."
		warn "remove the old installation or put the uv tool-bin directory first; see $DOCS/migrating-from-apt"
	fi
}

cleanup() {
	if [ -n "${TMPDIR_PM:-}" ] && [ -d "$TMPDIR_PM" ]; then
		rm -rf "$TMPDIR_PM"
	fi
}

main() {
	PINNED_TAG=""
	DRY_RUN=0
	TMPDIR_PM=""
	while [ "$#" -gt 0 ]; do
		case "$1" in
		--version)
			shift
			if [ "$#" -eq 0 ] || [ -z "$1" ]; then
				err "--version requires an argument (e.g. v0.5.3)"
			fi
			PINNED_TAG=$1
			;;
		--version=*) PINNED_TAG=${1#*=}; [ -n "$PINNED_TAG" ] || err "--version requires an argument" ;;
		--dry-run) DRY_RUN=1 ;;
		-h | --help) usage; return ;;
		*) err "unknown argument: $1 (try --help)" ;;
		esac
		shift
	done
	resolve_version
	detect_platform
	resolve_install_dir
	if [ "$DRY_RUN" -eq 1 ]; then
		print_dry_run
		return
	fi
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	info "pairmux package: $PYPI_PROJECT"
	info "package index: $PYPI_INDEX (exclusive; inherited UV_* and config ignored)"
	info "uv may download Python >=3.9 from Astral's upstream if none is available."
	ensure_uv
	check_entrypoints
	info "installing the PyPI platform wheel with uv (no source builds, no sudo)"
	print_command
	clean_uv "$UV_BIN" tool install --no-config --default-index "$PYPI_INDEX" \
		--no-sources --no-build --python '>=3.9' --upgrade --reinstall --no-cache \
		"$REQUIREMENT" || err "PyPI installation failed; check uv's error above (upgrade uv if its flags are unsupported)"
	local installed_ver
	installed_ver=$("$INSTALL_DIR/pairmux" version) || err "the uv-installed executable failed to run"
	if [ -n "$VERSION_NUM" ] && [ "$installed_ver" != "$VERSION_NUM" ]; then
		err "installed binary reports $installed_ver; expected $VERSION_NUM"
	fi
	[ -n "$installed_ver" ] || err "the installed binary did not report a version"
	info "pairmux $installed_ver is ready at $INSTALL_DIR/pairmux"
	check_tmux
	path_hint
	printf '\nQuickstart:\n'
	printf '  pairmux new --name build\n'
	printf '  pairmux run build "make -j4"\n'
	printf '  pairmux ls\n'
	printf '\nDocs: %s\n' "$DOCS"
}

main "$@"
