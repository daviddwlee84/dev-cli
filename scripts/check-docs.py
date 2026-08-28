#!/usr/bin/env python3
"""Validate the bilingual MkDocs corpus and generate LLM-friendly indexes."""

from __future__ import annotations

import argparse
import datetime as dt
import html.parser
import posixpath
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit

import yaml

ROOT = Path(__file__).resolve().parents[1]
DOCS = ROOT / "docs"
MKDOCS = ROOT / "mkdocs.yml"
LANG = "zh-TW"
REQUIRED_META = ("description", "authority", "status", "verified_on")
PARITY_META = ("authority", "status", "verified_on", "minimum_version", "tested_with")
AUTHORITIES = {
    "anthropic-docs",
    "anthropic-docs-and-project-policy",
    "git-and-project-policy",
    "git-scm",
    "github-docs",
    "project",
    "project-and-upstream",
    "project-policy",
}
STATUSES = {
    "evolving",
    "experimental-and-versioned",
    "generated-plus-authored",
    "maintained",
    "official",
    "research-preview-partial",
    "stable",
}
SNIPPET_RE = re.compile(r'^\s*--8<--\s+"([^"]+)"\s*$', re.MULTILINE)
MARKDOWN_LINK_RE = re.compile(r'(?<!!)\[([^]\n]+)\]\(([^)\n]+)\)')
PRIVATE_PATH_RE = re.compile(r"(?:/Users/[^/\s]+|/home/[^/\s]+|[A-Za-z]:\\Users\\[^\\\s]+)")


@dataclass(frozen=True)
class Page:
    label: str
    path: str
    section: str


@dataclass(frozen=True)
class Document:
    page: Page
    path: Path
    metadata: dict[str, object]
    body: str
    language: str


class LinkParser(html.parser.HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.hrefs: list[str] = []
        self.scripts: list[str] = []
        self.stylesheets: list[str] = []
        self.anchors: set[str] = set()

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        values = dict(attrs)
        if tag == "a" and values.get("href"):
            self.hrefs.append(values["href"] or "")
        if tag == "script" and values.get("src"):
            self.scripts.append(values["src"] or "")
        if tag == "link" and values.get("href") and values.get("rel") == "stylesheet":
            self.stylesheets.append(values["href"] or "")
        if values.get("id"):
            self.anchors.add(values["id"] or "")
        if tag == "a" and values.get("name"):
            self.anchors.add(values["name"] or "")


def fail(errors: list[str], message: str) -> None:
    errors.append(message)


def load_config() -> dict[str, object]:
    try:
        value = yaml.safe_load(MKDOCS.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as exc:
        raise RuntimeError(f"cannot read {MKDOCS}: {exc}") from exc
    if not isinstance(value, dict):
        raise RuntimeError(f"{MKDOCS} must contain a YAML mapping")
    return value


def flatten_nav(nav: object, section: str = "Overview") -> list[Page]:
    pages: list[Page] = []
    if isinstance(nav, list):
        for item in nav:
            pages.extend(flatten_nav(item, section))
    elif isinstance(nav, dict):
        for label, value in nav.items():
            if isinstance(value, str) and value.endswith(".md"):
                pages.append(Page(str(label), value, section))
            else:
                pages.extend(flatten_nav(value, str(label)))
    return pages


def plugin_config(config: dict[str, object], name: str) -> dict[str, object] | None:
    plugins = config.get("plugins", [])
    if not isinstance(plugins, list):
        return None
    for plugin in plugins:
        if isinstance(plugin, dict) and name in plugin and isinstance(plugin[name], dict):
            return plugin[name]
    return None


def split_front_matter(path: Path) -> tuple[dict[str, object], str]:
    text = path.read_text(encoding="utf-8")
    lines = text.splitlines()
    if not lines or lines[0] != "---":
        raise ValueError("missing YAML front matter")
    try:
        end = lines.index("---", 1)
    except ValueError as exc:
        raise ValueError("unterminated YAML front matter") from exc
    try:
        metadata = yaml.safe_load("\n".join(lines[1:end])) or {}
    except yaml.YAMLError as exc:
        raise ValueError(f"invalid YAML front matter: {exc}") from exc
    if not isinstance(metadata, dict):
        raise ValueError("front matter must be a mapping")
    return metadata, "\n".join(lines[end + 1 :]).strip() + "\n"


def translated_path(path: str) -> str:
    return f"{path[:-3]}.{LANG}.md"


def document_for(page: Page, language: str) -> Document:
    relative = page.path if language == "en" else translated_path(page.path)
    path = DOCS / relative
    metadata, body = split_front_matter(path)
    return Document(page, path, metadata, body, language)


def markdown_files() -> tuple[set[str], set[str]]:
    canonical: set[str] = set()
    translated: set[str] = set()
    for path in DOCS.rglob("*.md"):
        relative = path.relative_to(DOCS)
        if relative.parts[0] in {"_snippets", "assets"}:
            continue
        name = relative.as_posix()
        if name.endswith(f".{LANG}.md"):
            translated.add(name)
        else:
            canonical.add(name)
    return canonical, translated


def snippet_target(spec: str) -> tuple[str, int | None, int | None]:
    match = re.match(r"^(.*?)(?::(\d+))?(?::(\d+))?$", spec)
    if match is None:
        return spec, None, None
    path, start, end = match.groups()
    return path, int(start) if start else None, int(end) if end else None


def resolve_snippet(spec: str) -> tuple[Path | None, int | None, int | None]:
    name, start, end = snippet_target(spec)
    for candidate in (ROOT / name, DOCS / name, DOCS / "_snippets" / name):
        if candidate.is_file():
            return candidate, start, end
    return None, start, end


def expand_snippets(body: str) -> str:
    def replace(match: re.Match[str]) -> str:
        target, start, end = resolve_snippet(match.group(1))
        if target is None:
            return match.group(0)
        lines = target.read_text(encoding="utf-8").splitlines()
        first = max((start or 1) - 1, 0)
        last = end if end is not None else len(lines)
        return "\n".join(lines[first:last])

    return SNIPPET_RE.sub(replace, body)


def map_outside_fences(body: str, transform) -> str:
    lines: list[str] = []
    fence = ""
    for line in body.splitlines():
        stripped = line.lstrip()
        marker = stripped[:3] if stripped.startswith(("```", "~~~")) else ""
        if marker:
            if not fence:
                fence = marker
            elif marker == fence:
                fence = ""
            lines.append(line)
        elif fence:
            lines.append(line)
        else:
            lines.append(transform(line))
    return "\n".join(lines) + ("\n" if body.endswith("\n") else "")


def rewrite_document_links(document: Document, site_url: str, page_paths: set[str]) -> str:
    def rewrite_line(line: str) -> str:
        def replace(match: re.Match[str]) -> str:
            raw_target = match.group(2).strip()
            if raw_target.startswith("<") and ">" in raw_target:
                end = raw_target.index(">")
                target, suffix = raw_target[1:end], raw_target[end + 1 :]
            else:
                parts = raw_target.split(maxsplit=1)
                target = parts[0]
                suffix = f" {parts[1]}" if len(parts) == 2 else ""
            parsed = urlsplit(target)
            if parsed.scheme or parsed.netloc or target.startswith(("//", "mailto:", "tel:")):
                return match.group(0)

            language = document.language
            if not parsed.path and parsed.fragment:
                canonical = document.page.path
            elif parsed.path.endswith(".md"):
                canonical = posixpath.normpath(
                    posixpath.join(posixpath.dirname(document.page.path), unquote(parsed.path))
                )
                if canonical.endswith(f".{LANG}.md"):
                    canonical = f"{canonical[: -len(f'.{LANG}.md')]}.md"
                    language = LANG
                if canonical not in page_paths:
                    return match.group(0)
            else:
                return match.group(0)

            rewritten = page_url(site_url, canonical, language)
            if parsed.query:
                rewritten += f"?{parsed.query}"
            if parsed.fragment:
                rewritten += f"#{parsed.fragment}"
            return f"[{match.group(1)}]({rewritten}{suffix})"

        return MARKDOWN_LINK_RE.sub(replace, line)

    expanded = expand_snippets(document.body)
    return map_outside_fences(expanded, rewrite_line)


def markdown_links(body: str) -> list[str]:
    links: list[str] = []

    def collect(line: str) -> str:
        for match in MARKDOWN_LINK_RE.finditer(line):
            raw = match.group(2).strip()
            if raw.startswith("<") and ">" in raw:
                links.append(raw[1 : raw.index(">")])
            else:
                links.append(raw.split(maxsplit=1)[0])
        return line

    map_outside_fences(body, collect)
    return links


def page_title(body: str, fallback: str) -> str:
    match = re.search(r"^#\s+(.+?)\s*$", body, re.MULTILINE)
    return match.group(1) if match else fallback


def body_without_title(body: str) -> str:
    return re.sub(r"^#\s+.+?\s*\n+", "", body, count=1, flags=re.MULTILINE)


def page_url(site_url: str, relative: str, language: str) -> str:
    stem = relative[:-3]
    if stem == "index":
        route = ""
    elif stem.endswith("/index"):
        route = stem[: -len("index")]
    else:
        route = f"{stem}/"
    if language != "en":
        route = f"{language}/{route}"
    return urljoin(site_url.rstrip("/") + "/", route)


def expected_llms(config: dict[str, object], pages: list[Page]) -> tuple[str, str]:
    site_name = str(config.get("site_name", "Documentation"))
    description = str(config.get("site_description", "")).strip()
    site_url = str(config.get("site_url", "")).strip()
    documents = {
        language: [document_for(page, language) for page in pages]
        for language in ("en", LANG)
    }
    page_paths = {page.path for page in pages}
    translations: dict[str, str] = {}
    i18n = plugin_config(config, "i18n")
    if i18n is not None and isinstance(i18n.get("languages"), list):
        for language_config in i18n["languages"]:
            if isinstance(language_config, dict) and language_config.get("locale") == LANG:
                raw = language_config.get("nav_translations", {})
                if isinstance(raw, dict):
                    translations = {str(key): str(value) for key, value in raw.items()}

    index_lines = [f"# {site_name}", "", f"> {description}", ""]
    for language, heading in (("en", "English"), (LANG, "繁體中文")):
        index_lines.extend([f"## {heading}", ""])
        current_section = ""
        for document in documents[language]:
            if document.page.section != current_section:
                current_section = document.page.section
                display_section = translations.get(current_section, current_section) if language == LANG else current_section
                index_lines.extend([f"### {display_section}", ""])
            title = page_title(document.body, document.page.label)
            url = page_url(site_url, document.page.path, language)
            desc = str(document.metadata["description"]).strip()
            index_lines.append(f"- [{title}]({url}): {desc}")
        index_lines.append("")

    full_lines = [f"# {site_name} — full documentation", "", f"> {description}", ""]
    for language, heading in (("en", "English"), (LANG, "繁體中文")):
        full_lines.extend([f"## {heading}", ""])
        for document in documents[language]:
            title = page_title(document.body, document.page.label)
            url = page_url(site_url, document.page.path, language)
            desc = str(document.metadata["description"]).strip()
            expanded = body_without_title(
                rewrite_document_links(document, site_url, page_paths)
            ).strip()
            full_lines.extend(
                [
                    f"### {title}",
                    "",
                    f"Source: {url}",
                    "",
                    f"> {desc}",
                    "",
                    expanded,
                    "",
                    "---",
                    "",
                ]
            )

    return "\n".join(index_lines).rstrip() + "\n", "\n".join(full_lines).rstrip() + "\n"



def write_if_changed(path: Path, content: str) -> bool:
    if path.exists() and path.read_text(encoding="utf-8") == content:
        return False
    path.write_text(content, encoding="utf-8")
    return True


def check_source(generate_llms: bool) -> list[str]:
    errors: list[str] = []
    try:
        config = load_config()
    except RuntimeError as exc:
        return [str(exc)]
    pages = flatten_nav(config.get("nav"))
    if not pages:
        return ["mkdocs.yml nav contains no Markdown pages"]

    site_url = str(config.get("site_url", "")).rstrip("/")
    copy_config = plugin_config(config, "copy-to-llm")
    expected_base_path = urlsplit(site_url).path.rstrip("/")
    if copy_config is None:
        fail(errors, "mkdocs.yml is missing the copy-to-llm plugin")
    else:
        if str(copy_config.get("repo_url", "")).rstrip("/") != site_url:
            fail(errors, "copy-to-llm repo_url must equal site_url without a trailing slash")
        if str(copy_config.get("base_path", "")).rstrip("/") != expected_base_path:
            fail(errors, f"copy-to-llm base_path must be {expected_base_path!r}")
    hooks = config.get("hooks", [])
    if not isinstance(hooks, list) or "scripts/mkdocs_hooks.py" not in hooks:
        fail(errors, "mkdocs.yml must run scripts/mkdocs_hooks.py")

    nav_paths = [page.path for page in pages]
    if len(nav_paths) != len(set(nav_paths)):
        seen: set[str] = set()
        duplicates = sorted(path for path in nav_paths if path in seen or seen.add(path))
        fail(errors, f"duplicate nav paths: {', '.join(duplicates)}")

    canonical, translated = markdown_files()
    nav_set = set(nav_paths)
    for path in sorted(canonical - nav_set):
        fail(errors, f"canonical page missing from nav: docs/{path}")
    for path in sorted(nav_set - canonical):
        fail(errors, f"nav target missing: docs/{path}")

    expected_translations = {translated_path(path) for path in canonical}
    for path in sorted(expected_translations - translated):
        fail(errors, f"missing {LANG} sibling: docs/{path}")
    for path in sorted(translated - expected_translations):
        fail(errors, f"orphan {LANG} page: docs/{path}")

    today = dt.datetime.now(dt.timezone.utc).date()
    for page in pages:
        paired: dict[str, dict[str, object]] = {}
        for language in ("en", LANG):
            relative = page.path if language == "en" else translated_path(page.path)
            path = DOCS / relative
            if not path.is_file():
                continue
            try:
                metadata, body = split_front_matter(path)
            except (OSError, ValueError) as exc:
                fail(errors, f"docs/{relative}: {exc}")
                continue
            for field in REQUIRED_META:
                if field not in metadata or metadata[field] in (None, ""):
                    fail(errors, f"docs/{relative}: missing metadata field {field!r}")

            authority = metadata.get("authority")
            status = metadata.get("status")
            if authority not in AUTHORITIES:
                fail(errors, f"docs/{relative}: unknown authority {authority!r}")
            if status not in STATUSES:
                fail(errors, f"docs/{relative}: unknown status {status!r}")

            verified = metadata.get("verified_on")
            verified_date: dt.date | None = None
            if isinstance(verified, dt.datetime):
                verified_date = verified.date()
            elif isinstance(verified, dt.date):
                verified_date = verified
            elif isinstance(verified, str):
                try:
                    verified_date = dt.date.fromisoformat(verified)
                except ValueError:
                    fail(errors, f"docs/{relative}: verified_on must be an ISO date")
            elif verified is not None:
                fail(errors, f"docs/{relative}: verified_on must be an ISO date")
            if verified_date is not None:
                if verified_date > today:
                    fail(errors, f"docs/{relative}: verified_on cannot be in the future")
                metadata["verified_on"] = verified_date.isoformat()

            needs_version = (
                isinstance(authority, str) and "anthropic-docs" in authority
            ) or status in {"experimental-and-versioned", "research-preview-partial"}
            if needs_version and not (metadata.get("minimum_version") or metadata.get("tested_with")):
                fail(errors, f"docs/{relative}: version-sensitive page needs minimum_version or tested_with")
            if language == LANG and metadata.get("lang") != LANG:
                fail(errors, f"docs/{relative}: lang must be {LANG!r}")
            if '!!! warning "Translation pending"' in body:
                fail(errors, f"docs/{relative}: translation placeholder remains")
            if ".claude/worktrees/worktree-" in body:
                fail(errors, f"docs/{relative}: stale Claude worktree directory naming")
            private = PRIVATE_PATH_RE.search(body)
            if private:
                fail(errors, f"docs/{relative}: private absolute path {private.group(0)!r}")
            for spec in SNIPPET_RE.findall(body):
                target, _, _ = resolve_snippet(spec)
                if target is None:
                    fail(errors, f"docs/{relative}: missing snippet target {spec!r}")
            paired[language] = metadata

        if "en" in paired and LANG in paired:
            for field in PARITY_META:
                if paired["en"].get(field) != paired[LANG].get(field):
                    fail(errors, f"docs/{page.path}: {field} differs from {LANG} sibling")

    try:
        llms_txt, llms_full = expected_llms(config, pages)
    except (OSError, ValueError, KeyError) as exc:
        fail(errors, f"cannot generate LLM outputs: {exc}")
        return errors

    generated = (
        (DOCS / "llms.txt", llms_txt),
        (DOCS / "llms-full.txt", llms_full),
    )
    if generate_llms:
        for path, content in generated:
            if write_if_changed(path, content):
                print(f"generated {path.relative_to(ROOT)}")
    else:
        for path, content in generated:
            if not path.exists() or path.read_text(encoding="utf-8") != content:
                fail(errors, f"{path.relative_to(ROOT)} is stale; run --source --generate-llms")

    return errors


def parse_html(path: Path) -> LinkParser:
    parser = LinkParser()
    parser.feed(path.read_text(encoding="utf-8"))
    return parser


def output_path(site: Path, relative: str, language: str) -> Path:
    stem = relative[:-3]
    prefix = Path(language) if language != "en" else Path()
    if stem == "index":
        return site / prefix / "index.html"
    if stem.endswith("/index"):
        return site / prefix / f"{stem}.html"
    return site / prefix / stem / "index.html"


def publish_markdown(site: Path) -> list[str]:
    errors: list[str] = []
    try:
        config = load_config()
    except RuntimeError as exc:
        return [str(exc)]
    if not site.is_dir():
        return [f"site directory does not exist: {site}"]

    pages = flatten_nav(config.get("nav"))
    page_paths = {page.path for page in pages}
    site_url = str(config.get("site_url", "")).strip()
    written = 0
    for page in pages:
        for language in ("en", LANG):
            try:
                document = document_for(page, language)
            except (OSError, ValueError) as exc:
                relative = page.path if language == "en" else translated_path(page.path)
                fail(errors, f"cannot publish docs/{relative}: {exc}")
                continue
            target = output_path(site, page.path, language).with_suffix(".md")
            target.parent.mkdir(parents=True, exist_ok=True)
            content = rewrite_document_links(document, site_url, page_paths).rstrip() + "\n"
            if write_if_changed(target, content):
                written += 1
    print(f"published {len(pages) * 2} Markdown endpoint(s); {written} updated")
    return errors


def local_target(site: Path, site_url: str, source: Path, href: str) -> tuple[Path | None, str]:
    parsed = urlsplit(href)
    if parsed.scheme or parsed.netloc or href.startswith(("mailto:", "tel:", "javascript:", "data:")):
        return None, ""

    base_path = urlsplit(site_url).path.rstrip("/")
    source_rel = source.relative_to(site).as_posix()
    if source_rel == "index.html":
        source_route = "/"
    elif source_rel.endswith("/index.html"):
        source_route = f"/{source_rel[:-len('index.html')]}"
    else:
        source_route = f"/{source_rel}"
    public_source = f"{base_path}{source_route}"
    target_route = urljoin(public_source, parsed.path or "")
    if base_path and not (target_route == base_path or target_route.startswith(f"{base_path}/")):
        return None, ""
    relative = target_route[len(base_path) :].lstrip("/") if base_path else target_route.lstrip("/")
    relative = posixpath.normpath(relative)
    if relative == ".":
        relative = ""

    candidate = site / relative
    if target_route.endswith("/") or not relative:
        candidate = candidate / "index.html"
    elif candidate.is_dir():
        candidate = candidate / "index.html"
    elif not candidate.suffix:
        index = candidate / "index.html"
        if index.exists():
            candidate = index
    return candidate, unquote(parsed.fragment)


def absolute_site_target(site: Path, site_url: str, href: str) -> tuple[Path | None, str]:
    parsed = urlsplit(href)
    configured = urlsplit(site_url)
    if parsed.scheme not in {"http", "https"} or parsed.netloc != configured.netloc:
        return None, ""
    base_path = configured.path.rstrip("/")
    if base_path and not (parsed.path == base_path or parsed.path.startswith(f"{base_path}/")):
        return None, ""
    relative = parsed.path[len(base_path) :].lstrip("/") if base_path else parsed.path.lstrip("/")
    candidate = site / relative
    if not relative or parsed.path.endswith("/"):
        candidate = candidate / "index.html"
    elif candidate.is_dir():
        candidate = candidate / "index.html"
    elif not candidate.suffix:
        candidate = candidate / "index.html"
    return candidate, unquote(parsed.fragment)


def check_generated_markdown(
    path: Path,
    site: Path,
    site_url: str,
    parsed_cache: dict[Path, LinkParser],
    errors: list[str],
) -> None:
    for href in markdown_links(path.read_text(encoding="utf-8")):
        parsed = urlsplit(href)
        if not parsed.scheme and not parsed.netloc:
            fail(errors, f"{path.relative_to(site)}: generated Markdown keeps relative link {href!r}")
            continue
        target, fragment = absolute_site_target(site, site_url, href)
        if target is None:
            continue
        if not target.is_file():
            fail(errors, f"{path.relative_to(site)}: broken generated Markdown link {href!r}")
            continue
        if fragment and target.suffix == ".html":
            target_parser = parsed_cache.setdefault(target, parse_html(target))
            if fragment not in target_parser.anchors:
                fail(errors, f"{path.relative_to(site)}: missing generated Markdown anchor {href!r}")


def check_site(site: Path) -> list[str]:
    errors: list[str] = []
    try:
        config = load_config()
    except RuntimeError as exc:
        return [str(exc)]
    pages = flatten_nav(config.get("nav"))
    page_paths = {page.path for page in pages}
    site_url = str(config.get("site_url", "")).strip()
    if not site.is_dir():
        return [f"site directory does not exist: {site}"]

    generated_markdown: list[Path] = []
    for page in pages:
        for language in ("en", LANG):
            target = output_path(site, page.path, language)
            if not target.is_file():
                fail(errors, f"rendered page missing: {target.relative_to(ROOT)}")
            markdown_target = target.with_suffix(".md")
            generated_markdown.append(markdown_target)
            try:
                expected_markdown = rewrite_document_links(
                    document_for(page, language), site_url, page_paths
                ).rstrip() + "\n"
            except (OSError, ValueError) as exc:
                fail(errors, f"cannot validate {markdown_target.relative_to(ROOT)}: {exc}")
            else:
                if not markdown_target.is_file() or markdown_target.read_text(encoding="utf-8") != expected_markdown:
                    fail(errors, f"missing or stale page Markdown: {markdown_target.relative_to(ROOT)}")

    for name in ("llms.txt", "llms-full.txt"):
        target = site / name
        generated_markdown.append(target)
        if not target.is_file() or target.stat().st_size < 100:
            fail(errors, f"missing or empty LLM output: {target.relative_to(ROOT)}")

    parsed_cache: dict[Path, LinkParser] = {}
    for markdown_source in generated_markdown:
        if markdown_source.is_file():
            check_generated_markdown(markdown_source, site, site_url, parsed_cache, errors)

    expected_html = {
        output_path(site, page.path, language)
        for page in pages
        for language in ("en", LANG)
    }
    html_files = sorted(path for path in site.rglob("*.html") if path.name != "404.html")
    unexpected_html = set(html_files) - expected_html
    for path in sorted(unexpected_html):
        fail(errors, f"unexpected rendered page: {path.relative_to(site)}")
    for source in html_files:
        source_parser = parsed_cache.setdefault(source, parse_html(source))
        expected_assets = (
            ("copy-to-llm.js", source_parser.scripts),
            ("copy-to-llm.css", source_parser.stylesheets),
            ("copy-to-llm-custom.css", source_parser.stylesheets),
        )
        for asset, values in expected_assets:
            count = sum(urlsplit(value).path.endswith(f"/{asset}") for value in values)
            if count != 1:
                fail(errors, f"{source.relative_to(site)}: expected one {asset}, found {count}")
        for href in source_parser.hrefs:
            target, fragment = local_target(site, site_url, source, href)
            if target is None:
                continue
            if not target.exists():
                fail(errors, f"{source.relative_to(site)}: broken link {href!r}")
                continue
            if fragment and target.suffix == ".html":
                target_parser = parsed_cache.setdefault(target, parse_html(target))
                if fragment not in target_parser.anchors:
                    fail(errors, f"{source.relative_to(site)}: missing anchor {href!r}")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", action="store_true", help="validate docs sources and generated LLM files")
    parser.add_argument("--generate-llms", action="store_true", help="write docs/llms.txt and docs/llms-full.txt")
    parser.add_argument("--publish-markdown", type=Path, help="write per-page Markdown endpoints into a built site")
    parser.add_argument("--site", type=Path, help="validate rendered site directory")
    args = parser.parse_args()
    if not args.source and args.publish_markdown is None and args.site is None:
        parser.error("choose --source, --publish-markdown PATH, and/or --site PATH")
    if args.generate_llms and not args.source:
        parser.error("--generate-llms requires --source")

    errors: list[str] = []
    if args.source:
        errors.extend(check_source(args.generate_llms))
    if args.publish_markdown is not None:
        publish_site = args.publish_markdown if args.publish_markdown.is_absolute() else ROOT / args.publish_markdown
        errors.extend(publish_markdown(publish_site))
    if args.site is not None:
        site = args.site if args.site.is_absolute() else ROOT / args.site
        errors.extend(check_site(site))

    if errors:
        for message in errors:
            print(f"docs check: {message}", file=sys.stderr)
        print(f"docs check failed with {len(errors)} error(s)", file=sys.stderr)
        return 1
    print("docs check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
