# When to branch, and when not to

Should this be a branch, or just a commit on the trunk?

## Four questions, asked in order

```
1. Can this land safely in one commit?
      yes → commit to the trunk
      no  → branch

2. Is another writer touching the same files?
      no  → the checkout you are in
      yes → a worktree, on its own branch

3. Does anyone or anything else need to see it?
      no  → keep the branch local
      yes → push it

4. Are the branch's commits worth keeping in the trunk's history?
      yes → rebase, then merge --ff-only
      no  → squash at the integration point
```

That is the whole decision. Everything below is detail.

## Branch when

- it will take more than one commit;
- the implementation is still uncertain;
- you might stop halfway and do something else;
- an agent will change a lot of code and you want a clean rollback;
- the trunk has to stay releasable.

## Do not branch when

- it is a typo, a config tweak, a README edit, one clear bug;
- it fits in one commit you would be comfortable reverting.

Solo projects do not need a branch for every change. The ceremony has to earn
its cost.

## Naming

```
feat/…   fix/…   refactor/…   chore/…   docs/…   exp/…
agent/…            work an agent produced, so it can be triaged in bulk
worktree-…         created by `claude --worktree`
```

Separate namespaces are what make `git branch --list 'agent/*'` a safe cleanup
target rather than a risky one.

## A branch is an episode, not a home

Once `feat/auth` has merged, it is finished. When auth needs a refresh token
next week, branch again from the trunk:

```bash
git switch main && git pull --ff-only
git switch -c feat/auth-refresh
```

Do not revive the old branch. After a squash-merge especially, continuing on
the same branch re-introduces commits that were already squashed and generates
conflicts that did not need to exist.

## Long-lived branches

Only `release/*`, `maintenance/*` and the like, and only with an explicit
purpose, merge direction, lifetime and owner. Without all four, a long-lived
branch becomes a place work goes to be forgotten.

If two lines have genuinely stopped merging — different products, different
release cycles, different architecture — that is a fork, not a branch.

## Useful defaults

```bash
git config --global pull.rebase true          # rebase local work on pull
git config --global fetch.prune true          # drop deleted remote refs
git config --global push.autoSetupRemote true # first push sets upstream
git config --global merge.ff only             # refuse a surprise merge commit
```

The point of `merge.ff only` is that a diverged pull **fails loudly** instead
of silently producing a merge commit you did not ask for.
