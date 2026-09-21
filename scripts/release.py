#!/usr/bin/env python3
"""Build completions, verify release payloads, and publish a complete draft."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import struct
import subprocess
import tarfile
import tempfile
import time
import zipfile
import stat

TARGETS = [(os_name, arch) for os_name in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")]
STABLE_TAG = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def verify_binary(data, os_name, arch):
    if os_name == "linux":
        expected = {"amd64": 62, "arm64": 183}[arch]
        valid = len(data) >= 20 and data[:6] == b"\x7fELF\x02\x01" and struct.unpack_from("<H", data, 18)[0] == expected
    elif os_name == "darwin":
        expected = {"amd64": 0x01000007, "arm64": 0x0100000C}[arch]
        valid = len(data) >= 8 and data[:4] == b"\xcf\xfa\xed\xfe" and struct.unpack_from("<I", data, 4)[0] == expected
    else:
        offset = struct.unpack_from("<I", data, 60)[0] if len(data) >= 64 else len(data)
        valid = len(data) >= offset + 6 and data[:2] == b"MZ" and data[offset:offset + 4] == b"PE\0\0" and struct.unpack_from("<H", data, offset + 4)[0] == {"amd64": 0x8664, "arm64": 0xAA64}[arch]
    if not valid:
        raise ValueError(f"binary does not match {os_name}/{arch}")


def version_tuple(tag):
    if not STABLE_TAG.fullmatch(tag):
        raise ValueError("release requires a stable tag")
    return tuple(map(int, tag[1:].split(".")))


def verify_dist(dist, project, tag, smoke=False):
    version = version_tuple(tag)
    if project not in ("dev-cli", "translate"):
        raise ValueError("unsupported project")
    binary = "dev" if project == "dev-cli" else "translate"
    asset_version = tag if project == "dev-cli" else tag[1:]
    expected = {f"{project}_{asset_version}_{os_name}_{arch}.{'zip' if os_name == 'windows' else 'tar.gz'}": (os_name, arch) for os_name, arch in TARGETS}
    source = f"{project}_{asset_version}_source.tar.gz"
    source_required = version >= ((0, 2, 35) if project == "dev-cli" else (0, 6, 2))
    sums = "SHA256SUMS" if project == "dev-cli" else "checksums.txt"
    checksums = {}
    for line in (dist / sums).read_text().splitlines():
        parts = line.split()
        if len(parts) != 2 or not re.fullmatch(r"[0-9a-f]{64}", parts[0]):
            raise ValueError("malformed checksum entry")
        name = parts[1].lstrip("*")
        if name in checksums:
            raise ValueError("duplicate checksum entry")
        checksums[name] = parts[0]
    required_names = set(expected) | ({source} if source_required else set())
    if set(checksums) != required_names:
        raise ValueError("checksum manifest must name all six platforms and the version's required source archive")
    host = (platform.system().lower(), {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine().lower(), platform.machine().lower()))
    for name in sorted(required_names):
        path = dist / name
        if digest(path) != checksums[name]:
            raise ValueError(f"checksum mismatch: {name}")
        if name == source:
            with tarfile.open(path, "r:gz") as archive:
                names = {m.name.rstrip("/") for m in archive.getmembers()}
                evidence = (".specstory", ".claude/plans", ".codex/plans", ".cursor/plans", ".opencode/plans")
                if any(n == p or n.startswith(p + "/") for n in names for p in evidence):
                    raise ValueError("source archive contains development evidence")
                if not {"go.mod", "go.sum", "LICENSE"} <= names:
                    raise ValueError("source archive missing required inputs")
                for n in names:
                    if n.startswith("/") or ".." in n.split("/") or "\\" in n:
                        raise ValueError("unsafe source path")
            continue
        target = expected[name]
        executable = binary + (".exe" if target[0] == "windows" else "")
        required = {executable}
        if project == "translate":
            required |= {"LICENSE", "README.md", *[f"completions/translate.{ext}" for ext in ("bash", "zsh", "fish", "ps1")]}
        if target[0] == "windows":
            with zipfile.ZipFile(path) as archive:
                entries = [m for m in archive.infolist() if not m.is_dir()]
                if len(entries) != len(required) or {m.filename for m in entries} != required:
                    raise ValueError(f"unexpected ZIP entries: {name}")
                if any(stat.S_ISLNK(m.external_attr >> 16) for m in entries):
                    raise ValueError("symlink in binary archive")
                files = {m.filename: archive.read(m) for m in entries}
        else:
            with tarfile.open(path, "r:gz") as archive:
                entries = [m for m in archive.getmembers() if not m.isdir()]
                if len(entries) != len(required) or {m.name for m in entries} != required or any(not m.isfile() for m in entries):
                    raise ValueError(f"unexpected tar entries: {name}")
                if archive.getmember(executable).mode & 0o111 == 0:
                    raise ValueError("binary is not executable")
                files = {m.name: archive.extractfile(m).read() for m in entries}
        if any(not value.strip() for value in files.values()):
            raise ValueError("empty release file")
        verify_binary(files[executable], *target)
        if smoke and target == host:
            with tempfile.TemporaryDirectory(prefix="release-smoke-") as temporary:
                candidate = Path(temporary) / executable
                candidate.write_bytes(files[executable]); candidate.chmod(0o755)
                env = os.environ.copy()
                for key in tuple(env):
                    if key.startswith(("DEV_", "TRANSLATE_", "XDG_")) or key in ("HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"):
                        env.pop(key)
                env.update(HOME=temporary, USERPROFILE=temporary, APPDATA=temporary, LOCALAPPDATA=temporary)
                result = subprocess.check_output([str(candidate), "--version"], text=True, env=env, timeout=30)
                if tag not in result:
                    raise ValueError("binary version mismatch")
                for argv in (["--help"], ["completion", "bash"], ["completion", "zsh"]):
                    subprocess.run([str(candidate), *argv], check=True, env=env, stdout=subprocess.DEVNULL, timeout=30)
    assets = {name: dist / name for name in sorted(required_names)} | {sums: dist / sums}
    # Dev has always attached a separate Scoop manifest. Its hash is outside
    # SHA256SUMS for backwards compatibility, but its content is verified here.
    manifest = dist / "dev-cli.json"
    if project == "dev-cli" and version >= (0, 2, 41) and not manifest.is_file():
        raise ValueError("new dev release requires its Scoop manifest")
    if project == "dev-cli" and manifest.exists():
        data = json.loads(manifest.read_text())
        if data["version"] != tag[1:]:
            raise ValueError("Scoop version mismatch")
        for key, arch in (("64bit", "amd64"), ("arm64", "arm64")):
            archive = f"dev-cli_{tag}_windows_{arch}.zip"
            row = data["architecture"][key]
            if row["hash"] != checksums[archive] or row["url"] != f"https://github.com/daviddwlee84/dev-cli/releases/download/{tag}/{archive}":
                raise ValueError("Scoop manifest does not match release")
        assets[manifest.name] = manifest
    return assets


class GitHub:
    def __init__(self, repo, notes=None):
        self.repo = repo
        self.notes = notes

    def call(self, *args):
        return subprocess.check_output(["gh", *args], text=True)

    def release(self, tag):
        result = subprocess.run(["gh", "api", f"repos/{self.repo}/releases/tags/{tag}"],
                                capture_output=True, text=True)
        if result.returncode == 0:
            return json.loads(result.stdout)
        if "HTTP 404" in result.stderr:
            # GitHub's tag endpoint may omit drafts, even for their creator.
            # Enumerate every page before deciding it is safe to create one.
            pages = json.loads(self.call("api", "--paginate", "--slurp",
                                         f"repos/{self.repo}/releases?per_page=100"))
            if not isinstance(pages, list) or any(not isinstance(page, list) or any(not isinstance(item, dict) for item in page) for page in pages):
                raise RuntimeError("unexpected paginated release listing")
            matches = [item for page in pages for item in page if item.get("tag_name") == tag]
            if len(matches) > 1:
                raise RuntimeError("multiple releases match this tag; refusing to create or select a draft")
            return matches[0] if matches else None
        raise RuntimeError(f"cannot inspect release: {result.stderr.strip()}")

    def create(self, tag):
        args = ["release", "create", tag, "--repo", self.repo, "--verify-tag", "--draft", "--title", tag]
        args += ["--notes-file", str(self.notes)] if self.notes and self.notes.is_file() and self.notes.stat().st_size else ["--generate-notes"]
        self.call(*args)

    def upload(self, tag, path):
        # Deliberately never use --clobber.
        self.call("release", "upload", tag, str(path), "--repo", self.repo)

    def asset_digest(self, tag, name):
        with tempfile.TemporaryDirectory(prefix="release-asset-") as temporary:
            self.call("release", "download", tag, "--repo", self.repo, "--pattern", name, "--dir", temporary)
            return digest(Path(temporary) / name)

    def publish(self, tag):
        self.call("release", "edit", tag, "--repo", self.repo, "--draft=false", "--latest")


def publish_complete(remote, tag, assets):
    release = remote.release(tag)
    if release is None:
        remote.create(tag)
        # Draft creation can precede visibility in both GitHub lookup endpoints.
        # Retry only reads; creating again could make an ambiguous duplicate.
        for delay in (0, 2, 4, 8):
            if delay:
                time.sleep(delay)
            release = remote.release(tag)
            if release is not None:
                break
        if release is None:
            raise RuntimeError("created draft is not visible yet; rerun to resume")
    if release is None or release["prerelease"]:
        raise ValueError("expected a stable release or draft")
    existing = {a["name"] for a in release["assets"]}
    if len(existing) != len(release["assets"]) or not existing <= set(assets):
        raise ValueError("release contains unexpected or duplicate assets")
    for name in sorted(existing):
        if remote.asset_digest(tag, name) != digest(assets[name]):
            raise ValueError(f"existing release asset differs; refusing to replace {name}")
    missing = set(assets) - existing
    if missing and not release["draft"]:
        raise ValueError("published release is incomplete; refusing to mutate it")
    for name in sorted(missing):
        remote.upload(tag, assets[name])
    current = remote.release(tag)
    if current is None or len(current["assets"]) != len(assets) or {a["name"] for a in current["assets"]} != set(assets):
        raise ValueError("release assets are incomplete")
    for name in assets:
        if remote.asset_digest(tag, name) != digest(assets[name]):
            raise ValueError(f"uploaded release asset does not match: {name}")
    if current["draft"]:
        remote.publish(tag)


def verify_tag(repo, tag):
    if not STABLE_TAG.fullmatch(tag):
        raise ValueError("release requires an immutable stable vMAJOR.MINOR.PATCH tag")
    commit = subprocess.check_output(["git", "rev-parse", f"{tag}^{{commit}}"], text=True).strip()
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    if commit != head:
        raise ValueError("checkout is not the tagged commit")
    subprocess.run(["git", "fetch", "origin", "main"], check=True)
    subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], check=True)
    obj = json.loads(subprocess.check_output(["gh", "api", f"repos/{repo}/git/ref/tags/{tag}"], text=True))["object"]
    while obj["type"] == "tag":
        obj = json.loads(subprocess.check_output(["gh", "api", f"repos/{repo}/git/tags/{obj['sha']}"], text=True))["object"]
    if obj["type"] != "commit" or obj["sha"] != commit:
        raise ValueError("remote tag differs from the checked-out immutable tag")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("check", "publish"))
    parser.add_argument("--project", required=True, choices=("dev-cli", "translate"))
    parser.add_argument("--dist", type=Path, default=Path("dist"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--repo")
    parser.add_argument("--notes", type=Path)
    parser.add_argument("--smoke", action="store_true")
    args = parser.parse_args()
    assets = verify_dist(args.dist, args.project, args.tag, args.smoke)
    if args.action == "publish":
        if args.repo != "daviddwlee84/" + args.project:
            parser.error("publish requires the matching repository")
        verify_tag(args.repo, args.tag)
        publish_complete(GitHub(args.repo, args.notes), args.tag, assets)
    print(f"{args.action}: verified {len(assets)} release assets")


if __name__ == "__main__":
    main()
