#!/usr/bin/env python3
"""Verify and stage the exact GoReleaser artifacts approved for publishing."""

from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import sys
import tarfile
from pathlib import Path
from typing import Dict, Iterable, List, Tuple

from build_wheels import normalize_version


TARGETS = (
    ("darwin", "amd64"),
    ("darwin", "arm64"),
    ("linux", "amd64"),
    ("linux", "arm64"),
)
PACKAGE_FORMATS = ("deb", "rpm")
ALLOWED_TYPES = {"Metadata", "Binary", "Archive", "Linux Package", "Checksum"}


def fail(message: str) -> None:
    raise ValueError(message)


def read_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail("cannot read %s: %s" % (path, exc))


def artifact_path(dist_dir: Path, entry: dict) -> Path:
    raw = entry.get("path")
    if not isinstance(raw, str) or not raw:
        fail("artifact has no path: %r" % entry)
    path = Path(raw)
    if not path.is_absolute():
        if path.parts and path.parts[0] == dist_dir.name:
            path = dist_dir.parent / path
        else:
            path = dist_dir / path
    try:
        path.resolve().relative_to(dist_dir.resolve())
    except ValueError:
        fail("artifact path escapes dist directory: %s" % raw)
    if not path.is_file():
        fail("artifact file is missing: %s" % path)
    return path


def target(entry: dict) -> Tuple[str, str]:
    return entry.get("goos", ""), entry.get("goarch", "")


def one_by_target(entries: Iterable[dict], artifact_type: str) -> Dict[Tuple[str, str], dict]:
    result: Dict[Tuple[str, str], dict] = {}
    for entry in entries:
        key = target(entry)
        if key in result:
            fail("duplicate %s artifact for %s/%s" % (artifact_type, *key))
        result[key] = entry
    if set(result) != set(TARGETS):
        fail("%s targets are %r; expected %r" % (artifact_type, sorted(result), list(TARGETS)))
    return result


def checksum_rows(path: Path) -> Dict[str, str]:
    rows: Dict[str, str] = {}
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        fields = line.split()
        if len(fields) != 2 or len(fields[0]) != 64:
            fail("invalid checksum row %d in %s" % (number, path))
        digest, name = fields
        if any(character not in "0123456789abcdef" for character in digest):
            fail("invalid sha256 digest for %s" % name)
        if Path(name).name != name or name in rows:
            fail("invalid or duplicate checksum filename: %s" % name)
        rows[name] = digest
    return rows


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def archive_binary(path: Path) -> Tuple[bytes, int]:
    required = {"pairmux", "LICENSE", "README.md", "man/pairmux.1"}
    with tarfile.open(path, "r:gz") as archive:
        members = {member.name.removeprefix("./"): member for member in archive.getmembers()}
        missing = required - set(members)
        if missing:
            fail("%s is missing archive members: %s" % (path, ", ".join(sorted(missing))))
        for name in required:
            if not members[name].isfile():
                fail("%s member %s is not a regular file" % (path, name))
        binary = archive.extractfile(members["pairmux"])
        if binary is None:
            fail("cannot read pairmux from %s" % path)
        return binary.read(), members["pairmux"].mode


def verify_release(dist_dir: Path, tag: str, commit: str, out_dir: Path) -> List[Path]:
    wheel_version = normalize_version(tag)
    release_version = tag.removeprefix("v")
    if not tag.startswith("v"):
        fail("release tag must use the lowercase v prefix")

    metadata = read_json(dist_dir / "metadata.json")
    for field, expected in (("tag", tag), ("version", release_version), ("commit", commit)):
        if metadata.get(field) != expected:
            fail("metadata %s is %r; expected %r" % (field, metadata.get(field), expected))

    entries = read_json(dist_dir / "artifacts.json")
    if not isinstance(entries, list):
        fail("artifacts.json must contain an array")
    unexpected_types = {entry.get("type") for entry in entries} - ALLOWED_TYPES
    if unexpected_types:
        fail("unexpected artifact types: %s" % sorted(unexpected_types))

    grouped = {kind: [entry for entry in entries if entry.get("type") == kind]
               for kind in ALLOWED_TYPES}
    if len(grouped["Metadata"]) != 1 or len(grouped["Checksum"]) != 1:
        fail("expected exactly one Metadata and one Checksum artifact")
    binaries = one_by_target(grouped["Binary"], "Binary")
    archives = one_by_target(grouped["Archive"], "Archive")

    packages: Dict[Tuple[str, str], dict] = {}
    for entry in grouped["Linux Package"]:
        fmt = entry.get("extra", {}).get("Format")
        key = (entry.get("goarch", ""), fmt)
        if entry.get("goos") != "linux" or key in packages:
            fail("invalid or duplicate Linux package target: %r" % entry)
        packages[key] = entry
    expected_packages = {(arch, fmt) for arch in ("amd64", "arm64") for fmt in PACKAGE_FORMATS}
    if set(packages) != expected_packages:
        fail("Linux package targets are %r; expected %r" %
             (sorted(packages), sorted(expected_packages)))

    public: List[Path] = []
    for os_name, arch in TARGETS:
        binary_path = artifact_path(dist_dir, binaries[(os_name, arch)])
        archive_entry = archives[(os_name, arch)]
        expected_name = "pairmux_%s_%s_%s.tar.gz" % (release_version, os_name, arch)
        if archive_entry.get("name") != expected_name:
            fail("archive name is %r; expected %r" % (archive_entry.get("name"), expected_name))
        archive_path = artifact_path(dist_dir, archive_entry)
        archived, mode = archive_binary(archive_path)
        if archived != binary_path.read_bytes():
            fail("%s does not contain the verified %s/%s binary" % (archive_path, os_name, arch))
        if mode & 0o111 == 0:
            fail("pairmux is not executable in %s" % archive_path)
        public.append(archive_path)

    for arch, fmt in sorted(expected_packages):
        entry = packages[(arch, fmt)]
        expected_name = "pairmux_%s_linux_%s.%s" % (release_version, arch, fmt)
        if entry.get("name") != expected_name:
            fail("package name is %r; expected %r" % (entry.get("name"), expected_name))
        public.append(artifact_path(dist_dir, entry))

    checksum_entry = grouped["Checksum"][0]
    if checksum_entry.get("name") != "checksums.txt":
        fail("checksum artifact must be named checksums.txt")
    checksum_path = artifact_path(dist_dir, checksum_entry)
    checksums = checksum_rows(checksum_path)
    expected_names = {path.name for path in public}
    if set(checksums) != expected_names:
        fail("checksum filenames are %r; expected %r" %
             (sorted(checksums), sorted(expected_names)))
    for path in public:
        if sha256(path) != checksums[path.name]:
            fail("checksum mismatch for %s" % path.name)
    public.append(checksum_path)

    if out_dir.exists() and any(out_dir.iterdir()):
        fail("output directory is not empty: %s" % out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    staged = []
    for source in public:
        destination = out_dir / source.name
        shutil.copy2(source, destination)
        staged.append(destination)
    print("verified GoReleaser %s (%s) and staged %d release assets" %
          (release_version, wheel_version, len(staged)))
    return staged


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", default="dist", type=Path)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--out-dir", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        verify_release(args.dist_dir, args.tag, args.commit, args.out_dir)
    except (OSError, ValueError, tarfile.TarError) as exc:
        print("error: %s" % exc, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
