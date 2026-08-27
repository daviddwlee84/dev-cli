"""Shared pytest fixtures for the agent-history-hygiene test suite.

The redactor under test (`assets/redact_secrets.py`) is a PEP 723 uv
script with a `#!/usr/bin/env -S uv run --script` shebang. Its body is
otherwise a plain Python module — we load it via `importlib` so tests
can call individual functions without shelling out.
"""
from __future__ import annotations

import importlib.util
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

SKILL_ROOT = Path(__file__).resolve().parent.parent
ASSETS_DIR = SKILL_ROOT / "assets"
SCRIPTS_DIR = SKILL_ROOT / "scripts"
FIXTURES_DIR = Path(__file__).resolve().parent / "fixtures"


def _load_redact_secrets_module():
    """Import assets/redact_secrets.py as a module despite the shebang."""
    script_path = ASSETS_DIR / "redact_secrets.py"
    spec = importlib.util.spec_from_file_location("redact_secrets", script_path)
    if spec is None or spec.loader is None:  # pragma: no cover — defensive
        raise RuntimeError(f"Could not load spec for {script_path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules["redact_secrets"] = module
    spec.loader.exec_module(module)
    return module


@pytest.fixture(scope="session")
def redact_secrets():
    """The imported redact_secrets module."""
    return _load_redact_secrets_module()


@pytest.fixture(scope="session")
def fixtures_dir() -> Path:
    """Directory containing *.md corpus files."""
    return FIXTURES_DIR


@pytest.fixture(scope="session")
def assets_dir() -> Path:
    """Skill-local assets/ (templates + bundled redactor)."""
    return ASSETS_DIR


@pytest.fixture(scope="session")
def scripts_dir() -> Path:
    """Skill-local scripts/."""
    return SCRIPTS_DIR


@pytest.fixture(scope="session")
def gitleaks_available() -> bool:
    """Whether the gitleaks CLI is on PATH. Corpus tests skip when false."""
    return shutil.which("gitleaks") is not None


@pytest.fixture
def tmp_git_repo(tmp_path: Path, assets_dir: Path):
    """Bootstrap an empty git repo in tmp_path with our .gitleaks.toml
    config copied in. Returns the repo path.

    Uses -c user.email/name so the test works on CI boxes without a
    configured git identity.
    """
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(
        ["git", "init", "-q", "-b", "main"],
        cwd=repo,
        check=True,
    )
    # Copy our gitleaks config so rules + allowlists apply.
    shutil.copy(assets_dir / "gitleaks.toml.template", repo / ".gitleaks.toml")
    # Seed an initial commit so --staged has something to diff against.
    (repo / "README.md").write_text("init\n", encoding="utf-8")
    subprocess.run(
        [
            "git",
            "-c",
            "user.email=test@example.com",
            "-c",
            "user.name=test",
            "commit",
            "-q",
            "--allow-empty",
            "-m",
            "init",
        ],
        cwd=repo,
        check=True,
    )
    return repo
