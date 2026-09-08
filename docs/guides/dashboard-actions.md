---
description: Finish and recover tasks from the dashboard, dispose of Tries through system Trash, and open repository homepages.
authority: project
status: evolving
verified_on: 2026-09-08
---

# Dashboard lifecycle actions

## Select an action

Press `Ctrl+O` or right-click a row. TASKS and TRY also accept Space; REPOS Space
still expands linked worktrees. TASKS `a` shows completed tasks and does not mark
a task done. A missing checkout offers recovery instead of completion.

TASKS finish, resume, retirement and recovery use the existing CLI workflows.
The dashboard suspends during their prompts and refreshes when they finish,
including after an error. Task ID/revision checks prevent using a stale selection.
`dev sweep --task <id>` reports only the selected task; add `--apply` to confirm
its eligible suggestions. Other worktree-wide sweep modes cannot be combined
with `--task`.

Finishing through PR preserves HOT/WARM intent. Integration records DONE;
retirement separately removes eligible execution state and keeps the branch by
default. These actions retain the existing taskflow safety rules.

## Start and open work

REPOS Enter opens the selected repository without creating a task. `s` and `d`
open the full start wizard with worktree or direct preselected. The wizard asks
for the task and necessary branch/base/next action, then offers open (default)
or stay before final creation confirmation. A base is explicit in the reviewed
plan, never inferred from an arbitrary checked-out branch.

The dashboard releases its terminal before an attach, shell-directory handoff,
or external retirement coordinator runs. An ordinary completion/cancellation
returns to the dashboard. No agent is launched automatically.

## Dispose of a Try

```bash
dev tries delete <ref> --dry-run
dev tries delete <ref>
dev tries delete <id> --permanent --confirm-delete <id>
```

`rm` is an alias for `delete`. The default moves the selected folder to macOS
Trash, Linux GIO Trash, or Windows Recycle Bin. Missing helpers, unsupported
volumes or failures never cause a permanent-delete fallback.
Linux removal also requires kernel/filesystem support for statx mount identity,
so bind mounts cannot be mistaken for owned child directories. Trash, like archive,
still occupies disk space until the bytes are deleted. Helpers are not installed
automatically. On macOS 15+ the system `trash --stopOnError` utility is preferred;
older systems use Foundation through JXA without out-pointer bridging.

Permanent disposal is a separate choice requiring `DELETE <exact-id>` in the
interactive prompt or `--permanent --confirm-delete <id>` in scripts. `--yes`
alone approves only Trash. `--json` never prompts; `--dry-run` never deletes.
The preview covers ignored/untracked files and all local Git storage, including
branches, tags and stash. No remote backup has been verified.

Only independent, cataloged, local active/deprecated Tries can be disposed of,
including archived folders. Task claims, live runtime, cwd, nested repositories,
linked/shared Git, mount boundaries and unsafe paths block removal. Unknown
runtime coverage requires a separate `--assume-no-runtime` acknowledgement;
this does not override an observed live session.

Catalog ID, phase, metadata and other-host locations remain. The local location
becomes `evicted` with additive `removal_id`/`removal_method` fields. Durable
operation JSON is stored in `try-removals` beside assets; this is not cache or a
verified backup receipt. Interrupted operations stay indeterminate and never
retry deletion automatically. A recorded successful removal may finish an
interrupted catalog update during reconciliation.

## Restore through the system Trash

First restore the original directory using the OS Trash UI, then reassociate it
before editing its contents:

```bash
dev tries restore <ref> --from <restored-path>
```

The TRY history action provides the same path prompt. dev verifies the retained
filesystem identity and content inventory, restoring the same catalog ID.
It does not search Trash or recover permanently discarded bytes. Existing archive
restoration still uses `dev tries restore <ref> [--to <path>]`.

## Open a repository homepage

```bash
dev browse
dev repo browse api --remote origin
dev browse --print
dev repo context
```

With no argument, browse resolves the current repository, including a linked
worktree. It chooses current branch upstream, then origin, then the sole remote;
otherwise interactive use asks and scripts require `--remote`. `--print` only
prints an HTTPS URL and never prompts or opens a browser. Resolution uses local
Git, never fetch or a forge query. Unsupported hosts/SSH aliases are not guessed.

TASKS, REPOS, Git-backed TRY and REMOTE have browser actions. These keep the
dashboard open. `repo context` remains the detailed information report; browse
opens the repository homepage rather than a branch/file-specific page.

After an interrupted Trash operation, `restore --from` can explicitly reassociate
an unchanged original/restored folder; it never retries deletion. A folder put
back into its original archive path remains archived and can then use ordinary
`tries restore` to become visible again.
