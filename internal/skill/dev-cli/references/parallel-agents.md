# Parallel background agents

Read this when starting independent agent work while another agent remains live,
or when launching an agent into a worktree created by `dev start --json`.

## Boundary and owners

> **Worktree per change stream; one SpecStory root per worktree; exact pane per
> agent; exact transcript/plan finalization.**

- `dev` owns the durable task, branch and checkout.
- Herdr owns workspace/pane layout and live process detection.
- SpecStory owns rendered history rooted at the process launch checkout.
- Git owns the code, transcript and plan that survive cleanup.

`dev start` does not start an agent. This workflow composes `dev`, Herdr and
SpecStory after verifying each boundary.

## Preflight

- [ ] `test "${HERDR_ENV:-}" = 1`
- [ ] `dev doctor`
- [ ] `command -v specstory`
- [ ] Explicit task name and committed base ref are known.
- [ ] New work does not depend on another checkout's uncommitted state.
- [ ] Launch profile and permission mode are explicit.

If the new task needs dirty state, wait for the owning agent to make a checkpoint
commit and use that exact commit/ref as `--base`. Do not stash, reset, switch, or
copy another agent's checkout.

## Create and validate the target

Use Herdr's native repository/worktree tree as provenance. Worktree-mode
`dev start` passes the same `repo/branch` label as `dev wt create`; do not invent
special origin labels or metadata.

```bash
result="$(dev start <repo> --task '<task>' --base '<committed-ref>' --json)" || {
  # Side effects may exist even though no success JSON was emitted. Stop and
  # reconcile the reported task/worktree manually; do not retry blindly.
  exit 1
}
```

Accept the result only when all of these are true:

```text
mode == "worktree"
repo_path, worktree_path and checkout are absolute
checkout == worktree_path
runtime.name == "herdr"
runtime.surface == "worktree"
runtime.opened == true
runtime.created == true
runtime.root_pane_id is non-empty
```

Missing/reused/fallback/non-Herdr results remain useful tasks/worktrees, but are
not launchable targets. Stop without guessing another pane.

The child workspace handle and root pane come from JSON, while Herdr's native
worktree grouping supplies parent-repository provenance. `dev` pins
`worktree open --cwd <parent-root>` from Git, so this does not depend on the
caller's focused workspace.

## Verify the exact pane

Use only `runtime.root_pane_id` from the JSON object:

```bash
herdr pane get "$root_pane_id"
herdr pane process-info --pane "$root_pane_id"
```

Verify:

- pane cwd and foreground cwd equal the JSON `checkout`;
- it is the returned target workspace/pane, not the caller pane;
- no agent is recognized there;
- process info shows an interactive shell ready for launch.

Do not use current/focused pane, sidebar order, or `pane list` to choose a
fallback. The launch command itself is the final availability gate.

## Select a launcher profile

A new external process does **not** inherit the parent agent's permission mode,
and a resumed/forked Claude session does not restore bypass mode. The effective
SpecStory provider command or verified wrapper is the permission authority:
inspect it and record the selected profile/mode before launch. On this host the
configured Claude and Codex provider commands are elevated (`dangerously-skip-
permissions` / danger-full-access), so a wrapper using them is not implicitly
non-elevated.

| Profile | Exact root-pane command shape | Backend/prerequisite |
|---|---|---|
| standard Claude | `specstory run claude -c 'claude <native args>'` | effective provider/config permissions apply |
| standard Codex | `specstory run codex -c 'codex <native args>'` | effective provider/config permissions apply |
| `claude-copilot-once` | call the shell function directly with native args | already wraps SpecStory; Copilot proxy must already be running; preserves an existing Copilot pin and removes only a pin it created when absent |
| `codex-copilot-once` | call the shell function directly with native args | already wraps SpecStory and injects backend via CLI; may auto-start its proxy path |
| sticky/plain Claude | `specstory run claude -c 'claude <native args>'` | exact `.claude/settings.local.json` must be explicitly provisioned and verified |

Do not add a second watcher. Both `*-copilot-once` wrappers already invoke
SpecStory and preserve argv. Standard native arguments belong inside the single
quoted `-c` command string; arguments after that string are not launcher args.

For explicitly selected Claude bypass, the command *inside* `-c` includes:

```text
claude --permission-mode bypassPermissions
```

An explicit bypass handoff/fork includes all three because resume does not
restore bypass:

```text
claude --permission-mode bypassPermissions --resume <uuid> --fork-session
```

If a safer mode is required while the effective provider command is elevated,
do not append a conflicting mode to that dangerous command. Use a fully
specified SpecStory `-c` command with the intended mode and a materialized,
verified backend pin, or use a separately verified safer wrapper path.

The one-shot wrappers are shell functions, so execute them through the
interactive shell in the exact pane and preserve every native argument.

## Launch, detect and name

Send the fully shell-quoted selected command only to the exact returned pane:

```bash
herdr pane run "$root_pane_id" "$launch_command"
```

Run the selected command in the exact interactive root pane. Then let Herdr
detect the process and assign the requested unique agent name; do not start a
second process merely to obtain a name.

After detection, use Herdr's explicit agent surface to rename/verify the process
and submit work:

```bash
herdr agent rename "$root_pane_id" "$agent_name"
herdr agent get "$agent_name"
herdr agent prompt "$agent_name" "$explicit_prompt"
```

The prompt intentionally omits `--wait`, so independent work continues in the
background. Report checkout, branch, workspace, exact pane, profile and agent
name. Observe later with `agent get`, `agent read`, and `agent wait`; never focus
or send probe keys.

Treat `working`, `blocked`, `idle`, `done`, and `unknown` as occupied. `blocked`
requires a user decision. `done` means turn-settled, not cleanup-ready.

## Shared-checkout override

`dev` guards writer-claiming `start --direct`, `start --branch-only`, and
`resume` when another recognized Herdr agent resolves to the same canonical Git
worktree. The inherited pane ID is resolved through `herdr pane current
--current` before excluding the exact caller, so pane moves do not create a
false collision.

Pure `dev repo open`, `dev wt open`, and TUI Enter/focus are navigation: they
reuse/focus the live owner's workspace and do not authorize another writer, so
they need no override. Use global `--allow-shared-checkout` only for an explicit
writer claim after agents have coordinated disjoint file ownership; this skill
must never add it automatically. Default worktree creation remains independent.

## Project-local backend state

Do not add `.claude/settings.local.json` to global/default includes. It is
needed only by an explicitly selected sticky/plain-Claude profile:

```toml
[worktree]
include = [".env", ".env.local", ".claude/settings.local.json"]
```

The file must be a regular, gitignored source file. `dev` copies only explicitly
included matches, rejects source swaps and symlinked destination parents, never
overwrites a destination, and logs only the relative path—not file contents.
An existing destination is reported as skipped/already-present rather than
copied, so verify its effective backend instead of assuming it was refreshed.
If the required file is absent or stale, stop rather than silently falling back.

The `claude-copilot-once` and `codex-copilot-once` profiles do not need this
copy: their verified wrappers manage backend selection themselves.

## Finalize artifacts and clean up

Before park/integration, settle the agent, identify the exact session UUID, and
from the target worktree root run:

```bash
specstory sync claude -s <exact-session-id>   # or the selected provider
```

Stage exact transcript and plan paths. If one UUID has multiple rendered
candidates, require an explicit path; do not choose by mtime or bulk-sync every
session in the checkout.

Cleanup remains explicit and is never triggered by Herdr `done`:

- `dev park --next '<next>'` closes the workspace and keeps the worktree.
- `dev park --cold --push` closes the workspace and removes the pushed checkout.
- `dev done --ff` integrates and cleans up runtime/worktree.
- `dev done --pr` leaves task/runtime/worktree state for review.
- `dev sweep` reports first; use `--apply` only after review/confirmation.

`dev park --cold --keep-session` is invalid because it would strand a live
session on a removed checkout.
