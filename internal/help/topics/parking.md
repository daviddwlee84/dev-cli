# Parking and resuming

How to stop working on something without losing the thread.

## Why this exists

When the terminal multiplexer's sidebar is the only record of what you are
working on, nothing can ever be closed — closing it loses the thread. So the
sidebar grows until it is no longer scannable, which defeats its purpose.

`dev` gives that record a home outside the runtime. Then closing a session is
just closing a session.

## The states

| State | Git | Runtime | Meaning |
|---|---|---|---|
| 🔥 hot | worktree + branch | session open | working on it now |
| 🌤 warm | worktree + branch kept | session closed | back within days |
| ❄️ cold | committed and pushed, worktree removed | nothing | paused, reconstructible |
| ✅ done | merged | nothing | entry survives until swept |

Aim for **three to seven hot tasks per machine**. That is a cognitive limit,
not a technical one.

## Parking

```bash
dev park --next "reproduce the token refresh race, then add a regression test"
dev park --wip                  # checkpoint uncommitted work first
dev park --cold --push          # push, then remove the worktree
```

`--next` is the part that matters. Without it, resuming means re-deriving where
you were from a diff — which is most of the cost of a context switch.

## Resuming

```bash
dev resume "token refresh"
```

Warm tasks still have their worktree, so this just reopens a session. Cold
tasks do not: the worktree is rebuilt from `origin/<branch>`.

That is why going cold is safe. **The branch is the identity; the directory is
a cache.** Once that is true, the local filesystem stops being a graveyard of
half-finished checkouts.

## Moving between machines

The remote is the handoff boundary. Do not sync worktrees or runtime state —
sync branches.

```bash
# on the machine holding the work
dev park --cold --push

# on the machine picking it up
dev resume <task> --fetch
```

**One writer per branch at a time.** `dev` records an owner host and refuses to
resume someone else's task without `--force`. Two machines committing to one
branch is the reliable way to produce a conflict here; the ownership check
prevents it.

If two machines genuinely need the same feature at once, split the branch —
`feat/auth-ui`, `feat/auth-api` — and integrate afterwards. Parallel writers
get parallel branches.

## Sweeping

```bash
dev sweep            # report only
dev sweep --apply    # act, confirming each change
```

Reports drift (hot with no session, a worktree git no longer knows about),
staleness (warm and untouched, clean and pushed → could go cold), and live
sessions no task claims.

It never deletes uncommitted work and never acts without `--apply`. Cleanup
usually fails not from unwillingness but from the absence of a trustworthy
guarantee that the work is recoverable. Reporting first is that guarantee.
