#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Check a trial task without changing its checkout; JSON results on stdout."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def check(repo, task):
    env = dict(os.environ, GOWORK="off", GOPROXY="off")
    env.pop("GOROOT", None)
    env.setdefault("GOTOOLCHAIN", "local")
    with tempfile.TemporaryDirectory(prefix="textstat-acceptance-") as directory:
        binary = Path(directory) / ("textstat.exe" if os.name == "nt" else "textstat")
        built = subprocess.run(
            ["go", "build", "-o", str(binary), "."], cwd=repo, env=env,
            capture_output=True, timeout=120,
        )
        if built.returncode:
            return [{"case": "build", "passed": False,
                     "detail": built.stderr.decode(errors="replace")[-2000:]}]
        if task == "json":
            cases = [("empty", b"", 0, 0), ("ascii", b"one two\nthree\n", 2, 3)]
            args = ["--json"]
        else:
            cases = [
                ("nbsp-em-space", "one\u00a0two\u2003三\n".encode(), 1, 3),
                ("cjk", "你好 世界\n".encode(), 1, 2),
                ("emoji", "hi 👋🏽\n".encode(), 1, 2),
                ("line-separator", "a\u2028b".encode(), 1, 2),
            ]
            args = []
        results = []
        for name, data, lines, words in cases:
            run = subprocess.run([str(binary), *args], input=data, capture_output=True, timeout=10)
            expected = {"lines": lines, "words": words, "bytes": len(data)}
            if task == "json":
                try:
                    actual = json.loads(run.stdout)
                except (ValueError, UnicodeError):
                    actual = None
                matches = (actual == expected and isinstance(actual, dict)
                           and all(type(value) is int for value in actual.values())
                           and run.stdout.endswith(b"\n"))
            else:
                matches = run.stdout == f"lines={lines} words={words} bytes={len(data)}\n".encode()
            results.append({"case": name, "passed": run.returncode == 0 and not run.stderr and matches})
        if task == "unicode":
            invalid = subprocess.run([str(binary)], input=b"bad\xfftext", capture_output=True, timeout=10)
            results.append({"case": "invalid-utf8", "passed": invalid.returncode != 0
                            and not invalid.stdout and bool(invalid.stderr)})
        unknown = subprocess.run([str(binary), "--unknown"], input=b"", capture_output=True, timeout=10)
        results.append({"case": "unknown-flag", "passed": unknown.returncode != 0
                        and not unknown.stdout and bool(unknown.stderr)})
        if task == "json":
            plain = subprocess.run([str(binary)], input=b"one two\n", capture_output=True, timeout=10)
            results.append({"case": "plain-compatibility", "passed": plain.returncode == 0
                            and plain.stdout == b"lines=1 words=2 bytes=8\n" and not plain.stderr})
        return results


def main():
    parser = argparse.ArgumentParser(description=__doc__, epilog="Exit: 0 pass; 2 usage/dependency; 4 acceptance failure.")
    parser.add_argument("--repo", required=True, type=Path, help="checkout containing the Go CLI")
    parser.add_argument("--task", required=True, choices=("json", "unicode"))
    args = parser.parse_args()
    try:
        results = check(args.repo.resolve(strict=True), args.task)
    except (OSError, subprocess.TimeoutExpired) as error:
        print(f"acceptance: {error}; check the checkout and local Go installation", file=sys.stderr)
        return 2
    passed = all(item["passed"] for item in results)
    print(json.dumps({"task": args.task, "passed": passed, "cases": results}, indent=2))
    return 0 if passed else 4


if __name__ == "__main__":
    sys.exit(main())
