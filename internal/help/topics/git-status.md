# Git status symbols

Every dev inventory surface uses the same compact, starship-like status:

```text
⇕⇡3⇣2 =1 +4 !2 ?3
```

| Symbol | Meaning |
|---|---|
| `⇡N` | N commits ahead of upstream |
| `⇣N` | N commits behind upstream |
| `⇕⇡A⇣B` | diverged: A ahead and B behind |
| `=N` | conflicted paths |
| `+N` | staged paths |
| `!N` | unstaged paths |
| `?N` | untracked paths |
| `clean` | published, no divergence or file changes |
| `local` | clean, but no upstream configured |

`dev status` and the TUI detail pane add two lines:

```text
changes    4 changed paths (+1 staged, !2 unstaged, ?1 untracked)
types      1 modified, 1 deleted, 1 renamed/copied
```

`changed paths` is a unique-path total. A path staged and then modified again
is one changed path, but correctly appears in both staged and unstaged
categories — those category counts are not intended to be added together.

Machine-readable output keeps every count separate:

```bash
dev ls --json | jq '.[] | {
  repo, branch, changed, staged, unstaged, untracked, conflicted,
  added, modified, deleted, renamed, ahead, behind
}'
```
