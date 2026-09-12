#!/usr/bin/env python3
"""Build the real Git source archive, checking its packaging boundary.

Run after committing the packaging change: python3 scripts/check-source-archive.py
The checkout, index, refs and attributes are never modified. Go caches may grow.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ref", default="HEAD")
    parser.add_argument("--version", default="archive-check")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    oid = subprocess.check_output(
        ["git", "rev-parse", "--verify", "--end-of-options", args.ref + "^{commit}"],
        cwd=root, text=True,
    ).strip()
    with tempfile.TemporaryDirectory(prefix="dev-source-archive-") as temporary:
        directory = Path(temporary)
        archive = directory / "source.tar.gz"
        with archive.open("wb") as output:
            subprocess.run(
                ["git", "archive", "--format=tar.gz", oid], cwd=root,
                stdout=output, check=True,
            )
        source = directory / "source"
        source.mkdir()
        with tarfile.open(archive) as bundle:
            members = bundle.getmembers()
            names = {member.name for member in members}
            if any(name == ".specstory" or name.startswith(".specstory/") for name in names):
                raise SystemExit("source archive contains SpecStory data")
            required = {"go.mod", "go.sum", "cmd/dev/main.go", "internal/skill/dev-cli/SKILL.md"}
            if not required <= names:
                raise SystemExit("source archive is missing required build inputs")
            bundle.extractall(source, filter="data")
        binary = directory / ("dev.exe" if os.name == "nt" else "dev")
        subprocess.run([
            "go", "build", "-ldflags",
            "-X github.com/daviddwlee84/dev-cli/internal/cli.Version=" + args.version,
            "-o", str(binary), "./cmd/dev",
        ], cwd=source, check=True)
        version = subprocess.check_output([str(binary), "--version"], text=True).strip()
        if version != "dev version " + args.version:
            raise SystemExit("archive binary version mismatch")
        print(json.dumps({"commit": oid, "archive_bytes": archive.stat().st_size,
                          "files": sum(member.isfile() for member in members),
                          "specstory_excluded": True, "build": "passed"}))


if __name__ == "__main__":
    main()
