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

    build_wheels.py --version 0.1.0 --dist-dir dist/goreleaser --check

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
    ("darwin", "arm64"): "macosx_11_0_arm64",
    ("darwin", "amd64"): "macosx_10_12_x86_64",
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

# PEP 440 normalized public versions (no local "+segment" — PyPI rejects those
# on wheels anyway). Covers epoch, release, pre/post/dev.
VERSION_RE = re.compile(
    r"^([0-9]+!)?[0-9]+(\.[0-9]+)*((a|b|rc)[0-9]+)?(\.post[0-9]+)?(\.dev[0-9]+)?$"
)

# goreleaser build-subdir names end with _<goos>_<goarch>[_<variant>].
GORELEASER_DIR_RE = re.compile(r"_(darwin|linux)_(amd64|arm64)(?:_[0-9a-z]+)?$")


# --- helpers -----------------------------------------------------------------

def normalize_version(v: str) -> str:
    """Strip a leading v, lowercase, and validate as a normalized PEP 440 version."""
    v = v.strip()
    if v[:1] in ("v", "V"):
        v = v[1:]
    v = v.lower()
    if not VERSION_RE.match(v):
        raise ValueError(
            "version %r is not a normalized PEP 440 public version "
            "(examples: 0.1.0, 0.1.0.dev1, 1.2.3rc1); "
            "a git tag like v0.1.0 is fine, but hyphenated forms like "
            "0.1.0-dev are not" % v
        )
    return v


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

    'macosx_11_0_arm64' -> ['py3-none-macosx_11_0_arm64']
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
            jobs.append((binary, key))
    return jobs


def main(argv=None) -> int:
    p = argparse.ArgumentParser(
        prog="build_wheels.py",
        description="Build pairmux platform wheels (binary-in-scripts, no sdist).",
    )
    p.add_argument("--version", required=True,
                   help="release version, e.g. 0.1.0 or 0.1.0.dev1 (v-prefix ok)")
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
    args = p.parse_args(argv)

    try:
        version = normalize_version(args.version)
    except ValueError as e:
        p.error(str(e))

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

    out_dir = Path(args.out_dir)
    built: List[Path] = []
    for binary, plat, label in jobs:
        if not binary.is_file():
            print("error: binary not found: %s" % binary, file=sys.stderr)
            return 1
        whl = build_one(version, binary, plat, out_dir)
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
