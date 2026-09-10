# Dashboard workflows and explicit Try disposal

`Ctrl+O` or right-click opens row actions. TASKS/TRY Space also opens actions;
REPOS Space retains its worktree expansion behavior. TASKS `a` shows done tasks.

Finish, resume, retire and scoped recovery suspend the dashboard for the shared
CLI workflow. Task selections bind ID/revision; stale selections need reloading.
`dev sweep --task <id> [--apply]` only examines that task. Missing checkouts require
recovery, not a completion toggle. Finishing by PR leaves the existing HOT/WARM
intent; integration records DONE; retirement is a separate operation.

REPOS `s`/`d` preselect worktree/direct in the complete start wizard. Open is the
default final handoff choice; stay creates without navigating. Runtime, cd and
external retirement handoffs happen after the dashboard has released the terminal.
No agent is automatically launched.

`dev tries delete <ref>` (`rm` alias) uses native Trash by default. It confirms
the exact path and includes ignored files and all local Git storage. Trash
availability or invocation failure never authorizes permanent deletion. Trash
continues using space until emptied. `--dry-run` previews; `--json` never prompts.
Use `--yes` for noninteractive Trash. Permanent disposal instead needs
`--permanent --confirm-delete <exact-id>` or interactive `DELETE <exact-id>`.

Only independent, unclaimed, locally present/archived Tries are eligible.
Task/runtime claims, cwd, nested repositories, shared/linked Git, unsafe paths
and mount boundaries block removal. `--assume-no-runtime` explicitly records an
unobserved-coverage assumption and never overrides observed live occupation.

The catalog ID, phase, tags/note and other-host locations remain; this host becomes
`evicted` with additive `removal_id`/`removal_method` fields. Durable operation
records are stored beside assets under `try-removals`; they are not cache or
verified backup receipts. Unknown outcomes retain their intent and never trigger
automatic deletion retries. A positively recorded completed removal can finish
its interrupted catalog update during reconciliation.

First restore through the OS Trash UI, then run
`dev tries restore <ref> --from <path>` before editing the restored folder. The
original filesystem identity and contents must match. This only reassociates
restored bytes; it does not search Trash or restore permanent deletions.

`dev browse` / `dev repo browse [repo-or-path]` uses local Git to choose current
branch upstream, origin or the only remote. Other ambiguous choices require
interactive selection or `--remote`; `--print` never opens a browser or prompts.
Only supported URLs pass through the existing forge URL mapper. `repo context`
remains the detailed information command.

After an interrupted Trash operation, `restore --from` can explicitly reassociate
an unchanged original/restored folder; it never retries deletion. A folder put
back into its original archive path remains archived and can then use ordinary
`tries restore` to become visible again.

## Dashboard navigation and organizer entry

Enter always opens a dashboard row. REPOS/TRY `Ctrl+O` offers organization of the
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
