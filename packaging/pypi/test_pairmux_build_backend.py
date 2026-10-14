import os
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
import zipfile
from email.parser import BytesParser
from email.policy import compat32
from pathlib import Path
from unittest import mock

import build_wheels
import pairmux_build_backend as backend


def macho_bytes(cpu: int, major: int = 12, minor: int = 0) -> bytes:
    minos = (major << 16) | (minor << 8)
    command = struct.pack("<IIIIII", 0x32, 24, 1, minos, minos, 0)
    header = struct.pack("<IiiIIIII", 0xFEEDFACF, cpu, 0, 2, 1, len(command), 0, 0)
    return header + command + b"test-macho-payload"


def elf_bytes(machine: int) -> bytes:
    ident = b"\x7fELF" + bytes((2, 1, 1, 0, 0)) + bytes(7)
    return struct.pack(
        "<16sHHIQQQIHHHHHH", ident, 2, machine, 1, 0, 0, 0, 0, 64, 0, 0, 0, 0, 0,
    ) + b"test-elf-payload"


def pe_bytes(machine: int) -> bytes:
    dos = bytearray(64)
    dos[:2] = b"MZ"
    struct.pack_into("<I", dos, 0x3C, 64)
    optional = bytearray(240)
    struct.pack_into("<H", optional, 0, 0x20B)
    struct.pack_into("<H", optional, 68, 3)
    coff = struct.pack("<HHIIIHH", machine, 1, 0, 0, 0, len(optional), 0x0022)
    return bytes(dos) + b"PE\0\0" + coff + bytes(optional) + b"test-pe-payload"


# GOOS/GOARCH -> (synthetic native binary, expected wheel platform tag)
TARGETS = {
    ("darwin", "arm64"): (macho_bytes(0x0100000C), "macosx_12_0_arm64"),
    ("darwin", "amd64"): (macho_bytes(0x01000007), "macosx_12_0_x86_64"),
    ("linux", "amd64"): (elf_bytes(62), "linux_x86_64"),
    ("linux", "arm64"): (elf_bytes(183), "linux_aarch64"),
    ("windows", "amd64"): (pe_bytes(0x8664), "win_amd64"),
    ("windows", "arm64"): (pe_bytes(0xAA64), "win_arm64"),
}

GIT_IDENTITY = {
    "GIT_CONFIG_NOSYSTEM": "1",
    "GIT_AUTHOR_NAME": "pairmux test",
    "GIT_AUTHOR_EMAIL": "test@example.invalid",
    "GIT_COMMITTER_NAME": "pairmux test",
    "GIT_COMMITTER_EMAIL": "test@example.invalid",
}


def environment(**overrides: str) -> dict:
    """The real environment minus pairmux knobs, plus overrides.

    Never cleared wholesale: Windows needs SYSTEMROOT, TEMP and friends.
    """
    env = {key: value for key, value in os.environ.items()
           if not key.startswith("PAIRMUX_")}
    env.update(overrides)
    return env


class HookContractTest(unittest.TestCase):
    def test_needs_no_build_dependencies(self) -> None:
        self.assertEqual(backend.get_requires_for_build_wheel(), [])
        self.assertEqual(backend.get_requires_for_build_wheel({}), [])

    def test_rejects_config_settings_instead_of_ignoring_them(self) -> None:
        with self.assertRaisesRegex(backend.BuildError, "no config settings.*PAIRMUX_PREBUILT_BINARY"):
            backend.get_requires_for_build_wheel({"prebuilt": "/x"})
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaisesRegex(backend.BuildError, "no config settings"):
                backend.build_wheel(td, {"--build-option": "x"})
            with self.assertRaisesRegex(backend.BuildError, "no config settings"):
                backend.prepare_metadata_for_build_wheel(td, {"x": "y"})

    def test_sdist_is_unsupported_with_an_explanation(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaises(backend.UnsupportedOperation) as caught:
                backend.build_sdist(td)
            self.assertEqual(os.listdir(td), [])
        self.assertIn("does not build source distributions", str(caught.exception))
        self.assertIn("pip install .", str(caught.exception))
        self.assertIn("uv build --wheel", str(caught.exception))

    def test_editable_installs_are_refused_with_an_explanation(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaisesRegex(backend.BuildError, "editable installs are not supported"):
                backend.build_editable(td)

    def test_pyproject_points_at_this_backend_with_no_requirements(self) -> None:
        text = (backend.REPO_ROOT / "pyproject.toml").read_text(encoding="utf-8")
        self.assertIn("requires = []\n", text)
        self.assertIn('build-backend = "pairmux_build_backend"\n', text)
        self.assertIn('backend-path = ["packaging/pypi"]\n', text)
        self.assertNotIn("\n[project]", text)
        self.assertEqual(Path(backend.__file__).resolve().parent,
                         backend.REPO_ROOT / "packaging" / "pypi")


class HostTargetTest(unittest.TestCase):
    def test_maps_supported_interpreters(self) -> None:
        cases = (
            (dict(sys_platform="darwin", machine="arm64"), ("darwin", "arm64")),
            (dict(sys_platform="darwin", machine="x86_64"), ("darwin", "amd64")),
            (dict(sys_platform="linux", sysconfig_platform="linux-x86_64"), ("linux", "amd64")),
            (dict(sys_platform="linux", sysconfig_platform="linux-aarch64"), ("linux", "arm64")),
            (dict(sys_platform="win32", sysconfig_platform="win-amd64"), ("windows", "amd64")),
            (dict(sys_platform="win32", sysconfig_platform="win-arm64"), ("windows", "arm64")),
        )
        for probe, expected in cases:
            with self.subTest(probe=probe):
                self.assertEqual(
                    backend.host_target(maxsize=2 ** 63 - 1, version_info=(3, 12), **probe),
                    expected,
                )

    def test_rejects_unsupported_interpreters(self) -> None:
        cases = (
            (dict(sys_platform="win32", sysconfig_platform="win32"), "architecture 'win32'"),
            (dict(sys_platform="linux", sysconfig_platform="linux-armv7l"), "architecture"),
            (dict(sys_platform="linux", sysconfig_platform="linux-i686"), "architecture"),
            (dict(sys_platform="darwin", machine="ppc64"), "architecture"),
            (dict(sys_platform="freebsd14"), "unsupported platform 'freebsd14'"),
            (dict(sys_platform="cygwin"), "unsupported platform"),
            (dict(sys_platform="darwin", machine="arm64", maxsize=2 ** 31 - 1), "64-bit Python"),
            (dict(sys_platform="darwin", machine="arm64", version_info=(3, 8)), "Python 3.9"),
        )
        for probe, error in cases:
            with self.subTest(probe=probe):
                probe = dict(dict(maxsize=2 ** 63 - 1, version_info=(3, 12)), **probe)
                with self.assertRaisesRegex(backend.BuildError, error):
                    backend.host_target(**probe)

    def test_real_interpreter_is_supported_here(self) -> None:
        goos, goarch = backend.host_target()
        self.assertIn((goos, goarch), TARGETS)


class HostWheelTagTest(unittest.TestCase):
    def test_tags_follow_the_native_header(self) -> None:
        for (goos, goarch), (content, tag) in TARGETS.items():
            with self.subTest(target=(goos, goarch)), tempfile.TemporaryDirectory() as td:
                binary = Path(td) / build_wheels.binary_name(goos)
                binary.write_bytes(content)
                self.assertEqual(backend.host_wheel_tag(goos, goarch, binary), tag)

    def test_macos_deployment_target_comes_from_the_binary(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            binary = Path(td) / "pairmux"
            binary.write_bytes(macho_bytes(0x0100000C, major=13, minor=3))
            self.assertEqual(backend.host_wheel_tag("darwin", "arm64", binary),
                             "macosx_13_3_arm64")

    def test_rejects_binaries_for_another_host(self) -> None:
        cases = (
            ("darwin", "arm64", macho_bytes(0x01000007), backend.BuildError, "darwin/arm64"),
            ("linux", "amd64", elf_bytes(183), backend.BuildError, "linux/amd64"),
            ("windows", "arm64", pe_bytes(0x8664), backend.BuildError, "windows/arm64"),
            ("linux", "amd64", macho_bytes(0x01000007), ValueError, "not an ELF"),
            ("windows", "amd64", elf_bytes(62), ValueError, "no MZ header"),
            ("darwin", "amd64", pe_bytes(0x8664), ValueError, "Mach-O"),
        )
        for goos, goarch, content, error_type, error in cases:
            with self.subTest(target=(goos, goarch), error=error), tempfile.TemporaryDirectory() as td:
                binary = Path(td) / "candidate"
                binary.write_bytes(content)
                with self.assertRaisesRegex(error_type, error):
                    backend.host_wheel_tag(goos, goarch, binary)


@unittest.skipUnless(shutil.which("git"), "git is not installed")
class GitVersionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        # An empty global config isolates the user's signing/describe settings
        # on every OS (os.devnull is "nul" on Windows).
        global_config = Path(self.temp.name) / "gitconfig"
        global_config.write_text("", encoding="utf-8")
        self.git_env = dict(GIT_IDENTITY, GIT_CONFIG_GLOBAL=os.fspath(global_config))
        self.root = Path(self.temp.name) / "checkout"
        self.root.mkdir()
        self.git("init", "-q")
        (self.root / "tracked.txt").write_text("one\n", encoding="utf-8")
        self.git("add", "tracked.txt")
        self.git("commit", "-q", "-m", "feat: one")

    def git(self, *args: str) -> str:
        env = dict(os.environ, **self.git_env)
        result = subprocess.run(
            ["git", "-C", os.fspath(self.root), *args], env=env, check=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        return result.stdout.decode("utf-8").strip()

    def commit(self, message: str) -> None:
        self.git("commit", "-q", "--allow-empty", "-m", message)

    def sha(self) -> str:
        return self.git("rev-parse", "--short=12", "HEAD")

    def version(self):
        with mock.patch.dict(os.environ, self.git_env):
            return backend.resolve_version(self.root, environ={})

    def test_tagged_commit_is_still_a_development_build(self) -> None:
        self.git("tag", "v1.2.3")
        self.assertEqual(
            self.version(),
            ("1.2.3.post0.dev0+g" + self.sha(),) * 2 + ("git describe",),
        )

    def test_commits_after_the_latest_tag_count_as_post_releases(self) -> None:
        self.git("tag", "-a", "v1.2.3", "-m", "v1.2.3")
        self.commit("fix: two")
        self.commit("fix: three")
        self.assertEqual(self.version()[0], "1.2.3.post2.dev0+g" + self.sha())

    def test_prerelease_tags_normalize(self) -> None:
        self.git("tag", "v2.0.0-rc.1")
        self.commit("fix: two")
        self.assertEqual(self.version()[0], "2.0.0rc1.post1.dev0+g" + self.sha())

    def test_uncommitted_tracked_changes_mark_the_build_dirty(self) -> None:
        self.git("tag", "v1.2.3")
        (self.root / "untracked.txt").write_text("x\n", encoding="utf-8")
        self.assertEqual(self.version()[0], "1.2.3.post0.dev0+g" + self.sha())
        (self.root / "tracked.txt").write_text("two\n", encoding="utf-8")
        self.assertEqual(self.version()[0], "1.2.3.post0.dev0+g" + self.sha() + ".dirty")

    def test_untagged_history_uses_the_commit(self) -> None:
        self.git("tag", "release-candidate")  # does not match vX.Y.Z
        self.assertEqual(self.version()[0], "0.0.0.dev0+g" + self.sha())

    def test_non_canonical_release_tag_requires_an_explicit_version(self) -> None:
        self.git("tag", "v1.2.3-snapshot")
        with self.assertRaisesRegex(backend.BuildError, "not a canonical vX.Y.Z.*PAIRMUX_VERSION"):
            self.version()

    def test_tree_without_its_own_git_metadata_is_unknown(self) -> None:
        nested = self.root / "vendored" / "pairmux"
        nested.mkdir(parents=True)
        self.git("tag", "v1.2.3")
        with mock.patch.dict(os.environ, self.git_env):
            self.assertEqual(
                backend.resolve_version(nested, environ={}),
                (backend.UNKNOWN_VERSION, backend.UNKNOWN_VERSION, "no usable git metadata"),
            )

    def test_missing_git_is_unknown(self) -> None:
        with mock.patch.object(backend.shutil, "which", return_value=None):
            self.assertEqual(self.version()[0], backend.UNKNOWN_VERSION)

    def test_versions_are_valid_wheel_versions(self) -> None:
        self.git("tag", "v0.6.0")
        self.commit("fix: two")
        (self.root / "tracked.txt").write_text("two\n", encoding="utf-8")
        version = self.version()[0]
        self.assertRegex(version, r"^0\.6\.0\.post1\.dev0\+g[0-9a-f]{12}\.dirty$")
        self.assertTrue(backend.PEP440_CANONICAL_RE.fullmatch(version))


class VersionOverrideTest(unittest.TestCase):
    def test_canonical_semver_tags(self) -> None:
        cases = {
            "v1.2.3": ("1.2.3", "1.2.3"),
            "1.2.3": ("1.2.3", "1.2.3"),
            "v1.2.3-rc.1": ("1.2.3rc1", "1.2.3-rc.1"),
            "1.2.3-beta.2": ("1.2.3b2", "1.2.3-beta.2"),
        }
        for raw, (wheel, stamp) in cases.items():
            with self.subTest(raw=raw):
                self.assertEqual(
                    backend.resolve_version(Path("/nonexistent"), {"PAIRMUX_VERSION": raw}),
                    (wheel, stamp, "PAIRMUX_VERSION"),
                )

    def test_canonical_pep440_versions(self) -> None:
        for raw in ("1.2.3.dev4", "1.2.3.post1.dev0+gabc123.dirty", "2.0rc1", "0.7.0.dev0+local"):
            with self.subTest(raw=raw):
                self.assertEqual(
                    backend.resolve_version(Path("/nonexistent"), {"PAIRMUX_VERSION": raw}),
                    (raw, raw, "PAIRMUX_VERSION"),
                )

    def test_rejects_non_canonical_versions(self) -> None:
        for raw in ("V1.2.3", " 1.2.3", "1.2.3 ", "1.2.3-dev", "1.2.3+ABC", "1.2.3+",
                    "1.2.3.DEV1", "01.2.3", "1!1.2.3", "1.2.3-rc1", "latest"):
            with self.subTest(raw=raw):
                with self.assertRaisesRegex(backend.BuildError, "PAIRMUX_VERSION="):
                    backend.resolve_version(Path("/nonexistent"), {"PAIRMUX_VERSION": raw})

    def test_empty_override_is_ignored(self) -> None:
        self.assertEqual(
            backend.resolve_version(Path("/nonexistent"), {"PAIRMUX_VERSION": ""})[0],
            backend.UNKNOWN_VERSION,
        )


class PrebuiltBinaryTest(unittest.TestCase):
    def test_unset_or_empty_means_build_from_source(self) -> None:
        self.assertIsNone(backend.prebuilt_binary({}))
        self.assertIsNone(backend.prebuilt_binary({"PAIRMUX_PREBUILT_BINARY": ""}))

    def test_requires_an_absolute_regular_file(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            binary = Path(td) / "pairmux"
            binary.write_bytes(b"x")
            self.assertEqual(
                backend.prebuilt_binary({"PAIRMUX_PREBUILT_BINARY": os.fspath(binary)}), binary
            )
            cases = (
                ("pairmux", "must be an absolute path"),
                (os.path.join(td, "missing"), "is not a regular file"),
                (td, "is not a regular file"),
            )
            for raw, error in cases:
                with self.subTest(raw=raw):
                    with self.assertRaisesRegex(backend.BuildError, error):
                        backend.prebuilt_binary({"PAIRMUX_PREBUILT_BINARY": raw})


class GoBuildTest(unittest.TestCase):
    def run_go_build(self, goos: str, goarch: str, *, returncode: int = 0, write: bool = True):
        calls = []

        def fake_run(command, **kwargs):
            calls.append((command, kwargs))
            if write:
                Path(command[command.index("-o") + 1]).write_bytes(TARGETS[(goos, goarch)][0])
            return subprocess.CompletedProcess(command, returncode)

        with tempfile.TemporaryDirectory() as td, \
                mock.patch.object(backend.shutil, "which", return_value="/toolchain/go"), \
                mock.patch.object(backend.subprocess, "run", side_effect=fake_run):
            output = backend.go_build(
                Path("/checkout"), goos, goarch, "1.2.3.dev0+gabc", Path(td),
                {"PATH": "/toolchain", "GOFLAGS": "-mod=mod", "CGO_ENABLED": "1"},
            )
            return output, calls

    def test_builds_a_static_stamped_binary_for_the_host(self) -> None:
        for goos, goarch in TARGETS:
            with self.subTest(target=(goos, goarch)):
                output, calls = self.run_go_build(goos, goarch)
                self.assertEqual(output.name, "pairmux.exe" if goos == "windows" else "pairmux")
                [(command, kwargs)] = calls
                self.assertEqual(command, [
                    "/toolchain/go", "build", "-trimpath", "-buildvcs=false", "-ldflags",
                    "-s -w -X github.com/treeleaves30760/pairmux/internal/version.Version="
                    "1.2.3.dev0+gabc",
                    "-o", os.fspath(output), "./cmd/pairmux",
                ])
                self.assertEqual(kwargs["cwd"], os.fspath(Path("/checkout")))
                env = kwargs["env"]
                self.assertEqual((env["CGO_ENABLED"], env["GOOS"], env["GOARCH"]),
                                 ("0", goos, goarch))
                self.assertEqual(env["GOFLAGS"], "-mod=mod")  # the user's Go config is kept
                self.assertIs(kwargs["check"], False)

    def test_missing_go_explains_both_ways_out(self) -> None:
        with mock.patch.object(backend.shutil, "which", return_value=None):
            with self.assertRaises(backend.BuildError) as caught:
                backend.go_build(Path("/checkout"), "linux", "amd64", "1", Path("/tmp"), {})
        message = str(caught.exception)
        self.assertIn("install Go 1.25+ from https://go.dev/dl/", message.replace("Install", "install"))
        self.assertIn("GitHub Releases", message)
        self.assertIn("PAIRMUX_PREBUILT_BINARY", message)

    def test_failed_build_points_at_prebuilt_binaries(self) -> None:
        with self.assertRaisesRegex(backend.BuildError, "go build failed \\(exit 2.*PAIRMUX_PREBUILT_BINARY"):
            self.run_go_build("linux", "amd64", returncode=2)

    def test_success_without_output_fails(self) -> None:
        with self.assertRaisesRegex(backend.BuildError, "wrote no"):
            self.run_go_build("linux", "amd64", write=False)


class BuildWheelTest(unittest.TestCase):
    def build(self, goos: str, goarch: str, environ: dict, metadata_directory=None,
              out: Path = None):
        def fake_go_build(root, build_goos, build_goarch, stamp, out_dir, env):
            self.assertEqual((build_goos, build_goarch), (goos, goarch))
            self.stamps.append(stamp)
            binary = out_dir / build_wheels.binary_name(goos)
            binary.write_bytes(TARGETS[(goos, goarch)][0])
            return binary

        self.stamps = []
        with mock.patch.dict(os.environ, environment(**environ), clear=True), \
                mock.patch.object(backend, "host_target", return_value=(goos, goarch)), \
                mock.patch.object(backend, "go_build", side_effect=fake_go_build):
            return backend.build_wheel(os.fspath(out), None, metadata_directory)

    def test_builds_a_host_wheel_for_every_supported_target(self) -> None:
        for (goos, goarch), (content, tag) in TARGETS.items():
            with self.subTest(target=(goos, goarch)), tempfile.TemporaryDirectory() as td:
                out = Path(td)
                name = self.build(goos, goarch, {"PAIRMUX_VERSION": "1.2.3.dev4+gabc"}, out=out)
                self.assertEqual(name, "pairmux-1.2.3.dev4+gabc-py3-none-%s.whl" % tag)
                self.assertEqual(self.stamps, ["1.2.3.dev4+gabc"])
                wheel = out / name
                self.assertTrue(build_wheels.check_wheel(wheel))
                with zipfile.ZipFile(wheel) as zf:
                    script = "pairmux-1.2.3.dev4+gabc.data/scripts/" + (
                        "pairmux.exe" if goos == "windows" else "pairmux")
                    self.assertEqual(zf.read(script), content)
                    metadata = zf.read("pairmux-1.2.3.dev4+gabc.dist-info/METADATA")
                    self.assertEqual(metadata, build_wheels.metadata_bytes("1.2.3.dev4+gabc"))
                    wheel_info = zf.read("pairmux-1.2.3.dev4+gabc.dist-info/WHEEL").decode()
                    self.assertIn("Tag: py3-none-%s\n" % tag, wheel_info)
                    self.assertNotIn("manylinux", wheel_info)

    def test_semver_override_stamps_the_release_spelling(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            name = self.build("linux", "arm64", {"PAIRMUX_VERSION": "v2.0.0-rc.1"}, out=Path(td))
        self.assertEqual(name, "pairmux-2.0.0rc1-py3-none-linux_aarch64.whl")
        self.assertEqual(self.stamps, ["2.0.0-rc.1"])

    def test_prebuilt_binary_skips_go_and_is_packaged_verbatim(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            prebuilt = Path(td) / "download" / "pairmux.exe"
            prebuilt.parent.mkdir()
            prebuilt.write_bytes(TARGETS[("windows", "arm64")][0])
            out = Path(td) / "out"
            env = {"PAIRMUX_VERSION": "0.7.0.dev0+local",
                   "PAIRMUX_PREBUILT_BINARY": os.fspath(prebuilt)}
            name = self.build("windows", "arm64", env, out=out)
            self.assertEqual(self.stamps, [])
            with zipfile.ZipFile(out / name) as zf:
                self.assertEqual(
                    zf.read("pairmux-0.7.0.dev0+local.data/scripts/pairmux.exe"),
                    prebuilt.read_bytes(),
                )

    def test_prebuilt_binary_for_another_platform_is_rejected(self) -> None:
        cases = (
            (("windows", "amd64"), ("linux", "amd64"), "not an ELF binary"),
            (("darwin", "arm64"), ("darwin", "amd64"), "this Python is darwin/amd64"),
            (("linux", "arm64"), ("linux", "amd64"), "this Python is linux/amd64"),
        )
        for binary_target, host, error in cases:
            with self.subTest(binary=binary_target, host=host), tempfile.TemporaryDirectory() as td:
                prebuilt = Path(td) / "pairmux"
                prebuilt.write_bytes(TARGETS[binary_target][0])
                out = Path(td) / "out"
                env = {"PAIRMUX_VERSION": "1.0.0.dev0",
                       "PAIRMUX_PREBUILT_BINARY": os.fspath(prebuilt)}
                with self.assertRaisesRegex(backend.BuildError, error):
                    self.build(*host, env, out=out)
                self.assertFalse(out.exists() and any(out.iterdir()))

    def test_prepared_metadata_matches_the_wheel(self) -> None:
        env = {"PAIRMUX_VERSION": "0.7.0.dev3+gabc"}
        with tempfile.TemporaryDirectory() as td, \
                mock.patch.dict(os.environ, environment(**env), clear=True), \
                mock.patch.object(backend, "host_target", return_value=("linux", "amd64")):
            name = backend.prepare_metadata_for_build_wheel(td)
            self.assertEqual(name, "pairmux-0.7.0.dev3+gabc.dist-info")
            dist_info = Path(td) / name
            self.assertEqual(sorted(p.name for p in dist_info.iterdir()), ["METADATA", "WHEEL"])
            self.assertEqual((dist_info / "METADATA").read_bytes(),
                             build_wheels.metadata_bytes("0.7.0.dev3+gabc"))
            metadata = BytesParser(policy=compat32).parsebytes((dist_info / "METADATA").read_bytes())
            self.assertEqual((metadata["Name"], metadata["Version"]), ("pairmux", "0.7.0.dev3+gabc"))
            self.assertIn("Tag: py3-none-linux_x86_64",
                          (dist_info / "WHEEL").read_text(encoding="utf-8"))
        for passed in ("dist-info", "parent"):
            with self.subTest(passed=passed), tempfile.TemporaryDirectory() as td:
                metadata_root = Path(td) / "metadata"
                with mock.patch.dict(os.environ, environment(**env), clear=True), \
                        mock.patch.object(backend, "host_target", return_value=("linux", "amd64")):
                    prepared = backend.prepare_metadata_for_build_wheel(os.fspath(metadata_root))
                directory = metadata_root / prepared if passed == "dist-info" else metadata_root
                out = Path(td) / "out"
                name = self.build("linux", "amd64", env, os.fspath(directory), out=out)
                self.assertEqual(name, "pairmux-0.7.0.dev3+gabc-py3-none-linux_x86_64.whl")
                # A later change (for example a new commit) must not produce a
                # wheel whose version differs from the metadata already resolved.
                with self.assertRaisesRegex(backend.BuildError, "version changed from 0.7.0.dev3"):
                    self.build("linux", "amd64", {"PAIRMUX_VERSION": "0.7.0.dev4+gabc"},
                               os.fspath(directory), out=out)

    def test_release_shaped_override_warns(self) -> None:
        with tempfile.TemporaryDirectory() as td, \
                mock.patch.object(backend, "_log") as log:
            self.build("darwin", "arm64", {"PAIRMUX_VERSION": "v0.7.0"}, out=Path(td))
        messages = " ".join(call.args[0] for call in log.call_args_list)
        self.assertIn("release-shaped", messages)


class SourceInstallTest(unittest.TestCase):
    """Real frontend -> backend -> go build -> install, offline.

    The pip case runs whenever go and git are available (set
    PAIRMUX_TEST_SOURCE_INSTALL=0 to skip). The uv case runs when
    PAIRMUX_TEST_UV names a uv executable (a path or a command on PATH). With
    PAIRMUX_TEST_SOURCE_INSTALL=1, as in CI, a missing tool is a failure rather
    than a skip, so the job cannot pass without running both.
    """

    def requirement(self, condition: bool, reason: str) -> None:
        if condition:
            return
        if os.environ.get("PAIRMUX_TEST_SOURCE_INSTALL") == "1":
            self.fail(reason)
        self.skipTest(reason)

    def require_toolchain(self) -> None:
        if os.environ.get("PAIRMUX_TEST_SOURCE_INSTALL") == "0":
            self.skipTest("PAIRMUX_TEST_SOURCE_INSTALL=0")
        self.requirement(shutil.which("go") is not None, "go is not installed")
        self.requirement(shutil.which("git") is not None, "git is not installed")

    def test_uv_tool_installs_this_checkout_offline(self) -> None:
        self.require_toolchain()
        requested = os.environ.get("PAIRMUX_TEST_UV", "")
        self.requirement(bool(requested), "PAIRMUX_TEST_UV is not set")
        uv = shutil.which(requested)
        self.requirement(uv is not None, "PAIRMUX_TEST_UV=%r is not executable" % requested)
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            # Like the installers: no inherited uv settings, no config files,
            # no index, no Python downloads; everything under this directory.
            env = {key: value for key, value in environment().items()
                   if not key.startswith("UV_")}
            env.update(UV_TOOL_DIR=os.fspath(root / "tools"),
                       UV_TOOL_BIN_DIR=os.fspath(root / "bin"),
                       UV_CACHE_DIR=os.fspath(root / "cache"))
            expected = backend.resolve_version(backend.REPO_ROOT, env)[0]
            installed = subprocess.run(
                [uv, "tool", "install", "--offline", "--no-config",
                 "--no-python-downloads", "--python", sys.executable,
                 os.fspath(backend.REPO_ROOT)],
                env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=900,
            )
            self.assertEqual(installed.returncode, 0, installed.stdout.decode(errors="replace"))
            executable = root / "bin" / ("pairmux.exe" if os.name == "nt" else "pairmux")
            reported = subprocess.run([os.fspath(executable), "version"], env=env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
            self.assertEqual(reported.returncode, 0, reported.stderr.decode(errors="replace"))
            self.assertEqual(reported.stdout.decode().strip(), expected)
            # A development build is never an update target, even when it is a
            # persistent uv tool installation.
            update = subprocess.run([os.fspath(executable), "--json", "update"], env=env,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
            self.assertNotEqual(update.returncode, 0)
            self.assertIn('"code":"E_UPDATE"', update.stdout.decode(errors="replace"))

    def test_pip_installs_this_checkout_offline(self) -> None:
        self.require_toolchain()
        env = environment(PIP_DISABLE_PIP_VERSION_CHECK="1")
        with tempfile.TemporaryDirectory() as td:
            venv = Path(td) / "venv"
            created = subprocess.run([sys.executable, "-m", "venv", os.fspath(venv)],
                                     stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            self.requirement(created.returncode == 0,
                             "venv/ensurepip is unavailable: %s" % created.stdout.decode(errors="replace"))
            bindir = venv / ("Scripts" if os.name == "nt" else "bin")
            python = bindir / ("python.exe" if os.name == "nt" else "python")
            expected = backend.resolve_version(backend.REPO_ROOT, env)[0]
            installed = subprocess.run(
                [os.fspath(python), "-m", "pip", "--isolated", "install", "--no-index",
                 "--no-deps", "--no-cache-dir", os.fspath(backend.REPO_ROOT)],
                env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=900,
            )
            self.assertEqual(installed.returncode, 0, installed.stdout.decode(errors="replace"))
            executable = bindir / ("pairmux.exe" if os.name == "nt" else "pairmux")
            reported = subprocess.run([os.fspath(executable), "version"], env=env,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
            self.assertEqual(reported.returncode, 0, reported.stderr.decode(errors="replace"))
            self.assertEqual(reported.stdout.decode().strip(), expected)
            self.assertIn(".dev", expected)
            self.assertIn("+", expected)


if __name__ == "__main__":
    unittest.main()
