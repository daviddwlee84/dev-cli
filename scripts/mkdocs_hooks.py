"""MkDocs hooks for generated, route-local LLM Markdown endpoints."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path


def on_config(config, **_kwargs):
    """Deduplicate assets when static-i18n reconfigures plugins per locale."""
    for key in ("extra_javascript", "extra_css"):
        values = config.get(key) or []
        seen: set[str] = set()
        unique = []
        for value in values:
            identity = str(value)
            if identity in seen:
                continue
            seen.add(identity)
            unique.append(value)
        config[key] = unique
    return config


def on_post_build(config, **_kwargs) -> None:
    """Publish one index.md endpoint beside every rendered HTML page."""
    root = Path(config.config_file_path).resolve().parent
    subprocess.run(
        [
            sys.executable,
            str(root / "scripts" / "check-docs.py"),
            "--publish-markdown",
            str(config.site_dir),
        ],
        cwd=root,
        check=True,
    )
