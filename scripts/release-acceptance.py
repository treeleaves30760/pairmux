#!/usr/bin/env python3
"""Read public release metadata and verify selected downloads (run with python -I)."""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
import re
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile
from pathlib import Path

REPO = "treeleaves30760/pairmux"
STABLE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
PLATFORMS = {
    "darwin-amd64": "macosx_12_0_x86_64",
    "darwin-arm64": "macosx_12_0_arm64",
    "linux-amd64": "manylinux_2_17_x86_64.manylinux2014_x86_64",
    "linux-arm64": "manylinux_2_17_aarch64.manylinux2014_aarch64",
    "windows-amd64": "win_amd64",
    "windows-arm64": "win_arm64",
}
# Published releases come in exactly two shapes, recognised from the actual
# GitHub asset set (never from the tag number): v0.6.0 and earlier ship four
# macOS/Linux tarballs, two RPMs and checksums.txt with four wheels; releases
# with the built-in multiplexer add two Windows zips and two Windows wheels.
# Anything else, including a mix of the two, fails closed.
UNIX_TARGETS = ("darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64")
SHAPES = {
    "macos-linux": UNIX_TARGETS,
    "macos-linux-windows": UNIX_TARGETS + ("windows-amd64", "windows-arm64"),
}


def archive_name(version: str, target: str) -> str:
    os_name, arch = target.split("-")
    return f"pairmux_{version}_{os_name}_{arch}." + ("zip" if os_name == "windows" else "tar.gz")


def native_names(version: str, shape: str) -> set[str]:
    names = {archive_name(version, target) for target in SHAPES[shape]}
    return names | {f"pairmux_{version}_linux_{arch}.rpm" for arch in ("amd64", "arm64")}


def release_shape(version: str, asset_names: set[str]) -> str:
    matches = [shape for shape in SHAPES if asset_names == native_names(version, shape) | {"checksums.txt"}]
    if len(matches) != 1:
        raise ValueError("GitHub release assets match no published release shape: expected four tarballs, "
                         "two RPMs and checksums.txt, optionally plus exactly two Windows zips")
    return matches[0]


def fetch(url: str) -> bytes:
    headers = {"User-Agent": "pairmux-published-release-acceptance"}
    if url.startswith("https://api.github.com/"):
        headers["Accept"] = "application/vnd.github+json"
        token = os.environ.get("GH_TOKEN")
        if token:
            headers["Authorization"] = "Bearer " + token
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=120) as reply:
        return reply.read()


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def checksum_rows(data: bytes) -> dict[str, str]:
    rows = {}
    for line in data.decode("utf-8").splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})\s+\*?([A-Za-z0-9_.-]+)", line)
        if not match or match[2] in rows:
            raise ValueError("invalid or duplicate release checksum row")
        rows[match[2]] = match[1]
    return rows


def download(out: Path, url: str, name: str, expected: str | None = None) -> tuple[Path, bytes]:
    # Each fetched file has its own new directory; no interpreter runs there.
    path = Path(tempfile.mkdtemp(prefix="download-", dir=out)) / name
    data = fetch(url)
    path.write_bytes(data)
    if expected is not None and digest(data) != expected:
        raise ValueError("SHA256 mismatch for " + name)
    return path, data


def verify(tag: str, out: Path, target: str | None = None, rpm: bool = False) -> dict:
    if not STABLE.fullmatch(tag):
        raise ValueError("tag must be canonical stable vX.Y.Z (no prerelease or leading zeros)")
    version = tag[1:]
    release_url = f"https://api.github.com/repos/{REPO}/releases/tags/{tag}"
    latest_url = f"https://api.github.com/repos/{REPO}/releases/latest"
    pypi_url = f"https://pypi.org/pypi/pairmux/{version}/json"
    release = json.loads(fetch(release_url))
    if release.get("tag_name") != tag or release.get("draft") is not False or release.get("prerelease") is not False or not release.get("published_at"):
        raise ValueError("requested GitHub release is not a published, non-draft stable release")
    assets = release.get("assets", [])
    by_name = {asset["name"]: asset for asset in assets}
    if len(assets) != len(by_name):
        raise ValueError("GitHub release lists a duplicate asset name")
    shape = release_shape(version, set(by_name))
    names = native_names(version, shape)
    targets = SHAPES[shape]
    base = f"https://github.com/{REPO}/releases/download/{tag}/"
    for name, asset in by_name.items():
        if asset.get("state") != "uploaded" or asset.get("browser_download_url") != base + name or asset.get("size", 0) <= 0:
            raise ValueError("invalid public asset metadata for " + name)
    _, checksum_data = download(out, base + "checksums.txt", "checksums.txt")
    checksums = checksum_rows(checksum_data)
    if set(checksums) != names:
        raise ValueError(f"checksum inventory does not match the {len(names)} native assets")

    pypi = json.loads(fetch(pypi_url))
    if pypi.get("info", {}).get("name") != "pairmux" or pypi["info"].get("version") != version:
        raise ValueError("PyPI project/version does not match the requested release")
    wheels = pypi.get("urls", [])
    # PyPI must match the shape GitHub published: a Windows zip without its
    # wheel (or the reverse) is an incomplete release, not a smaller one.
    wheel_names = {f"pairmux-{version}-py3-none-{PLATFORMS[target]}.whl" for target in targets}
    by_wheel = {wheel["filename"]: wheel for wheel in wheels}
    if len(wheels) != len(wheel_names) or len(by_wheel) != len(wheel_names) or set(by_wheel) != wheel_names:
        raise ValueError(f"PyPI must publish exactly the {len(wheel_names)} platform wheels of a {shape} release")
    for name, wheel in by_wheel.items():
        if wheel.get("packagetype") != "bdist_wheel" or wheel.get("yanked") is not False or not wheel.get("url", "").startswith("https://files.pythonhosted.org/") or not re.fullmatch(r"[a-f0-9]{64}", wheel.get("digests", {}).get("sha256", "")):
            raise ValueError("invalid, yanked, or unchecked PyPI wheel " + name)

    latest = json.loads(fetch(latest_url))
    latest_tag = latest.get("tag_name", "")
    if not STABLE.fullmatch(latest_tag) or latest.get("draft") is not False or latest.get("prerelease") is not False:
        raise ValueError("GitHub latest is not canonical stable")
    all_pypi_url = "https://pypi.org/pypi/pairmux/json"
    all_pypi = json.loads(fetch(all_pypi_url))
    stable_versions = [v for v, files in all_pypi.get("releases", {}).items()
                       if STABLE.fullmatch("v" + v) and any(f.get("packagetype") == "bdist_wheel" and f.get("yanked") is False for f in files)]
    if not stable_versions:
        raise ValueError("no public stable PyPI wheel version")
    latest_version = max(stable_versions, key=lambda v: tuple(int(x) for x in v.split(".")))
    report = {
        "tag": tag, "version": version, "published_at": release["published_at"],
        "sources": {"github": release_url, "pypi": pypi_url, "latest_github": latest_url, "latest_pypi": all_pypi_url},
        "latest_github_tag": latest_tag, "latest_pypi_version": latest_version,
        "tag_is_latest_on_both": latest_tag == tag and latest_version == version,
        "shape": shape, "targets": list(targets),
        "checksums_sha256": digest(checksum_data), "checksums": checksums,
        "assets": [{"name": n, "url": base + n, "sha256": checksums[n]} for n in sorted(names)],
        "wheels": [{"name": n, "url": by_wheel[n]["url"], "sha256": by_wheel[n]["digests"]["sha256"]} for n in sorted(wheel_names)],
    }
    if target:
        if target not in targets:
            raise ValueError(f"release {tag} has no {target} assets ({shape} release)")
        name = archive_name(version, target)
        archive_path, archive_data = download(out, base + name, name, checksums[name])
        if target.startswith("windows-"):
            executable = "pairmux.exe"
            with zipfile.ZipFile(io.BytesIO(archive_data)) as archive:
                members = [member for member in archive.infolist() if member.filename == executable]
                if (len(members) != 1 or members[0].is_dir()
                        or stat.S_IFMT(members[0].external_attr >> 16) not in (0, stat.S_IFREG)):
                    raise ValueError("native archive has no unique regular pairmux.exe")
                binary_hash = digest(archive.read(members[0]))
        else:
            executable = "pairmux"
            with tarfile.open(fileobj=io.BytesIO(archive_data), mode="r:gz") as archive:
                members = [member for member in archive.getmembers() if member.name.removeprefix("./") == executable]
                if len(members) != 1 or not members[0].isfile() or not members[0].mode & 0o111:
                    raise ValueError("native archive has no unique regular executable pairmux")
                stream = archive.extractfile(members[0])
                if stream is None:
                    raise ValueError("cannot read archived binary")
                binary_hash = digest(stream.read())
        wheel_name = f"pairmux-{version}-py3-none-{PLATFORMS[target]}.whl"
        wheel = by_wheel[wheel_name]
        wheel_path, wheel_data = download(out, wheel["url"], wheel_name, wheel["digests"]["sha256"])
        with zipfile.ZipFile(io.BytesIO(wheel_data)) as archive:
            binary_name = f"pairmux-{version}.data/scripts/{executable}"
            if archive.namelist().count(binary_name) != 1 or digest(archive.read(binary_name)) != binary_hash:
                raise ValueError("selected public wheel binary differs from the checksummed native archive")
        report["selected"] = {"target": target, "archive": str(archive_path), "wheel": str(wheel_path), "binary_sha256": binary_hash}
    if rpm:
        if target != "linux-amd64":
            raise ValueError("RPM acceptance requires the linux-amd64 target")
        name = f"pairmux_{version}_linux_amd64.rpm"
        path, _ = download(out, base + name, name, checksums[name])
        report["selected"]["rpm"] = str(path)
    return report


def actual_provenance(previous: dict, version: str, out: Path) -> dict:
    """Bind upgrade acceptance to its installed version, not a latest predicate."""
    if not STABLE.fullmatch("v" + version):
        raise ValueError("installed version must be canonical stable X.Y.Z")
    target = previous.get("selected", {}).get("target")
    if target not in PLATFORMS:
        raise ValueError("previous provenance has no verified platform target")
    if previous.get("version") == version and previous.get("tag") == "v" + version:
        report = previous
    else:
        # Historical pinned releases remain supported. Resolve the ACTUAL
        # upgraded release, verifying its native inventory/checksum/wheel bytes.
        # Missing or inconsistent publication fails closed, never version-only.
        report = verify("v" + version, out, target)
    if not re.fullmatch(r"[a-f0-9]{64}", report.get("selected", {}).get("binary_sha256", "")):
        raise ValueError("actual installed version has no verified native binary SHA256")
    return report


def check_installed(executable: Path, version: str, report: dict) -> dict:
    if report.get("version") != version or report.get("tag") != "v" + version:
        raise ValueError("installed version does not match its verified release provenance")
    canonical = executable.resolve(strict=True)
    info = canonical.stat()
    if not canonical.is_file() or not info.st_mode & 0o111:
        raise ValueError("installed binary is not a regular executable")
    expected = report["selected"]["binary_sha256"]
    actual = digest(canonical.read_bytes())
    if actual != expected:
        raise ValueError("installed PyPI binary differs from checksummed public release " + version)
    reply = subprocess.run([str(executable), "version"], capture_output=True, text=True, timeout=60)
    if reply.returncode or reply.stdout.strip() != version:
        raise ValueError("installed binary does not report the verified release version")
    return {"canonical": str(canonical), "device": info.st_dev, "inode": info.st_ino, "sha256": actual}


def refresh_installed(executable: Path, version: str, report: dict, out: Path) -> dict:
    """Prove reinstall replaced the binary while retaining the old file open."""
    before = check_installed(executable, version, report)
    canonical = Path(before["canonical"])
    with canonical.open("rb") as held:
        held_before = os.fstat(held.fileno())
        if (held_before.st_dev, held_before.st_ino) != (before["device"], before["inode"]) or digest(held.read()) != before["sha256"]:
            raise ValueError("installed binary changed before the held-file refresh probe")
        try:
            result = subprocess.run([str(executable), "--json", "update"], capture_output=True, timeout=300)
        except subprocess.TimeoutExpired as exc:
            (out / "self-refresh.json").write_bytes(exc.stdout or b"")
            (out / "self-refresh.stderr").write_bytes(exc.stderr or b"")
            raise ValueError("self-refresh timed out") from exc
        (out / "self-refresh.json").write_bytes(result.stdout)
        (out / "self-refresh.stderr").write_bytes(result.stderr)
        if result.returncode:
            raise ValueError("self-refresh command failed")
        reply = json.loads(result.stdout)
        if not isinstance(reply, dict) or reply.get("schema") != "pairmux.v1" or reply.get("ok") is not True or reply.get("status") != "refreshed" or "error" in reply:
            raise ValueError("self-update did not return a healthy refreshed response")
        if "source: https://pypi.org/simple" not in reply.get("output", ""):
            raise ValueError("self-update did not report public PyPI")
        held.seek(0)
        held_after = os.fstat(held.fileno())
        if (held_after.st_dev, held_after.st_ino) != (held_before.st_dev, held_before.st_ino) or digest(held.read()) != before["sha256"]:
            raise ValueError("self-refresh changed bytes of the held old executable")
        replacement = executable.resolve(strict=True)
        replacement_info = replacement.stat()
        if replacement != canonical:
            raise ValueError("self-refresh changed the canonical executable path")
        if (replacement_info.st_dev, replacement_info.st_ino) == (held_before.st_dev, held_before.st_ino):
            raise ValueError("self-refresh did not replace the old executable (no-op refresh)")
        # Keeping the old descriptor open prevents inode reuse from satisfying
        # the identity assertion. Replacement bytes must still be the verified
        # native binary for the ACTUAL version, regardless of latest metadata.
        after = check_installed(executable, version, report)
    identity = {"version": version, "before": before, "replacement": after, "held_old_bytes_preserved": True}
    (out / "self-refresh-identity.json").write_text(json.dumps(identity, indent=2) + "\n", encoding="utf-8")
    return identity


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out-dir", required=True, type=Path)
    parser.add_argument("--target", choices=PLATFORMS)
    parser.add_argument("--rpm", action="store_true")
    parser.add_argument("--installed", type=Path, help="verify an installed binary against its actual public release")
    parser.add_argument("--installed-version", help="actual canonical stable binary version")
    parser.add_argument("--provenance", type=Path, help="previously verified selected release provenance")
    parser.add_argument("--refresh", action="store_true", help="hold the old binary open and prove a same-version reinstall")
    args = parser.parse_args()
    try:
        args.out_dir.mkdir(parents=True, exist_ok=True)
        if not args.out_dir.is_absolute() or any(args.out_dir.iterdir()):
            raise ValueError("output must be an absolute new, empty directory")
        if args.installed:
            if not args.installed.is_absolute() or not args.installed_version or not args.provenance or args.target or args.rpm:
                raise ValueError("installed probes require absolute --installed, --installed-version, and --provenance only")
            previous = json.loads(args.provenance.read_text(encoding="utf-8"))
            report = actual_provenance(previous, args.installed_version, args.out_dir)
            (args.out_dir / "provenance.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
            if args.refresh:
                refresh_installed(args.installed, args.installed_version, report, args.out_dir)
                print("verified actual public binary replacement and preserved held bytes for " + args.installed_version)
            else:
                installed = check_installed(args.installed, args.installed_version, report)
                (args.out_dir / "installed-identity.json").write_text(json.dumps(installed, indent=2) + "\n", encoding="utf-8")
                print("verified installed binary against actual public release " + args.installed_version)
            return 0
        if args.refresh or args.installed_version or args.provenance:
            raise ValueError("installed probe options require --installed")
        report = verify(os.environ.get("ACCEPTANCE_TAG", ""), args.out_dir, args.target, args.rpm)
        (args.out_dir / "provenance.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        if os.environ.get("GITHUB_OUTPUT"):
            # windows_assets lets the workflow skip the native Windows job only on
            # an explicit, verified "false"; every other value runs it and fails.
            outputs = {"version": report["version"], "latest_version": report["latest_pypi_version"],
                       "shape": report["shape"],
                       "windows_assets": "true" if "windows-amd64" in report["targets"] else "false"}
            outputs.update(report.get("selected", {}))
            with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
                for key, value in outputs.items():
                    if "\n" in str(value) or "\r" in str(value):
                        raise ValueError("invalid output value")
                    stream.write(f"{key}={value}\n")
        print(f"verified published {report['tag']} ({report['shape']}): {len(report['assets']) + 1} GitHub assets and "
              f"{len(report['wheels'])} PyPI wheels; latest public PyPI stable {report['latest_pypi_version']}")
        return 0
    except (ValueError, OSError, KeyError, tarfile.TarError, zipfile.BadZipFile, subprocess.SubprocessError) as exc:
        print("release acceptance failed: " + str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
