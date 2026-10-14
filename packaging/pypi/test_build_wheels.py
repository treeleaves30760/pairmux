import os
import re
import struct
import tempfile
import unittest
import zipfile
from email.parser import BytesParser
from email.policy import compat32
from pathlib import Path

import build_wheels


class BuildWheelsTest(unittest.TestCase):
    def make_binary(self, path: Path, content: bytes = b"test-binary") -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        path.chmod(0o755)

    def make_macho(self, path: Path, cpu: int, major: int = 12, minor: int = 0) -> None:
        minos = (major << 16) | (minor << 8)
        command = struct.pack("<IIIIII", 0x32, 24, 1, minos, minos, 0)
        header = struct.pack("<IiiIIIII", 0xFEEDFACF, cpu, 0, 2, 1, len(command), 0, 0)
        self.make_binary(path, header + command + b"test-macho-payload")

    def make_elf(self, path: Path, machine: int) -> None:
        ident = b"\x7fELF" + bytes((2, 1, 1, 0, 0)) + bytes(7)
        header = struct.pack(
            "<16sHHIQQQIHHHHHH",
            ident, 2, machine, 1, 0, 0, 0, 0, 64, 0, 0, 0, 0, 0,
        )
        self.make_binary(path, header + b"test-elf-payload")

    @staticmethod
    def pe_bytes(
        machine: int = 0x8664, *, magic: int = 0x20B, subsystem: int = 3,
        characteristics: int = 0x0022, optional_size: int = 240,
        dos_magic: bytes = b"MZ", signature: bytes = b"PE\0\0", pe_offset: int = 64,
    ) -> bytes:
        dos = bytearray(64)
        dos[:2] = dos_magic
        struct.pack_into("<I", dos, 0x3C, pe_offset)
        optional = bytearray(optional_size)
        if optional_size >= 2:
            struct.pack_into("<H", optional, 0, magic)
        if optional_size >= 70:
            struct.pack_into("<H", optional, 68, subsystem)
        coff = struct.pack(
            "<HHIIIHH", machine, 1, 0, 0, 0, optional_size, characteristics
        )
        padding = bytes(max(0, pe_offset - len(dos)))
        return bytes(dos) + padding + signature + coff + bytes(optional)

    def make_pe(self, path: Path, machine: int, **fields) -> None:
        self.make_binary(path, self.pe_bytes(machine, **fields) + b"test-pe-payload")

    def test_normalize_version(self) -> None:
        self.assertEqual(build_wheels.normalize_version("v1.2.3-rc.1"), "1.2.3rc1")
        self.assertEqual(build_wheels.normalize_version("1.2.3-beta.2"), "1.2.3b2")
        self.assertEqual(build_wheels.normalize_version("1.2.3-alpha.1"), "1.2.3a1")
        self.assertEqual(build_wheels.normalize_version("v0.0.0"), "0.0.0")
        for invalid in (
            "", " 1.2.3", "1.2.3 ", "V1.2.3", "v1", "v1.2",
            "v01.2.3", "v1.02.3", "v1.2.003", "v1.2.3rc1",
            "v1.2.3-rc.01", "1.2.3-alpha1", "1.2.3-dev",
            "release-1.2.3", "1.2.3+local",
        ):
            with self.subTest(invalid=invalid):
                with self.assertRaises(ValueError):
                    build_wheels.normalize_version(invalid)

    def test_metadata_uses_the_markdown_description_source(self) -> None:
        metadata = BytesParser(policy=compat32).parsebytes(
            build_wheels.metadata_bytes("1.2.3")
        )

        self.assertTrue(build_wheels.DESCRIPTION_PATH.is_absolute())
        self.assertEqual(
            metadata["Description-Content-Type"],
            "text/markdown; charset=UTF-8; variant=GFM",
        )
        self.assertEqual(
            metadata["Summary"],
            "Blocking terminal control for AI agents on macOS, Linux and Windows, "
            "with captured logs and live human handoff",
        )
        self.assertNotIn("tmux", metadata["Summary"])
        classifiers = metadata.get_all("Classifier")
        for classifier in (
            "Operating System :: MacOS",
            "Operating System :: Microsoft :: Windows",
            "Operating System :: POSIX :: Linux",
        ):
            with self.subTest(classifier=classifier):
                self.assertIn(classifier, classifiers)
        self.assertEqual(
            metadata.get_all("Project-URL"),
            [
                "Homepage, https://pairmux.treeleaves30760.com",
                "Repository, https://github.com/treeleaves30760/pairmux",
                "Documentation, https://pairmux-docs.treeleaves30760.com/",
                "Changelog, "
                "https://github.com/treeleaves30760/pairmux/blob/main/ChangeLog.md",
                "Issues, https://github.com/treeleaves30760/pairmux/issues",
            ],
        )
        self.assertEqual(metadata["Requires-Python"], ">=3.9")
        # tmux is optional now (built-in multiplexer), so it is not declared
        # as an external requirement of any wheel.
        self.assertIsNone(metadata.get_all("Requires-External"))
        self.assertIsNone(metadata.get_all("Requires-Dist"))
        self.assertEqual(
            metadata["Keywords"],
            "ai agents, terminal, pty, conpty, tmux, cli, mcp, developer tools",
        )
        self.assertEqual(
            metadata.get_payload(decode=True).decode("utf-8"),
            build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8").strip("\n")
            + "\n",
        )

    def test_markdown_description_uses_portable_links_and_real_json_commands(
        self,
    ) -> None:
        description = build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8")
        destinations = re.findall(r"\]\(([^)]+)\)", description)

        self.assertTrue(destinations)
        self.assertTrue(
            all(destination.startswith("https://") for destination in destinations),
            destinations,
        )
        self.assertIn("pairmux --json new --name demo", description)
        self.assertIn("pairmux --json run demo", description)

    def test_markdown_description_distinguishes_install_modes_and_sources(self) -> None:
        description = build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8")
        for command in (
            "uv tool install pairmux",
            "pairmux version",
            "pairmux doctor",
            "pairmux update",
            "uvx pairmux version",
            "uvx pairmux doctor",
            "curl -fsSL https://pairmux.treeleaves30760.com/install.sh | bash",
        ):
            with self.subTest(command=command):
                self.assertIn(command, description)
        self.assertIn("temporary tool environment", description)
        self.assertIn("persistent", description)
        self.assertIn("retains your environment and uv configuration", description)
        self.assertIn("https://pypi.org/project/pairmux/", description)
        self.assertIn("https://pypi.org/simple", description)
        self.assertIn("clears all inherited `UV_*` settings", description)
        self.assertIn("no persistent cache or source builds", description)
        self.assertIn("Astral upstream", description)
        self.assertNotIn("pairmux-apt", description)
        self.assertNotIn(".deb", description)
        self.assertNotIn("apt install pairmux", description)
        self.assertNotIn("https://treeleaves30760.github.io/pairmux/", description)

    def test_markdown_description_treats_tmux_as_optional_and_windows_as_native(
        self,
    ) -> None:
        description = build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8")
        for text in (
            "**tmux is optional.**",
            "uses tmux when it is installed",
            "tmux 3.2 or newer, installed separately",
            "built-in multiplexer",
            "Windows always uses the built-in multiplexer",
            "`PAIRMUX_BACKEND=builtin` or `PAIRMUX_BACKEND=tmux`",
            "Windows 10 version 1809+ (ConPTY)",
            "irm https://pairmux.treeleaves30760.com/install.ps1 | iex",
            "$env:PAIRMUX_INSTALL_TARGET = 'wsl'",
            "WSL remains supported",
        ):
            with self.subTest(text=text):
                self.assertIn(text, description)
        for text in (
            "no native Windows wheel",
            "layer over tmux",
            "does not implement another PTY",
            "tmux 3.2 or newer, installed separately** with",
        ):
            with self.subTest(absent=text):
                self.assertNotIn(text, description)

    def test_markdown_description_documents_source_checkout_install(self) -> None:
        description = build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8")
        for text in (
            "### From a source checkout, without PyPI",
            "`python -m pip install .`",
            "`uv tool install .`",
            "Go 1.25+",
            "`PAIRMUX_PREBUILT_BINARY`",
            "development version and are not\n`pairmux update` targets",
            "packaging/pypi/README.md#install-from-a-source-checkout",
        ):
            with self.subTest(text=text):
                self.assertIn(text, description)

    def test_markdown_description_prefers_persistent_install_then_update(self) -> None:
        description = build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8")
        self.assertIn(
            "```bash\nuv tool install pairmux\npairmux version\npairmux doctor\n```",
            description,
        )
        self.assertLess(
            description.index("uv tool install pairmux"),
            description.index("pairmux update"),
        )
        self.assertLess(
            description.index("pairmux update"),
            description.index("uvx pairmux version"),
        )
        self.assertIn("### Optional temporary run with uvx", description)
        self.assertIn("`uvx` remains supported", description)

    def test_markdown_description_limits_update_to_v060_persistent_uv(self) -> None:
        description = build_wheels.DESCRIPTION_PATH.read_text(encoding="utf-8")
        for text in (
            "### Update (v0.6.0+)",
            "From **v0.6.0** onward",
            "verified persistent uv tool installation",
            "**v0.5.3 and older have no `update` command.**",
            "uv tool install --upgrade pairmux",
            "when available",
            "Temporary runs cannot use `pairmux update`",
            "latest compatible stable",
            "`updated`",
            "`refreshed`",
            "`E_UPDATE`",
            "without a guaranteed rollback",
            "Homebrew, RPM, pipx/pip, manual/development installs, or temporary uvx",
            "does not touch tmux",
            "global `--json` is supported",
        ):
            with self.subTest(text=text):
                self.assertIn(text, description)

    def test_scan_dist_accepts_goreleaser_variants(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            dist = Path(td)
            expected = {
                "pairmux_darwin_arm64_v8.0": ("darwin", "arm64"),
                "pairmux_darwin_amd64_v1": ("darwin", "amd64"),
                "pairmux_linux_arm64_v8.0": ("linux", "arm64"),
                "pairmux_linux_amd64_v1": ("linux", "amd64"),
                "pairmux_windows_amd64_v1": ("windows", "amd64"),
                "pairmux_windows_arm64_v8.0": ("windows", "arm64"),
            }
            for dirname, (goos, _) in expected.items():
                name = "pairmux.exe" if goos == "windows" else "pairmux"
                self.make_binary(dist / dirname / name)
            # Unsupported targets and extension-less Windows files are ignored.
            self.make_binary(dist / "pairmux_freebsd_amd64_v1" / "pairmux")
            self.make_binary(dist / "pairmux_windows_386_sse2" / "pairmux.exe")
            self.make_binary(dist / "other_windows_amd64_v2" / "pairmux")

            found = {
                binary.parent.name: platform
                for binary, platform in build_wheels.scan_dist_dir(dist)
            }
            self.assertEqual(found, expected)
            for binary, (goos, _) in build_wheels.scan_dist_dir(dist):
                with self.subTest(binary=binary):
                    self.assertEqual(
                        binary.name, "pairmux.exe" if goos == "windows" else "pairmux"
                    )

    def test_scan_dist_rejects_duplicate_windows_platform(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            dist = Path(td)
            self.make_binary(dist / "pairmux_windows_arm64_v8.0" / "pairmux.exe")
            self.make_binary(dist / "another_windows_arm64_v9.0" / "pairmux.exe")
            with self.assertRaisesRegex(ValueError, "duplicate windows/arm64 binaries"):
                build_wheels.scan_dist_dir(dist)

    def test_scan_dist_rejects_duplicate_platform(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            dist = Path(td)
            self.make_binary(dist / "pairmux_linux_amd64_v1" / "pairmux")
            self.make_binary(dist / "another_linux_amd64_v2" / "pairmux")
            with self.assertRaisesRegex(ValueError, "duplicate linux/amd64 binaries"):
                build_wheels.scan_dist_dir(dist)

    def test_build_one_writes_verified_executable_wheel(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            binary = root / "pairmux"
            self.make_macho(binary, 0x0100000C)

            wheel = build_wheels.build_one(
                "0.1.0", binary, "macosx_12_0_arm64", root / "wheels"
            )

            self.assertTrue(build_wheels.check_wheel(wheel))
            with zipfile.ZipFile(wheel) as zf:
                names = zf.namelist()
                self.assertEqual(len(names), 4)
                self.assertFalse(any(name.endswith(".py") for name in names))
                script = next(name for name in names if ".data/scripts/" in name)
                mode = (zf.getinfo(script).external_attr >> 16) & 0xFFFF
                self.assertTrue(mode & 0o111)
                self.assertEqual(zf.read(script), binary.read_bytes())

    def test_build_one_rejects_mislabeled_macos_target(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            binary = root / "pairmux"
            self.make_macho(binary, 0x0100000C, major=13)
            with self.assertRaisesRegex(ValueError, "requires macosx_13_0_arm64"):
                build_wheels.build_one(
                    "0.1.0", binary, "macosx_12_0_arm64", root / "wheels"
                )

    def test_build_one_accepts_matching_linux_targets(self) -> None:
        cases = (
            (62, "manylinux_2_17_x86_64.manylinux2014_x86_64"),
            (183, "manylinux_2_17_aarch64.manylinux2014_aarch64"),
        )
        for machine, platform in cases:
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                binary = root / "pairmux"
                self.make_elf(binary, machine)
                wheel = build_wheels.build_one(
                    "0.1.0", binary, platform, root / "wheels"
                )
                self.assertTrue(build_wheels.check_wheel(wheel))

    def test_build_one_rejects_swapped_linux_targets(self) -> None:
        cases = (
            (62, "manylinux_2_17_aarch64.manylinux2014_aarch64", "x86_64"),
            (183, "manylinux_2_17_x86_64.manylinux2014_x86_64", "aarch64"),
        )
        for machine, platform, actual in cases:
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                binary = root / "pairmux"
                self.make_elf(binary, machine)
                with self.assertRaisesRegex(
                    ValueError, "ELF architecture is " + actual
                ):
                    build_wheels.build_one(
                        "0.1.0", binary, platform, root / "wheels"
                    )

    def test_build_one_writes_windows_exe_script(self) -> None:
        cases = ((0x8664, "win_amd64"), (0xAA64, "win_arm64"))
        for machine, platform in cases:
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                binary = root / "pairmux.exe"
                self.make_pe(binary, machine)
                wheel = build_wheels.build_one(
                    "0.1.0", binary, platform, root / "wheels"
                )
                self.assertEqual(
                    wheel.name, "pairmux-0.1.0-py3-none-%s.whl" % platform
                )
                self.assertTrue(build_wheels.check_wheel(wheel))
                with zipfile.ZipFile(wheel) as zf:
                    scripts = [n for n in zf.namelist() if ".data/scripts/" in n]
                    self.assertEqual(
                        scripts, ["pairmux-0.1.0.data/scripts/pairmux.exe"]
                    )
                    self.assertEqual(zf.read(scripts[0]), binary.read_bytes())
                    wheel_meta = next(
                        n for n in zf.namelist() if n.endswith(".dist-info/WHEEL")
                    )
                    self.assertIn(
                        "Tag: py3-none-%s\n" % platform,
                        zf.read(wheel_meta).decode("utf-8"),
                    )

    def test_build_one_rejects_swapped_windows_targets(self) -> None:
        cases = (
            (0x8664, "win_arm64", "amd64"),
            (0xAA64, "win_amd64", "arm64"),
        )
        for machine, platform, actual in cases:
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                binary = root / "pairmux.exe"
                self.make_pe(binary, machine)
                with self.assertRaisesRegex(ValueError, "PE machine is " + actual):
                    build_wheels.build_one("0.1.0", binary, platform, root / "wheels")
                self.assertFalse((root / "wheels").exists())

    def test_build_one_rejects_non_pe_binaries_under_windows_tags(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            elf = root / "elf"
            macho = root / "macho"
            self.make_elf(elf, 62)
            self.make_macho(macho, 0x01000007)
            for binary in (elf, macho):
                with self.subTest(binary=binary.name):
                    with self.assertRaisesRegex(ValueError, "no MZ header"):
                        build_wheels.build_one(
                            "0.1.0", binary, "win_amd64", root / "wheels"
                        )
            pe = root / "pairmux.exe"
            self.make_pe(pe, 0x8664)
            with self.assertRaisesRegex(ValueError, "not a little-endian 64-bit Mach-O"):
                build_wheels.build_one("0.1.0", pe, "macosx_12_0_x86_64", root / "wheels")
            with self.assertRaisesRegex(ValueError, "not an ELF binary"):
                build_wheels.build_one(
                    "0.1.0", pe, "manylinux_2_17_x86_64.manylinux2014_x86_64",
                    root / "wheels",
                )

    def test_windows_pe_arch_rejects_malformed_headers(self) -> None:
        valid = self.pe_bytes()
        cases = {
            "short": (valid[:63], "too short"),
            "dos-magic": (self.pe_bytes(dos_magic=b"ZM"), "no MZ header"),
            "offset-low": (self.pe_bytes(pe_offset=32), "unreasonable PE header offset"),
            "offset-high": (valid[:0x3C] + struct.pack("<I", 0x7FFFFFFF) + valid[0x40:],
                            "unreasonable PE header offset"),
            "truncated-coff": (valid[:64 + 10], "truncated PE header"),
            "signature": (self.pe_bytes(signature=b"NE\0\0"), "no PE signature"),
            "optional-short": (self.pe_bytes(optional_size=60), "optional header too short"),
            "optional-truncated": (valid[:64 + 24 + 100], "truncated PE optional header"),
            "pe32": (self.pe_bytes(magic=0x10B), "not a 64-bit PE32\\+ image"),
            "not-executable": (self.pe_bytes(characteristics=0x0020), "not marked as a PE executable"),
            "dll": (self.pe_bytes(characteristics=0x2022), "is a DLL"),
            "gui": (self.pe_bytes(subsystem=2), "subsystem 2, not a console"),
            "machine-x86": (self.pe_bytes(machine=0x14C), "machine type 0x14c"),
            "machine-arm32": (self.pe_bytes(machine=0x1C4), "machine type 0x1c4"),
        }
        for name, (content, error) in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as td:
                candidate = Path(td) / "pairmux.exe"
                self.make_binary(candidate, content)
                with self.assertRaisesRegex(ValueError, error):
                    build_wheels.windows_pe_arch(candidate)

    def test_windows_pe_arch_accepts_padded_header_offset(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            candidate = Path(td) / "pairmux.exe"
            self.make_pe(candidate, 0xAA64, pe_offset=0x80)
            self.assertEqual(build_wheels.windows_pe_arch(candidate), "arm64")

    def test_validate_binary_rejects_unknown_platform_tags(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            binary = Path(td) / "pairmux"
            self.make_elf(binary, 62)
            for platform in ("any", "linux_i686", "musllinux_1_2_x86_64", "win32"):
                with self.subTest(platform=platform):
                    with self.assertRaisesRegex(ValueError, "no native header check"):
                        build_wheels.validate_binary(binary, platform)

    def test_validate_binary_accepts_host_only_linux_tags(self) -> None:
        cases = ((62, "linux_x86_64"), (183, "linux_aarch64"))
        for machine, platform in cases:
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as td:
                binary = Path(td) / "pairmux"
                self.make_elf(binary, machine)
                build_wheels.validate_binary(binary, platform)
                other = "linux_aarch64" if machine == 62 else "linux_x86_64"
                with self.assertRaisesRegex(ValueError, "ELF architecture"):
                    build_wheels.validate_binary(binary, other)

    def test_release_platforms_never_use_host_only_tags(self) -> None:
        for platform in build_wheels.PLATFORM_TAGS.values():
            with self.subTest(platform=platform):
                self.assertNotIn(platform, build_wheels.LOCAL_LINUX_PLATFORM_ARCHES)
                self.assertFalse(platform.startswith("linux_"))

    def test_linux_elf_arch_rejects_malformed_headers(self) -> None:
        binary = Path("pairmux")
        ident = b"\x7fELF" + bytes((2, 1, 1, 0, 0)) + bytes(7)

        def elf_header(
            *, file_type: int = 2, machine: int = 62, version: int = 1,
            header_size: int = 64,
        ) -> bytes:
            return struct.pack(
                "<16sHHIQQQIHHHHHH",
                ident, file_type, machine, version, 0, 0, 0, 0,
                header_size, 0, 0, 0, 0, 0,
            )

        valid = bytearray(elf_header())
        cases = {
            "short": (bytes(valid[:63]), "too short"),
            "magic": (b"NOPE" + bytes(valid[4:]), "not an ELF"),
            "class": (bytes(valid[:4] + b"\x01" + valid[5:]), "not a 64-bit"),
            "endian": (bytes(valid[:5] + b"\x02" + valid[6:]), "not a little-endian"),
            "version": (bytes(valid[:6] + b"\x02" + valid[7:]), "identification version 2"),
            "header-version": (elf_header(version=2), "header version 2"),
            "file-type": (elf_header(file_type=1), "not an ELF executable"),
            "header-size": (elf_header(header_size=63), "header size 63"),
            "machine": (elf_header(machine=0), "machine type 0"),
        }
        for name, (content, error) in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as td:
                candidate = Path(td) / binary
                self.make_binary(candidate, content)
                with self.assertRaisesRegex(ValueError, error):
                    build_wheels.linux_elf_arch(candidate)

    def test_main_builds_all_scanned_platforms(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = root / "goreleaser"
            out = root / "wheels"
            self.make_macho(
                dist / "pairmux_darwin_arm64_v8.0" / "pairmux", 0x0100000C
            )
            self.make_macho(
                dist / "pairmux_darwin_amd64_v1" / "pairmux", 0x01000007
            )
            self.make_elf(
                dist / "pairmux_linux_arm64_v8.0" / "pairmux", 183
            )
            self.make_elf(
                dist / "pairmux_linux_amd64_v1" / "pairmux", 62
            )
            self.make_pe(
                dist / "pairmux_windows_amd64_v1" / "pairmux.exe", 0x8664
            )
            self.make_pe(
                dist / "pairmux_windows_arm64_v8.0" / "pairmux.exe", 0xAA64
            )

            rc = build_wheels.main(
                [
                    "--version",
                    "v0.2.0",
                    "--dist-dir",
                    os.fspath(dist),
                    "--out-dir",
                    os.fspath(out),
                    "--check",
                ]
            )

            self.assertEqual(rc, 0)
            wheels = list(out.glob("*.whl"))
            self.assertEqual(len(wheels), 6)
            self.assertEqual(
                {wheel.name for wheel in wheels},
                {
                    "pairmux-0.2.0-py3-none-macosx_12_0_arm64.whl",
                    "pairmux-0.2.0-py3-none-macosx_12_0_x86_64.whl",
                    "pairmux-0.2.0-py3-none-manylinux_2_17_aarch64."
                    "manylinux2014_aarch64.whl",
                    "pairmux-0.2.0-py3-none-manylinux_2_17_x86_64."
                    "manylinux2014_x86_64.whl",
                    "pairmux-0.2.0-py3-none-win_amd64.whl",
                    "pairmux-0.2.0-py3-none-win_arm64.whl",
                },
            )

            metadata_blobs = set()
            for wheel in wheels:
                with zipfile.ZipFile(wheel) as zf:
                    metadata_name = next(
                        name
                        for name in zf.namelist()
                        if name.endswith(".dist-info/METADATA")
                    )
                    metadata_blobs.add(zf.read(metadata_name))
            self.assertEqual(len(metadata_blobs), 1)

    def test_main_accepts_explicit_windows_platform_tokens(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            binary = root / "in" / "pairmux.exe"
            self.make_pe(binary, 0xAA64)
            out = root / "wheels"
            rc = build_wheels.main(
                [
                    "--version", "v1.0.0", "--binary", os.fspath(binary),
                    "--platform", "windows_arm64", "--out-dir", os.fspath(out),
                    "--check",
                ]
            )
            self.assertEqual(rc, 0)
            self.assertEqual(
                [wheel.name for wheel in out.glob("*.whl")],
                ["pairmux-1.0.0-py3-none-win_arm64.whl"],
            )
            with self.assertRaises(SystemExit):
                build_wheels.main(
                    ["--version", "v1.0.0", "--binary", os.fspath(binary),
                     "--platform", "windows_386"]
                )

    def test_validate_version_only_needs_no_binary(self) -> None:
        self.assertEqual(
            build_wheels.main(
                ["--version", "v2.0.0-rc.3", "--validate-version-only"]
            ),
            0,
        )


if __name__ == "__main__":
    unittest.main()
