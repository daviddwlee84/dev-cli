---
description: Use Claude Code worktrees with correct paths, base selection, ignored-file provisioning, retention, and main-checkout enforcement.
authority: anthropic-docs
status: evolving
verified_on: 2026-08-29
tested_with: Claude Code 2.1.250
---

# Worktree isolation

Claude Code can create or enter a Git worktree so a session or subagent edits separate files and a separate branch while sharing repository history.

!!! info "Current naming"
    A named session uses directory `.claude/worktrees/<name>/` and branch `worktree-<name>`. Older notes that put `worktree-` in the directory name are stale.

## Start or enter

From a shell:

```bash
claude --worktree feature-auth
claude -w feature-auth
```

During a session, ask Claude to work in a worktree; the harness uses `EnterWorktree`. `ExitWorktree` returns a session to its original directory and can keep or remove a harness-created worktree according to explicit cleanup intent.

Add this directory to the repository ignore rules:

```gitignore
/.claude/worktrees/
```

Claude Code refuses symlinked `.claude`/worktree creation paths and verifies the Git identity before using an isolation checkout.

## Choose the base

`worktree.baseRef` accepts:

| Value | Behavior |
|---|---|
| `fresh` (default) | prefer the remote default branch; refresh/cache `origin/HEAD`; fall back to local `HEAD` when no usable remote ref exists |
| `head` | branch from the current checkout's local `HEAD`, including its committed in-progress state |

```json
{
  "worktree": {
    "baseRef": "head"
  }
}
```

Use `fresh` for independent work from the repository baseline. Use `head` when an isolated worker must build on commits from the current stream. Uncommitted changes never appear automatically in another worktree.

For a specific existing branch or custom external location, create the worktree with Git or `dev wt create` rather than treating `baseRef` as an arbitrary ref selector.

## Carry ignored files

Create a tracked `.worktreeinclude` using `.gitignore` patterns:

```text
.env
.env.local
config/secrets.json
```

Claude Code copies only paths that both match and are Git-ignored; tracked files already arrive through checkout. Keep the list minimal, review secret exposure, and install dependencies in the new directory.

This is Claude Code's provisioning surface. `dev` uses `[worktree].include`, dependency strategies, and `.dev-cli/config.toml` (with legacy `.dev.toml` compatibility) for `dev`-owned worktrees; do not assume the two formats are interchangeable.

## Cleanup and retention

Cleanup depends on how the worktree was created and whether it contains work:

- an interactive clean unnamed `--worktree` can be removed automatically at exit;
- a named or changed session prompts to keep/remove;
- non-interactive `-p` runs leave worktrees for later cleanup;
- an isolated subagent worktree with no changes can be removed when it finishes;
- changed/untracked/unpushed worktrees are retained by safe periodic cleanup;
- worktrees created manually or without Claude's ownership marker are retained;
- running agents hold Git worktree locks so concurrent cleanup cannot remove them.

Do not summarize this as “the worktree dies with the turn.” The harness owns the lifecycle, but preservation depends on changes, commits, execution mode, marker/lock state, and versioned cleanup rules.

## Enforcement inside isolation

Current Claude Code blocks selected tool calls that could escape into the protected main checkout:

- `Edit`/`Write`/`NotebookEdit` targeting main-checkout paths;
- shell commands whose working directory resolves to or cannot be proven outside the main checkout;
- Git redirects using `git -C`, `GIT_DIR`, `GIT_WORK_TREE`, or equivalent `cd` into the main checkout;
- command shapes the safety analyzer cannot trace.

These checks protect the checkout boundary, not external services or other local resources. Permissions and sandboxing still apply.

## Subagent isolation

A custom subagent can request:

```yaml
---
name: refactorer
isolation: worktree
---
```

Use this for independent writers or experiments. Do not use it automatically for read-only agents or cooperating writers that already share one `dev` change stream with explicit ownership.

## dev-cli ownership rule

- A human may review/resume the change tomorrow: create the stream with `dev`.
- A harness owns a disposable isolated experiment: use Claude Code worktree isolation.
- Already inside a `dev` worktree: launch the agent there unless it represents a genuinely separate mutation stream.

This avoids nested owners and ambiguous cleanup.

## Sources

- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees)
- [Tools reference](https://code.claude.com/docs/en/tools-reference)
- [Git worktree documentation](https://git-scm.com/docs/git-worktree)
- [`internal/skill/dev-cli/references/worktree-ownership.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/skill/dev-cli/references/worktree-ownership.md)
