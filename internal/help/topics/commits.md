# Commits

## What one commit should be

> One meaningful change that can be understood, and ideally reverted, on its own.

Not one feature. Not one file. Not one day's work.

A feature branch can perfectly well read:

```
add user session model
add session repository
add login endpoint
add auth middleware
add auth tests
```

Five commits, one feature. That is good history, not a failure to squash.

## Branch history vs trunk history

These are different things and do not have to match:

```
feature branch = construction history — how you got there
trunk          = product history      — what changed
```

So `wip`, `fix test`, `oops`, `actually fix types` on a branch is fine. It is a
workspace. Decide what the trunk sees at the integration boundary, not while
you are still working.

## Format

Conventional Commits, in English, even when the conversation is not — the log
is read by tooling (changelogs, version bumps) and by people who do not share
your prompt language.

```
<type>(<optional scope>): <subject>

<optional body — the why, wrapped ~72 cols>

<optional footers — Refs: #123 / BREAKING CHANGE: … / Co-Authored-By: …>
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`,
`ci`, `chore`, `revert`.

Subject: imperative, lowercase, no trailing period, header ≤ ~72 chars.
"add retry to client" — not "Added retry." or "Adds retry".

Breaking change: `feat!:` or a `BREAKING CHANGE:` footer. Drives a major bump.

## WIP commits are fine

```bash
dev park --wip --next "reproduce the token refresh race"
```

makes `wip: checkpoint — reproduce the token refresh race`.

Preferred over `git stash` because a stash is invisible in the log, easy to
forget, and cannot be pushed — so it can never reach another machine. A
checkpoint commit is searchable, diffable, pushable, and can be squashed away
later.

## Squash or preserve?

One question: **are these commits worth being in the trunk's history?**

- Yes → `git rebase <base>` then `git merge --ff-only`. Linear, and each commit
  stays revertable.
- No, it is `wip / fix / fix / lint` noise → squash into one logical commit.

Do not make it a blanket rule either way. "Always squash" forces
`one feature = one commit`, which throws away useful bisect granularity on
changes that had real structure.

And after a squash-merge, stop using that branch — continuing on it
re-introduces the commits that were squashed away.
