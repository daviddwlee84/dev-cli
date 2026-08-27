"""Unit tests for the pure functions in assets/redact_secrets.py.

These cover the redaction primitives independent of gitleaks, so they
pass even on a box without the binary installed. Integration coverage
of the full gitleaks call-path lives in `test_gitleaks_corpus.py`.
"""
from __future__ import annotations

from pathlib import Path


class TestRedactSecret:
    """`redact_secret` turns a long secret into `first3...last3`."""

    def test_redacts_long_secret_to_prefix_ellipsis_suffix(self, redact_secrets):
        # secret ends with "xyzAA" → last 3 chars are "zAA"
        result = redact_secrets.redact_secret("sk-ant-api03-" + "a" * 90 + "xyzAA")
        assert result.startswith("sk-")
        assert result.endswith("zAA")
        assert "..." in result
        assert len(result) == 3 + 3 + 3  # prefix + ellipsis + suffix

    def test_redacts_short_secret_to_placeholder(self, redact_secrets):
        # Threshold is keep_chars*2 + 3 = 9. "short" (5 chars) is below.
        assert redact_secrets.redact_secret("short") == "[REDACTED]"

    def test_custom_keep_chars(self, redact_secrets):
        result = redact_secrets.redact_secret("a" * 30, keep_chars=5)
        assert result == "aaaaa...aaaaa"


class TestFilterByPrefixes:
    """`filter_by_prefixes` keeps findings whose File matches any prefix."""

    def test_filters_to_only_matching_prefixes(self, redact_secrets):
        findings = [
            {"File": ".claude/plans/p1.md", "Secret": "sk-real"},
            {"File": "src/main.py", "Secret": "sk-real"},
            {"File": ".specstory/history/2026.md", "Secret": "sk-real"},
        ]
        filtered = redact_secrets.filter_by_prefixes(
            findings, [".claude/plans", ".specstory/history"]
        )
        assert len(filtered) == 2
        assert {f["File"] for f in filtered} == {
            ".claude/plans/p1.md",
            ".specstory/history/2026.md",
        }

    def test_returns_empty_when_no_match(self, redact_secrets):
        findings = [{"File": "src/main.py", "Secret": "x"}]
        assert redact_secrets.filter_by_prefixes(findings, [".claude/plans"]) == []

    def test_trailing_slash_tolerance(self, redact_secrets):
        """The helper normalizes trailing slashes, so both forms work."""
        findings = [{"File": ".claude/plans/p1.md", "Secret": "x"}]
        with_slash = redact_secrets.filter_by_prefixes(findings, [".claude/plans/"])
        without_slash = redact_secrets.filter_by_prefixes(findings, [".claude/plans"])
        assert len(with_slash) == 1
        assert len(without_slash) == 1


class TestFindPrivateKeyFiles:
    """`find_private_key_files` detects PEM blocks + stray key *headers*, and
    IGNORES bare 'PRIVATE KEY' prose (which detect-private-key ignores too)."""

    def test_detects_pem_block(self, redact_secrets, tmp_path: Path):
        f = tmp_path / "p.md"
        f.write_text(
            "prose\n"
            "-----BEGIN RSA PRIVATE KEY-----\n"  # gitleaks:allow
            "fake material\n"
            "-----END RSA PRIVATE KEY-----\n"
            "more prose\n",
            encoding="utf-8",
        )
        results = redact_secrets.find_private_key_files([f])
        assert f in results
        # Description should mention at least one PEM block
        assert any("PEM" in desc for desc in results[f])

    def test_ignores_bare_mention_without_header(self, redact_secrets, tmp_path: Path):
        """Prose that merely says 'PRIVATE KEY' is not key material.
        detect-private-key ignores it, and flagging it caused a
        non-converging redact loop, so we ignore it too."""
        f = tmp_path / "p.md"
        f.write_text("hey here is a PRIVATE KEY mention\n", encoding="utf-8")
        assert redact_secrets.find_private_key_files([f]) == {}

    def test_detects_stray_header_without_block(self, redact_secrets, tmp_path: Path):
        """A key *header* quoted in prose (no matching END) is exactly what
        detect-private-key greps for, so it must be flagged."""
        f = tmp_path / "p.md"
        f.write_text(
            "oops pasted -----BEGIN OPENSSH PRIVATE KEY----- then stopped\n",
            encoding="utf-8",
        )
        results = redact_secrets.find_private_key_files([f])
        assert f in results
        assert any("header" in desc for desc in results[f])

    def test_detects_non_pem_blacklist_headers(self, redact_secrets, tmp_path: Path):
        """PuTTY + OpenVPN headers have no 'PRIVATE KEY' text but are on the
        detect-private-key BLACKLIST. The OpenVPN token is built from split
        string literals in the source; this guards that it still matches."""
        f = tmp_path / "p.md"
        f.write_text(
            "PuTTY-User-Key-File-2: ssh-rsa\n"
            "-----BEGIN OpenVPN Static key V1-----\n",
            encoding="utf-8",
        )
        results = redact_secrets.find_private_key_files([f])
        assert f in results
        assert any("header" in desc for desc in results[f])

    def test_ignores_non_md_suffix(self, redact_secrets, tmp_path: Path):
        f = tmp_path / "p.txt"
        f.write_text("-----BEGIN RSA PRIVATE KEY-----\n", encoding="utf-8")  # gitleaks:allow
        assert redact_secrets.find_private_key_files([f]) == {}

    def test_ignores_clean_file(self, redact_secrets, tmp_path: Path):
        f = tmp_path / "p.md"
        f.write_text("all clean here\n", encoding="utf-8")
        assert redact_secrets.find_private_key_files([f]) == {}


class TestRedactFile:
    """`redact_file` rewrites matching findings in place, returns True if modified."""

    def test_replaces_secret_in_place(self, redact_secrets, tmp_path: Path):
        secret = "sk-proj-" + "A" * 90
        f = tmp_path / "p.md"
        f.write_text(f"line1\nOPENAI={secret}\nline3\n", encoding="utf-8")
        findings = [{"File": str(f), "Secret": secret}]
        findings[0]["RuleID"] = "openai-project-key"
        modified = redact_secrets.redact_file(f, findings)
        assert modified is True
        content = f.read_text(encoding="utf-8")
        assert secret not in content
        # Same sentinel shape SpecStory >= 2.4.0 writes natively.
        assert "[REDACTED:openai-project-key]" in content

    def test_legacy_keeps_truncated_form(self, redact_secrets, tmp_path: Path):
        secret = "sk-proj-" + "A" * 90
        f = tmp_path / "p.md"
        f.write_text(f"OPENAI={secret}\n", encoding="utf-8")
        findings = [{"File": str(f), "Secret": secret, "RuleID": "openai-project-key"}]
        assert redact_secrets.redact_file(f, findings, legacy=True) is True
        content = f.read_text(encoding="utf-8")
        assert "sk-...AAA" in content  # first3 + ... + last3
        assert "[REDACTED:" not in content

    def test_returns_false_when_secret_not_present(
        self, redact_secrets, tmp_path: Path
    ):
        """Edge case: gitleaks found secret in staged diff but working
        copy was already redacted by a prior run."""
        f = tmp_path / "p.md"
        f.write_text("already redacted: sk-...AAA\n", encoding="utf-8")
        findings = [{"File": str(f), "Secret": "sk-proj-" + "A" * 90}]
        modified = redact_secrets.redact_file(f, findings)
        assert modified is False

    def test_ignores_findings_for_other_files(self, redact_secrets, tmp_path: Path):
        secret = "sk-proj-" + "A" * 90
        f = tmp_path / "p.md"
        other = tmp_path / "other.md"
        f.write_text(f"OPENAI={secret}\n", encoding="utf-8")
        findings = [{"File": str(other), "Secret": secret}]
        modified = redact_secrets.redact_file(f, findings)
        assert modified is False  # finding was for `other`, not `f`
        assert secret in f.read_text(encoding="utf-8")


class TestRedactPrivateKeys:
    """`redact_private_keys` scrubs PEM blocks + stray key *headers*, and
    LEAVES bare 'PRIVATE KEY' prose untouched."""

    def test_replaces_pem_block_wholesale(self, redact_secrets, tmp_path: Path):
        f = tmp_path / "p.md"
        f.write_text(
            "prose\n"
            "-----BEGIN RSA PRIVATE KEY-----\n"  # gitleaks:allow
            "fake material\n"
            "-----END RSA PRIVATE KEY-----\n"
            "more prose\n",
            encoding="utf-8",
        )
        modified = redact_secrets.redact_private_keys(f)
        assert modified is True
        content = f.read_text(encoding="utf-8")
        # Sentinel must contain neither a header token nor the bare phrase, so
        # a re-run is a no-op. Lowercase on purpose: the header regex matches
        # uppercase only. Same label SpecStory emits for this class.
        assert "[REDACTED:private-key]" in content
        assert "fake material" not in content
        # The original PEM header must be gone (it contains "PRIVATE KEY").
        assert "-----BEGIN RSA PRIVATE KEY-----" not in content

    def test_legacy_writes_the_pre_2_4_0_sentinels(
        self, redact_secrets, tmp_path: Path
    ):
        """--legacy changes only the bytes written, never what is detected."""
        f = tmp_path / "p.md"
        f.write_text(
            "-----BEGIN RSA PRIVATE KEY-----\n"  # gitleaks:allow
            "fake material\n"
            "-----END RSA PRIVATE KEY-----\n"
            "log: -----BEGIN OPENSSH PRIVATE KEY----- truncated\n",
            encoding="utf-8",
        )
        assert redact_secrets.redact_private_keys(f, legacy=True) is True
        content = f.read_text(encoding="utf-8")
        assert "[REDACTED PEM PRIVKEY BLOCK]" in content
        assert "[REDACTED PRIVKEY HEADER]" in content
        assert "[REDACTED:" not in content

    def test_leaves_bare_mention_untouched(self, redact_secrets, tmp_path: Path):
        """Bare prose mentions are not key material; redacting them mangled
        legitimate text and never converged against a live transcript writer."""
        f = tmp_path / "p.md"
        original = "mention PRIVATE KEY here\n"
        f.write_text(original, encoding="utf-8")
        modified = redact_secrets.redact_private_keys(f)
        assert modified is False
        assert f.read_text(encoding="utf-8") == original

    def test_redacts_stray_header(self, redact_secrets, tmp_path: Path):
        """A header quoted in prose (no matching END) still trips
        detect-private-key, so it is scrubbed to a header-free sentinel."""
        f = tmp_path / "p.md"
        f.write_text(
            "log: -----BEGIN OPENSSH PRIVATE KEY----- truncated\n", encoding="utf-8"
        )
        modified = redact_secrets.redact_private_keys(f)
        assert modified is True
        content = f.read_text(encoding="utf-8")
        assert "BEGIN OPENSSH PRIVATE KEY" not in content
        assert "[REDACTED:private-key-header]" in content

    def test_leaves_clean_file_unchanged(self, redact_secrets, tmp_path: Path):
        f = tmp_path / "p.md"
        original = "plain prose\n"
        f.write_text(original, encoding="utf-8")
        modified = redact_secrets.redact_private_keys(f)
        assert modified is False
        assert f.read_text(encoding="utf-8") == original


class TestRedactionPlaceholder:
    """`redaction_placeholder` mirrors SpecStory's `[REDACTED:%s]` shape."""

    def test_uses_rule_id(self, redact_secrets):
        assert (
            redact_secrets.redaction_placeholder("openai-project-key")
            == "[REDACTED:openai-project-key]"
        )

    def test_normalizes_odd_rule_ids(self, redact_secrets):
        assert (
            redact_secrets.redaction_placeholder("Generic API Key")
            == "[REDACTED:generic-api-key]"
        )

    def test_falls_back_when_rule_id_missing(self, redact_secrets):
        assert redact_secrets.redaction_placeholder("") == "[REDACTED:secret]"

    def test_placeholder_retains_no_secret_bytes(self, redact_secrets):
        """The whole point: a placeholder can never be re-flagged."""
        secret = "sk-proj-" + "A" * 90
        placeholder = redact_secrets.redaction_placeholder("openai-project-key")
        assert secret[:8] not in placeholder
        assert secret[-8:] not in placeholder


class TestIdempotency:
    """A second pass must not rewrite a file the first pass already redacted.

    This is what stops the `git add` -> commit -> "files were modified by this
    hook" -> `git add` -> commit loop.
    """

    def test_second_redact_file_pass_is_a_noop(self, redact_secrets, tmp_path: Path):
        secret = "sk-proj-" + "A" * 90
        f = tmp_path / "p.md"
        f.write_text(f"OPENAI={secret}\n", encoding="utf-8")
        findings = [{"File": str(f), "Secret": secret, "RuleID": "openai-project-key"}]
        assert redact_secrets.redact_file(f, findings) is True
        after_first = f.read_text(encoding="utf-8")
        assert redact_secrets.redact_file(f, findings) is False
        assert f.read_text(encoding="utf-8") == after_first

    def test_specstory_placeholder_is_left_alone(self, redact_secrets, tmp_path: Path):
        """A transcript SpecStory already redacted must not be touched."""
        f = tmp_path / "p.md"
        original = "GITHUB_TOKEN=[REDACTED:github-pat]\nprose about a PRIVATE KEY\n"
        f.write_text(original, encoding="utf-8")
        assert redact_secrets.redact_private_keys(f) is False
        assert redact_secrets.find_private_key_files([f]) == {}
        assert f.read_text(encoding="utf-8") == original


class TestDefaultPaths:
    """The DEFAULT_PATHS list must cover every artifact dir we advertise."""

    def test_includes_all_advertised_dirs(self, redact_secrets):
        expected = {
            ".specstory/history",
            ".claude/plans",
            ".cursor/plans",
            ".cursor/rules",
            ".opencode/plans",
            ".specify",
            ".codex",
        }
        assert expected.issubset(set(redact_secrets.DEFAULT_PATHS))
