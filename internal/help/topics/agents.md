# Parallel agents

How to run several coding agents without them stepping on each other.

## The one rule

> **Worktree per change stream. Pane per agent.**

Not one worktree per agent. Isolation should follow the *git mutation
boundary*, not the identity of who is typing.

```
one independent change stream
        → one worktree
        → one workspace
        → N tabs / panes
        → N agents
```

## When agents can share a checkout

They can, and often should, when **file ownership is clear**:

| Parallel work | Same checkout? |
|---|---|
| One researching, one coding | yes |
| Frontend and backend | yes |
| Tests and implementation | usually |
| `src/foo/*` and `src/bar/*` | yes |
| Both editing `package.json` | risky |
| Both changing one API surface | risky |
| Both running a formatter or codegen | risky |
| Both doing large refactors | no |
| Either running `checkout` / `reset` / `rebase` | no |

The question is never "how many agents?" It is **"can these two mutate the same
state?"**

## When they need worktrees

- Mutually exclusive *approaches* to one problem, to be compared and one kept.
- Unknown file ranges.
- Any agent that will run git operations changing HEAD.

```bash
dev wt create exp/jwt      --base main
dev wt create exp/session  --base main
dev wt create exp/oauth    --base main
```

Then compare, keep one, remove the rest. These branches are execution
artifacts, not architecture — they are meant to disappear.

## Two layers, two owners

```
outer isolation   dev      change streams a human will return to
inner isolation   Claude   turn-scoped subagent worktrees, auto-cleaned
```

Do not nest one inside the other. A `dev` worktree containing a Claude worktree
raises questions with no good answers: which branch am I committing to, who
cleans up, does closing the session delete the branch.

Inside a `dev` worktree, just run the agent — no `--worktree` flag needed. The
directory is already the isolation.

## Bringing environment into a worktree

Every worktree is a clean checkout: no `node_modules`, no `.venv`, no `.env`.
`dev` provisions on create. If an agent reports a project that will not run in
a fresh worktree, that is usually the cause:

```bash
dev wt provision      # re-run it
```
