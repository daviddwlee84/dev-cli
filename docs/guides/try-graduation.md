---
description: Graduate a Try with a reviewed local name and publication choice, retain prior naming across demotion, and understand partial remote failures.
authority: project
status: maintained
verified_on: 2026-09-24
minimum_version: v0.3.1
---

# Graduate a Try

`dev tries graduate [try]` promotes an experiment into
`<project_root>/<category>/<name>`, retaining its catalog identity and current
files. Without a Try argument it uses the experiment containing the current
directory. The permanent `dev graduate` shortcut has the same behavior.

## Choose the name and publication

In a terminal, graduation opens a wizard: choose the project name and optional
category, choose publication, review the destination and effects, then confirm.
The default is a local project. TRY → Ctrl+O → graduate opens the same workflow.
Cancellation returns to and refreshes the dashboard; success leaves the dashboard
before opening the graduated project or handing off its shell directory.

| Choice | Default behavior |
|---|---|
| Local project | Move locally and preserve all existing remotes |
| Add an existing URL | Add the URL as origin; do not push unless selected |
| Create on GitHub or GitLab | Create a private repository and push by default |

Publication choices are offered when the Try has no remotes. If it already has
any remote, even only `upstream`, graduation preserves every remote; an explicit
add/create request fails before moving. Changing the local project name does
not rename packages, modules, source code or an existing remote repository.

A Try without Git is initialized, and a first commit is made when needed by the
existing graduation flow. Moving and publication remain separate effects.
The reviewed plan binds the source identity and contents across prompts; a
changed plan must be reviewed again before it can apply.

## Scripts and previews

`--yes` / `-y` bypasses the wizard and confirmation. Noninteractive input also
uses the explicit flags and defaults directly. `--dry-run` never prompts,
checks forge authentication, moves files or publishes; it prints the plan.
Cancellation leaves graduation and publication unapplied.

```bash
dev tries graduate parser                 # interactive wizard
dev tries graduate parser --name parser-core --category Tools --dry-run
dev tries graduate parser --name parser-core --category Tools --yes
dev tries graduate parser --remote-url git@github.com:example/parser-core.git --yes
dev tries graduate parser --remote-url git@github.com:example/parser-core.git --push --yes
dev tries graduate parser --forge github --namespace example --visibility private --yes
```

`--forge` accepts `auto`, `github`, `gitlab`, or `none`. Explicit `github` or
`gitlab` selects creation; `--remote` retains the existing create-remote spelling
and uses automatic provider selection unless overridden. `--namespace` selects
the new remote owner/group. `--visibility` accepts `private`, `public`, or GitLab's
`internal`; creation defaults to private. The existing `--private` and `--push`
flags remain supported.

For creation, push defaults to true. For an added URL it defaults to false;
explicit `--push` or `--push=false` controls either path. Local graduation does
not push existing remotes. Conflicting remote choices, create-only flags with
an added URL, incompatible visibility, or contradictory explicit `--private`
and `--visibility` fail before the local move.

## Graduate again after demotion

After `dev tries demote`, the next graduation chooses its default name in this
order:

1. Explicit `--name` for this invocation.
2. The last successful local graduation's optional `graduated_name`.
3. A valid project-name basename from legacy `graduated_path` metadata.
4. The current Try name with its date prefix removed.

Legacy POSIX and Windows paths can supply that fallback without rewriting the
catalog during reads. Preview and cancellation do not change remembered naming.
A successful local graduation records its name, time and destination together;
a later publication failure keeps those completed local facts. Category is
chosen for the current invocation and is not remembered from the previous one.

Demotion retains current files, Git history and remotes; it does not reverse
publication. Regraduation therefore normally keeps those remotes and moves
locally. See [demotion and its move constraints](../reference/cli-v0.3.md#return-a-graduated-repository-to-try).

## If remote work fails

Provider/authentication preflight errors stop before the local move. After the
move succeeds, an add/create/push failure returns a nonzero exit code and reports
the completed local graduation separately. The local project and any completed remote effects
are retained. There is no automatic retry or rollback of publication or the
local move. Inspect the reported destination and remote outcome before choosing
a follow-up action; rerunning graduation is not a remote-operation retry.

Publication revalidates the graduated checkout and the reviewed branch/commit
before each stage. If they change after remote creation, the remote is retained
and the push is skipped; the result reports the completed and skipped effects.
