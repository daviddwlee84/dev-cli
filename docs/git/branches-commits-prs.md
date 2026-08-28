---
description: Keep branches short-lived, commits meaningful, pull requests reviewable, and release semantics separate from message style.
authority: git-and-project-policy
status: stable
verified_on: 2026-08-28
---

# Branches, commits, and pull requests

A branch is an episode of work, a commit is one meaningful reversible change, and a pull request is the review/integration boundary.

## When to branch

Use a branch when work spans several commits, the implementation is uncertain, another writer may overlap, interruption is likely, or the default branch must remain releasable.

The current GitHub Flow always starts a change on a branch. `dev` additionally allows one small safe, reversible commit directly on trunk for solo/project-policy workflows. Do not call that exception GitHub Flow, and do not use it when branch protection or team review requires a pull request.

## Branch naming and lifetime

Prefer descriptive namespaces:

```text
feat/...  fix/...  refactor/...  docs/...  chore/...  exp/...
```

A branch ends when its change stream ends. After a squash merge, do not continue new work on the old branch: its original commits are not ancestors of the squash commit and can reappear in later comparisons. Start again from the updated default branch.

Long-lived `release/*` or `maintenance/*` branches need an explicit owner, merge direction, purpose, and retirement condition.

## One meaningful commit

A useful commit can be understood and ideally reverted on its own. It is not necessarily one feature, one file, or one day.

Feature-branch history is construction history; trunk history is product history. Temporary `wip:` checkpoints are acceptable on a private feature branch when they make handoff recoverable, but should be reworded or squashed at integration unless they communicate a useful product change.

## Conventional Commits: specification versus policy

The normative shape is:

```text
<type>[optional scope][optional !]: <description>

[optional body]

[optional footer(s)]
```

Conventional Commits 1.0.0 defines `feat` and `fix`, permits other types, and marks breaking changes with `!`, `BREAKING CHANGE:`, or `BREAKING-CHANGE:`. It does **not** require English, imperative mood, lowercase, a 72-character limit, or a fixed type allowlist.

This project recommends consistent English imperative subjects and commonly uses:

```text
feat fix docs style refactor perf test build ci chore revert
```

That is a house policy for readable/tool-friendly logs, not part of the Git or Conventional Commits specifications. `dev park --wip` deliberately creates a temporary `wip:` type outside that list.

## Pull requests

Open a draft early when design feedback is useful; mark it ready only when its acceptance criteria are testable. A good description includes:

- problem and intended outcome;
- scope and non-goals;
- behavior, migration, or compatibility impact;
- automated and manual test evidence;
- screenshots/logs for user-visible changes;
- linked issues and follow-up work;
- deployment and rollback notes when relevant.

Do not let several agents independently edit the pull-request description, shared manifest, migration order, or generated lockfiles without one integration owner.

## Merge strategy

| Strategy | Choose when | Trade-off |
|---|---|---|
| fast-forward after rebase | branch commits are meaningful product history | preserves bisect/revert granularity |
| squash merge | construction history is noisy or inseparable | one clean commit, loses internal granularity |
| merge commit | preserving branch topology matters | explicit boundary, non-linear history |
| rebase merge | linearized individual commits are desired | commit IDs change; coordinate before rewriting published work |

Never rewrite a shared/published branch without coordination. If rewriting is approved, use a scoped `--force-with-lease`, not an unguarded force push.

## Branch cleanup

`git branch --merged <base>` proves ancestry-based containment. It does not recognize a squash merge because the original commits are not ancestors of the squash commit. Likewise, a `[gone]` upstream only says the remote ref disappeared; it does not prove integration.

Before deleting, inspect the pull request and compare the patch or commit range. Prefer `git branch -d` over `-D`; keep a backup ref when uncertain.

## Release semantics

Semantic Versioning describes compatibility of a declared public API:

- `PATCH`: compatible fixes;
- `MINOR`: compatible additions/deprecations;
- `MAJOR`: incompatible API changes;
- `0.y.z`: initial development; stability is not implied;
- prerelease identifiers sort below the corresponding stable release.

Mapping commit types to version bumps is automation policy, not SemVer itself. Always evaluate the actual public API impact. Published releases and tags should be treated as immutable; create a new version rather than moving a released tag.

## Sources

- [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)
- [Semantic Versioning 2.0.0](https://semver.org/)
- [Current GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow)
- [`internal/help/topics/commits.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/commits.md)
- [`internal/help/topics/branching.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/branching.md)
