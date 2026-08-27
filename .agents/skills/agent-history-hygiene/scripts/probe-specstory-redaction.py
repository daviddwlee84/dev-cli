#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Measure which secret classes SpecStory's native redaction actually covers.

SpecStory >= 2.4.0 redacts secrets from saved markdown (and cloud-synced
session data) by default, using the betterleaks ruleset. This probe answers
the only question that matters for our pre-commit layer: *what does it miss?*

Method (no real agent session required):

  1. Synthesize a Claude Code session JSONL under
     ~/.claude/projects/<slug>/<uuid>.jsonl for a throwaway project dir.
  2. Render it twice via `specstory sync claude -s <uuid> --print`:
     once with redaction on (default), once with --no-redact-secrets.
  3. Diff per token class -> covered / not covered, capturing the literal
     [REDACTED:<label>] placeholder SpecStory emitted.
  4. Re-scan the redacted markdown with our own .gitleaks.toml rules. Whatever
     still fires is the residual set our redactor must keep handling.

Every probe token is generated at runtime from a seeded PRNG, so this file
contains key *shapes* but never a literal high-entropy token that would trip
a secret scanner on its own source.

Usage:
    ./probe-specstory-redaction.py             # markdown table on stdout
    ./probe-specstory-redaction.py --json      # JSON lines on stdout
    ./probe-specstory-redaction.py --keep      # keep the synthetic session
    ./probe-specstory-redaction.py --dry-run   # print the plan, touch nothing

Exit codes:
    0   probe completed
    2   `specstory sync` failed
   30   specstory CLI not on PATH
"""
from __future__ import annotations

import argparse
import json
import random
import re
import shutil
import string
import subprocess
import sys
import tempfile
import uuid
from datetime import datetime, timezone
from pathlib import Path

SKILL_ROOT = Path(__file__).resolve().parent.parent
GITLEAKS_CONFIG = SKILL_ROOT / "assets" / "gitleaks.toml.template"

ALNUM = string.ascii_letters + string.digits
B64ISH = ALNUM + "_-"
HEX = "0123456789abcdef"
UPPER_NUM = string.ascii_uppercase + string.digits


def make_catalog(rng: random.Random) -> list[dict]:
    """Build one synthetic token per secret class.

    Each entry: name, token, note. Tokens are shape-accurate (they satisfy the
    corresponding gitleaks/betterleaks regex) and high-entropy enough to clear
    entropy thresholds, but are random garbage with no real credential behind
    them.
    """

    def r(n: int, alphabet: str = ALNUM) -> str:
        return "".join(rng.choice(alphabet) for _ in range(n))

    def entry(name: str, token: str, note: str = "") -> dict:
        return {"name": name, "token": token, "note": note}

    return [
        # --- classes gitleaks defaults and betterleaks both claim to cover ---
        entry("anthropic-api-key", f"sk-ant-api03-{r(93, B64ISH)}AA"),
        entry("openai-project-key", f"sk-proj-{r(100, B64ISH)}"),
        entry("openai-legacy-key", f"sk-{r(20)}T3BlbkFJ{r(20)}"),
        entry("github-pat", f"ghp_{r(36)}"),
        entry("github-fine-grained-pat", f"github_pat_{r(22)}_{r(59)}"),
        entry("github-oauth-token", f"gho_{r(36)}"),
        entry("github-actions-token", f"ghs_{r(36)}"),
        entry("gitlab-pat", f"glpat-{r(20, B64ISH)}"),
        entry("aws-access-key-id", f"AKIA{r(16, UPPER_NUM)}"),
        entry("google-api-key", f"AIza{r(35, B64ISH)}"),
        entry("groq-api-key", f"gsk_{r(52)}"),
        entry("slack-bot-token", f"xoxb-{r(11, string.digits)}-{r(12, string.digits)}-{r(24)}"),
        entry("stripe-secret-key", f"sk_live_{r(24)}"),
        entry("telegram-bot-token", f"{r(10, string.digits)}:A{r(34, B64ISH)}"),
        # --- classes covered by OUR custom rules in gitleaks.toml.template ---
        entry("cursor-api-key", f"cursor-{r(40)}"),
        entry("huggingface-token", f"hf_{r(37)}"),
        entry("supabase-service-pat", f"sbp_{r(40, HEX)}"),
        entry("linear-api-key", f"lin_api_{r(40)}"),
        entry("tailscale-auth-key", f"tskey-auth-{r(12)}-{r(24)}"),
        entry("notion-integration-token", f"ntn_{r(46)}"),
        entry("wakatime-api-key", f"waka_{r(8, HEX)}-{r(4, HEX)}-{r(4, HEX)}-{r(4, HEX)}-{r(12, HEX)}"),
        entry("discord-webhook-url",
              f"https://discord.com/api/webhooks/{r(18, string.digits)}/{r(68, B64ISH)}"),
        entry("zapier-webhook-url",
              f"https://hooks.zapier.com/hooks/catch/{r(8, string.digits)}/{r(7)}/"),
        entry("make-webhook-url", f"https://hook.eu1.make.com/{r(34)}"),
        entry("stripe-webhook-secret", f"whsec_{r(32)}"),
        # --- shape cases that are not a single prefixed token ---
        entry(
            "private-key-pem",
            "-----BEGIN RSA PRIVATE "
            + "KEY-----\n"
            + "\n".join(r(64, ALNUM + "+/") for _ in range(3))
            + "\n-----END RSA PRIVATE "
            + "KEY-----",
            note="multi-line PEM block",
        ),
        entry(
            "dotenv-dump",
            "DATABASE_URL=postgres://appuser:" + r(24) + "@db.internal:5432/prod\n"
            "SESSION_SECRET=" + r(48) + "\n"
            "SMTP_PASSWORD=" + r(20),
            note="`cat .env` style block",
        ),
    ]


# Negative controls: documentation-shaped strings that BOTH layers must leave
# alone. If SpecStory redacts these, transcripts lose the ability to discuss
# key shapes at all -- worth knowing.
NEGATIVE_CONTROLS = [
    ("example-truncated-openai", "sk-proj-XXX..."),
    ("example-truncated-anthropic", "sk-ant-api03-XXX..."),
    ("example-placeholder", "your-api-key-here"),
    ("example-marker", "example-key"),
    ("already-redacted-sentinel", "OPENAI_API_KEY=REDACTED"),
]

CLAUDE_CODE_VERSION = "2.1.207"


_SLUG_UNSAFE_RE = re.compile(r"[^A-Za-z0-9]")


def slug_for(path: Path) -> str:
    """Claude Code project slug for an absolute path.

    Every non-alphanumeric character becomes '-', not just '/'. Verified
    against a real session: `/home/<user>/.local/share/chezmoi` is stored as
    `-home-<user>--local-share-chezmoi` (the dot collapses to a second dash).
    Getting this wrong makes `specstory sync` report "1 session not found",
    which is why the probe also keeps its own temp path alphanumeric.
    """
    return _SLUG_UNSAFE_RE.sub("-", str(path))


def build_session(project_dir: Path, catalog: list[dict]) -> tuple[Path, str]:
    """Write a synthetic Claude Code session JSONL. Returns (jsonl, session_id)."""
    session_id = str(uuid.uuid4())
    claude_dir = Path.home() / ".claude" / "projects" / slug_for(project_dir)
    claude_dir.mkdir(parents=True, exist_ok=True)
    jsonl = claude_dir / f"{session_id}.jsonl"

    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")
    common = {
        "isSidechain": False,
        "userType": "external",
        "entrypoint": "cli",
        "cwd": str(project_dir),
        "sessionId": session_id,
        "version": CLAUDE_CODE_VERSION,
        "gitBranch": "main",
    }

    lines: list[dict] = []
    parent: str | None = None

    def add_user(text: str) -> None:
        nonlocal parent
        node = str(uuid.uuid4())
        lines.append(
            {
                "parentUuid": parent,
                "type": "user",
                "message": {"role": "user", "content": text},
                "uuid": node,
                "timestamp": now,
                **common,
            }
        )
        parent = node

    def add_assistant(text: str) -> None:
        nonlocal parent
        node = str(uuid.uuid4())
        lines.append(
            {
                "parentUuid": parent,
                "type": "assistant",
                "message": {
                    "id": f"msg_{uuid.uuid4().hex[:20]}",
                    "type": "message",
                    "role": "assistant",
                    "model": "claude-opus-4-8",
                    "content": [{"type": "text", "text": text}],
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 1, "output_tokens": 1},
                },
                "uuid": node,
                "timestamp": now,
                **common,
            }
        )
        parent = node

    add_user("Probe session for agent-history-hygiene. Each turn carries one "
             "secret class in two contexts: an assignment and bare prose.")
    add_assistant("Understood, echoing each one back.")

    for item in catalog:
        # Two distinct tokens per class: a shared token would let a single
        # replacement mask both contexts and fake a "covered" result.
        add_user(
            f"[class:{item['name']}:assign]\n"
            f"{item['name'].upper().replace('-', '_')}={item['token']}"
        )
        add_assistant(
            f"[class:{item['name']}:prose]\n"
            f"The tool printed {item['token_prose']} to stdout just now."
        )

    add_user(
        "[class:negative-controls]\n"
        + "\n".join(f"{name}: {value}" for name, value in NEGATIVE_CONTROLS)
    )
    add_assistant("Probe complete.")

    with jsonl.open("w", encoding="utf-8") as f:
        for line in lines:
            f.write(json.dumps(line, ensure_ascii=False) + "\n")

    return jsonl, session_id


def run_sync(project_dir: Path, session_id: str, redact: bool) -> str:
    cmd = [
        "specstory", "sync", "claude",
        "-s", session_id,
        "--print",
        "--no-cloud-sync",
        "--no-stats",
        "--no-usage-analytics",
        "--no-version-check",
        "--silent",
    ]
    if not redact:
        cmd.append("--no-redact-secrets")
    proc = subprocess.run(cmd, cwd=project_dir, capture_output=True, text=True)
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout).strip()
        print(f"specstory sync failed (rc={proc.returncode}): {detail}",
              file=sys.stderr)
        if "session not found" in detail:
            print("hint: the synthetic session's Claude project slug did not "
                  "match. Check slug_for() against a real "
                  "~/.claude/projects/<slug> entry.", file=sys.stderr)
        raise SystemExit(2)
    return proc.stdout


_PLACEHOLDER_RE = re.compile(r"\[REDACTED:[^\]\s]+\]")


def classify(markdown: str, class_name: str, context: str, token: str) -> dict:
    """Determine whether `token` survived redaction in the given block."""
    marker = f"[class:{class_name}:{context}]"
    idx = markdown.find(marker)
    block = ""
    if idx >= 0:
        # The block runs until the next marker or the next section separator.
        rest = markdown[idx + len(marker):]
        stop = rest.find("[class:")
        block = rest if stop < 0 else rest[:stop]

    present = token in block if block else token in markdown
    placeholders = _PLACEHOLDER_RE.findall(block)
    return {
        "present": present,
        "placeholders": sorted(set(placeholders)),
        "block_found": idx >= 0,
    }


def rule_for(token: str, findings: list[dict]) -> str | None:
    """Which of OUR gitleaks rules (if any) claims this token."""
    for finding in findings:
        secret = finding.get("Secret", "")
        if not secret:
            continue
        if secret in token or token in secret:
            return finding.get("RuleID", "?")
    return None


def gitleaks_scan(markdown: str) -> list[dict]:
    """Scan text with OUR gitleaks config; return findings."""
    if shutil.which("gitleaks") is None:
        return []
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        report = f.name
    cmd = [
        "gitleaks", "stdin",
        "--report-format", "json",
        "--report-path", report,
        "--exit-code", "0",
    ]
    if GITLEAKS_CONFIG.is_file():
        cmd.extend(["--config", str(GITLEAKS_CONFIG)])
    try:
        subprocess.run(cmd, input=markdown, capture_output=True, text=True)
        content = Path(report).read_text(encoding="utf-8", errors="replace")
        return json.loads(content) if content.strip() else []
    except (json.JSONDecodeError, FileNotFoundError):
        return []
    finally:
        Path(report).unlink(missing_ok=True)


def specstory_version() -> str | None:
    if shutil.which("specstory") is None:
        return None
    proc = subprocess.run(["specstory", "--version"], capture_output=True, text=True)
    match = re.search(r"Current version:\s*([0-9][^\s│|]*)", proc.stdout)
    if match:
        return match.group(1).strip()
    match = re.search(r"([0-9]+\.[0-9]+\.[0-9]+)", proc.stdout)
    return match.group(1) if match else "unknown"


def redaction_config_state() -> str:
    """Report the effective [redaction] enabled setting we can see from config."""
    for candidate in (Path(".specstory/cli/config.toml"),
                      Path.home() / ".specstory/cli/config.toml"):
        if not candidate.is_file():
            continue
        text = candidate.read_text(encoding="utf-8", errors="replace")
        match = re.search(r"^\s*\[redaction\](.*?)(?=^\s*\[|\Z)", text,
                          re.MULTILINE | re.DOTALL)
        if match and re.search(r"^\s*enabled\s*=\s*false", match.group(1), re.MULTILINE):
            return f"disabled by {candidate}"
        if match:
            return f"[redaction] block present in {candidate} (default on)"
    return "no [redaction] block in config -> default on"


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Probe which secret classes SpecStory's native redaction covers.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--json", action="store_true",
                        help="Emit JSON lines on stdout instead of a markdown table")
    parser.add_argument("--keep", action="store_true",
                        help="Keep the synthetic session + project dir for inspection")
    parser.add_argument("--dry-run", action="store_true",
                        help="Describe what would run; create nothing")
    parser.add_argument("--seed", type=int, default=20260818,
                        help="PRNG seed for synthetic tokens (default: 20260818)")
    args = parser.parse_args()

    version = specstory_version()
    if version is None:
        print("specstory not found on PATH -- install it to run this probe.",
              file=sys.stderr)
        return 30

    if args.dry_run:
        print(f"specstory {version}; redaction config: {redaction_config_state()}",
              file=sys.stderr)
        print("Would create a temp project dir, write a synthetic Claude Code "
              "session under ~/.claude/projects/<slug>/, render it twice via "
              "`specstory sync claude --print` (with and without "
              "--no-redact-secrets), and diff per class.", file=sys.stderr)
        return 0

    rng = random.Random(args.seed)
    catalog = make_catalog(rng)
    # Second token per class for the prose context.
    prose_rng = random.Random(args.seed + 1)
    for item, prose_item in zip(catalog, make_catalog(prose_rng)):
        item["token_prose"] = prose_item["token"]

    # Alphanumeric suffix on purpose: tempfile.mkdtemp() can emit '_', and a
    # slug-collapsed character makes the session undiscoverable (see slug_for).
    project_dir = Path(tempfile.gettempdir()) / f"specstoryprobe{uuid.uuid4().hex[:12]}"
    project_dir.mkdir(parents=True, exist_ok=False)
    jsonl, session_id = build_session(project_dir, catalog)
    claude_dir = jsonl.parent

    print(f"specstory {version}; redaction config: {redaction_config_state()}",
          file=sys.stderr)
    print(f"synthetic session: {jsonl}", file=sys.stderr)

    try:
        redacted = run_sync(project_dir, session_id, redact=True)
        baseline = run_sync(project_dir, session_id, redact=False)
    finally:
        if not args.keep:
            shutil.rmtree(claude_dir, ignore_errors=True)
            shutil.rmtree(project_dir, ignore_errors=True)
        else:
            print(f"kept: {claude_dir} and {project_dir}", file=sys.stderr)

    base_findings = gitleaks_scan(baseline)
    red_findings = gitleaks_scan(redacted)

    rows: list[dict] = []
    for item in catalog:
        for context, token in (("assign", item["token"]),
                               ("prose", item["token_prose"])):
            base = classify(baseline, item["name"], context, token)
            red = classify(redacted, item["name"], context, token)
            covered = base["present"] and not red["present"]
            ours = rule_for(token, base_findings)
            if covered:
                verdict = "specstory"
            elif ours:
                verdict = "ours-residual"
            else:
                verdict = "uncovered"
            rows.append({
                "class": item["name"],
                "context": context,
                "in_baseline": base["present"],
                "in_redacted": red["present"],
                "covered": covered,
                "our_rule": ours,
                "verdict": verdict,
                "placeholders": red["placeholders"],
                "note": item.get("note", ""),
            })

    for name, value in NEGATIVE_CONTROLS:
        rows.append({
            "class": f"negative:{name}",
            "context": "control",
            "in_baseline": value in baseline,
            "in_redacted": value in redacted,
            # For a negative control, "covered" means SpecStory wrongly redacted it.
            "covered": (value in baseline) and (value not in redacted),
            "our_rule": rule_for(value, base_findings),
            "verdict": "control",
            "placeholders": [],
            "note": "must survive redaction",
        })

    residual_rules = sorted({f.get("RuleID", "?") for f in red_findings})

    if args.json:
        for row in rows:
            print(json.dumps({"specstory": version, **row}, ensure_ascii=False))
        print(json.dumps({
            "specstory": version,
            "residual_gitleaks_rules": residual_rules,
            "residual_finding_count": len(red_findings),
        }, ensure_ascii=False))
    else:
        print(f"# SpecStory native redaction coverage (specstory {version})\n")
        print("| Class | Context | Caught by | Placeholder / our rule |")
        print("|---|---|---|---|")
        for row in rows:
            if not row["in_baseline"]:
                caught = "n/a (absent from baseline)"
                detail = "—"
            elif row["class"].startswith("negative:"):
                caught = "**redacted (false positive)**" if row["covered"] else "left alone"
                detail = f"`{row['our_rule']}`" if row["our_rule"] else "—"
            elif row["verdict"] == "specstory":
                caught = "SpecStory"
                detail = ", ".join(f"`{p}`" for p in row["placeholders"]) or "—"
            elif row["verdict"] == "ours-residual":
                caught = "**ours only**"
                detail = f"`{row['our_rule']}`"
            else:
                caught = "**nobody**"
                detail = "—"
            print(f"| `{row['class']}` | {row['context']} | {caught} | {detail} |")
        print()
        counts = {v: sum(1 for r in rows if r.get("verdict") == v)
                  for v in ("specstory", "ours-residual", "uncovered")}
        print(f"SpecStory: {counts['specstory']} · ours only: "
              f"{counts['ours-residual']} · uncovered by both: {counts['uncovered']}")
        print(f"\nResidual gitleaks findings against our config after SpecStory "
              f"redaction: {len(red_findings)}")
        if residual_rules:
            print("Rules still firing: " + ", ".join(f"`{r}`" for r in residual_rules))

    missed = [r for r in rows
              if r["in_baseline"] and not r["covered"]
              and not r["class"].startswith("negative:")]
    print(f"\n{len(rows)} checks, {len(missed)} class/context pairs NOT covered "
          f"by SpecStory.", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
