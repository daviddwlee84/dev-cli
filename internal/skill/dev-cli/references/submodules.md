# Submodule workspaces

## Add a known repository

```bash
dev git submodule add owner/library libs/library --dry-run --json
dev git submodule add owner/library libs/library --checkout=pinned --ref=v1.2.0 --yes --json
dev repo add-as-submodule owner/library libs/library --checkout=default-branch --yes
```

Use the dry-run to verify the exact parent checkout, portable network source,
relative path and mode. Only apply the approved request. `--parent PATH` selects
an exact checkout; the default is the nearest repo containing cwd, not its
canonical or outermost checkout. Source selection combines known local remotes
and cached forge repositories without refreshing. Ambiguous names need an exact
URL or interactive selection. Local-only sources and existing targets/stores are
not adopted. REPOS/REMOTE `y u` copies the selected network clone URL locally.

The wizard offers pinned/default-branch. Non-interactive default is pinned;
`--ref` is pinned-only, and a missing ref resolves the fresh clone's default
branch commit. Default-branch mode tracks the real default, not an assumed main.
Both stage a fixed gitlink; neither enables remote-following updates or creates
task-member intent. `--submodules=recursive|none` controls only new descendants.
Only `.gitmodules` and the new gitlink are staged; unrelated staged/dirty work
and other checkouts are preserved. No commit, push, task branch or runtime is
created. Use `develop` separately for managed task membership.

**Gotchas:** `.gitmodules` must be absent or unchanged, including hidden index
edits; filters/encodings, conflicts, active Git operations, detached/unborn
parents and unsafe paths block addition. `--yes` is required for non-interactive
mutation; `--json` never prompts. A local `planned` result has no resolved HEAD.
An error may still return a partial result (`phase`, `git_dir`, `head`, `staged`,
`warnings`). Inspect retained paths and finish metadata/staging manually; for
`initialization-incomplete`, run `dev git submodule init` inside the new child.
Never force-delete partial clones or replay add over them. The retirement
`recover` journal format does not apply to addition.

## Initialize and develop existing gitlinks

`dev repo clone`, clone-style `repo new`, and new linked worktrees initialize
recursively at committed gitlinks by default. Use `--submodules=none` to opt out;
`--no-provision` only skips environment provisioning. Global and project
`[submodules] init = "recursive"|"none"` control the default.

Detached HEAD at a gitlink is intentional. Select development members with
repeatable `dev work start --submodule PATH`; they get the task branch at the same
commit. `--submodule-base PATH=REF` records their integration target. Project
`submodules.develop` supplies optional default paths. Direct/branch-only modes
do not switch child branches. Later use `dev git submodule develop PATH...` from
the managed worktree. Never infer permission to reset an existing child branch.

`dev git submodule init` fills missing checkouts, preserves existing ones and refuses
nonempty uninitialized directories. Initialization failure is not ready: retain
partial results, retry init, and do not launch an agent. `status --json` reports
the local graph without remote access; parent state never substitutes for child
state. Workspace-member intent is versioned separately from task TOML.

Commit/integrate/push affected children before updating and publishing the
outer gitlinks. For dotfiles-all, both affected platform gitlinks belong in one
outer commit. Dev verifies ordering; it does not perform recursive commits,
pushes or merges. Outer `--push` never implies a child push.

## Recursive cleanup

`park --cold --recursive`, `retire --recursive`, `wt rm --recursive` and
`sweep --recursive` retain full recovery requirements for initialized child
clones. Cold permits unmerged published work; Retire additionally verifies
recorded child integration targets. Fresh isolated remote proof covers private
refs, HEAD/gitlinks, reflog-only and unreachable objects. Local-only refs,
ignored/untracked/dirty content, stash, unfinished artifacts, other tasks/runtimes/
worktrees and unknown storage/configuration block disposal.
`objects/info/alternates` remains an intentional guard. A generic `--force`
cannot bypass child checks.

**Empty-child exception:** only when removing a linked worktree, a gitlink whose
path is absent or truly empty and has no retained child Git store can be proved
empty locally. `--recursive` is still required, but do not initialize, download,
request remote proof or push such children merely for cleanup. Initialization
and publication policy are unchanged. Deinitialized retained data, orphan stores,
local/ignored files, unexpected `.git`, symlinks/reparse points, external ownership
and incomplete observations block this exception.

Physical emptiness alone is not removal permission. Manually deleting a tracked
gitlink directory makes the parent dirty; ordinary non-force removal blocks
before admin pruning. Never create or restore empty placeholders to bypass this
guard. A never-initialized, Git-created empty directory can pass; an absent child
alone does not prove parent cleanliness.

Check empty-child task/artifact claims by path; parent-inherited Git discovery
is not child identity. Any matching non-discarded artifact intent, even finalized,
blocks; discarded records still bind plan authority. Changed layouts, new child
initialization or new claims require a new plan.

Whole child clone disposal differs from removing an ordinary linked worktree:
approved recursive cleanup deletes private child refs/objects only after remote
proof. Canonical/shared repositories and the outer branch remain. Real children
are staged deepest-first with synced journals, including in mixed empty/initialized
workspaces. Only exact reviewed empty modules admin scaffolding may be pruned
under existing locks, rechecking directory identity and emptiness immediately
before native directory-only removal. Empty checkout directories stay for ordinary
non-force `git worktree remove`; never substitute force or recursive file deletion.
General worktree removal guards remain in force. Partial pruning failures are
reported, not called RETIRED, and real-store journals/rollback remain intact.

For a retained `journal.json`, use `dev git submodule recover JOURNAL --dry-run`, then
`recover` from outside. Never delete quarantine data using an old remote proof.
Reload shell integration after upgrading for recursive post-done handoff.
