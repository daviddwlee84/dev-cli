---
description: Learn the v0.3 command families, permanent shortcuts, command-tree help, and reversible Try graduation.
authority: project
status: maintained
verified_on: 2026-09-24
minimum_version: v0.3.0
---

# Command navigation in v0.3

The root help presents 17 command families and independent entrypoints. Existing
top-level commands remain permanent shortcuts with the same arguments, flags,
output and exit behavior. Scripts do not need to migrate, and shortcuts do not
print deprecation warnings. New examples use the grouped forms.

## Find a command

```bash
dev --help                         # the primary entrypoints
dev help --tree                    # two levels of the canonical command tree
dev help --tree --depth 0          # the whole canonical tree
dev help --tree agent artifact     # focus on one command family
dev help --tree --aliases          # also show shortcuts and aliases
dev tries demote --help            # exact arguments and flags
dev help tries                    # workflow and safety guidance
```

Root completion prioritizes the primary entrypoints. Existing shortcut
invocations retain argument and flag completion. Reload the generated shell
integration after upgrading to pick up grouped command handoffs:
`dev self shell-init <shell>`; shell completion is available through
`dev self completion <shell>`.

| Primary entrypoint | Responsibility |
|---|---|
| `work` | Task lifecycle: `list`, `start`, `park`, `resume`, `done`, `adopt`, `retire`, `sweep` |
| `repo` | Repositories, remotes, `bootstrap`, `note`, and independent `flow` preview |
| `tries` | Experiments: `try`, `open`, `list`, `graduate`, `demote`, and retention actions |
| `git` | Guarded Git operations, `ignore`, `worktree`, `submodule`, and `hygiene` |
| `agent` | `skill`, `mcp`, `instructions`, `prompt`, and `artifact` including `prepare` |
| `activity` | `journal` and durable `stats` |
| `self` | `config`, `cache`, `doctor`, `version`, `upgrade`, `completion`, `shell-init`, `feedback` |
| `snippet` | Cross-provider small-file sharing |
| `ssh` | OpenSSH profiles, keys, discovery, and connections |
| `fleet` | Configured remote machines and their host-local work |
| `dotfile` | Native chezmoi configuration and explicit operations |
| `pr` | Pull-request and merge-request inbox |
| `summary` | Current machine-wide project snapshot |
| `triage` | Cross-repository and Try attention, reports, and reviewed batches |
| `status` | Current repository, checkout, task, and runtime context |
| `tui` | Interactive dashboard and configured tool bindings |
| `help` | Workflow topics and command-tree navigation |

`work` does not introduce a new stored object or task state. Retire and sweep
retain their existing unmanaged-checkout capabilities. `repo flow` remains an
independent preview interface, and ordinary `repo open` still needs no task.
`activity stats` remains durable data; it is not cleared by `self cache clear`.

## Existing shortcuts

| Existing spelling | Canonical spelling |
|---|---|
| `ls`, `list` | `work list` |
| `start`, `park`, `resume`, `done`, `adopt`, `retire`, `sweep` | Corresponding `work` command |
| `bootstrap`, `note`, `flow`, `browse` | Corresponding `repo` command |
| `try` | `tries try` |
| `graduate` | `tries graduate` |
| `gitignore`, `ignore` | `git ignore` |
| `wt`, `worktree` | `git worktree` |
| `submodule`, `hygiene` | Corresponding `git` command |
| `skill`, `mcp`, `instructions`, `prompt`, `artifact` | Corresponding `agent` command |
| `prepare` | `agent artifact prepare` |
| `journal`, `stats` | Corresponding `activity` command |
| `config`, `cache`, `doctor`, `version`, `upgrade`, `completion`, `shell-init`, `feedback` | Corresponding `self` command |
| `edit` | `self config edit` |
| `gist` | GitHub-only shortcut for the snippet workflow |

`dev try archive` still creates or opens an experiment named `archive`; the
explicit management command is `dev tries archive <ref>`. The grouped equivalent
of the former is `dev tries try archive`. A `gist` shortcut keeps its GitHub-only
provider behavior; it is not an unqualified cross-provider snippet request.

Documented JSON schemas, task files, catalog identity and TUI tab names remain
compatible. Shortcuts stay callable even when omitted from the concise root
listing. `git ignore --stdout` and `--list` still work outside a repository.

## Return a graduated repository to Try

```bash
dev tries graduate scratch-parser
dev tries demote scratch-parser --dry-run
dev tries demote scratch-parser
dev tries demote <catalog-id> --to ~/src/tries/2026-09-24-parser
```

Demote accepts only a repository that was previously graduated from a Try. It
moves the current directory back into the configured Try root and keeps its
catalog ID, tags, notes, graduation history, current Git history/remotes and all
current files, including dirty, untracked and ignored data. The resulting Try
is active and present. It does not reverse commits, push, repository publication,
or Git initialization, and does not leave a symlink at the old location.

Run from outside the checkout. A destination must be an immediate visible child
of the current Try root, and runtime coverage must be observable; `none` does
not prove that the checkout is unused.

The default destination is the recorded original Try path. If it is occupied or
outside the current `tries_root`, supply a safe `--to` destination instead.
Existing task, runtime/agent, artifact and linked-worktree claims are not silently
retargeted. Unsafe paths, incomplete observations and stale plans block the
operation. Moves retain the existing same-filesystem, source-revalidation and
recovery safeguards. The CLI applies after showing the move; `--dry-run` only
previews. In REPOS, use the demote action, review the move, and confirm it.

| Dimension | Transitions |
|---|---|
| Identity | Try → `graduate` → Repo → `demote` → Try |
| Intent | active → `deprecate` → deprecated → `reactivate` → active |
| Storage | Try → `archive` → Archive → `restore` → Try |
| Disposal | Try → `delete` → system Trash; restore there first, then `tries restore --from` |

Deprecation does not move files. Trash is not an archive or verified remote
backup, and permanent deletion has no restore path. Remote existence alone does
not make a local clone safely disposable: ignored data, local refs and other
retained state still matter.

Repository open/browse/remote/search behavior is unchanged. This release adds
neither remote Tab search nor a unified search wizard, and does not add repository
eviction, mirror synchronization, `tries new`/`clone`, or graduation symlinks.
