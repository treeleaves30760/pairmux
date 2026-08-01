import hashlib
import io
import json
import tarfile
import tempfile
import unittest
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
            binary = dist / ("pairmux_%s_%s_v1" % (os_name, arch)) / "pairmux"
            binary.parent.mkdir()
            binary.write_bytes(content)
            binary.chmod(0o755)
            entries.append({
                "type": "Binary", "name": "pairmux", "path": str(binary),
                "goos": os_name, "goarch": arch,
            })

            name = "pairmux_%s_%s_%s.tar.gz" % (version, os_name, arch)
            archive = dist / name
            with tarfile.open(archive, "w:gz") as tf:
                for member_name, data, mode in (
                    ("pairmux", content, 0o755),
                    ("LICENSE", b"license", 0o644),
                    ("README.md", b"readme", 0o644),
                    ("man/pairmux.1", b"man", 0o644),
                ):
                    info = tarfile.TarInfo(member_name)
                    info.size = len(data)
                    info.mode = mode
                    tf.addfile(info, io.BytesIO(data))
            entries.append({
                "type": "Archive", "name": name, "path": str(archive),
                "goos": os_name, "goarch": arch, "extra": {"Format": "tar.gz"},
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
        cask.write_text('cask "pairmux" do\nend\n', encoding="utf-8")
        entries.append({"type": "Homebrew Cask", "name": "pairmux.rb", "path": str(cask)})
        (dist / "metadata.json").write_text(json.dumps({
            "tag": tag, "version": version, "commit": commit,
        }), encoding="utf-8")
        (dist / "artifacts.json").write_text(json.dumps(entries), encoding="utf-8")
        return dist

    def test_verifies_and_stages_exact_public_artifacts(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            dist = self.make_fixture(root)
            staged = verify_release.verify_release(
                dist, "v1.2.3", "abc123", root / "staged"
            )
            self.assertEqual(len(staged), 9)
            self.assertEqual(len(list((root / "staged").iterdir())), 9)

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
