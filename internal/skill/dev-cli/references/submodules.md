# Submodule workspaces

`dev repo clone`, clone-style `repo new`, and new linked worktrees initialize
recursively at committed gitlinks by default. Use `--submodules=none` to opt out;
`--no-provision` only skips environment provisioning. Global and project
`[submodules] init = "recursive"|"none"` control the default.

Detached HEAD at a gitlink is intentional. Select development members with
repeatable `dev start --submodule PATH`; they get the task branch at the same
commit. `--submodule-base PATH=REF` records their integration target. Project
`submodules.develop` supplies optional default paths. Direct/branch-only modes
do not switch child branches. Later use `dev submodule develop PATH...` from
the managed worktree. Never infer permission to reset an existing child branch.

`dev submodule init` fills missing checkouts, preserves existing ones and refuses
nonempty uninitialized directories. Initialization failure is not ready: retain
partial results, retry init, and do not launch an agent. `status --json` reports
the local graph without remote access; parent state never substitutes for child
state. Workspace-member intent is versioned separately from task TOML.

Commit/integrate/push affected children before updating and publishing the
outer gitlinks. For dotfiles-all, both affected platform gitlinks belong in one
outer commit. Dev verifies ordering; it does not perform recursive commits,
pushes or merges. Outer `--push` never implies a child push.

`park --cold --recursive`, `retire --recursive`, `wt rm --recursive` and
`sweep --recursive` require complete child recovery evidence. Cold permits
unmerged published work; Retire additionally verifies recorded child integration
targets. Fresh isolated remote proof covers private refs, HEAD/gitlinks,
reflog-only and unreachable objects. Local-only refs, ignored/untracked/dirty
content, stash, unfinished artifacts, other tasks/runtimes/worktrees and unknown
storage/configuration block disposal. A generic `--force` cannot bypass this.

Whole child clone disposal differs from removing an ordinary linked worktree:
approved recursive cleanup deletes private child refs/objects only after remote
proof. Canonical/shared repositories and the outer branch remain. Cleanup stages
children deepest-first, preserves empty gitlinks, and removes the outer worktree
without force or shared-config deinit. Interrupted operations retain a synced
`journal.json`; use `dev submodule recover JOURNAL --dry-run`, then `recover`
from outside. Never remove retained quarantine data merely because a prior
remote check passed. Reload shell integration after upgrading for recursive
post-done handoff.
