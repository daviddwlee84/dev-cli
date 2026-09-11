---
description: Discover chezmoi configuration, choose an optional platform-specific dotfiles preset, delegate native operations, and inspect selected fleet hosts.
authority: project
status: stable
verified_on: 2026-09-11
---

# Dotfiles

`dev dotfile` is an optional entry point to chezmoi. Chezmoi owns source state,
templates and deployment; the dotfiles repository owns prompts and package
installation. Dev locates the configuration, guides setup and delegates explicit
native operations. It does not require the author's dotfiles.

## Inspect before setup

```bash
dev dotfile
dev dotfile status --json
dev dotfile status --chezmoi-config /path/to/chezmoi.toml
```

Status reads configuration and local Git identity without invoking chezmoi,
rendering templates, running hooks or fetching refs. It distinguishes the
configured source directory, the source-state directory selected by
`.chezmoiroot`, and the Git working tree. A source revision does not prove that
the revision has been applied: deployment drift is explicitly not evaluated.

TOML, YAML, JSON and JSONC configurations are supported. Discovery follows
native XDG search precedence, including secondary configuration/data directories. Ambiguous, unreadable
or invalid configuration remains unknown; it does not fall back to another
checkout or authorize replacing an existing setup. `--chezmoi-config` selects
the local native configuration and is distinct from dev's global `--config`.
Absence of a chezmoi setup says nothing about other dotfile managers.

## Choose a repository

```bash
dev dotfile setup
dev dotfile setup --repo https://github.com/you/dotfiles.git
dev dotfile setup --preset david
```

The interactive flow reviews the repository and native initialization command.
Outside a terminal, setup reports its plan unless `--yes` is supplied. Add
`--apply` to request deployment after initialization. Dev's confirmation does
not answer repository-owned prompts or suppress chezmoi conflict handling.
Existing sources are retained rather than replaced with a recommendation.

The optional `david` preset recommends an author-maintained repository for the
environment in which dev is running:

| Environment | Repository | Support |
|---|---|---|
| macOS, ordinary Linux, WSL inside Linux | [dotfiles](https://github.com/daviddwlee84/dotfiles) | Primary |
| Native Windows | [dotfiles-windows](https://github.com/daviddwlee84/dotfiles-windows) | Windows |
| Native Android Termux | [dotfiles-Termux](https://github.com/daviddwlee84/dotfiles-Termux) | Experimental |
| iSH | [dotfiles-iSH](https://github.com/daviddwlee84/dotfiles-iSH) | Experimental |
| OpenWrt / ImmortalWrt | [dotfiles-OpenWrt](https://github.com/daviddwlee84/dotfiles-OpenWrt) | Experimental |

The `dotfiles-all` superproject is for maintaining these repositories together;
users select the relevant standalone repository. A Windows host is not WSL,
ordinary Alpine is not iSH, and an Android shell is not necessarily Termux.
Ambiguous or unsupported environments receive guidance rather than an assumed
desktop installation. Experimental recommendations do not promise dev binary
availability on every architecture.

If chezmoi is absent, dev shows installation and repository bootstrap guidance.
Specialized platform bootstraps remain native entry points. Dev does not download
and execute bootstrap scripts or choose a package manager on the user's behalf.

## Explicit native operations

```bash
dev dotfile diff
dev dotfile apply
dev dotfile update
```

Diff and apply accept target paths. Apply uses the current source; update uses
chezmoi's own update behavior, including configured commands and subsequent
deployment. Native prompts, output and failures remain visible. Advanced
operations such as `chezmoi edit`, `add` and `re-add` stay with chezmoi.

These explicit operations can evaluate templates and hooks. Apply and update
can run repository scripts and install packages. Even native dry-run is not a
sandbox: [chezmoi hooks run in dry-run mode](https://www.chezmoi.io/reference/configuration-file/hooks/).
Dev does not cache rendered diffs or promise rollback of arbitrary scripts.

## Observe another machine

```bash
dev fleet dotfile status --host lab
dev fleet dotfile status --host lab --host winlab --json
```

Each selected host reads its own user's conventional configuration through a
bounded, versioned passive helper. This does not run chezmoi or load the host's
repository/runtime inventory. Missing or incompatible remote dev is reported
separately from missing chezmoi. Local configuration overrides are not sent to
remote hosts. There is no remote apply or HOME/configuration transfer in this
first version.

The [FLEET host menu](remote-fleet.md#dashboard-host-tree) exposes the same status
without requiring a repository to be selected.

## Existing helpers and ownership

The author's `fleet chezmoi` remains a separate tool with its existing inventory
and behavior. Dev does not import its machine list or redefine its apply/update
semantics. `dev fleet sync` still synchronizes eligible Git branches, and
`dev fleet files` still handles explicitly exported ignored files.

`dotcfg` is the author's repository-specific prompt editor; appsrc diagnoses
application installation sources. They remain optional standalone helpers.
Dotfiles may install dev as a package, while their bootstrap and native
operations continue to work without dev, avoiding a required circular dependency.
