# Task lifecycle

Read this when work needs to be paused, picked up on another machine, or when
a machine has accumulated more open sessions than anyone can hold in mind.

## The problem

A terminal multiplexer's sidebar is a *runtime* view: it shows what is running
now. When it is also the only record of what you are working on, nothing can be
closed, because closing it loses the thread. The sidebar grows until it stops
being scannable, which defeats its purpose.

The fix is not discipline. It is giving "what am I working on" a home that is
not the runtime.

## The four states

| State | Git | Runtime | Meaning |
|---|---|---|---|
| 🔥 `hot` | worktree + branch | session open | working on it now |
| 🌤 `warm` | worktree + branch kept | session **closed** | back within days |
| ❄️ `cold` | committed and pushed; worktree removed | nothing | paused, reconstructible anywhere |
| ✅ `done` | merged | nothing | entry survives until swept |

Only the transitions between them cost anything:

```
        dev start
            │
            ▼
         🔥 hot ──── dev done ────► ✅ done
          │  ▲
   dev park│  │dev resume
          ▼  │
        🌤 warm
          │  ▲
dev park  │  │dev resume
  --cold  ▼  │  (rebuilds the worktree)
        ❄️ cold
```

**A machine should hold roughly three to seven hot tasks.** That is a
cognitive limit, not a technical one. Everything else belongs in `dev ls`.

## Parking

```bash
dev park --next "reproduce the token refresh race, then add a regression test"
```

This is the move the whole design exists to make safe. It records what to do
next, closes the runtime session, and leaves the branch and worktree exactly
where they are.

`--next` is the important part. Without it, resuming means re-deriving where
you were from a diff, which is most of the cost of a context switch. Always
supply one.

Useful variations:

```bash
dev park --wip                    # checkpoint uncommitted work first
dev park --cold --push            # push, remove the worktree, keep the branch
dev park --keep-session           # record the state, leave the session open
```

## Checkpoint commits, not stashes

`--wip` makes a `wip: checkpoint — <next>` commit rather than a `git stash`.

A stash is invisible in the log, easy to forget, and **cannot be pushed** — so
it can never reach another machine. That last point is decisive: the whole
value of parking is that the work is recoverable from anywhere.

Messy WIP commits on a feature branch are fine. Branch history is *construction
history*; the base branch's history is *product history*. They do not have to
match — clean the branch up at the integration boundary with an interactive
rebase, or squash it, and decide then.

## Cold, and why it is safe

Going cold removes the checkout. That feels lossy, and is not, because:

- the branch still holds every commit;
- `dev park --cold` refuses unless the branch is pushed, so the remote holds
  them too;
- `dev resume` rebuilds the worktree from `origin/<branch>`.

The directory is a cache. The branch is the identity. Once that is true, a
local filesystem stops being a graveyard of half-finished checkouts.

## Moving work between machines

The remote is the handoff boundary. Do not sync worktrees, runtime state, or
the multiplexer's database between machines — sync branches.

On the machine holding the work:

```bash
dev park --cold --push
```

On the machine picking it up:

```bash
dev resume <task> --fetch
```

**One writer per branch at a time.** `dev` records an `owner` host and refuses
to resume a task owned by another machine without `--force`. Two machines
committing to one branch is the only reliable way to make this workflow produce
a conflict; the ownership check is what prevents it.

If two machines genuinely need to work the same feature at once, split the
branch — `feat/auth-ui` and `feat/auth-api` — and integrate afterwards. That is
the same rule as for parallel agents: parallel writers get parallel branches.

The task registry itself (`$XDG_DATA_HOME/dev/tasks/`) is one small TOML file
per task, specifically so it can live in a private git repo and merge cleanly
across machines. Point `paths.state_dir` at one if you want a shared view.

## Sweeping

```bash
dev sweep            # report only
dev sweep --apply    # act, confirming each change
```

`sweep` reports two kinds of problem:

- **Drift** — a task marked hot with no live session, a warm task with one, a
  recorded worktree git no longer knows about.
- **Staleness** — a warm task nobody has touched in two weeks, clean and
  pushed, that could go cold.

It also lists **live sessions no task claims** — the other half of a crowded
sidebar, and usually the larger half.

`sweep` never deletes uncommitted work, and never acts without `--apply`. The
reason cleanup usually does not happen is not unwillingness; it is the absence
of a trustworthy guarantee that the work is recoverable. Reporting first, and
requiring confirmation, is that guarantee.

## Integration

```bash
dev done --ff     # rebase onto the base, then fast-forward it
dev done --pr     # push and open a pull/merge request via gh or glab
```

Which one depends on a single question: **are this branch's commits worth
keeping in the base's history?**

- Yes → `--ff`. Linear history, no merge commit, `git bisect` and `git blame`
  stay legible.
- No, it is WIP noise → squash at the integration point.
- Someone (or CI) should look first → `--pr`.

`dev done` refuses on a dirty tree, and `--delete-branch` only removes a branch
git agrees is fully contained in the base. "Merged" is not always "finished" —
work often continues on a branch after its first integration, which is why the
branch survives by default.
