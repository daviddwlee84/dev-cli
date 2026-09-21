import hashlib
import io
import json
import subprocess
from unittest import mock
from pathlib import Path
import struct
import tarfile
import zipfile
import tempfile
import unittest

import release


class FakeRemote:
    def __init__(self, files=None, draft=True, exists=True):
        self.files = files or {}
        self.draft = draft
        self.exists = exists
        self.uploaded = []
        self.published = False

    def release(self, tag):
        if not self.exists:
            return None
        return {"draft": self.draft, "prerelease": False, "assets": [{"name": name} for name in self.files]}

    def create(self, tag):
        self.exists = True

    def asset_digest(self, tag, name):
        return hashlib.sha256(self.files[name]).hexdigest()

    def upload(self, tag, path):
        assert path.name not in self.files
        self.files[path.name] = path.read_bytes()
        self.uploaded.append(path.name)

    def publish(self, tag):
        self.draft = False
        self.published = True


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def assets(self):
        paths = {}
        for name in ("one.tar.gz", "two.tar.gz", "checksums.txt"):
            paths[name] = self.root / name
            paths[name].write_bytes(name.encode())
        return paths

    def test_resume_draft_and_idempotent_public_release(self):
        assets = self.assets()
        remote = FakeRemote({"one.tar.gz": assets["one.tar.gz"].read_bytes()})
        release.publish_complete(remote, "v0.1.0", assets)
        self.assertTrue(remote.published)
        self.assertEqual(set(remote.uploaded), {"two.tar.gz", "checksums.txt"})
        remote.published = False
        remote.uploaded.clear()
        release.publish_complete(remote, "v0.1.0", assets)
        self.assertFalse(remote.published)
        self.assertEqual(remote.uploaded, [])

    def test_different_existing_bytes_are_never_overwritten(self):
        remote = FakeRemote({"one.tar.gz": b"different"})
        with self.assertRaisesRegex(ValueError, "refusing to replace"):
            release.publish_complete(remote, "v0.1.0", self.assets())
        self.assertEqual(remote.uploaded, [])
        self.assertFalse(remote.published)

    def test_new_draft_visibility_retries_reads_without_creating_twice(self):
        draft = {"draft": True, "prerelease": False, "assets": []}
        remote = mock.Mock()
        remote.release.side_effect = [None, None, None, draft, draft]
        with mock.patch.object(release.time, "sleep") as sleep:
            release.publish_complete(remote, "v0.1.0", {})
        remote.create.assert_called_once_with("v0.1.0")
        remote.upload.assert_not_called()
        remote.publish.assert_called_once_with("v0.1.0")
        self.assertEqual(sleep.call_args_list, [mock.call(2), mock.call(4)])

    def test_new_draft_visibility_timeout_preserves_draft_for_safe_resume(self):
        remote = mock.Mock()
        remote.release.return_value = None
        with mock.patch.object(release.time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "not visible yet"):
                release.publish_complete(remote, "v0.1.0", {})
        remote.create.assert_called_once_with("v0.1.0")
        self.assertEqual(remote.release.call_count, 5)
        self.assertEqual(sleep.call_args_list, [mock.call(2), mock.call(4), mock.call(8)])
        remote.upload.assert_not_called()
        remote.publish.assert_not_called()

    def test_incomplete_public_release_is_not_mutated(self):
        remote = FakeRemote({"one.tar.gz": b"one.tar.gz"}, draft=False)
        with self.assertRaisesRegex(ValueError, "incomplete"):
            release.publish_complete(remote, "v0.1.0", self.assets())
        self.assertEqual(remote.uploaded, [])


    def fixture_dist(self, project="translate", tag="v0.6.2", source=True, evidence=False):
        rows = []
        version = tag if project == "dev-cli" else tag[1:]
        binary_name = "dev" if project == "dev-cli" else "translate"
        for os_name, arch in release.TARGETS:
            if os_name == "linux":
                binary = bytearray(20); binary[:6] = b"\x7fELF\x02\x01"
                struct.pack_into("<H", binary, 18, {"amd64": 62, "arm64": 183}[arch])
            elif os_name == "darwin":
                binary = bytearray(b"\xcf\xfa\xed\xfe" + b"\0" * 4)
                struct.pack_into("<I", binary, 4, {"amd64": 0x01000007, "arm64": 0x0100000C}[arch])
            else:
                binary = bytearray(70); binary[:2] = b"MZ"; struct.pack_into("<I", binary, 60, 64)
                binary[64:68] = b"PE\0\0"; struct.pack_into("<H", binary, 68, {"amd64": 0x8664, "arm64": 0xAA64}[arch])
            files = {binary_name + (".exe" if os_name == "windows" else ""): bytes(binary)}
            if project == "translate":
                files.update({"LICENSE": b"MIT", "README.md": b"readme", **{f"completions/translate.{ext}": ext.encode() for ext in ("bash", "zsh", "fish", "ps1")}})
            name = f"{project}_{version}_{os_name}_{arch}.{'zip' if os_name == 'windows' else 'tar.gz'}"
            if os_name == "windows":
                with zipfile.ZipFile(self.root / name, "w") as archive:
                    for path, data in files.items(): archive.writestr(path, data)
            else:
                self.tar(name, files, binary_name)
            rows.append(f"{release.digest(self.root / name)}  {name}")
        if source:
            name = f"{project}_{version}_source.tar.gz"
            files = {"go.mod": b"module example.test", "go.sum": b"", "LICENSE": b"MIT"}
            if evidence: files[".specstory/history.md"] = b"evidence"
            self.tar(name, files)
            rows.append(f"{release.digest(self.root / name)}  {name}")
        (self.root / ("SHA256SUMS" if project == "dev-cli" else "checksums.txt")).write_text("\n".join(rows) + "\n")
        if project == "dev-cli":
            data = {"version": tag[1:], "architecture": {}}
            for key, arch in (("64bit", "amd64"), ("arm64", "arm64")):
                name = f"dev-cli_{tag}_windows_{arch}.zip"
                data["architecture"][key] = {"url": f"https://github.com/daviddwlee84/dev-cli/releases/download/{tag}/{name}", "hash": release.digest(self.root / name)}
            (self.root / "dev-cli.json").write_text(json.dumps(data))

    def tar(self, name, files, executable=None):
        with tarfile.open(self.root / name, "w:gz") as archive:
            for path, data in files.items():
                info = tarfile.TarInfo(path); info.size = len(data)
                info.mode = 0o755 if path == executable else 0o644
                archive.addfile(info, io.BytesIO(data))

    def test_six_platforms_source_and_checksums(self):
        self.fixture_dist()
        self.assertEqual(len(release.verify_dist(self.root, "translate", "v0.6.2")), 8)
        (self.root / "translate_0.6.2_windows_arm64.zip").write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            release.verify_dist(self.root, "translate", "v0.6.2")

    def test_dev_retains_tag_in_asset_names_and_source(self):
        self.fixture_dist("dev-cli", "v0.2.41")
        self.assertEqual(len(release.verify_dist(self.root, "dev-cli", "v0.2.41")), 9)

    def test_new_version_requires_source(self):
        self.fixture_dist(source=False)
        with self.assertRaisesRegex(ValueError, "required source"):
            release.verify_dist(self.root, "translate", "v0.6.2")

    def test_source_evidence_is_rejected(self):
        self.fixture_dist(evidence=True)
        with self.assertRaisesRegex(ValueError, "development evidence"):
            release.verify_dist(self.root, "translate", "v0.6.2")

    def test_old_translate_contract_remains_valid(self):
        self.fixture_dist(tag="v0.6.1", source=False)
        assets = release.verify_dist(self.root, "translate", "v0.6.1")
        self.assertEqual(len(assets), 7)
        remote = FakeRemote({name: path.read_bytes() for name, path in assets.items()}, draft=False)
        release.publish_complete(remote, "v0.6.1", assets)
        self.assertEqual(remote.uploaded, [])

    def test_wrong_native_platform_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "windows/arm64"):
            release.verify_binary(b"bad", "windows", "arm64")

    def test_duplicate_checksum_is_rejected(self):
        self.fixture_dist()
        path = self.root / "checksums.txt"
        path.write_text(path.read_text() + path.read_text().splitlines()[0] + "\n")
        with self.assertRaisesRegex(ValueError, "duplicate"):
            release.verify_dist(self.root, "translate", "v0.6.2")


class GitHubAdapterTests(unittest.TestCase):
    def setUp(self):
        self.remote = release.GitHub("owner/example")
        self.tag = "v0.1.0"
        self.draft = {"id": 123, "tag_name": self.tag, "draft": True,
                      "prerelease": False, "assets": []}
        self.lookup = mock.patch("release.subprocess.run")
        self.run = self.lookup.start()
        self.addCleanup(self.lookup.stop)
        self.run.return_value = subprocess.CompletedProcess([], 1, "", "gh: Not Found (HTTP 404)")
        self.calls = mock.patch("release.subprocess.check_output")
        self.call = self.calls.start()
        self.addCleanup(self.calls.stop)

    def test_tag_404_finds_existing_draft_on_later_page(self):
        self.call.return_value = json.dumps([[{"tag_name": "v0.0.9"}], [self.draft]])
        self.assertEqual(self.remote.release(self.tag), self.draft)
        self.run.assert_called_once_with(
            ["gh", "api", "repos/owner/example/releases/tags/v0.1.0"],
            capture_output=True, text=True)
        self.call.assert_called_once_with(
            ["gh", "api", "--paginate", "--slurp", "repos/owner/example/releases?per_page=100"],
            text=True)

    def test_successful_tag_lookup_does_not_list_releases(self):
        self.run.return_value = subprocess.CompletedProcess([], 0, json.dumps(self.draft), "")
        self.assertEqual(self.remote.release(self.tag), self.draft)
        self.call.assert_not_called()

    def test_absent_tag_returns_none_only_after_all_pages(self):
        self.call.return_value = json.dumps([[{"tag_name": "v0.0.9"}], []])
        self.assertIsNone(self.remote.release(self.tag))
        self.call.assert_called_once()

    def test_duplicate_tag_refuses_publish_without_creating(self):
        self.call.return_value = json.dumps([[self.draft], [{**self.draft, "id": 456}]])
        with mock.patch.object(self.remote, "create") as create:
            with self.assertRaisesRegex(RuntimeError, "multiple releases"):
                release.publish_complete(self.remote, self.tag, {})
            create.assert_not_called()

    def test_auth_error_is_not_treated_as_missing_release(self):
        self.run.return_value = subprocess.CompletedProcess([], 1, "", "gh: Forbidden (HTTP 403)")
        with self.assertRaisesRegex(RuntimeError, "cannot inspect"):
            self.remote.release(self.tag)
        self.call.assert_not_called()

    def test_listing_failure_cannot_authorize_creation(self):
        self.call.side_effect = subprocess.CalledProcessError(1, ["gh", "api"])
        with mock.patch.object(self.remote, "create") as create:
            with self.assertRaises(subprocess.CalledProcessError):
                release.publish_complete(self.remote, self.tag, {})
            create.assert_not_called()

    def test_malformed_listing_is_rejected(self):
        for value in ({"message": "not pages"}, [[None]], [self.draft]):
            with self.subTest(value=value):
                self.call.return_value = json.dumps(value)
                with self.assertRaisesRegex(RuntimeError, "unexpected"):
                    self.remote.release(self.tag)


if __name__ == "__main__":
    unittest.main()
