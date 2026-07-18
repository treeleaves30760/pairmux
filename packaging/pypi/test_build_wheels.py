import os
import struct
import tempfile
import unittest
import zipfile
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

    def test_scan_dist_accepts_goreleaser_variants(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            dist = Path(td)
            expected = {
                "pairmux_darwin_arm64_v8.0": ("darwin", "arm64"),
                "pairmux_darwin_amd64_v1": ("darwin", "amd64"),
                "pairmux_linux_arm64_v8.0": ("linux", "arm64"),
                "pairmux_linux_amd64_v1": ("linux", "amd64"),
            }
            for dirname in expected:
                self.make_binary(dist / dirname / "pairmux")
            self.make_binary(dist / "pairmux_windows_amd64_v1" / "pairmux")

            found = {
                binary.parent.name: platform
                for binary, platform in build_wheels.scan_dist_dir(dist)
            }
            self.assertEqual(found, expected)

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
                script = next(name for name in zf.namelist() if ".data/scripts/" in name)
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
            self.assertEqual(len(list(out.glob("*.whl"))), 4)

    def test_validate_version_only_needs_no_binary(self) -> None:
        self.assertEqual(
            build_wheels.main(
                ["--version", "v2.0.0-rc.3", "--validate-version-only"]
            ),
            0,
        )


if __name__ == "__main__":
    unittest.main()
