---
name: retirement
summary: Why an agent integrates first, exits, and is cleaned up externally.
---

# Agent-safe retirement

A process must not delete the worktree that contains its own current directory.
The terminal can remain visible after Unix unlinks the path, but config reloads,
relative files and new subprocesses then fail. Herdr still showing a pane does
not mean the Git checkout is healthy.

Use three separate milestones:

1. **READY** — commit product work, run `dev prepare`, then exit normally so the
   post-SpecStory finalizer can commit the exact final transcript. A manual
   external finalizer must pass `--writer-stopped`; a Claude SessionEnd observer
   can provide the same durable proof without staging during teardown.
2. **MERGED** — an external coordinator runs `dev done --ff`, or verifies an
   external merge with `dev done --merged --base-ref <ref>`. Runtime and
   worktree remain intact.
3. **RETIRED** — from outside the target workspace run `dev retire`. It closes
   eligible runtime sessions, waits until they disappear, revalidates Git, and
   removes the worktree without force.

Retirement never overrides `working`, `blocked`, or `waiting` agents. Unknown
status needs external `--close-unknown`. A workspace containing panes outside
the target is mixed-purpose and must be reorganized or closed manually.

Raw `git worktree remove --force` bypasses these protections. Do not use it on
an agent-owned checkout.
