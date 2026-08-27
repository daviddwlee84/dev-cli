#!/usr/bin/env python3
# /// script
# requires-python = ">=3.9"
# dependencies = []
# ///
# NOTE: stdlib-only, so a plain `python3` shebang is the portable default
# (works on macOS system 3.9, CI, and as a pre-commit `language: script` hook
# with no uv dependency). The PEP 723 block above still lets you run it under
# `uv run --script redact_secrets.py` if you prefer an isolated interpreter.
"""
Check for secrets in agent artifact directories (specstory history + coding
agent plan/rules dirs) using gitleaks and detect-private-key patterns.
Reports findings and suggests redaction.

Default covered prefixes (kept in sync with assets/artifact-dirs.txt):
    .specstory/history/   (SpecStory chat transcripts)
    .claude/plans/        (Claude Code plans)
    .cursor/plans/        (Cursor plans)
    .cursor/rules/        (Cursor rules)
    .opencode/plans/      (OpenCode plans)
    .specify/             (GitHub spec-kit artifacts)
    .codex/               (Codex CLI artifacts)

Redacted secrets are replaced with `[REDACTED:<rule-id>]`, the same sentinel
shape SpecStory >= 2.4.0 writes natively (its binary carries the format string
`[REDACTED:%s]`). Sharing one shape keeps the two layers idempotent: whichever
runs first, the other sees an inert placeholder and leaves the file alone. See
references/specstory-native-redaction.md for measured coverage of what
SpecStory already handles and what is left to us.

Usage:
    ./redact_secrets.py                         # Check staged files (default paths)
    ./redact_secrets.py --fix                   # Auto-redact and re-stage files
    ./redact_secrets.py --working-dir           # Scan working directory instead of staged
    ./redact_secrets.py --paths .specstory/history
    ./redact_secrets.py --fix --paths .cursor/plans .claude/plans
    ./redact_secrets.py --fix --legacy         # pre-2.4.0 placeholder style
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import tempfile
from pathlib import Path


DEFAULT_PATHS = [
    ".specstory/history",
    ".claude/plans",
    ".cursor/plans",
    ".cursor/rules",
    ".opencode/plans",
    ".specify",
    ".codex",
]

# Repo-root gitleaks config; we pass it explicitly so custom rules apply
# even when the script is invoked from a different CWD (pre-commit etc.).
GITLEAKS_CONFIG = ".gitleaks.toml"


def read_text(path: Path) -> str:
    """Read as UTF-8 while preserving invalid bytes via surrogate escape."""
    return path.read_text(encoding="utf-8", errors="surrogateescape")


def write_text(path: Path, content: str) -> None:
    """Write UTF-8 while preserving surrogate-escaped bytes."""
    path.write_text(content, encoding="utf-8", errors="surrogateescape")


def run_gitleaks_staged() -> list[dict]:
    """Run gitleaks on staged files and return findings as JSON."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
        report_path = f.name

    # Post v8.19.0 `gitleaks protect` is deprecated in favor of `gitleaks git`.
    cmd = [
        "gitleaks",
        "git",
        "--staged",
        "--report-format",
        "json",
        "--report-path",
        report_path,
        "--exit-code",
        "0",
    ]
    if Path(GITLEAKS_CONFIG).is_file():
        cmd.extend(["--config", GITLEAKS_CONFIG])
    try:
        subprocess.run(cmd, capture_output=True, text=True)
        content = read_text(Path(report_path))
        if not content.strip():
            return []
        return json.loads(content)
    except (json.JSONDecodeError, FileNotFoundError):
        return []
    finally:
        Path(report_path).unlink(missing_ok=True)


def run_gitleaks_workdir(target_path: str) -> list[dict]:
    """Scan every *.md under target_path and return combined findings.

    We pipe each file through `gitleaks stdin` instead of `gitleaks dir`
    because the latter silently skips hidden directories (e.g.
    `.claude/plans/`) in gitleaks >= 8.x, which is exactly where our
    agent artifacts live.
    """
    findings: list[dict] = []
    target = Path(target_path)
    if not target.exists():
        return findings
    for md_file in sorted(target.rglob("*.md")):
        if not md_file.is_file():
            continue
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False) as f:
            report_path = f.name
        cmd = [
            "gitleaks",
            "stdin",
            "--report-format",
            "json",
            "--report-path",
            report_path,
            "--exit-code",
            "0",
        ]
        if Path(GITLEAKS_CONFIG).is_file():
            cmd.extend(["--config", GITLEAKS_CONFIG])
        try:
            with open(md_file, "rb") as src:
                subprocess.run(cmd, stdin=src, capture_output=True)
            content = read_text(Path(report_path))
            if content.strip():
                file_findings = json.loads(content)
                # `gitleaks stdin` reports File="" — rewrite to the real path
                # so downstream code can match against it.
                for finding in file_findings:
                    finding["File"] = str(md_file)
                findings.extend(file_findings)
        except (json.JSONDecodeError, FileNotFoundError):
            pass
        finally:
            Path(report_path).unlink(missing_ok=True)
    return findings


def redact_secret(secret: str, keep_chars: int = 3) -> str:
    """Redact secret keeping first/last N chars: sk-abc...xyz

    This is the *console report* form -- a fingerprint helps identify which
    credential to rotate. What lands in the file is redaction_placeholder().
    Under --legacy it is also what gets written to disk.
    """
    if len(secret) <= keep_chars * 2 + 3:
        return "[REDACTED]"
    return f"{secret[:keep_chars]}...{secret[-keep_chars:]}"


# Sentinel shape shared with SpecStory's native redaction (`[REDACTED:%s]`).
_RULE_SLUG_RE = re.compile(r"[^A-Za-z0-9._-]+")


def redaction_placeholder(rule_id: str) -> str:
    """`[REDACTED:<rule-id>]` -- what actually replaces a secret in the file.

    The rule id names the credential type, which is what a reader needs in
    order to know what to rotate; keeping no bytes of the secret means the
    placeholder can never be re-flagged by a scanner, so a re-run is a no-op.
    """
    slug = _RULE_SLUG_RE.sub("-", rule_id or "").strip("-").lower()
    return f"[REDACTED:{slug or 'secret'}]"


def redact_file(file_path: Path, findings: list[dict], legacy: bool = False) -> bool:
    """Redact secrets in a file. Returns True if modified."""
    content = read_text(file_path)
    original = content

    file_findings = [
        f for f in findings if Path(f["File"]).resolve() == file_path.resolve()
    ]

    for finding in file_findings:
        secret = finding.get("Secret", "")
        if secret and secret in content:
            replacement = (
                redact_secret(secret)
                if legacy
                else redaction_placeholder(finding.get("RuleID", ""))
            )
            content = content.replace(secret, replacement)

    if content != original:
        write_text(file_path, content)
        return True
    return False


def filter_by_prefixes(findings: list[dict], prefixes: list[str]) -> list[dict]:
    """Filter findings whose File path contains any of the given prefixes."""
    normalized = [p.rstrip("/") + "/" for p in prefixes]
    return [
        f
        for f in findings
        if any(p in f.get("File", "") for p in normalized)
    ]


# Matches actual PEM private key blocks (header … base64 body … footer).
_PEM_BLOCK_RE = re.compile(
    r"-----BEGIN[^-]*PRIVATE KEY[^-]*-----.*?-----END[^-]*PRIVATE KEY[^-]*-----",
    re.DOTALL,
)

# The private-key *header* tokens the downstream detect-private-key hook greps
# for (pre-commit/pre-commit-hooks BLACKLIST, matched as plain substrings via
# `any(line in content for line in BLACKLIST)`). These headers — NOT the bare
# phrase "PRIVATE KEY" — are what actually fail a commit, so the redactor scopes
# to exactly them. Matching the bare phrase instead used to flag and mangle
# prose that merely *discusses* private keys; against a live transcript writer
# that re-appends the words on every diagnostic command, that never converged.
# See the "Active SpecStory writer can defeat the redact loop" pitfall in
# SKILL.md.
#
# NOTE: the OpenVPN token is split across two adjacent string literals on
# purpose so THIS source file does not itself contain the contiguous BLACKLIST
# string — otherwise detect-private-key would flag redact_secrets.py in every
# repo that installs it. Do not join them back together.
_PRIVATE_KEY_HEADER_RE = re.compile(
    r"BEGIN [A-Z0-9 ]*PRIVATE KEY"      # RSA/DSA/EC/OPENSSH/PGP/ENCRYPTED/SSH2/plain
    r"|PuTTY-User-Key-File-\d"
    r"|BEGIN OpenVPN " r"Static key V1"
)


def find_private_key_files(files: list[Path]) -> dict[Path, list[str]]:
    """Find files containing private-key material. Returns {path: [descriptions]}.

    Only real key indicators count: a full PEM block, or a stray key *header*
    token (a truncated key, or a header quoted in prose) that the downstream
    detect-private-key hook would grep for. Bare "PRIVATE KEY" prose is
    deliberately ignored — detect-private-key ignores it too, and flagging it
    caused a non-converging redact loop.
    """
    results: dict[Path, list[str]] = {}
    for path in files:
        if not path.is_file() or path.suffix != ".md":
            continue
        try:
            content = read_text(path)
        except OSError:
            continue
        matches = []
        pem_blocks = _PEM_BLOCK_RE.findall(content)
        if pem_blocks:
            matches.append(f"{len(pem_blocks)} PEM private key block(s)")
        # Count header tokens OUTSIDE full PEM blocks (which are handled above):
        # truncated keys, or a header quoted in prose.
        without_pem = _PEM_BLOCK_RE.sub("", content)
        header_count = len(_PRIVATE_KEY_HEADER_RE.findall(without_pem))
        if header_count:
            matches.append(f"{header_count} private-key header(s)")
        if matches:
            results[path] = matches
    return results


def redact_private_keys(file_path: Path, legacy: bool = False) -> bool:
    """Redact private-key material in a file. Returns True if modified.

    Scrubs full PEM blocks and any stray key *header* tokens — exactly what the
    detect-private-key hook greps for. The bare phrase "PRIVATE KEY" in prose is
    left untouched on purpose: redacting it mangled legitimate text and never
    converged against a live transcript writer. Both placeholders contain
    neither a header token nor the bare phrase, so a re-run is a no-op --
    `[REDACTED:private-key]` is lowercase, while the header regex matches only
    uppercase `BEGIN ... PRIVATE KEY`. `[REDACTED:private-key]` is also the
    exact label SpecStory emits for this class.
    """
    content = read_text(file_path)
    original = content
    pem_placeholder = (
        "[REDACTED PEM PRIVKEY BLOCK]" if legacy else "[REDACTED:private-key]"
    )
    header_placeholder = (
        "[REDACTED PRIVKEY HEADER]" if legacy else "[REDACTED:private-key-header]"
    )
    # Full PEM blocks first (header + body + footer).
    content = _PEM_BLOCK_RE.sub(pem_placeholder, content)
    # Any remaining stray header tokens (truncated keys / headers quoted in
    # prose) -- these alone would still trip detect-private-key.
    content = _PRIVATE_KEY_HEADER_RE.sub(header_placeholder, content)
    if content != original:
        write_text(file_path, content)
        return True
    return False


def main():
    parser = argparse.ArgumentParser(
        description=(
            "Check/redact secrets in agent artifact directories "
            "(specstory history + coding agent plan dirs)."
        )
    )
    parser.add_argument(
        "--fix", action="store_true", help="Auto-redact secrets and re-stage files"
    )
    parser.add_argument(
        "--working-dir",
        action="store_true",
        help="Scan working directory instead of staged files (default: scan staged)",
    )
    parser.add_argument(
        "--legacy",
        action="store_true",
        help=(
            "Write the pre-2.4.0 placeholders (`sk-abc...xyz` truncation, "
            "`[REDACTED PEM PRIVKEY BLOCK]`) instead of the `[REDACTED:<rule-id>]` "
            "sentinel shared with SpecStory's native redaction. Detection is "
            "unchanged; only the bytes written differ."
        ),
    )
    parser.add_argument(
        "--paths",
        nargs="+",
        default=DEFAULT_PATHS,
        metavar="PREFIX",
        help=(
            "Path prefixes to scan. Default: "
            + " ".join(DEFAULT_PATHS)
        ),
    )
    args = parser.parse_args()

    prefixes = [p.rstrip("/") for p in args.paths]
    existing_prefixes = [p for p in prefixes if Path(p).exists()]
    missing_prefixes = [p for p in prefixes if not Path(p).exists()]

    for p in missing_prefixes:
        print(f"Skipping missing path: {p}/")

    if not existing_prefixes:
        print("No target directories found; nothing to scan.")
        return 0

    prefix_list_str = ", ".join(f"{p}/" for p in existing_prefixes)

    # --- Detect gitleaks secrets ---
    if args.working_dir:
        print(f"Scanning working directory: {prefix_list_str}")
        findings: list[dict] = []
        for p in existing_prefixes:
            findings.extend(run_gitleaks_workdir(p))
    else:
        print(f"Scanning staged files under: {prefix_list_str}")
        all_findings = run_gitleaks_staged()
        findings = filter_by_prefixes(all_findings, existing_prefixes)

    has_issues = False

    if findings:
        has_issues = True
        print(f"\nFound {len(findings)} potential secret(s):\n")

        # Group by file
        by_file: dict[str, list[dict]] = {}
        for f in findings:
            file_path = f["File"]
            by_file.setdefault(file_path, []).append(f)

        for file_path, file_findings in by_file.items():
            print(f"  {file_path}:")
            for finding in file_findings:
                rule = finding.get("RuleID", "unknown")
                secret = finding.get("Secret", "")
                line = finding.get("StartLine", "?")
                redacted = redact_secret(secret)
                print(f"    Line {line}: [{rule}] {redacted}")
            print()
    else:
        by_file = {}

    # --- Detect private key patterns ---
    if args.working_dir:
        scan_files: list[Path] = []
        for p in existing_prefixes:
            scan_files.extend(Path(p).glob("*.md"))
    else:
        result = subprocess.run(
            ["git", "diff", "--cached", "--name-only", "--diff-filter=ACM"],
            capture_output=True,
            text=True,
        )
        staged_md = [
            Path(f)
            for f in result.stdout.strip().splitlines()
            if f.endswith(".md")
            and any(f.startswith(p + "/") for p in existing_prefixes)
        ]
        scan_files = staged_md

    pk_files = find_private_key_files(scan_files)
    if pk_files:
        has_issues = True
        print(f"Found private key pattern(s) in {len(pk_files)} file(s):\n")
        for path, descriptions in pk_files.items():
            print(f"  {path}:")
            for desc in descriptions:
                print(f"    {desc}")
            print()

    if not has_issues:
        print(f"No secrets found in: {prefix_list_str}")
        return 0

    if args.fix:
        print("Redacting secrets...")
        modified_files: set[Path] = set()

        # Redact gitleaks findings
        for file_path in by_file:
            path = Path(file_path)
            if path.suffix == ".md" and redact_file(
                path, findings, legacy=args.legacy
            ):
                modified_files.add(path)

        # Redact private key patterns
        for path in pk_files:
            if redact_private_keys(path, legacy=args.legacy):
                modified_files.add(path)

        for f in modified_files:
            print(f"  Redacted: {f}")

        # Verify private keys
        remaining_pk = find_private_key_files(
            [Path(f) for f in modified_files if Path(f).is_file()]
        )
        if remaining_pk:
            print("ERROR: Private key patterns still detected after redaction!")
            return 1

        print(f"\nSuccessfully redacted {len(modified_files)} file(s)")
        print("Review changes with: git diff")
        stage_hint = " ".join(f"{p}/" for p in existing_prefixes)
        print(f"Then stage with: git add {stage_hint}")
        return 0
    else:
        print("Run with --fix to auto-redact these secrets")
        print("Or manually edit the files to remove sensitive information")
        return 1


if __name__ == "__main__":
    sys.exit(main())
