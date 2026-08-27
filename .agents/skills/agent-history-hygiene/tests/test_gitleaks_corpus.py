"""Golden-corpus tests: stage each fixture in a throwaway git repo, run
`gitleaks git --staged` against `assets/gitleaks.toml.template`, and
assert the expected finding count.

These catch regressions in:
- allowlist semantics (e.g. defaulting to OR instead of AND)
- config-file compatibility (e.g. mixing `[allowlist]` with `[[allowlists]]`)
- path-scoped allowlist paths drifting away from the real artifact dirs

If gitleaks is not installed, the whole file skips.
"""
from __future__ import annotations

import json
import re
import shutil
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.skipif(
    shutil.which("gitleaks") is None,
    reason="gitleaks binary not on PATH",
)


# Fixtures carry an inline `<!-- gitleaks:allow -->` marker so a repo-root or
# downstream gitleaks scan skips them by default. Strip it before staging and
# expand provider-scanner-sensitive placeholders so the secret bytes have the
# exact shape the rules expect and the assertions below still fire. No-op for
# lines without the marker
# (clean.md / example_shapes.md), which must keep their must-not-fire behavior.
_GITLEAKS_MARKER_RE = re.compile(r"[ \t]*<!--[ ]?gitleaks:allow[ ]?-->")


def _stage_fixture_at(repo: Path, fixture_path: Path, dest_rel: str) -> None:
    """Copy `fixture_path` to `repo/dest_rel`, stripping the inline
    `gitleaks:allow` marker, and git-add it."""
    dest = repo / dest_rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    text = fixture_path.read_text(encoding="utf-8").replace(
        "__SYNTHETIC_STRIPE_WEBHOOK_SECRET__", "whsec_" + "a" * 32
    )
    dest.write_text(_GITLEAKS_MARKER_RE.sub("", text), encoding="utf-8")
    subprocess.run(["git", "add", "--", dest_rel], cwd=repo, check=True)


def _run_gitleaks_staged(repo: Path) -> list[dict]:
    """Run `gitleaks git --staged` and return the JSON findings array."""
    report = repo / "_report.json"
    result = subprocess.run(
        [
            "gitleaks",
            "git",
            "--staged",
            "--config",
            ".gitleaks.toml",
            "--report-format",
            "json",
            "--report-path",
            str(report),
            "--exit-code",
            "0",
        ],
        cwd=repo,
        capture_output=True,
        text=True,
    )
    # Non-zero would indicate config error — surface it for debugging.
    assert result.returncode == 0, (
        f"gitleaks failed:\nstdout: {result.stdout}\nstderr: {result.stderr}"
    )
    if not report.exists() or report.stat().st_size == 0:
        return []
    return json.loads(report.read_text(encoding="utf-8"))


class TestCleanFixture:
    def test_no_findings(self, tmp_git_repo, fixtures_dir: Path):
        _stage_fixture_at(tmp_git_repo, fixtures_dir / "clean.md", "notes.md")
        assert _run_gitleaks_staged(tmp_git_repo) == []


class TestRealOpenaiFixture:
    """The `sk-proj-` + 100-char filler key must fire in any path."""

    def test_fires_outside_artifact_dirs(self, tmp_git_repo, fixtures_dir: Path):
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "real_openai.md", "src/leaks.md"
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        assert "openai-project-key" in rule_ids

    def test_fires_inside_claude_plans(self, tmp_git_repo, fixtures_dir: Path):
        """Real leaks are never allowlisted, even inside agent-artifact dirs."""
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "real_openai.md", ".claude/plans/p1.md"
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        assert "openai-project-key" in rule_ids, (
            "Real OpenAI key in .claude/plans/ was incorrectly allowlisted — "
            "check condition=AND on the path-scoped allowlist"
        )


class TestRealAnthropicFixture:
    """The `sk-ant-api\\d{2}-...AA` shape must fire in any path."""

    def test_fires_outside_artifact_dirs(self, tmp_git_repo, fixtures_dir: Path):
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "real_anthropic.md", "src/leaks.md"
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        # Both the default `anthropic-api-key` and our custom strict variant
        # should match; at minimum one of the two must fire.
        assert rule_ids & {"anthropic-api-key", "anthropic-api-key-strict"}

    def test_fires_inside_specstory_history(self, tmp_git_repo, fixtures_dir: Path):
        _stage_fixture_at(
            tmp_git_repo,
            fixtures_dir / "real_anthropic.md",
            ".specstory/history/2026-04-24.md",
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        assert rule_ids & {"anthropic-api-key", "anthropic-api-key-strict"}


class TestExampleShapesFixture:
    """Truncated/placeholder shapes must be allowlisted inside artifact dirs."""

    @pytest.mark.parametrize(
        "dest",
        [
            ".claude/plans/p1.md",
            ".specstory/history/2026-04-24.md",
            ".cursor/plans/p1.md",
            ".cursor/rules/p1.md",
            ".opencode/plans/p1.md",
            ".specify/p1.md",
            ".codex/p1.md",
        ],
    )
    def test_allowlisted_in_each_artifact_dir(
        self, tmp_git_repo, fixtures_dir: Path, dest: str
    ):
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "example_shapes.md", dest
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        assert findings == [], (
            f"Example shapes in {dest} should be allowlisted; got {findings}"
        )

    def test_NOT_allowlisted_in_regular_path(
        self, tmp_git_repo, fixtures_dir: Path
    ):
        """The path-scoped allowlist must NOT apply outside artifact dirs.

        Here the global allowlist still covers truncated `sk-XXX...`
        patterns, but the explicit `your-api-key-here` / `example-key`
        placeholders might differ depending on gitleaks default rules.
        We assert only that the canonical allowlisted sentinels
        (REDACTED, truncated sk-.../sk-ant-...) don't fire regardless.
        """
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "example_shapes.md", "src/docs.md"
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        # No strict-shape keys in this fixture → no custom rules should fire.
        rule_ids = {f["RuleID"] for f in findings}
        assert "openai-project-key" not in rule_ids
        assert "anthropic-api-key-strict" not in rule_ids


class TestWebhookFixture:
    """Custom webhook rules for Discord / Zapier / Make.com / Stripe.

    The fixture also implicitly checks that gitleaks default rules for
    Slack/Teams/Telegram still work (no rule-name collision with our
    custom additions).
    """

    EXPECTED_RULE_IDS = {
        "discord-webhook-url",
        "zapier-webhook-url",
        "make-webhook-url",
        "stripe-webhook-secret",
    }

    def test_all_webhook_rules_fire_outside_artifact_dirs(
        self, tmp_git_repo, fixtures_dir: Path
    ):
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "webhook_urls.md", "src/leaks.md"
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        missing = self.EXPECTED_RULE_IDS - rule_ids
        assert not missing, (
            f"Expected webhook rules did not fire: {sorted(missing)}. "
            f"Got: {sorted(rule_ids)}"
        )

    def test_webhooks_NOT_allowlisted_in_artifact_dirs(
        self, tmp_git_repo, fixtures_dir: Path
    ):
        """Real-shape webhook tokens must still fire even inside
        .claude/plans/ etc. — the path-scoped allowlist only covers
        explicit example/REDACTED markers, not realistic tokens.
        """
        _stage_fixture_at(
            tmp_git_repo,
            fixtures_dir / "webhook_urls.md",
            ".claude/plans/leaky-plan.md",
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        missing = self.EXPECTED_RULE_IDS - rule_ids
        assert not missing, (
            f"Webhook leak in .claude/plans/ was incorrectly allowlisted: "
            f"{sorted(missing)}. Check condition=AND on path-scoped allowlist."
        )


class TestLocalCredentials:
    """age identities and plain password assignments.

    These differ from the SaaS-token rules: an age identity is the one
    secret that cannot be cheaply rotated, because it decrypts every
    committed `encrypted_*.age` blob in the repo's history.
    """

    EXPECTED_RULE_IDS = {
        "age-secret-key",
        "generic-password-assignment",
    }

    def test_rules_fire_outside_artifact_dirs(
        self, tmp_git_repo, fixtures_dir: Path
    ):
        _stage_fixture_at(
            tmp_git_repo, fixtures_dir / "local_credentials.md", "src/leaks.md"
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        missing = self.EXPECTED_RULE_IDS - rule_ids
        assert not missing, (
            f"Expected local-credential rules did not fire: {sorted(missing)}. "
            f"Got: {sorted(rule_ids)}"
        )

    def test_rules_NOT_allowlisted_in_artifact_dirs(
        self, tmp_git_repo, fixtures_dir: Path
    ):
        """Real-shape credentials must still fire inside .specstory/history/ —
        the path-scoped allowlist only covers explicit example/REDACTED
        markers, and agent transcripts are exactly where these two leak.
        """
        _stage_fixture_at(
            tmp_git_repo,
            fixtures_dir / "local_credentials.md",
            ".specstory/history/2026-01-01_00-00-00Z-session.md",
        )
        findings = _run_gitleaks_staged(tmp_git_repo)
        rule_ids = {f["RuleID"] for f in findings}
        missing = self.EXPECTED_RULE_IDS - rule_ids
        assert not missing, (
            f"Local-credential leak in .specstory/history/ was incorrectly "
            f"allowlisted: {sorted(missing)}. Check condition=AND."
        )


class TestConfigValidity:
    """The bundled gitleaks.toml.template must load without errors."""

    def test_gitleaks_accepts_our_config(self, tmp_git_repo):
        """A bad config (e.g. `[allowlist]` mixed with `[[allowlists]]`)
        causes gitleaks to exit non-zero with `FTL Failed to load config`.
        `_run_gitleaks_staged` asserts rc=0, so this passing is itself
        the guarantee.
        """
        # Stage a neutral file and just confirm the run returns cleanly.
        (tmp_git_repo / "neutral.md").write_text("no secrets\n", encoding="utf-8")
        subprocess.run(["git", "add", "neutral.md"], cwd=tmp_git_repo, check=True)
        findings = _run_gitleaks_staged(tmp_git_repo)
        assert findings == []
