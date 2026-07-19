#!/usr/bin/env python3
"""Build pairmux platform wheels — no build backend, stdlib only.

pairmux is a Go binary. We ship it to PyPI the same way ruff/uv do: one wheel
per platform, each carrying the prebuilt native binary in the wheel's *scripts*
section. Installers copy files from ``<name>-<ver>.data/scripts/`` straight into
the target environment's ``bin/`` directory, so ``uv tool install pairmux`` /
``pipx install pairmux`` land the executable on PATH with no compilation. We do
NOT publish an sdist (an sdist would invite a source build on the user's box).

This script needs no third-party packages and no build backend (setuptools,
maturin, hatch, ...). It writes the .whl zip by hand: METADATA, WHEEL and a
RECORD with correct sha256 hashes, plus the binary with its 0755 exec bit
encoded in the zip entry's external_attr.

Shebang note: pip/uv rewrite the shebang of a script in .data/scripts/ ONLY when
its first bytes are ``#!python``. A native ELF/Mach-O binary starts with its own
magic, so it is copied verbatim (exec bit preserved) — exactly what we want.

Usage
-----
Explicit binary/platform pairs (repeatable)::

    build_wheels.py --version 0.1.0 \
        --binary dist/pairmux_darwin_arm64/pairmux --platform darwin_arm64 \
        --binary dist/pairmux_linux_amd64_v1/pairmux --platform linux_amd64 \
        --check

Scan a goreleaser dist directory (this is how CI calls it)::

    build_wheels.py --version 0.1.0 --dist-dir dist --out-dir dist/wheels --check

Scanning follows goreleaser's default binary layout: each build lives in a
subdirectory named ``<build>_<goos>_<goarch>[_<variant>]`` (e.g.
``pairmux_darwin_arm64``, ``pairmux_linux_amd64_v1``) containing the ``pairmux``
executable. Only the four supported goos/goarch combinations are picked up;
anything else (windows, universal, checksums, archives) is ignored.

Wheels are written to ``--out-dir`` (default ``dist/``) as
``pairmux-<version>-py3-none-<platform-tag>.whl``. Publish them with
``uv publish dist/*.whl``.

``--platform`` takes a goreleaser ``goos_goarch`` token (``darwin_arm64``), not a
PyPI platform tag; the mapping to the wheel platform tag lives in PLATFORM_TAGS.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import os
import re
import struct
import sys
import time
import zipfile
from pathlib import Path
from typing import List, Tuple

# --- project constants -------------------------------------------------------

PROJECT_NAME = "pairmux"          # PyPI distribution name (already lowercase/normalized)
SCRIPT_NAME = "pairmux"           # command installed into bin/
SUMMARY = "Reliable terminal primitives for AI agents on tmux"
HOMEPAGE = "https://github.com/treeleaves30760/pairmux"
REQUIRES_PYTHON = ">=3.9"
GENERATOR = "pairmux-build"
METADATA_VERSION = "2.1"

# goreleaser (goos, goarch) -> PyPI platform tag written into the wheel filename.
# manylinux tags are compressed (two sub-tags joined by "."); that expands into
# multiple WHEEL "Tag:" lines, see expand_tags().
PLATFORM_TAGS = {
    # Go 1.25 emits LC_BUILD_VERSION minos 12.0 for both Darwin targets. The
    # builder parses every Mach-O and rejects a mismatch so a future Go
    # toolchain cannot silently produce an over-permissive wheel tag.
    ("darwin", "arm64"): "macosx_12_0_arm64",
    ("darwin", "amd64"): "macosx_12_0_x86_64",
    ("linux", "amd64"): "manylinux_2_17_x86_64.manylinux2014_x86_64",
    ("linux", "arm64"): "manylinux_2_17_aarch64.manylinux2014_aarch64",
}

# Trove classifiers — all valid values from the official list, so PyPI accepts
# the upload the sibling release step performs.
CLASSIFIERS = [
    "Development Status :: 3 - Alpha",
    "Environment :: Console",
    "Intended Audience :: Developers",
    "License :: OSI Approved :: MIT License",
    "Operating System :: MacOS",
    "Operating System :: POSIX :: Linux",
    "Programming Language :: Go",
    "Topic :: Software Development",
    "Topic :: Terminals",
    "Topic :: Utilities",
]

LONG_DESCRIPTION = """\
pairmux — reliable terminal primitives for AI agents on tmux

pairmux lets an AI agent drive a real terminal through a reliable, blocking
interface while a human can watch, attach, and help at any time. It is an
Agent-Computer Interface (ACI) layer built on top of tmux, not a replacement
for it: tmux stays the terminal-state engine, so a human can `tmux attach` and
take over almost for free.

Highlights:
  * Blocking `run` / `wait` calls return when a command actually finishes,
    times out, or starts asking for input — the agent never guesses `sleep N`.
  * A per-terminal journal is the single source of truth: full scrollback is
    always available via `log`, even after output scrolls off screen.
  * Completion detection degrades gracefully: OSC 133 shell integration first,
    then a printed sentinel, then output quiescence.
  * Output is shaped for models (ANSI stripped, carriage-return redraws folded)
    and truncated responses always carry the exact command to fetch the rest.
  * Humans and agents share one live terminal; `note`, `attach`, and a
    per-terminal writer lock keep hand-offs sane.

Installation model:
  This is a platform wheel bundling a prebuilt native `pairmux` executable.
  Installing it drops the binary straight onto your PATH (the environment's
  bin/ directory) — there is no Python code and no build step, so
  `uv tool install pairmux` and `pipx install pairmux` just work.

Runtime requirement: tmux >= 3.2 (`pairmux doctor` checks your environment).

Homepage / source: https://github.com/treeleaves30760/pairmux
License: MIT
"""

# Release tags use one canonical SemVer shape. Keeping this stricter than the
# full PEP 440 grammar prevents GitHub, GoReleaser, and wheel metadata from
# assigning subtly different versions to the same artifact set.
SEMVER_RE = re.compile(
    r"^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-(alpha|beta|rc)\.(0|[1-9][0-9]*))?$"
)

MACHO_MAGIC_64 = 0xFEEDFACF
LC_VERSION_MIN_MACOSX = 0x24
LC_BUILD_VERSION = 0x32
PLATFORM_MACOS = 1
MACHO_CPU_TO_WHEEL_ARCH = {
    0x01000007: "x86_64",
    0x0100000C: "arm64",
}
ELF_MAGIC = b"\x7fELF"
ELFCLASS64 = 2
ELFDATA2LSB = 1
EV_CURRENT = 1
ET_EXEC = 2
ET_DYN = 3
ELF64_HEADER_FORMAT = "<16sHHIQQQIHHHHHH"
ELF64_HEADER_SIZE = struct.calcsize(ELF64_HEADER_FORMAT)
ELF_MACHINE_TO_WHEEL_ARCH = {
    62: "x86_64",    # EM_X86_64
    183: "aarch64",  # EM_AARCH64
}
LINUX_PLATFORM_ARCHES = {
    PLATFORM_TAGS[("linux", "amd64")]: "x86_64",
    PLATFORM_TAGS[("linux", "arm64")]: "aarch64",
}

# goreleaser build-subdir names end with _<goos>_<goarch>[_<variant>]. Go's
# arm64 default is rendered as ``v8.0``, so variants may contain dots.
GORELEASER_DIR_RE = re.compile(
    r"_(darwin|linux)_(amd64|arm64)(?:_[0-9A-Za-z][0-9A-Za-z.]*)?$"
)


# --- helpers -----------------------------------------------------------------

def normalize_version(v: str) -> str:
    """Map a canonical SemVer release tag to its PEP 440 wheel version.

    Stable ``v1.2.3``/``1.2.3`` versions pass through without the optional
    prefix. Canonical prereleases translate ``1.2.3-rc.1`` to ``1.2.3rc1``.
    """
    match = SEMVER_RE.fullmatch(v)
    if match is None:
        raise ValueError(
            "version %r is not canonical SemVer (expected v1.2.3 or "
            "v1.2.3-rc.1; a missing lowercase v prefix is also accepted)" % v
        )
    release = ".".join(match.group(index) for index in (1, 2, 3))
    if match.group(4) is None:
        return release
    pre = {"alpha": "a", "beta": "b", "rc": "rc"}[match.group(4)]
    return release + pre + match.group(5)


def macos_platform_tag(binary_path: Path) -> str:
    """Read a thin 64-bit Mach-O's architecture and minimum macOS version."""
    with binary_path.open("rb") as f:
        header = f.read(32)
        if len(header) != 32:
            raise ValueError("%s is too short to be a 64-bit Mach-O" % binary_path)
        magic, cpu, _, _, ncmds, sizeofcmds, _, _ = struct.unpack(
            "<IiiIIIII", header
        )
        if magic != MACHO_MAGIC_64:
            raise ValueError("%s is not a little-endian 64-bit Mach-O" % binary_path)
        if sizeofcmds > 16 * 1024 * 1024:
            raise ValueError("%s has an unreasonable Mach-O command table" % binary_path)
        commands = f.read(sizeofcmds)
    if len(commands) != sizeofcmds:
        raise ValueError("%s has a truncated Mach-O command table" % binary_path)

    arch = MACHO_CPU_TO_WHEEL_ARCH.get(cpu)
    if arch is None:
        raise ValueError("%s has unsupported Mach-O cpu type %#x" % (binary_path, cpu))

    offset = 0
    minos = None
    for _ in range(ncmds):
        if offset + 8 > len(commands):
            raise ValueError("%s has a truncated Mach-O load command" % binary_path)
        cmd, cmdsize = struct.unpack_from("<II", commands, offset)
        if cmdsize < 8 or offset + cmdsize > len(commands):
            raise ValueError("%s has an invalid Mach-O load command size" % binary_path)
        if cmd == LC_BUILD_VERSION and cmdsize >= 24:
            platform, minos = struct.unpack_from("<II", commands, offset + 8)
            if platform != PLATFORM_MACOS:
                raise ValueError("%s is a Mach-O for platform %d, not macOS" % (binary_path, platform))
            break
        if cmd == LC_VERSION_MIN_MACOSX and cmdsize >= 16:
            (minos,) = struct.unpack_from("<I", commands, offset + 8)
            break
        offset += cmdsize
    if minos is None:
        raise ValueError("%s has no macOS deployment-target load command" % binary_path)

    major = minos >> 16
    minor = (minos >> 8) & 0xFF
    return "macosx_%d_%d_%s" % (major, minor, arch)


def linux_elf_arch(binary_path: Path) -> str:
    """Read a little-endian ELF64 header and return its wheel architecture."""
    with binary_path.open("rb") as f:
        header = f.read(ELF64_HEADER_SIZE)
    if len(header) < ELF64_HEADER_SIZE:
        raise ValueError("%s is too short to be an ELF64 binary" % binary_path)
    (ident, file_type, machine, version, _, _, _, _, header_size,
     _, _, _, _, _) = struct.unpack(ELF64_HEADER_FORMAT, header)
    if ident[:4] != ELF_MAGIC:
        raise ValueError("%s is not an ELF binary" % binary_path)
    if ident[4] != ELFCLASS64:
        raise ValueError("%s is not a 64-bit ELF binary" % binary_path)
    if ident[5] != ELFDATA2LSB:
        raise ValueError("%s is not a little-endian ELF binary" % binary_path)
    if ident[6] != EV_CURRENT:
        raise ValueError("%s has unsupported ELF identification version %d" %
                         (binary_path, ident[6]))
    if version != EV_CURRENT:
        raise ValueError("%s has unsupported ELF header version %d" %
                         (binary_path, version))
    if file_type not in (ET_EXEC, ET_DYN):
        raise ValueError("%s is not an ELF executable (type %d)" %
                         (binary_path, file_type))
    if header_size != ELF64_HEADER_SIZE:
        raise ValueError("%s has invalid ELF64 header size %d" %
                         (binary_path, header_size))

    arch = ELF_MACHINE_TO_WHEEL_ARCH.get(machine)
    if arch is None:
        raise ValueError("%s has unsupported ELF machine type %d" %
                         (binary_path, machine))
    return arch


def parse_platform_token(token: str) -> Tuple[str, str]:
    """Turn a goreleaser 'goos_goarch' token into (goos, goarch)."""
    parts = token.split("_")
    if len(parts) != 2 or (parts[0], parts[1]) not in PLATFORM_TAGS:
        supported = ", ".join("%s_%s" % k for k in PLATFORM_TAGS)
        raise ValueError(
            "--platform expects a supported goos_goarch token (%s), got %r"
            % (supported, token)
        )
    return parts[0], parts[1]


def expand_tags(plat: str) -> List[str]:
    """Expand a (possibly compressed) platform tag into full WHEEL Tag lines.

    'macosx_12_0_arm64' -> ['py3-none-macosx_12_0_arm64']
    'manylinux_2_17_x86_64.manylinux2014_x86_64' ->
        ['py3-none-manylinux_2_17_x86_64', 'py3-none-manylinux2014_x86_64']
    """
    return ["py3-none-" + sub for sub in plat.split(".")]


def _zip_time() -> Tuple[int, int, int, int, int, int]:
    """Reproducible zip timestamp; honors SOURCE_DATE_EPOCH, floors at 1980."""
    sde = os.environ.get("SOURCE_DATE_EPOCH")
    if sde:
        t = time.gmtime(int(sde))
        return (max(t.tm_year, 1980), t.tm_mon, t.tm_mday,
                t.tm_hour, t.tm_min, t.tm_sec)
    return (1980, 1, 1, 0, 0, 0)


def _hash(data: bytes) -> str:
    """RECORD hash form: 'sha256=' + urlsafe base64 of the digest, no padding."""
    digest = hashlib.sha256(data).digest()
    return "sha256=" + base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


def _zipinfo(arcname: str, mode: int) -> zipfile.ZipInfo:
    """A deflated zip entry whose high external_attr bits carry the unix mode."""
    zi = zipfile.ZipInfo(arcname, date_time=_zip_time())
    zi.create_system = 3            # Unix — so the mode bits are honored
    zi.external_attr = (mode & 0xFFFF) << 16
    zi.compress_type = zipfile.ZIP_DEFLATED
    return zi


def metadata_bytes(version: str) -> bytes:
    lines = [
        "Metadata-Version: " + METADATA_VERSION,
        "Name: " + PROJECT_NAME,
        "Version: " + version,
        "Summary: " + SUMMARY,
        "License: MIT",
    ]
    lines += ["Classifier: " + c for c in CLASSIFIERS]
    lines += [
        "Project-URL: Homepage, " + HOMEPAGE,
        "Project-URL: Repository, " + HOMEPAGE,
        "Requires-Python: " + REQUIRES_PYTHON,
        "Description-Content-Type: text/plain",
    ]
    header = "\n".join(lines)
    body = LONG_DESCRIPTION.strip("\n")
    return (header + "\n\n" + body + "\n").encode("utf-8")


def wheel_bytes(plat: str) -> bytes:
    lines = [
        "Wheel-Version: 1.0",
        "Generator: " + GENERATOR,
        "Root-Is-Purelib: false",
    ]
    lines += ["Tag: " + tag for tag in expand_tags(plat)]
    return ("\n".join(lines) + "\n").encode("utf-8")


def render_record(rows: List[Tuple[str, str, str]]) -> bytes:
    """RECORD as CSV. Our arcnames never contain ',' or '\"', so plain joins are
    exact and match what pip/wheel emit. RECORD's own row has empty hash+size."""
    return ("\n".join("%s,%s,%s" % r for r in rows) + "\n").encode("utf-8")


def build_one(version: str, binary_path: Path, plat: str, out_dir: Path) -> Path:
    if plat.startswith("macosx_"):
        actual = macos_platform_tag(binary_path)
        if actual != plat:
            raise ValueError(
                "%s declares %s but its Mach-O requires %s"
                % (binary_path, plat, actual)
            )
    elif plat in LINUX_PLATFORM_ARCHES:
        expected = LINUX_PLATFORM_ARCHES[plat]
        actual = linux_elf_arch(binary_path)
        if actual != expected:
            raise ValueError(
                "%s declares %s but its ELF architecture is %s"
                % (binary_path, plat, actual)
            )
    binary = binary_path.read_bytes()
    prefix = "%s-%s" % (PROJECT_NAME, version)
    scripts_arc = "%s.data/scripts/%s" % (prefix, SCRIPT_NAME)
    meta_arc = "%s.dist-info/METADATA" % prefix
    wheel_arc = "%s.dist-info/WHEEL" % prefix
    record_arc = "%s.dist-info/RECORD" % prefix

    meta = metadata_bytes(version)
    wheel = wheel_bytes(plat)

    rows = [
        (scripts_arc, _hash(binary), str(len(binary))),
        (meta_arc, _hash(meta), str(len(meta))),
        (wheel_arc, _hash(wheel), str(len(wheel))),
        (record_arc, "", ""),
    ]
    record = render_record(rows)

    out_dir.mkdir(parents=True, exist_ok=True)
    whl = out_dir / ("%s-py3-none-%s.whl" % (prefix, plat))
    with zipfile.ZipFile(whl, "w", zipfile.ZIP_DEFLATED) as zf:
        zf.writestr(_zipinfo(scripts_arc, 0o100755), binary)   # rwxr-xr-x
        zf.writestr(_zipinfo(meta_arc, 0o100644), meta)
        zf.writestr(_zipinfo(wheel_arc, 0o100644), wheel)
        zf.writestr(_zipinfo(record_arc, 0o100644), record)
    return whl


def check_wheel(path: Path) -> bool:
    """Re-open the wheel and verify RECORD: every file present, hashes and sizes
    match, RECORD's own row is empty, and the script keeps its exec bit."""
    ok = True

    def fail(msg: str) -> None:
        nonlocal ok
        ok = False
        print("  %s" % msg, file=sys.stderr)

    with zipfile.ZipFile(path) as zf:
        names = zf.namelist()
        record_name = next(
            (n for n in names if n.endswith(".dist-info/RECORD")), None)
        if record_name is None:
            fail("no RECORD entry")
            return False

        listed = {}
        for line in zf.read(record_name).decode("utf-8").splitlines():
            if not line.strip():
                continue
            name, h, size = line.rsplit(",", 2)   # arcnames have no commas
            listed[name] = (h, size)

        for n in names:
            data = zf.read(n)
            if n == record_name:
                h, size = listed.get(n, (None, None))
                if h != "" or size != "":
                    fail("RECORD's own row must have empty hash and size")
                continue
            if n not in listed:
                fail("%s is in the zip but missing from RECORD" % n)
                continue
            h, size = listed[n]
            if h != _hash(data):
                fail("%s hash mismatch (RECORD %s)" % (n, h))
            if size != str(len(data)):
                fail("%s size mismatch (RECORD %s, actual %d)" % (n, size, len(data)))

        for n in listed:
            if n not in names:
                fail("RECORD lists %s but it is not in the zip" % n)

        for n in names:
            if ".data/scripts/" in n:
                mode = (zf.getinfo(n).external_attr >> 16) & 0xFFFF
                if not mode & 0o111:
                    fail("%s is not executable (mode %o)" % (n, mode))
    return ok


def scan_dist_dir(dist_dir: Path) -> List[Tuple[Path, Tuple[str, str]]]:
    jobs: List[Tuple[Path, Tuple[str, str]]] = []
    seen: dict[Tuple[str, str], Path] = {}
    if not dist_dir.is_dir():
        raise ValueError("--dist-dir %s is not a directory" % dist_dir)
    for child in sorted(dist_dir.iterdir()):
        if not child.is_dir():
            continue
        m = GORELEASER_DIR_RE.search(child.name)
        if not m:
            continue
        key = (m.group(1), m.group(2))
        if key not in PLATFORM_TAGS:
            continue
        binary = child / SCRIPT_NAME
        if binary.is_file():
            if previous := seen.get(key):
                raise ValueError(
                    "duplicate %s/%s binaries in %s and %s"
                    % (key[0], key[1], previous, binary)
                )
            seen[key] = binary
            jobs.append((binary, key))
    return jobs


def main(argv=None) -> int:
    p = argparse.ArgumentParser(
        prog="build_wheels.py",
        description="Build pairmux platform wheels (binary-in-scripts, no sdist).",
    )
    p.add_argument("--version", required=True,
                   help="canonical SemVer release, e.g. v0.1.0 or v0.2.0-rc.1")
    p.add_argument("--binary", action="append", default=[], metavar="PATH",
                   help="path to a pairmux binary; pair with --platform (repeatable)")
    p.add_argument("--platform", action="append", default=[], metavar="GOOS_GOARCH",
                   help="goreleaser token for the matching --binary, e.g. darwin_arm64")
    p.add_argument("--dist-dir", metavar="DIR",
                   help="scan a goreleaser dist dir for <build>_<goos>_<goarch>*/pairmux")
    p.add_argument("--out-dir", default="dist", metavar="DIR",
                   help="where to write the .whl files (default: dist)")
    p.add_argument("--check", action="store_true",
                   help="after building, re-open each wheel and verify RECORD")
    p.add_argument("--validate-version-only", action="store_true",
                   help="normalize --version, print it, and exit without building")
    args = p.parse_args(argv)

    try:
        version = normalize_version(args.version)
    except ValueError as e:
        p.error(str(e))

    if args.validate_version_only:
        print(version)
        return 0

    if len(args.binary) != len(args.platform):
        p.error("--binary and --platform must be given in equal numbers (as pairs)")

    jobs: List[Tuple[Path, str, str]] = []
    try:
        for b, token in zip(args.binary, args.platform):
            goos, goarch = parse_platform_token(token)
            jobs.append((Path(b), PLATFORM_TAGS[(goos, goarch)], "%s_%s" % (goos, goarch)))
        if args.dist_dir:
            for binary, key in scan_dist_dir(Path(args.dist_dir)):
                jobs.append((binary, PLATFORM_TAGS[key], "%s_%s" % key))
    except ValueError as e:
        p.error(str(e))

    if not jobs:
        p.error("nothing to build: pass --binary/--platform pairs or a --dist-dir "
                "that contains goreleaser build subdirectories")

    seen_labels: set[str] = set()
    for _binary, _platform, label in jobs:
        if label in seen_labels:
            p.error("duplicate binary target: %s" % label)
        seen_labels.add(label)

    out_dir = Path(args.out_dir)
    built: List[Path] = []
    for binary, plat, label in jobs:
        if not binary.is_file():
            print("error: binary not found: %s" % binary, file=sys.stderr)
            return 1
        try:
            whl = build_one(version, binary, plat, out_dir)
        except (OSError, ValueError) as e:
            print("error: %s" % e, file=sys.stderr)
            return 1
        print("built  %s   (%s -> %s)" % (whl, label, plat))
        built.append(whl)

    ok = True
    if args.check:
        for whl in built:
            if check_wheel(whl):
                print("check  OK    %s" % whl)
            else:
                print("check  FAIL  %s" % whl, file=sys.stderr)
                ok = False

    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
