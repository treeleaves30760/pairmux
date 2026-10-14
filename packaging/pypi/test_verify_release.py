import hashlib
import io
import json
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path

import verify_release


class VerifyReleaseTest(unittest.TestCase):
    def make_fixture(self, root: Path, *, tag: str = "v1.2.3", commit: str = "abc123"):
        dist = root / "dist"
        dist.mkdir()
        version = tag.removeprefix("v")
        entries = [{"type": "Metadata", "name": "metadata.json", "path": "dist/metadata.json"}]
        public = []

        for os_name, arch in verify_release.TARGETS:
            content = ("binary-%s-%s" % (os_name, arch)).encode()
            executable = "pairmux.exe" if os_name == "windows" else "pairmux"
            binary = dist / ("pairmux_%s_%s_v1" % (os_name, arch)) / executable
            binary.parent.mkdir()
            binary.write_bytes(content)
            binary.chmod(0o755)
            entries.append({
                "type": "Binary", "name": executable, "path": str(binary),
                "goos": os_name, "goarch": arch,
            })

            members = (
                (executable, content, 0o755),
                ("LICENSE", b"license", 0o644),
                ("README.md", b"readme", 0o644),
                ("man/pairmux.1", b"man", 0o644),
            )
            if os_name == "windows":
                name = "pairmux_%s_%s_%s.zip" % (version, os_name, arch)
                archive = dist / name
                self.write_zip(archive, members)
                archive_format = "zip"
            else:
                name = "pairmux_%s_%s_%s.tar.gz" % (version, os_name, arch)
                archive = dist / name
                with tarfile.open(archive, "w:gz") as tf:
                    for member_name, data, mode in members:
                        info = tarfile.TarInfo(member_name)
                        info.size = len(data)
                        info.mode = mode
                        tf.addfile(info, io.BytesIO(data))
                archive_format = "tar.gz"
            entries.append({
                "type": "Archive", "name": name, "path": str(archive),
                "goos": os_name, "goarch": arch, "extra": {"Format": archive_format},
            })
            public.append(archive)

        for arch in ("amd64", "arm64"):
            for fmt in verify_release.PACKAGE_FORMATS:
                name = "pairmux_%s_linux_%s.%s" % (version, arch, fmt)
                package = dist / name
                package.write_bytes(("package-%s-%s" % (arch, fmt)).encode())
                entries.append({
                    "type": "Linux Package", "name": name, "path": str(package),
                    "goos": "linux", "goarch": arch, "extra": {"Format": fmt},
                })
                public.append(package)

        checksums = dist / "checksums.txt"
        checksums.write_text("".join(
            "%s  %s\n" % (hashlib.sha256(path.read_bytes()).hexdigest(), path.name)
            for path in sorted(public)
        ), encoding="utf-8")
        entries.append({"type": "Checksum", "name": "checksums.txt", "path": str(checksums)})

        cask = dist / "homebrew" / "Casks" / "pairmux.rb"
        cask.parent.mkdir(parents=True)
        cask.write_text(self.cask_text(version), encoding="utf-8")
        entries.append({"type": "Homebrew Cask", "name": "pairmux.rb", "path": str(cask)})
        (dist / "metadata.json").write_text(json.dumps({
            "tag": tag, "version": version, "commit": commit,
        }), encoding="utf-8")
        (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
        return dist

    @staticmethod
    def write_zip(path: Path, members, *, unix: bool = True) -> None:
        with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as zf:
            for member_name, data, mode in members:
                info = zipfile.ZipInfo(member_name, date_time=(2026, 1, 1, 0, 0, 0))
                if unix:
                    info.create_system = 3
                    info.external_attr = (0o100000 | mode) << 16
                zf.writestr(info, data)

    @staticmethod
    def cask_text(version: str) -> str:
        # Shape of GoReleaser's rendered cask: tarball URLs interpolate the
        # version, and only darwin/linux archives appear.
        base = "https://github.com/treeleaves30760/pairmux/releases/download/v%s/" % version
        urls = "".join(
            '  url "%spairmux_#{version}_%s_%s.tar.gz"\n' % (base, os_name, arch)
            for os_name, arch in verify_release.CASK_TARGETS
        )
        return 'cask "pairmux" do\n  version "%s"\n%s  depends_on formula: ["tmux"]\nend\n' % (
            version, urls)

    def rewrite_checksums(self, dist: Path) -> None:
        names = [
            path for path in dist.iterdir()
            if path.is_file() and path.name.endswith((".tar.gz", ".zip", ".rpm"))
        ]
        (dist / "checksums.txt").write_text("".join(
            "%s  %s\n" % (hashlib.sha256(path.read_bytes()).hexdigest(), path.name)
            for path in sorted(names)
        ), encoding="utf-8")

    def test_verifies_and_stages_exact_public_artifacts(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            staged = verify_release.verify_release(
                dist, "v1.2.3", "abc123", root / "staged"
            )
            expected_names = {
                "pairmux_1.2.3_darwin_amd64.tar.gz",
                "pairmux_1.2.3_darwin_arm64.tar.gz",
                "pairmux_1.2.3_linux_amd64.tar.gz",
                "pairmux_1.2.3_linux_arm64.tar.gz",
                "pairmux_1.2.3_windows_amd64.zip",
                "pairmux_1.2.3_windows_arm64.zip",
                "pairmux_1.2.3_linux_amd64.rpm",
                "pairmux_1.2.3_linux_arm64.rpm",
                "checksums.txt",
            }
            self.assertEqual(len(staged), 9)
            self.assertEqual({path.name for path in staged}, expected_names)
            self.assertEqual(
                {path.name for path in (root / "staged").iterdir()}, expected_names
            )
            for path in staged:
                self.assertEqual(path.read_bytes(), (dist / path.name).read_bytes())

    def test_rejects_unexpected_debian_package(self):
        for arch in ("amd64", "arm64"):
            with self.subTest(arch=arch), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                dist = self.make_fixture(root)
                package = dist / ("pairmux_1.2.3_linux_%s.deb" % arch)
                package.write_bytes(b"unexpected-debian-package")
                entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
                entries.append({
                    "type": "Linux Package", "name": package.name, "path": str(package),
                    "goos": "linux", "goarch": arch, "extra": {"Format": "deb"},
                })
                (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
                with (dist / "checksums.txt").open("a", encoding="utf-8") as stream:
                    stream.write("%s  %s\n" % (
                        hashlib.sha256(package.read_bytes()).hexdigest(), package.name
                    ))
                with self.assertRaisesRegex(ValueError, "Linux package targets"):
                    verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")
                self.assertFalse((root / "staged").exists())

    def test_rejects_unexpected_debian_checksum(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            with (dist / "checksums.txt").open("a", encoding="utf-8") as stream:
                stream.write("%s  pairmux_1.2.3_linux_amd64.deb\n" % ("0" * 64))
            with self.assertRaisesRegex(ValueError, "checksum filenames"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")
            self.assertFalse((root / "staged").exists())

    def test_rejects_wrong_commit(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            with self.assertRaisesRegex(ValueError, "metadata commit"):
                verify_release.verify_release(dist, "v1.2.3", "wrong", root / "staged")

    def test_rejects_checksum_omission(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            rows = (dist / "checksums.txt").read_text(encoding="utf-8").splitlines()
            (dist / "checksums.txt").write_text("\n".join(rows[:-1]) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "checksum filenames"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_missing_homebrew_cask(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
            entries = [entry for entry in entries if entry["type"] != "Homebrew Cask"]
            (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "Homebrew Cask"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_unexpected_publishable_type(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
            entries.append({"type": "SBOM", "name": "pairmux.spdx.json", "path": "outside"})
            (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "unexpected artifact types"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_duplicate_homebrew_cask(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
            cask = next(entry for entry in entries if entry["type"] == "Homebrew Cask")
            entries.append(dict(cask))
            (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "Homebrew Cask"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_unexpected_archive_member(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            archive = dist / "pairmux_1.2.3_darwin_amd64.tar.gz"
            with tarfile.open(archive, "w:gz") as tf:
                info = tarfile.TarInfo("../escape")
                info.size = 1
                tf.addfile(info, io.BytesIO(b"x"))
            with self.assertRaisesRegex(ValueError, "unsafe archive member"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_accepts_windows_zip_without_unix_file_type(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            archive = dist / "pairmux_1.2.3_windows_arm64.zip"
            self.write_zip(archive, (
                ("pairmux.exe", b"binary-windows-arm64", 0o755),
                ("LICENSE", b"license", 0o644),
                ("README.md", b"readme", 0o644),
                ("man/pairmux.1", b"man", 0o644),
            ), unix=False)
            self.rewrite_checksums(dist)
            staged = verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")
            self.assertEqual(len(staged), 9)

    def test_rejects_invalid_windows_zip_members(self):
        good = (
            ("pairmux.exe", b"binary-windows-amd64", 0o755),
            ("LICENSE", b"license", 0o644),
            ("README.md", b"readme", 0o644),
            ("man/pairmux.1", b"man", 0o644),
        )
        cases = {
            "extra": (good + (("pairmux", b"x", 0o755),), "unexpected archive members"),
            "missing-exe": (good[1:], "missing archive members: pairmux.exe"),
            "unix-name": ((("pairmux", b"binary-windows-amd64", 0o755),) + good[1:],
                          "unexpected archive members: pairmux"),
            "traversal": (good + (("../escape", b"x", 0o644),), "unsafe archive member"),
            "absolute": (good + (("/escape", b"x", 0o644),), "unsafe archive member"),
            "backslash": (good + (("man\\evil", b"x", 0o644),), "unsafe archive member"),
            "drive": (good + (("C:evil", b"x", 0o644),), "unsafe archive member"),
            "symlink": ((("pairmux.exe", b"target", 0o120777),) + good[1:],
                        "pairmux.exe is not a regular file"),
            "directory-type": (good[:3] + (("man/pairmux.1", b"", 0o040755),),
                               "man/pairmux.1 is not a regular file"),
            "wrong-bytes": ((("pairmux.exe", b"other", 0o755),) + good[1:],
                            "does not contain the verified windows/amd64 binary"),
        }
        for name, (members, error) in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                dist = self.make_fixture(root)
                archive = dist / "pairmux_1.2.3_windows_amd64.zip"
                with zipfile.ZipFile(archive, "w") as zf:
                    for member_name, data, mode in members:
                        info = zipfile.ZipInfo(member_name)
                        # On Windows ZipInfo rewrites os.sep to "/"; keep the
                        # hostile name byte-for-byte so it is really tested.
                        info.filename = member_name
                        info.create_system = 3
                        file_type = 0 if mode & 0o170000 else 0o100000
                        info.external_attr = (file_type | mode) << 16
                        zf.writestr(info, data)
                self.rewrite_checksums(dist)
                with self.assertRaisesRegex(ValueError, error):
                    verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")
                self.assertFalse((root / "staged").exists())

    def test_rejects_duplicate_and_directory_zip_members(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            archive = dist / "pairmux_1.2.3_windows_amd64.zip"
            with zipfile.ZipFile(archive, "a") as zf, self.assertWarns(UserWarning):
                zf.writestr("LICENSE", b"again")
            self.rewrite_checksums(dist)
            with self.assertRaisesRegex(ValueError, "duplicate archive member: LICENSE"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            archive = dist / "pairmux_1.2.3_windows_amd64.zip"
            with zipfile.ZipFile(archive, "w") as zf:
                zf.writestr("pairmux.exe", b"binary-windows-amd64")
                zf.writestr("LICENSE", b"license")
                zf.writestr("README.md", b"readme")
                zf.writestr("man/pairmux.1", b"man")
                zf.writestr(zipfile.ZipInfo("man/"), b"")
            self.rewrite_checksums(dist)
            with self.assertRaisesRegex(ValueError, "unexpected archive members: man/$"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_windows_archive_in_the_wrong_format(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
            archive = next(entry for entry in entries
                           if entry["type"] == "Archive" and entry["goos"] == "windows"
                           and entry["goarch"] == "amd64")
            tarball = dist / "pairmux_1.2.3_windows_amd64.tar.gz"
            with tarfile.open(tarball, "w:gz") as tf:
                info = tarfile.TarInfo("pairmux.exe")
                info.size = 0
                tf.addfile(info, io.BytesIO(b""))
            archive.update(name=tarball.name, path=str(tarball), extra={"Format": "tar.gz"})
            (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "expected 'pairmux_1.2.3_windows_amd64.zip'"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_missing_windows_target(self):
        for artifact_type in ("Binary", "Archive"):
            with self.subTest(artifact_type=artifact_type), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                dist = self.make_fixture(root)
                entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
                entries = [entry for entry in entries
                           if not (entry["type"] == artifact_type
                                   and entry.get("goos") == "windows"
                                   and entry.get("goarch") == "arm64")]
                (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, artifact_type + " targets"):
                    verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_windows_zip_checksum_omission(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            rows = (dist / "checksums.txt").read_text(encoding="utf-8").splitlines()
            rows = [row for row in rows if not row.endswith("_windows_arm64.zip")]
            (dist / "checksums.txt").write_text("\n".join(rows) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "checksum filenames"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")

    def test_rejects_cask_that_references_windows_or_misses_a_tarball(self):
        cases = {
            "windows-zip": (
                lambda text: text.replace("end\n", '  url "pairmux_#{version}_windows_amd64.zip"\nend\n'),
                "references a Windows or zip archive",
            ),
            "missing-linux-arm64": (
                lambda text: "".join(line + "\n" for line in text.splitlines()
                                     if "_linux_arm64.tar.gz" not in line),
                "no linux/arm64 tarball",
            ),
        }
        for name, (mutate, error) in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                dist = self.make_fixture(root)
                cask = dist / "homebrew" / "Casks" / "pairmux.rb"
                cask.write_text(mutate(cask.read_text(encoding="utf-8")), encoding="utf-8")
                with self.assertRaisesRegex(ValueError, error):
                    verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")
                self.assertFalse((root / "staged").exists())

    def test_rejects_manifest_name_path_mismatch(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            alias = dist / "not-checksums.txt"
            alias.write_bytes((dist / "checksums.txt").read_bytes())
            entries = json.loads((dist / "artifacts.json").read_text(encoding="utf-8"))
            checksum = next(entry for entry in entries if entry["type"] == "Checksum")
            checksum["path"] = str(alias)
            (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "does not match path basename"):
                verify_release.verify_release(dist, "v1.2.3", "abc123", root / "staged")


if __name__ == "__main__":
    unittest.main()
