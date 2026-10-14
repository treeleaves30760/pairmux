"""In-tree PEP 517 backend: install pairmux from a source checkout, offline.

This exists for people who can clone the repository but cannot install pairmux
from PyPI. The repository-root ``pyproject.toml`` points here through
``backend-path`` and declares ``requires = []``, so a frontend needs nothing
from any index::

    python -m pip install .                          # active environment
    uv tool install .                                # persistent uv tool
    python -m pip install git+https://github.com/treeleaves30760/pairmux

It is **not** the release path. Release wheels come from build_wheels.py, fed
by GoReleaser and verified by the tag workflow. This backend reuses that
module's metadata, header checks and wheel writer, but always produces a
host-only *development* wheel:

* **Binary.** ``PAIRMUX_PREBUILT_BINARY=/absolute/path/to/pairmux[.exe]`` (for
  example, extracted from a GitHub Release archive) is packaged as-is after
  the same Mach-O/ELF/PE header checks the release wheels use, against the
  host platform; it is never executed here. Otherwise the backend runs
  ``go build`` (Go 1.25+, ``CGO_ENABLED=0``, the host GOOS/GOARCH) into a
  temporary directory outside the checkout. Nothing is downloaded by this
  backend; Go itself resolves its toolchain as you configured it.
* **Version.** ``git describe`` of the latest canonical ``vX.Y.Z`` tag becomes
  ``X.Y.Z.post<N>.dev0+g<sha>`` (``.dirty`` appended for uncommitted changes),
  even on the tagged commit itself. The ``.dev`` release segment and the
  ``+`` local label make the build impossible to confuse with, or upload as, a
  PyPI release, and the same string is stamped into the binary, which
  ``pairmux update`` refuses as a non-canonical release. Without usable git
  metadata the version is ``0.0.0.dev0+unknown`` (or ``0.0.0.dev0+g<sha>``
  when the history has no tag). ``PAIRMUX_VERSION`` overrides all of this
  with a canonical SemVer tag or a canonical PEP 440 version.
* **Tag.** The interpreter's platform: ``macosx_<minos>_<arch>`` read from the
  Mach-O, ``linux_<arch>`` (a host-only tag: honest for a wheel built here,
  installable on glibc and musl alike, and refused by PyPI), or
  ``win_amd64``/``win_arm64``.

There is deliberately no sdist (as for the release, an sdist would invite
source builds elsewhere) and no editable mode (a compiled binary has nothing to
link). Both hooks fail with an explanation instead.
"""
from __future__ import annotations

import os
import platform
import re
import shutil
import subprocess
import sys
import sysconfig
import tempfile
from pathlib import Path
from typing import Mapping, Optional, Tuple

import build_wheels

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_PACKAGE = "./cmd/pairmux"
VERSION_SYMBOL = "github.com/treeleaves30760/pairmux/internal/version.Version"
PREBUILT_ENV = "PAIRMUX_PREBUILT_BINARY"
VERSION_ENV = "PAIRMUX_VERSION"
UNKNOWN_VERSION = "0.0.0.dev0+unknown"
RELEASES_URL = "https://github.com/treeleaves30760/pairmux/releases"

GO_MISSING_HINT = (
    "`go` was not found on PATH. Install Go 1.25+ from https://go.dev/dl/ "
    "or download a prebuilt binary from GitHub Releases (%s), extract "
    "pairmux (pairmux.exe on Windows) and set %s to its absolute path."
    % (RELEASES_URL, PREBUILT_ENV)
)

# Canonical PEP 440 (no epoch), optionally with a local label. Canonical form
# is required so the wheel filename, METADATA and binary stamp agree exactly.
PEP440_CANONICAL_RE = re.compile(
    r"(0|[1-9][0-9]*)(?:\.(0|[1-9][0-9]*))*"
    r"(?:(a|b|rc)(0|[1-9][0-9]*))?"
    r"(?:\.post(0|[1-9][0-9]*))?"
    r"(?:\.dev(0|[1-9][0-9]*))?"
    r"(?:\+[a-z0-9]+(?:\.[a-z0-9]+)*)?"
)
DESCRIBE_RE = re.compile(r"(v[0-9]\S*)-([0-9]+)-g([0-9a-f]{7,64})(-dirty)?")
UNTAGGED_RE = re.compile(r"([0-9a-f]{7,64})(-dirty)?")

GOARCH_TO_ELF_ARCH = {"amd64": "x86_64", "arm64": "aarch64"}
GOARCH_TO_MACHO_ARCH = {"amd64": "x86_64", "arm64": "arm64"}


class BuildError(RuntimeError):
    """An actionable reason the source build cannot proceed."""


class UnsupportedOperation(Exception):
    """PEP 517's name for a hook this backend deliberately does not offer."""


def _log(message: str) -> None:
    print("pairmux-build: " + message, file=sys.stderr, flush=True)


def _reject_config_settings(config_settings: Optional[Mapping]) -> None:
    # Fail loudly rather than silently ignoring a setting the user expects to
    # take effect. The two supported knobs are environment variables.
    if config_settings:
        raise BuildError(
            "this backend accepts no config settings (got %s); use %s and %s "
            "instead" % (", ".join(sorted(map(str, config_settings))),
                         PREBUILT_ENV, VERSION_ENV)
        )


# --- host platform -----------------------------------------------------------

def host_target(*, sys_platform: Optional[str] = None, machine: Optional[str] = None,
                sysconfig_platform: Optional[str] = None,
                maxsize: Optional[int] = None,
                version_info: Optional[Tuple[int, int]] = None) -> Tuple[str, str]:
    """The (GOOS, GOARCH) of the interpreter that will install the wheel.

    Installers select wheels by the interpreter's platform, not the kernel's:
    an x86-64 Python under Rosetta needs an x86_64 wheel on an arm64 Mac, and a
    32-bit Python on a 64-bit kernel can install neither architecture. The
    keyword arguments exist for tests; None reads the running interpreter.
    """
    sys_platform = sys.platform if sys_platform is None else sys_platform
    maxsize = sys.maxsize if maxsize is None else maxsize
    version_info = tuple(sys.version_info[:2]) if version_info is None else version_info
    if version_info < (3, 9):
        raise BuildError("pairmux needs Python 3.9+ to build and install its wheel")
    if maxsize <= 2 ** 32:
        raise BuildError("pairmux needs a 64-bit Python (this interpreter is 32-bit)")
    if sys_platform == "darwin":
        goos = "darwin"
        label = platform.machine() if machine is None else machine
        arch = {"arm64": "arm64", "x86_64": "amd64"}.get(label)
    elif sys_platform.startswith("linux"):
        goos = "linux"
        label = sysconfig.get_platform() if sysconfig_platform is None else sysconfig_platform
        arch = {"linux-x86_64": "amd64", "linux-aarch64": "arm64"}.get(label)
    elif sys_platform == "win32":
        goos = "windows"
        label = sysconfig.get_platform() if sysconfig_platform is None else sysconfig_platform
        arch = {"win-amd64": "amd64", "win-arm64": "arm64"}.get(label)
    else:
        raise BuildError(
            "unsupported platform %r: pairmux builds for macOS, Linux and "
            "Windows only" % sys_platform
        )
    if arch is None:
        raise BuildError(
            "unsupported %s architecture %r: pairmux builds for x86-64 and "
            "ARM64 only" % (goos, label)
        )
    return goos, arch


def host_wheel_tag(goos: str, goarch: str, binary: Path) -> str:
    """Prove the binary runs on the host, then return its wheel platform tag."""
    if goos == "darwin":
        tag = build_wheels.macos_platform_tag(binary)
        if not tag.endswith("_" + GOARCH_TO_MACHO_ARCH[goarch]):
            raise BuildError("%s is %s, but this Python is darwin/%s" % (binary, tag, goarch))
        # The deployment target comes from the Mach-O itself, so a newer Go
        # that raises it cannot produce a wheel claiming older macOS support.
        return tag
    if goos == "linux":
        arch = build_wheels.linux_elf_arch(binary)
        if arch != GOARCH_TO_ELF_ARCH[goarch]:
            raise BuildError("%s is linux/%s, but this Python is linux/%s" % (binary, arch, goarch))
        return "linux_" + arch
    if goos == "windows":
        arch = build_wheels.windows_pe_arch(binary)
        if arch != goarch:
            raise BuildError("%s is windows/%s, but this Python is windows/%s" % (binary, arch, goarch))
        return "win_" + arch
    raise BuildError("unsupported GOOS %r" % goos)


# --- version -----------------------------------------------------------------

def _git(root: Path, *args: str) -> Optional[str]:
    git = shutil.which("git")
    if git is None:
        return None
    try:
        result = subprocess.run(
            [git, "-C", os.fspath(root), *args],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, timeout=60, check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    if result.returncode != 0:
        return None
    return result.stdout.decode("utf-8", "replace").strip()


def git_version(root: Path) -> Optional[str]:
    """Development version from git metadata of exactly ``root``, or None."""
    # A tree copied without its .git (or vendored into another repository) must
    # not describe whichever enclosing repository git would otherwise find.
    if not (root / ".git").exists():
        return None
    toplevel = _git(root, "rev-parse", "--show-toplevel")
    if toplevel is None or Path(toplevel).resolve() != root.resolve():
        return None
    described = _git(
        root, "describe", "--tags", "--long", "--always", "--dirty",
        "--abbrev=12", "--match", "v[0-9]*.[0-9]*.[0-9]*",
    )
    if described is None:
        return None
    match = DESCRIBE_RE.fullmatch(described)
    if match:
        tag, distance, sha, dirty = match.groups()
        try:
            base = build_wheels.normalize_version(tag)
        except ValueError:
            raise BuildError(
                "the nearest tag %r is not a canonical vX.Y.Z release tag; "
                "set %s to the version to build" % (tag, VERSION_ENV)
            ) from None
        return "%s.post%s.dev0+g%s%s" % (base, distance, sha, ".dirty" if dirty else "")
    match = UNTAGGED_RE.fullmatch(described)
    if match:
        sha, dirty = match.groups()
        return "0.0.0.dev0+g%s%s" % (sha, ".dirty" if dirty else "")
    return None


def resolve_version(root: Path = REPO_ROOT,
                    environ: Optional[Mapping[str, str]] = None) -> Tuple[str, str, str]:
    """Return (wheel version, binary version stamp, human-readable source)."""
    environ = os.environ if environ is None else environ
    override = environ.get(VERSION_ENV)
    if override:
        try:
            return (build_wheels.normalize_version(override),
                    override.removeprefix("v"), VERSION_ENV)
        except ValueError:
            pass
        if PEP440_CANONICAL_RE.fullmatch(override):
            return override, override, VERSION_ENV
        raise BuildError(
            "%s=%r is neither a canonical SemVer tag (v1.2.3, v1.2.3-rc.1) nor "
            "a canonical PEP 440 version (1.2.3.dev4+local)" % (VERSION_ENV, override)
        )
    described = git_version(root)
    if described is not None:
        return described, described, "git describe"
    return UNKNOWN_VERSION, UNKNOWN_VERSION, "no usable git metadata"


def _describe_source(version: str, source: str) -> str:
    if source == VERSION_ENV and "dev" not in version and "+" not in version:
        _log("warning: %s makes this local build release-shaped (%s); its version "
             "no longer distinguishes it from a published release, and "
             "`pairmux update` will treat it as one" % (VERSION_ENV, version))
    return "version %s (from %s)" % (version, source)


def _metadata_version(metadata_directory: str) -> str:
    # PEP 517 passes the prepared .dist-info directory itself; accept its
    # parent too rather than guessing wrong about a frontend.
    directory = Path(metadata_directory)
    candidates = [directory / "METADATA"] if (directory / "METADATA").is_file() \
        else list(directory.glob("*.dist-info/METADATA"))
    if len(candidates) != 1:
        raise BuildError("expected one prepared METADATA in %s" % metadata_directory)
    for line in candidates[0].read_text(encoding="utf-8").splitlines():
        if not line:
            break
        if line.startswith("Version: "):
            return line[len("Version: "):]
    raise BuildError("prepared METADATA in %s has no Version" % metadata_directory)


# --- binary ------------------------------------------------------------------

def prebuilt_binary(environ: Mapping[str, str]) -> Optional[Path]:
    raw = environ.get(PREBUILT_ENV)
    if not raw:
        return None
    path = Path(raw)
    # Relative paths would silently resolve against the frontend's build
    # directory (a temporary clone for git+https installs), not the user's.
    if not path.is_absolute():
        raise BuildError("%s must be an absolute path, got %r" % (PREBUILT_ENV, raw))
    if not path.is_file():
        raise BuildError("%s=%s is not a regular file" % (PREBUILT_ENV, raw))
    return path


def go_build(root: Path, goos: str, goarch: str, stamp: str, out_dir: Path,
             environ: Mapping[str, str]) -> Path:
    go = shutil.which("go", path=environ.get("PATH"))
    if go is None:
        raise BuildError(GO_MISSING_HINT)
    output = out_dir / build_wheels.binary_name(goos)
    # Mirrors .goreleaser.yaml (static, stripped, version-stamped). VCS
    # stamping is off: it can fail on ownership checks in a frontend's build
    # environment, and the version already names the commit.
    command = [
        go, "build", "-trimpath", "-buildvcs=false",
        "-ldflags", "-s -w -X %s=%s" % (VERSION_SYMBOL, stamp),
        "-o", os.fspath(output), GO_PACKAGE,
    ]
    env = dict(environ)
    env.update({"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch})
    _log("running %s (GOOS=%s GOARCH=%s CGO_ENABLED=0)" % (" ".join(command[1:]), goos, goarch))
    try:
        result = subprocess.run(command, cwd=os.fspath(root), env=env,
                                stdin=subprocess.DEVNULL, check=False)
    except OSError as exc:
        raise BuildError("could not run %s: %s. %s" % (go, exc, GO_MISSING_HINT)) from exc
    if result.returncode != 0:
        raise BuildError(
            "go build failed (exit %d; Go's errors are above). pairmux needs Go "
            "1.25+ (see go.mod); an untagged commit may also simply not build. "
            "Check out a release tag, or set %s to the absolute path of a "
            "prebuilt binary from %s" % (result.returncode, PREBUILT_ENV, RELEASES_URL)
        )
    if not output.is_file():
        raise BuildError("go build reported success but wrote no %s" % output)
    return output


# --- PEP 517 hooks -----------------------------------------------------------

def get_requires_for_build_wheel(config_settings=None):
    _reject_config_settings(config_settings)
    return []


def prepare_metadata_for_build_wheel(metadata_directory, config_settings=None):
    _reject_config_settings(config_settings)
    goos, goarch = host_target()
    version, _, source = resolve_version()
    _log(_describe_source(version, source))
    dist_info = Path(metadata_directory) / ("%s-%s.dist-info" % (build_wheels.PROJECT_NAME, version))
    dist_info.mkdir(parents=True, exist_ok=True)
    (dist_info / "METADATA").write_bytes(build_wheels.metadata_bytes(version))
    # The final tag is read from the built binary; this is the expected one.
    predicted = {
        "darwin": "macosx_12_0_" + GOARCH_TO_MACHO_ARCH[goarch],
        "linux": "linux_" + GOARCH_TO_ELF_ARCH[goarch],
        "windows": "win_" + goarch,
    }[goos]
    (dist_info / "WHEEL").write_bytes(build_wheels.wheel_bytes(predicted))
    return dist_info.name


def build_wheel(wheel_directory, config_settings=None, metadata_directory=None):
    _reject_config_settings(config_settings)
    environ = os.environ
    goos, goarch = host_target()
    version, stamp, source = resolve_version(REPO_ROOT, environ)
    if metadata_directory is not None:
        prepared = _metadata_version(metadata_directory)
        if prepared != version:
            raise BuildError(
                "version changed from %s to %s between metadata preparation and "
                "the build (did the checkout change?); retry the install"
                % (prepared, version)
            )
    _log("%s, target %s/%s" % (_describe_source(version, source), goos, goarch))

    with tempfile.TemporaryDirectory(prefix="pairmux-build-") as scratch:
        binary = prebuilt_binary(environ)
        if binary is None:
            binary = go_build(REPO_ROOT, goos, goarch, stamp, Path(scratch), environ)
        else:
            _log("packaging %s=%s; `pairmux version` reports that binary's own "
                 "stamp, not %s" % (PREBUILT_ENV, binary, version))
        try:
            tag = host_wheel_tag(goos, goarch, binary)
            wheel = build_wheels.build_one(version, binary, tag, Path(wheel_directory))
        except ValueError as exc:
            raise BuildError("%s (expected a pairmux executable for %s/%s)"
                             % (exc, goos, goarch)) from exc
    if not build_wheels.check_wheel(wheel):
        raise BuildError("the built wheel failed its RECORD self-check: %s" % wheel)
    _log("built %s" % wheel.name)
    return wheel.name


def build_sdist(sdist_directory, config_settings=None):
    raise UnsupportedOperation(
        "pairmux does not build source distributions: install from this "
        "checkout directly (python -m pip install . or uv tool install .), or "
        "build a wheel (python -m pip wheel --no-deps . or uv build --wheel)"
    )


def build_editable(wheel_directory, config_settings=None, metadata_directory=None):
    raise BuildError(
        "editable installs are not supported: pairmux is a compiled Go binary, "
        "so there is nothing to link. Run `python -m pip install .` (or "
        "`uv tool install --reinstall .`) again after changing the source"
    )
