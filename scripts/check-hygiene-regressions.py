#!/usr/bin/env python3
"""Run the real scanner regressions; an unavailable scanner is a failed gate."""
import json
import shutil
import subprocess

required = {"TestGitleaksGoNoiseAcrossScopes", "TestGitleaksGoNoiseDoesNotHideCredentials", "TestGitleaksGoHistoryRetainsDeletedAndMergeOnlyCredentials"}
if not shutil.which("gitleaks"):
    raise SystemExit("gitleaks must be installed before this required gate")
result = subprocess.run(["go", "test", "./internal/hygiene", "-run", "^TestGitleaksGo", "-count=1", "-json"], capture_output=True, text=True)
passed = set()
for line in result.stdout.splitlines():
    event = json.loads(line)
    if event.get("Output"):
        print(event["Output"], end="")
    if event.get("Action") == "skip":
        raise SystemExit("required scanner regression was skipped")
    if event.get("Action") == "pass":
        passed.add(event.get("Test"))
if result.returncode or not required <= passed:
    print(result.stderr)
    raise SystemExit("required scanner regressions did not all pass")
