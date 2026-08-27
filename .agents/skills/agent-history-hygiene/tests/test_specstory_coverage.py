"""Regression tests for SpecStory's native secret redaction.

SpecStory >= 2.4.0 redacts secrets when it writes a transcript. Two facts
about that behavior are load-bearing for this skill:

1. It still redacts at all -- otherwise `.specstory/history/` is only as
   protected as our own pre-commit layer.
2. Its placeholder is still `[REDACTED:<label>]`. Our redactor writes the same
   shape and `.gitleaks.toml` allowlists it, which is what makes the two
   layers idempotent instead of fighting each other over every commit.

If upstream changes either one, this fails loudly rather than silently
re-introducing the redact -> re-stage -> re-commit loop.

Skipped entirely when the specstory CLI is not installed.
"""
from __future__ import annotations

import json
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

SCRIPTS_DIR = Path(__file__).resolve().parent.parent / "scripts"
PROBE = SCRIPTS_DIR / "probe-specstory-redaction.py"

pytestmark = pytest.mark.skipif(
    shutil.which("specstory") is None,
    reason="specstory CLI not installed",
)


@pytest.fixture(scope="module")
def probe_rows() -> list[dict]:
    """Run the coverage probe once and return its JSON-line rows."""
    proc = subprocess.run(
        [sys.executable, str(PROBE), "--json"],
        capture_output=True,
        text=True,
        timeout=300,
    )
    if proc.returncode == 30:
        pytest.skip("specstory CLI not usable")
    assert proc.returncode == 0, f"probe failed: {proc.stderr}"
    rows = [json.loads(line) for line in proc.stdout.splitlines() if line.strip()]
    assert rows, "probe produced no rows"
    return rows


def _row(rows: list[dict], cls: str, context: str) -> dict:
    for row in rows:
        if row.get("class") == cls and row.get("context") == context:
            return row
    raise AssertionError(f"probe produced no row for {cls}/{context}")


class TestSpecStoryStillRedacts:
    def test_redaction_is_on_by_default(self, probe_rows):
        """A bare Anthropic key must not survive a default `specstory sync`."""
        row = _row(probe_rows, "anthropic-api-key", "prose")
        assert row["in_baseline"], "probe token never reached the markdown"
        assert row["covered"], (
            "SpecStory no longer redacts an Anthropic key -- check whether "
            "[redaction] got disabled or upstream changed the ruleset"
        )

    def test_pem_private_key_block_is_redacted(self, probe_rows):
        row = _row(probe_rows, "private-key-pem", "assign")
        assert row["covered"]


class TestPlaceholderShape:
    """The shared `[REDACTED:<label>]` sentinel our allowlist depends on."""

    def test_placeholder_shape_is_unchanged(self, probe_rows):
        placeholders = {
            p
            for row in probe_rows
            for p in row.get("placeholders", [])
        }
        assert placeholders, "SpecStory emitted no [REDACTED:*] placeholders"
        for placeholder in placeholders:
            assert placeholder.startswith("[REDACTED:")
            assert placeholder.endswith("]")

    def test_pem_label_matches_our_own(self, probe_rows):
        """We write `[REDACTED:private-key]` for PEM blocks; so does SpecStory."""
        row = _row(probe_rows, "private-key-pem", "assign")
        assert "[REDACTED:private-key]" in row["placeholders"]


class TestOurLayerIsStillNeeded:
    """Classes SpecStory misses -- the justification for keeping our hook.

    These are not upstream bugs to wait on: SpecStory catches most of them in
    `KEY=value` form via betterleaks' entropy-based generic-api-key rule, but
    not when the same token appears in prose, which is exactly how a tool
    transcript prints one.
    """

    @pytest.mark.parametrize(
        "cls",
        [
            "cursor-api-key",
            "tailscale-auth-key",
            "discord-webhook-url",
            "zapier-webhook-url",
            "make-webhook-url",
        ],
    )
    def test_class_is_ours_to_catch(self, probe_rows, cls):
        row = _row(probe_rows, cls, "prose")
        if row["covered"]:
            pytest.skip(f"upstream now covers {cls} -- coverage table needs a refresh")
        assert row["our_rule"], (
            f"{cls} is caught by neither SpecStory nor our gitleaks config"
        )


class TestNegativeControls:
    """Documentation-shaped strings must survive redaction in both layers."""

    @pytest.mark.parametrize(
        "cls",
        [
            "negative:example-truncated-openai",
            "negative:example-placeholder",
            "negative:example-marker",
        ],
    )
    def test_example_shapes_are_left_alone(self, probe_rows, cls):
        row = _row(probe_rows, cls, "control")
        assert row["in_redacted"], (
            f"SpecStory redacted {cls} -- transcripts can no longer discuss "
            "key shapes in prose"
        )
