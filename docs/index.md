---
description: Use dev-cli to keep Git change streams durable while worktrees, runtimes, and coding agents remain replaceable.
authority: project
status: stable
verified_on: 2026-08-28
---

# dev-cli

`dev` is a thin coordination layer over Git, worktrees, forges, terminal runtimes, and coding agents. It keeps the durable identity of work in Git while recording just enough intent to close a terminal today and resume the same change stream tomorrow.

!!! tip "Start with one rule"
    **One branch per independent change stream. One worktree per mutation boundary. One pane per cooperating agent.**

## What problem it solves

Four layers often collapse into one mental model. `dev` keeps their responsibilities separate:

| Layer | Owns | Safe to recreate? |
|---|---|---|
| Git remote and branch | durable code history and handoff | no — this is the source of truth |
| Git worktree | one checked-out view of a branch | yes, after commits are recoverable |
| Herdr, tmux, Zellij, or a shell | a live per-host runtime | yes |
| `dev` task registry | state, owner, next action, and reconstruction metadata | yes, from Git plus human intent |

Closing a runtime therefore closes a session, not the work itself. Going cold removes a checkout, not its branch. A task becomes done only after integration.

## Choose a path

- **First use:** install and run a complete task in [Getting started](getting-started.md).
- **Daily workflow:** follow the [change-stream workflow](guides/change-stream-workflow.md).
- **Several agents:** use the [parallel work decision guide](claude/parallel-work-chooser.md).
- **Fresh worktree is missing `.env` or dependencies:** read [Worktrees and provisioning](guides/worktrees-provisioning.md).
- **Need a short policy:** use the [best-practice checklist](best-practices.md).
- **Need exact flags:** open [Commands and configuration](reference/commands-config.md).

## The site map

- **Concepts** defines the lifecycle, ownership boundaries, and vocabulary.
- **Using dev-cli** gives task-, worktree-, runtime-, and TUI-oriented recipes.
- **Git and GitHub** separates current GitHub Flow from Git semantics and historical variants.
- **Claude Code** explains the public agentic harness, its parallel-work primitives, and their limits.
- **Reference** records commands, compatibility, sources, and freshness.
- **Notes** provides a stable home and template for future research.

## LLM-friendly outputs

Every page provides copy-to-LLM controls. The build also publishes:

- [`llms.txt`](https://daviddwlee84.github.io/dev-cli/llms.txt) — a bilingual page index with descriptions.
- [`llms-full.txt`](https://daviddwlee84.github.io/dev-cli/llms-full.txt) — the complete English and Traditional Chinese corpus.

These outputs are generated from the same navigation and page metadata as the site and are checked in CI.

## Source policy

Project behavior is checked against code and tests. Git and GitHub behavior comes from current official documentation. Claude Code behavior comes from current Anthropic documentation and is marked when experimental or research preview. Historical material is never presented as the current workflow.

See [Sources and freshness](reference/sources-freshness.md) for the claim matrix and review dates.
