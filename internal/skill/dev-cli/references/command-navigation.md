# Command navigation

`dev --help` presents 17 primary entrypoints. `dev help --tree` shows two levels
of the canonical tree; `--depth 0` expands all levels, an optional command path
focuses a subtree, and `--aliases` includes shortcuts. Use leaf `--help` for
syntax and `dev help <topic>` for workflow guidance.

| Family | Operations |
|---|---|
| `work` | list/start/park/resume/done/adopt/retire/sweep |
| `repo` | repository operations, bootstrap, note, independent flow preview |
| `tries` | try/open/list/graduate/demote and retention |
| `git` | transactions, ignore, worktree, submodule, hygiene |
| `agent` | skill, mcp, instructions, prompt, artifact including prepare |
| `activity` | journal, durable stats |
| `self` | config/cache/doctor/version/upgrade/completion/shell-init/feedback |

The independent roots are snippet, ssh, fleet, dotfile, pr, summary, triage,
status, tui and help. Older top-level spellings remain permanent shortcuts with
unchanged arguments, output and exit behavior. They do not emit deprecation
warnings. Existing JSON and task/catalog storage contracts are unchanged.

`dev try <name>` and `dev tries try <name>` retain create-or-open semantics,
including names matching management verbs. `dev gist` retains GitHub-only scope.
`dev prepare` is `dev agent artifact prepare`; `dev edit` is `dev self config edit`.
`dev git ignore --stdout/--list` remains usable outside repositories.

`dev repo cd <repo>` is equivalent to `dev repo open <repo> --runtime none`.
It preserves repository completion and resolution, always bypassing runtime
launch/focus. The shell wrapper consumes its directory handoff; without the
wrapper it prints a quoted `cd` command.

Prefer canonical paths in new guidance. Root completion emphasizes these paths;
shortcut argument/flag completion stays available. Reload shell integration
after an upgrade with `dev self shell-init <shell>`.

## Try graduation and demotion

### Graduation (v0.3.1)

`dev tries graduate [try]` and `dev graduate` share the terminal/dashboard
wizard: name, optional category, local (default)/add URL/create GitHub or GitLab,
then preview and confirm. The reviewed source identity and contents are
revalidated before apply. `--yes`/`-y` or non-TTY use runs directly from flags;
`--dry-run` never prompts, probes forge authentication, moves or publishes.

Use `--remote-url` for an existing URL, or `--forge github|gitlab` for creation;
`--remote` retains automatic create-provider selection. Forge also accepts
`auto|none`. Creation supports `--namespace` and `--visibility private|public|internal`
(internal only on GitLab); legacy `--private` remains compatible. Create defaults
to private with push, add URL to no push; explicit `--push[=false]` overrides.
Any existing remote blocks add/create and is preserved by local graduation.
A local name does not rename source code or an existing remote repository.

Regraduation name precedence is explicit `--name`, optional `graduated_name`,
valid legacy `graduated_path` basename (POSIX or Windows), then date-stripped Try
name. Fallback is read-only. Only successful local graduation records the name,
time and path together; category is not remembered. A later remote failure
keeps these local facts, returns nonzero, and never automatically retries or
rolls back the local project or completed remote effects. Inspect the reported
outcome before a follow-up publication action.
Publication revalidates the graduated checkout and reviewed branch/commit before
each stage. Drift after remote creation retains the remote and skips push.
Provider/authentication preflight errors stop before the local move.

### Demotion

`dev tries demote <repo-or-path-or-catalog-id> [--to <path>] [--dry-run]`
accepts only previously graduated Tries. It returns the current bytes and stable
identity to the configured Try root as active/present, retaining dirty/untracked/
ignored files, Git history/remotes, tags, notes and prior graduation history.
It does not reverse Git initialization, commits or publication, create a symlink,
retarget task/runtime/artifact ownership, or turn an arbitrary repo into a Try.

Run from outside the source checkout. Observations cover all available runtime
backends; incomplete coverage or `none` cannot prove absence. The destination
must be an immediate visible child of the current Try root. The recorded original
Try path is the default destination. An occupied or
out-of-root original path requires an explicit safe `--to`; never overwrite or
invent a new destination. Plan/apply revalidates source, destination, catalog,
Git and live ownership. Incomplete observations, active claims, canonical repositories with linked
worktrees and unsafe/cross-filesystem paths block the move; preserve its recovery
journal on an interrupted operation. Use `--dry-run` to preview or REPOS's
confirmed demote action.

On Windows, demotion and concurrent dev lifecycle writers must all use v0.3.0
or later to share the lease across the directory move. Older dev binaries, raw
Git and external tools are outside this move guarantee.

Keep identity, intent and storage separate: graduate/demote changes Try/Repo
identity; deprecate/reactivate changes intent without moving files; archive/
restore moves retained bytes. Delete normally uses system Trash; restore there
before `tries restore --from`. Permanent deletion is not recoverable.
