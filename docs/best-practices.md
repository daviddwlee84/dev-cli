---
description: A concise checklist for safe Git, worktree, and coding-agent collaboration with dev-cli.
authority: project-policy
status: stable
verified_on: 2026-08-31
---

# Best practices

Use this page as the short operating policy. Follow the links only when a decision needs more detail.

## The checklist

1. **Start from a known base.** Fetch first and name the intended default-branch commit or base branch.
2. **Create one branch per independent change stream.** Keep unrelated changes and competing approaches separate.
3. **Choose worktrees by mutation boundary, not agent count.** Read-only researchers and writers with clearly disjoint files can share a checkout; overlapping or unknown mutation needs separate branches and worktrees.
4. **Use one integration owner.** Assign file ownership, dependency order, merge order, and the final test responsibility before parallel work starts.
5. **Provision from an allowlist.** Copy only explicitly listed files that Git confirms are ignored. Prefer reinstalling dependencies unless the ecosystem makes copying sound.
6. **Authorize export separately.** A local worktree include is not off-machine permission. Use a separate portable-file allowlist, inspect the report, independently verify the target UUID, and never let `--yes` imply replacement.
7. **Keep one writer per branch.** Across machines, push the branch and transfer ownership before another writer resumes it.
8. **Checkpoint before handoff.** Commit and push recoverable work, and record a concrete `--next` action. Use temporary `wip:` commits on a feature branch rather than stash for cross-machine work.
9. **Review the integrated result.** Worker-local tests are not enough; run the complete relevant suite after changes are combined.
10. **Clean up only recoverable state.** Do not remove dirty, untracked, unpushed, or locked worktrees. Removing a worktree must not silently delete its branch.
11. **Label the authority.** Keep Git semantics, current product behavior, experimental harness behavior, project policy, and historical advice visibly distinct.

## Pick the smallest safe topology

| Situation | Branches | Worktrees | Coordination |
|---|---:|---:|---|
| One small reversible change | 0 or 1 | current checkout | one writer |
| One feature, disjoint files | 1 | 1 | panes/subagents with explicit file owners |
| Research plus implementation | 1 | 1 | read-only researcher, one writer |
| Competing implementations | one per approach | one per approach | compare, keep one, remove the rest safely |
| Unknown or overlapping mutation | one per writer/stream | one per writer/stream | integration owner and ordered merges |
| Large repeatable analysis | as required by write boundaries | isolated where needed | Dynamic Workflow plus verification stages |

A worktree is not a coordination system. It prevents working-directory collisions; it does not assign ownership or isolate ports, databases, caches, hooks, shared refs, or deployment targets.

## The default dev-cli loop

```bash
dev start api --task "refresh tokens" --base main
dev park --next "add expiry regression test" --wip
dev resume "refresh tokens"
dev done --ff
dev sweep
```

Use `dev done --pr` instead of `--ff` when review or CI owns integration. Opening the request does not mark the task DONE.

## The default GitHub Flow loop

Current GitHub Flow is branch → commits/push → pull request → review → merge → delete branch. Deployment policy belongs to the project, not to the modern flow definition.

See [GitHub Flow](git/github-flow.md) and [Branches, commits, and pull requests](git/branches-commits-prs.md).

## The default parallel-agent rule

> **Worktree per change stream. Pane per cooperating agent.**

Use the [parallel work decision guide](claude/parallel-work-chooser.md) before selecting subagents, Agent view, agent teams, or Dynamic Workflows.

## Sources

- [Current GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow)
- [Git worktree documentation](https://git-scm.com/docs/git-worktree)
- [Claude Code: run agents in parallel](https://code.claude.com/docs/en/agents)
- [`internal/help/topics/agents.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/agents.md)
