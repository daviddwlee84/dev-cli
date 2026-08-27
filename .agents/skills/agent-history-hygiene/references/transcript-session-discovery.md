# Transcript & session discovery

How `find-session.sh` locates "the session you are in right now" across
the fragmented landscape of coding-agent artifacts.

## The core problem

Several agents can be running in parallel against the same repository —
one Claude Code window, a background subagent, a Cursor window, plus
VS Code with the SpecStory extension auto-saving. Each writes into its
own directory using its own naming convention. There is no canonical
"current session" pointer; the best you can do is pick the freshest
artifact tied to `$PWD` and verify.

## Claude Code session layout

Claude Code stores each session as a JSON-lines file inside a
project-scoped directory:

```
~/.claude/projects/<slug>/<session-uuid>.jsonl
```

The slug is the absolute working directory with **every non-alphanumeric
character** replaced by `-`, not just `/`. For
`$PWD=/Volumes/Data/Program/Personal/agent-skills` the slug is
`-Volumes-Data-Program-Personal-agent-skills` (leading dash included). A dot or
underscore collapses the same way, which is easy to miss:
`/home/<user>/.local/share/chezmoi` is stored as
`-home-<user>--local-share-chezmoi` — the dot becomes the second dash. Rebuild
the slug with `sed 's/[^A-Za-z0-9]/-/g'`, not `tr '/' '-'`, or a path
containing `.`/`_`/space silently resolves to a directory that doesn't exist
(`specstory sync` then reports `1 session not found`). The UUID is the Claude
session ID surfaced in `/status`.

Heuristic: **the newest `.jsonl` in that directory is the active
session's transcript.** This breaks if you spawn a subagent that writes
into the same project dir and then stop interacting with the parent — the
subagent's JSONL wins by mtime. In practice the user knows which session
they're working in; the heuristic is fine for the "commit my chat" flow.

## SpecStory history layout

SpecStory writes one Markdown file per conversation:

```
<repo-root>/.specstory/history/<timestamp>.md
```

The timestamp is UTC ISO-8601-ish (e.g. `2026-04-24_05-05-10Z.md`). The
VS Code extension auto-saves continuously (controlled by
`specstory.autoSave`) so the file grows during the conversation. The CLI
(`specstory run claude`, `specstory sync claude -s <uuid>`) emits a new
file per session explicitly.

Heuristic: **the newest `.md` in `.specstory/history/` is the active
transcript.** Same caveat as Claude — parallel sessions produce multiple
fresh files; mtime wins.

## `find-session.sh` output shape

TSV by default (two columns per line, tab-separated):

```
specstory_path      <abs path or empty>
claude_session_uuid <uuid or empty>
claude_jsonl_path   <abs path or empty>
```

`--json` emits a single-line JSON object. The script NEVER fails (always
exits 0) so agents can pipe it into downstream commands without
`set -e` explosions when a session isn't present.

## When the heuristic breaks

1. **Multiple concurrent agents in the same repo.** If you care which is
   "yours", open the session picker in your IDE (Claude Code's `/status`;
   SpecStory's session list) and grep for the UUID directly.
2. **Subagent writes a newer file than the parent.** After a long
   subagent run, the parent session's JSONL may look stale. The
   `--format=specstory` and `--format=claude` flags let you isolate one
   dimension.
3. **Repository renamed on disk.** Claude's slug is frozen at session
   start — if you `mv` the repo mid-session, the slug stops matching
   `$PWD`. Restart the agent to get a fresh session/slug pair.
4. **Worktrees.** `git worktree` directories have their own `$PWD` and
   thus their own slug. SpecStory stores per-worktree history too (it
   keys off `.specstory/history/` inside each worktree root).

## SpecStory CLI integration (optional)

If the user runs `specstory` directly:

```bash
# Start a wrapped Claude Code session that auto-logs into .specstory/history/
specstory run claude

# Sync a specific Claude session's JSONL into SpecStory's Markdown format
specstory sync claude -s <uuid>

# List Claude sessions SpecStory knows about
specstory list claude
```

When the VS Code extension is the source of truth (most common), the CLI
commands are unnecessary — the extension already wrote the file. Only
use `specstory sync` when you want to force-refresh a transcript for a
session whose files haven't auto-saved.

## Cross-reference

- [`pre-commit-redaction-stack.md`](./pre-commit-redaction-stack.md) —
  how the located session files get scanned / redacted at commit time.
- [`../scripts/find-session.sh`](../scripts/find-session.sh) — the
  script implementing these heuristics.
- [`../scripts/stage-agent-artifacts.sh`](../scripts/stage-agent-artifacts.sh)
  — the caller that uses `find-session.sh --format=specstory` to locate
  the current transcript for `--session-only` staging.
