---
description: Install dev-cli, initialize a machine, and run one change stream from start through integration.
authority: project
status: stable
verified_on: 2026-08-29
---

# Getting started

Install `dev`, create or clone a repository, then run one small change stream through `start`, `park`, `resume`, and `done`.

## 1. Install and initialize

```bash
make install
dev config init
```

Add the shell integration so commands that open a checkout can change the current shell's directory:

=== "zsh or bash"

    ```bash
    eval "$(dev shell-init zsh)"   # use bash for bash
    ```

=== "fish"

    ```fish
    dev shell-init fish | source
    ```

Check the effective environment:

```bash
dev doctor
dev config show
```

Only Git is required. Herdr, tmux, Zellij, `gh`, and `glab` add richer runtime and forge behavior but degrade cleanly when absent.

## 2. Create or clone a repository

From any directory, open the repository bootstrap wizard:

```bash
dev repo new
```

It chooses a destination under configured `project_root`, previews the plan,
and can use the built-in `minimal` or `agent-ready` preset. `agent-ready` adds
starter agent guidance and project-scoped Claude plans; when selected, optional
skills such as `agent-history-hygiene` and `project-knowledge-harness` are
installed, while reviewed built-in initializers create their project surfaces
during this flow without executing newly downloaded skill code.

To acquire existing code, pass an owner/name or Git URL to clone. To apply the
same setup to an existing checkout, use setup:

```bash
dev repo clone owner/api
dev repo clone git@gitlab.example.com:group/api.git
dev repo setup . --preset agent-ready
```

The wizard offers GitHub or GitLab publishing only when the corresponding
`gh` or `glab` CLI is installed and authenticated. Local-only remains the
default. Its final handoff can stay in place, `cd` into the repository, open
the configured terminal runtime, or continue to the `dev start` wizard.
Bootstrap and `dev start` do not launch a coding agent.

If you already have a repository and need no setup, continue there directly.

## 3. Start work

From any discovered repository, start a named task. Pass the base explicitly in scripts and agent-driven commands:

```bash
dev start api --task "token refresh" --base main
```

The default mode creates a branch, a linked worktree at the configured path, provisions it, opens the best available runtime, and records a HOT task.

Use a lighter checkout mode only when it matches the work:

```bash
dev start api --task "one-line typo" --direct
dev start api --task "small local branch" --branch-only --base main
```

- `--direct` works in the canonical checkout and cannot go COLD.
- `--branch-only` creates a branch without a linked worktree.
- The default worktree mode is best for independent or parallel mutation.

## 4. Park with a useful next action

```bash
dev park --next "reproduce the refresh race, then add a regression test"
```

This closes the runtime and marks the task WARM while retaining the branch and checkout. If the tree is dirty, make a recoverable checkpoint instead of an invisible stash:

```bash
dev park --wip --next "finish the regression test"
```

For cross-machine handoff, commit and push before removing the checkout:

```bash
dev park --cold --push
```

## 5. Resume and inspect

```bash
dev ls
dev resume "token refresh" --fetch
dev status
```

A WARM task reopens its existing checkout. A COLD task reconstructs a worktree from the remote branch. The branch is the durable identity; the directory is a cache.

## 6. Integrate deliberately

```bash
dev done --ff
```

`--ff` rebases the change branch onto its base and fast-forwards the base, preserving useful commits. If review or CI should decide the merge, open a request instead:

```bash
dev done --pr
```

`--pr` pushes and opens a pull or merge request, but intentionally leaves the task unchanged and not DONE while review is pending. After integration, inspect cleanup suggestions:

```bash
dev sweep
dev sweep --apply
```

## Next steps

- [Mental model and lifecycle](concepts/mental-model.md)
- [Repository bootstrap and project configuration](reference/commands-config.md#repository-bootstrap)
- [Change-stream workflow](guides/change-stream-workflow.md)
- [Worktrees and provisioning](guides/worktrees-provisioning.md)
- [Commands and configuration](reference/commands-config.md)

## Sources

- [`README.md`](https://github.com/daviddwlee84/dev-cli/blob/main/README.md)
- [`internal/cli/repo_create.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/repo_create.go)
- [`internal/help/topics/parking.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/parking.md)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
