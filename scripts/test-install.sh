#!/bin/sh

set -eu

ROOT=$(unset CDPATH; cd "$(dirname "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/pairmux-install-test.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT INT TERM

FAKE_BIN="$TEST_ROOT/bin"
FIXTURES="$TEST_ROOT/fixtures"
INSTALL_DIR="$TEST_ROOT/install"
mkdir -p "$FAKE_BIN" "$FIXTURES" "$INSTALL_DIR"

cat >"$FAKE_BIN/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' Linux ;;
  -m) printf '%s\n' x86_64 ;;
  *) exit 2 ;;
esac
EOF

cat >"$FAKE_BIN/curl" <<'EOF'
#!/bin/sh
url=$2
output=$4
cp "$PAIRmux_TEST_FIXTURES/${url##*/}" "$output"
EOF

cat >"$FAKE_BIN/tmux" <<'EOF'
#!/bin/sh
printf '%s\n' 'tmux 3.2'
EOF
chmod +x "$FAKE_BIN/uname" "$FAKE_BIN/curl" "$FAKE_BIN/tmux"

file_sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

build_fixture() {
	version=$1
	work="$TEST_ROOT/build-$version"
	mkdir -p "$work"
	cat >"$work/pairmux" <<EOF
#!/bin/sh
test "\${1:-}" = version
printf '%s\\n' '$version'
EOF
	chmod +x "$work/pairmux"
	tar -czf "$FIXTURES/pairmux_1.2.3_linux_amd64.tar.gz" -C "$work" pairmux
	digest=$(file_sha256 "$FIXTURES/pairmux_1.2.3_linux_amd64.tar.gz")
	printf '%s  %s\n' "$digest" pairmux_1.2.3_linux_amd64.tar.gz >"$FIXTURES/checksums.txt"
}

run_installer() {
	PATH="$FAKE_BIN:$PATH" \
	PAIRmux_TEST_FIXTURES="$FIXTURES" \
	PAIRMUX_INSTALL_DIR="$INSTALL_DIR" \
	sh "$ROOT/install.sh" --version v1.2.3 >/dev/null
}

victim="$TEST_ROOT/victim"
printf '%s\n' untouched >"$victim"
ln -s "$victim" "$INSTALL_DIR/pairmux"
build_fixture 1.2.3
run_installer

test ! -L "$INSTALL_DIR/pairmux"
test -x "$INSTALL_DIR/pairmux"
test "$("$INSTALL_DIR/pairmux" version)" = 1.2.3
test "$(cat "$victim")" = untouched

printf '%s\n' previous >"$INSTALL_DIR/pairmux"
chmod +x "$INSTALL_DIR/pairmux"
before=$(file_sha256 "$INSTALL_DIR/pairmux")
build_fixture 9.9.9
if run_installer 2>/dev/null; then
	printf '%s\n' 'expected installer to reject the mismatched binary version' >&2
	exit 1
fi
after=$(file_sha256 "$INSTALL_DIR/pairmux")
test "$before" = "$after"

printf '%s\n' 'install.sh atomic replacement tests passed'
