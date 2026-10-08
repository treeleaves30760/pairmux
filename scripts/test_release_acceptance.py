#!/usr/bin/env python3
"""Offline unit tests for the published-byte acceptance helpers."""
from __future__ import annotations

import hashlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import sys
import tarfile
import textwrap
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("release_acceptance", SCRIPTS / "release-acceptance.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class PublishedMetadataTest(unittest.TestCase):
    def setUp(self):
        self.tag = "v0.6.0"
        self.version = "0.6.0"
        self.base = f"https://github.com/{MODULE.REPO}/releases/download/{self.tag}/"
        self.binary = b"a public native executable fixture"
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w:gz") as archive:
            entry = tarfile.TarInfo("pairmux")
            entry.size, entry.mode = len(self.binary), 0o755
            archive.addfile(entry, io.BytesIO(self.binary))
        self.archive = buf.getvalue()
        self.native = {f"pairmux_{self.version}_{os_name}_{arch}.tar.gz": self.archive
                       for os_name in ("darwin", "linux") for arch in ("amd64", "arm64")}
        self.native.update({f"pairmux_{self.version}_linux_{arch}.rpm": b"rpm fixture"
                            for arch in ("amd64", "arm64")})
        self.urls = {self.base + name: data for name, data in self.native.items()}
        checksum = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n"
                           for name, data in sorted(self.native.items())).encode()
        self.urls[self.base + "checksums.txt"] = checksum
        self.release = {
            "tag_name": self.tag, "draft": False, "prerelease": False, "published_at": "2026-10-09T00:00:00Z",
            "assets": [{"name": name, "state": "uploaded", "size": len(data), "browser_download_url": self.base + name}
                       for name, data in self.native.items()]
                      + [{"name": "checksums.txt", "state": "uploaded", "size": len(checksum), "browser_download_url": self.base + "checksums.txt"}],
        }
        self.pypi = {"info": {"name": "pairmux", "version": self.version}, "urls": []}
        for platform in MODULE.PLATFORMS.values():
            name = f"pairmux-{self.version}-py3-none-{platform}.whl"
            buf = io.BytesIO()
            with zipfile.ZipFile(buf, "w") as archive:
                archive.writestr(f"pairmux-{self.version}.data/scripts/pairmux", self.binary)
            data = buf.getvalue()
            url = "https://files.pythonhosted.org/fixtures/" + name
            self.urls[url] = data
            self.pypi["urls"].append({"filename": name, "packagetype": "bdist_wheel", "yanked": False,
                                      "url": url, "digests": {"sha256": hashlib.sha256(data).hexdigest()}})
        self.latest = dict(self.release)
        self.all_pypi = {"releases": {self.version: self.pypi["urls"]}}

    def fetch(self, url):
        if url.endswith("/releases/tags/" + self.tag):
            return json.dumps(self.release).encode()
        if url.endswith("/releases/latest"):
            return json.dumps(self.latest).encode()
        if url == f"https://pypi.org/pypi/pairmux/{self.version}/json":
            return json.dumps(self.pypi).encode()
        if url == "https://pypi.org/pypi/pairmux/json":
            return json.dumps(self.all_pypi).encode()
        return self.urls[url]

    def verify(self, tag=None):
        with tempfile.TemporaryDirectory() as directory, patch.object(MODULE, "fetch", self.fetch):
            return MODULE.verify(tag or self.tag, Path(directory), "linux-amd64", True)

    def test_selected_public_bytes_and_inventory(self):
        report = self.verify()
        self.assertEqual(report["selected"]["binary_sha256"], hashlib.sha256(self.binary).hexdigest())
        self.assertTrue(report["tag_is_latest_on_both"])
        self.assertEqual(len(report["assets"]), 6)
        self.assertEqual(len(report["wheels"]), 4)

    def test_canonical_tag(self):
        for tag in ("0.6.0", "v00.6.0", "v0.6.0-rc.1", "v0.6.0;exit 0"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                self.verify(tag)

    def test_draft_and_prerelease_are_rejected(self):
        for field in ("draft", "prerelease"):
            self.release[field] = True
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.verify()
            self.release[field] = False

    def test_missing_native_asset(self):
        self.release["assets"].pop()
        with self.assertRaises(ValueError):
            self.verify()

    def test_yanked_wheel(self):
        self.pypi["urls"][0]["yanked"] = True
        with self.assertRaises(ValueError):
            self.verify()

    def test_native_checksum_mismatch(self):
        self.urls[self.base + f"pairmux_{self.version}_linux_amd64.tar.gz"] = b"corrupt"
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            self.verify()

    def test_wheel_binary_mismatch(self):
        wheel = next(w for w in self.pypi["urls"] if "x86_64.manylinux" in w["filename"])
        buf = io.BytesIO()
        with zipfile.ZipFile(buf, "w") as archive:
            archive.writestr(f"pairmux-{self.version}.data/scripts/pairmux", b"different binary")
        data = buf.getvalue()
        self.urls[wheel["url"]] = data
        wheel["digests"]["sha256"] = hashlib.sha256(data).hexdigest()
        with self.assertRaisesRegex(ValueError, "differs"):
            self.verify()

    def test_older_tag_does_not_claim_latest(self):
        self.latest["tag_name"] = "v0.7.0"
        self.all_pypi["releases"]["0.7.0"] = [{"packagetype": "bdist_wheel", "yanked": False}]
        report = self.verify()
        self.assertFalse(report["tag_is_latest_on_both"])
        self.assertEqual(report["latest_pypi_version"], "0.7.0")

    def test_actual_historical_upgrade_resolves_verified_target(self):
        previous = {"version": "0.5.3", "tag": "v0.5.3", "selected": {"target": "linux-amd64"}}
        with tempfile.TemporaryDirectory() as directory, patch.object(MODULE, "fetch", self.fetch):
            report = MODULE.actual_provenance(previous, "0.6.0", Path(directory))
        self.assertEqual(report["version"], "0.6.0")
        self.assertEqual(report["selected"]["binary_sha256"], hashlib.sha256(self.binary).hexdigest())

    def test_actual_historical_upgrade_fails_closed_on_corrupt_public_binary(self):
        previous = {"version": "0.5.3", "tag": "v0.5.3", "selected": {"target": "linux-amd64"}}
        self.urls[self.base + f"pairmux_{self.version}_linux_amd64.tar.gz"] = b"corrupt"
        with tempfile.TemporaryDirectory() as directory, patch.object(MODULE, "fetch", self.fetch):
            with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                MODULE.actual_provenance(previous, "0.6.0", Path(directory))


class InstalledRefreshTest(unittest.TestCase):
    def invoke(self, mode, wrong_bytes=False, divergent_latest=False):
        with tempfile.TemporaryDirectory(prefix="pmx-refresh-test-") as directory:
            root = Path(directory)
            canonical = root / "tools" / "pairmux" / "bin" / "pairmux"
            canonical.parent.mkdir(parents=True)
            entry = root / "bin" / "pairmux"
            entry.parent.mkdir()
            # This script is trusted test code. Replacement reuses exactly its
            # verified bytes; only file identity must change during refresh.
            binary = textwrap.dedent("""\
                #!/bin/sh
                if [ "$1" = version ]; then printf '0.6.0\\n'; exit 0; fi
                if [ "$1" = --json ] && [ "$2" = update ]; then
                  case "${PAIRMUX_TEST_REFRESH_MODE:-replace}" in
                    replace) cp "$0" "$0.next"; chmod +x "$0.next"; mv "$0.next" "$0" ;;
                    noop) : ;;
                    mutate) printf '\\n# mutated old bytes\\n' >> "$0"; cp "$0" "$0.next"; chmod +x "$0.next"; mv "$0.next" "$0" ;;
                    wrong-replacement) cp "$0" "$0.next"; printf '\\n# incorrect replacement\\n' >> "$0.next"; chmod +x "$0.next"; mv "$0.next" "$0" ;;
                    *) exit 99 ;;
                  esac
                  printf '%s\\n' '{"schema":"pairmux.v1","ok":true,"status":"refreshed","output":"source: https://pypi.org/simple"}'
                  exit 0
                fi
                exit 98
                """).encode()
            # Operate at the canonical path, just as Go's real os.Executable.
            binary = binary.replace(b'"$0"', b'"' + str(canonical).encode() + b'"')
            binary = binary.replace(b'"$0.next"', b'"' + str(canonical).encode() + b'.next"')
            canonical.write_bytes(binary + (b"\n# wrong initial bytes\n" if wrong_bytes else b""))
            canonical.chmod(0o755)
            entry.symlink_to(canonical)
            report = {"version": "0.6.0", "tag": "v0.6.0", "latest_pypi_version": "0.6.0",
                      "latest_github_tag": "v0.7.0" if divergent_latest else "v0.6.0",
                      "tag_is_latest_on_both": not divergent_latest,
                      "selected": {"target": "linux-amd64", "binary_sha256": hashlib.sha256(binary).hexdigest()}}
            provenance = root / "provenance.json"
            provenance.write_text(json.dumps(report))
            out = root / "probe"
            # A selected actual version must reuse its verified native hash, not
            # perform live metadata calls or disable hashing on latest mismatch.
            env = dict(os.environ, PAIRMUX_TEST_REFRESH_MODE=mode)
            result = subprocess.run([sys.executable, "-I", str(SCRIPTS / "release-acceptance.py"),
                                     "--installed", str(entry), "--installed-version", "0.6.0",
                                     "--provenance", str(provenance), "--refresh", "--out-dir", str(out)],
                                    env=env, capture_output=True, text=True, timeout=20)
            identity_path = out / "self-refresh-identity.json"
            identity = json.loads(identity_path.read_text()) if identity_path.exists() else None
            return result, identity

    def test_noop_refreshed_response_is_rejected(self):
        result, identity = self.invoke("noop")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no-op refresh", result.stderr)
        self.assertIsNone(identity)

    def test_genuine_same_bytes_replacement_passes_with_divergent_latest(self):
        result, identity = self.invoke("replace", divergent_latest=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(identity["held_old_bytes_preserved"])
        self.assertEqual(identity["before"]["sha256"], identity["replacement"]["sha256"])
        self.assertNotEqual((identity["before"]["device"], identity["before"]["inode"]),
                            (identity["replacement"]["device"], identity["replacement"]["inode"]))

    def test_divergent_latest_never_disables_actual_version_hash(self):
        result, identity = self.invoke("replace", wrong_bytes=True, divergent_latest=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("differs from checksummed public release 0.6.0", result.stderr)
        self.assertIsNone(identity)

    def test_held_old_bytes_must_not_change(self):
        result, identity = self.invoke("mutate")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("held old executable", result.stderr)
        self.assertIsNone(identity)

    def test_replacement_bytes_always_checked_even_if_latest_disagree(self):
        result, identity = self.invoke("wrong-replacement", divergent_latest=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("differs from checksummed public release 0.6.0", result.stderr)
        self.assertIsNone(identity)


class UVScriptContractTest(unittest.TestCase):
    def invoke(self, mode="replace", divergent_latest=False, corrupt_upgrade=False):
        with tempfile.TemporaryDirectory(prefix="pmx-uv-script-test-") as directory:
            root = Path(directory)
            trusted = root / "trusted"
            trusted.mkdir()
            for name in ("test-release-uv.sh", "release-acceptance.py"):
                shutil.copy2(SCRIPTS / name, trusted / name)
            # Only this test's owned staging copy has a no-op runtime. Publication
            # acceptance always calls the real common runtime in the repository.
            (trusted / "test-release-runtime.sh").write_text("#!/bin/sh\nexit 0\n")
            bins = root / "bin"
            bins.mkdir()
            source = root / "fixtures"
            source.mkdir()
            binary = ("#!" + sys.executable + " -I\n" + textwrap.dedent("""\
                import json, os, pathlib, shutil, sys
                version = sys.argv[0]
                if sys.argv[1:] == ["version"]:
                    print("0.5.3" if "old-tool" in str(pathlib.Path(version).resolve()) else "0.6.0")
                elif sys.argv[1:] == ["--json", "update"]:
                    path = pathlib.Path(version).resolve()
                    if path.parents[3].joinpath("mode").read_text() != "noop":
                        replacement = path.with_name("pairmux.next")
                        shutil.copyfile(path, replacement)
                        replacement.chmod(0o755)
                        os.replace(replacement, path)
                    print(json.dumps({"schema":"pairmux.v1", "ok":True, "status":"refreshed", "output":"source: https://pypi.org/simple"}))
                else:
                    raise SystemExit(98)
                """)).encode()
            binary_path = source / "pairmux"
            binary_path.write_bytes(binary)
            binary_path.chmod(0o755)
            bad_path = source / "wrong-pairmux"
            bad_path.write_bytes(binary + b"\n# incorrect upgraded binary\n")
            bad_path.chmod(0o755)
            uv = bins / "uv"
            uv.write_text("#!" + sys.executable + " -I\n" + textwrap.dedent("""\
                import os, pathlib, shutil, sys
                if sys.argv[1:] == ["--version"]:
                    print("uv 0.11.16")
                    raise SystemExit(0)
                root = pathlib.Path(os.environ["UV_TOOL_DIR"]).parent
                old = "pairmux==0.5.3" in sys.argv
                directory = root / ("old-tool" if old else "tools/pairmux/bin")
                directory.mkdir(parents=True, exist_ok=True)
                installed = directory / "pairmux"
                fixture = BAD if CORRUPT and not old and "upgrade" in str(root) else GOOD
                shutil.copyfile(fixture, installed)
                installed.chmod(0o755)
                link = pathlib.Path(os.environ["UV_TOOL_BIN_DIR"]) / "pairmux"
                link.unlink(missing_ok=True)
                link.symlink_to(installed)
                (root / "mode").write_text(MODE)
                """).replace("BAD", repr(str(bad_path))).replace("GOOD", repr(str(binary_path)))
                                  .replace("CORRUPT", repr(corrupt_upgrade)).replace("MODE", repr(mode)))
            uv.chmod(0o755)
            installer = source / "install.sh"
            installer.write_text("#!/bin/sh\n" + str(uv) + ' tool install pairmux==0.6.0\n')
            curl = bins / "curl"
            curl.write_text("#!" + sys.executable + " -I\nimport pathlib,shutil,sys\n"
                            + "shutil.copyfile(" + repr(str(installer)) + ', sys.argv[sys.argv.index("-o")+1])\n')
            curl.chmod(0o755)
            report = {"version":"0.6.0", "tag":"v0.6.0", "latest_pypi_version":"0.6.0",
                      "latest_github_tag":"v0.7.0" if divergent_latest else "v0.6.0",
                      "tag_is_latest_on_both":not divergent_latest,
                      "selected":{"target":"linux-amd64", "binary_sha256":hashlib.sha256(binary).hexdigest()}}
            provenance = root / "provenance.json"
            provenance.write_text(json.dumps(report))
            env = dict(os.environ, PATH=str(bins) + os.pathsep + os.environ["PATH"])
            result = subprocess.run(["bash", str(trusted / "test-release-uv.sh"), str(uv), sys.executable,
                                     "0.6.0", str(provenance), str(root / "logs")],
                                    env=env, capture_output=True, text=True, timeout=30)
            details = "\n".join(p.read_text() for p in (root / "logs").rglob("*.txt"))
            return result, details

    def test_script_rejects_noop_refreshed(self):
        result, details = self.invoke(mode="noop")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no-op refresh", details)

    def test_script_rejects_wrong_upgraded_bytes_when_latest_disagree(self):
        result, details = self.invoke(divergent_latest=True, corrupt_upgrade=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("differs from checksummed public release 0.6.0", details)

    def test_script_accepts_verified_replacement_even_when_latest_disagree(self):
        result, details = self.invoke(divergent_latest=True)
        self.assertEqual(result.returncode, 0, result.stderr + details)
        self.assertIn("verified actual public binary replacement", details)


class RuntimeContractTest(unittest.TestCase):
    def invoke(self, doctor):
        with tempfile.TemporaryDirectory(prefix="pmx-runtime-test-") as directory:
            root = Path(directory)
            bins = root / "bin"
            bins.mkdir()
            fake_tmux = bins / "tmux"
            fake_tmux.write_text("#!/bin/sh\nexit 0\n")
            fake_tmux.chmod(0o755)
            fake = bins / "pairmux"
            # Mock test only: verifies the helper rejects zero-exit unhealthy
            # doctor output; it is not a platform or publication acceptance pass.
            fake.write_text("#!/bin/sh\nif [ \"$1\" = version ]; then printf '0.6.0\\n'; exit 0; fi\n"
                            + "if [ \"$2\" = doctor ]; then printf '%s\\n' '" + json.dumps(doctor) + "'; exit 0; fi\nexit 99\n")
            fake.chmod(0o755)
            env = dict(os.environ, PATH=str(bins) + os.pathsep + os.environ["PATH"])
            result = subprocess.run(["bash", str(SCRIPTS / "test-release-runtime.sh"), str(fake), "0.6.0", str(root / "logs")],
                                    env=env, capture_output=True, text=True, timeout=20)
            self.assertTrue((root / "logs" / "cleanup.txt").is_file(), "owned cleanup was not attempted")
            return result

    def test_zero_exit_doctor_issues_is_failure(self):
        result = self.invoke({"schema": "pairmux.v1", "ok": True, "status": "issues"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected status", result.stderr)

    def test_wrong_doctor_schema_is_failure(self):
        result = self.invoke({"schema": "other.v1", "ok": True, "status": "ok"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("pairmux.v1", result.stderr)


if __name__ == "__main__":
    unittest.main()
