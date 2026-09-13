# Dashboard lifecycle actions

## Select an action

Press `Ctrl+O`, right-click a row, or click the selected row. Space expands/collapses REPOS
worktrees, FLEET hosts and SSH profiles; it is unused in flat lists. TASKS `a` shows completed tasks and does not mark
a task done. A missing checkout offers recovery instead of completion.

Click another row to select it, then click it again to open the same menu. A row
selected by the keyboard or startup also qualifies; timing is unrestricted and
the menu-opening click executes no option. TASKS stays the initial view; the first
REPOS visit selects the startup repository by common-directory identity without
changing sort order. Subdirectories and linked worktrees select the main repo row.
Manual navigation wins over delayed inventory results.

An outside startup Git repo has clickable Add this repo / Scan parent directory
entries and matching Ctrl+O actions, including when REPOS is empty. Preview the
active config file, field, root and scope before confirming. Exact repo_paths are
preferred for isolated repos; scan_roots uses the main repo's parent and includes
sibling projects. Append only necessary entries, preserve comments/defaults/order,
respect --config, and never consolidate existing entries automatically. Apply
revalidates source bytes and repo identity with private config-recovery data.
Unsupported TOML, symlink configs and non-macOS/Linux platforms offer manual edits.
Successful saves reload local inventory and select the added repo; failed scans
remain unknown, and saved configuration is distinguished from failed reloads.

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

## Dashboard navigation and organizer entry

Enter opens a repository/task row or starts FLEET host navigation. Space expands or collapses REPOS/FLEET/SSH trees; flat lists leave Space unused. REPOS/TRY `Ctrl+O` offers organization of the
current item, filtered results, or all local work; multi-selection belongs to the
independent triage screen. `1–7` selects TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS,
and MCP respectively. TASKS state filters live in its action menu (`a` still
shows done tasks). Click a data column for ascending → descending → default
ordering; FLEET HOST groups machines. Sorting is local to each view/session and
uses the current snapshot, with unknown values last. The footer keeps two lines
of primary actions and navigation; tools, state filters and sorting are in
`Ctrl+O`, and `?` lists the full key map. Existing custom tool bindings for `4–7`
need reassignment; `x`/Ctrl+A are no longer reserved dashboard selection keys.

Triage uses grouped repo/Try checkboxes, mouse selection, and Ctrl+A all/none
within the filtered scope. Results preserve failures when returning to the
dashboard. Git synchronization diagnostics retain a category, exit code, bounded
redacted output and a next step; old receipts cannot recover discarded reasons.
Retries require a new preview. Authentication, fetch and rebase are never
silently performed as error recovery. See [local triage](local-triage.md).


In Ctrl+O, `/` filters the current menu; arrows move and Enter continues. Escape
clears search before closing. REPOS offers current/filtered repository skills,
and SKILLS offers selected skill, project and global scopes. These launch the
[dev skill manage](skills-management.md) wizard. Updates check and preview first;
experimental restore/sync remain single-project actions.


## Issues and suggested actions

Every page's `Ctrl+O` menu includes **issues / suggested actions**, including an
empty list. Start with this page or switch to all pages. The list includes known
inventory and row problems and recent operation failures; it does not visit or
probe unopened pages. Selecting an issue shows its full diagnostic, source,
recovery advice, and a copy action. Long issue lists are paginated and full
messages remain available in the scrollable diagnostic view.

The recovery coverage is explicit:

| Source | Findings retained | Suggested action and completion check |
| --- | --- | --- |
| TASKS | Inventory/Git failures, missing checkout and lifecycle blockers | Recheck local task observations; inspect and recover the exact task through the existing sweep workflow. A changed task revision requires a new review. |
| REPOS | Repository identity, worktree, runtime, task, Git topology and disk-usage failures | Recheck the repository inventory; open the exact item's source/actions where it is still present. Organization retains its existing preview. |
| FLEET | Host observation, connection, trust/version and Herdr failures | Inspect the host's native actions; local recheck reads cache. Authentication and live host refresh remain explicit host actions. |
| TRY | Runtime/Git/disk failures, missing folder and unfinished moves | Recheck local Try state; review the exact Try in the existing organizer/recovery workflow. Never repeat a move or deletion automatically. |
| REMOTE | Inventory/provider and clone/open failures | Read cached observations, inspect retained clone results, and use the provider's login or explicit refresh. If no forge CLI is installed, review installation options. |
| SKILLS | Native lock/file diagnostic codes and paths, integrity and update failures | Read/copy the full diagnostic, open its exact source file, or use the selected skill's management actions. A successful native reload removes resolved diagnostics. |
| MCP | Native source/config diagnostics and declaration coverage codes | Open the exact diagnostic source or declaration actions, then reload static declarations. No server is started by issue inspection. |
| SSH | Registry/SSH permissions, missing tools, failed/partial sources, discovery/test/setup failures | Review exact permission repairs, source details or installation options; local recheck retains independently usable profiles. Discovery does not prove authentication. |
| Shared operations | Notes, stats, clipboard, editor/config saves and partial workflow receipts | Preserve full errors/receipts and copy guidance; reread observations without repeating the operation. Notes Markdown and stats databases remain durable. |

Issue inspection only checks local dependencies and metadata. Unknown error
text never becomes a shell command. A recovery action prepares a concrete preview
before **run this reviewed action in the foreground** becomes available. Closing
the preview does not run it. Operation failures may describe partial success;
completed steps are retained and no automatic retry is started.

Registry permission findings include the exact path, owner, current mode and
expected conditions. Missing registry state is normal. On supported Unix systems,
repair can only tighten the listed current-user-owned paths after revalidating
the complete inspected metadata. Symlinks, hardlinks, other owners and changes
since review block apply. Repairs never recurse or change ownership. Windows
registry ACL repair provides manual guidance until a verified repair backend is
available. Existing SSH permission repair uses its own guarded service.

Dependency options support reviewed Homebrew packages on macOS, apt packages on
Debian/Ubuntu, and exact WinGet package IDs on Windows. The preview includes the
package, resolved command and official instructions. Unsupported tools/platforms
or a missing package manager get instructions only. The foreground installer
keeps its native prompts; dev does not add repositories, install a package
manager, or preaccept terms. The executable is checked afterwards, with an
OpenSSH capability query for `ssh`; authentication and service startup remain
separate. Installation cancellation/failure is not retried.
