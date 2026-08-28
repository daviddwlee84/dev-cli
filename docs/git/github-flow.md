---
description: Follow the current GitHub Flow while keeping older deploy-and-merge variants in clearly labeled historical context.
authority: github-docs
status: official
verified_on: 2026-08-28
---

# GitHub Flow

Current GitHub Flow is a lightweight branch-and-pull-request workflow. Its authoritative steps are branch → changes and pushes → pull request → review → merge → delete branch.

!!! info "Freshness"
    **Authority:** current GitHub Docs · **Status:** official workflow guidance · **Verified:** 2026-08-28. Historical sources below are descriptive, not today's operating procedure.

## Current flow

### 1. Create a branch

Start from the repository's default branch and use a short descriptive name. Keep unrelated changes in separate branches.

```bash
git switch main
git pull --ff-only
git switch -c feat/token-refresh
```

### 2. Make, commit, and push changes

Create focused commits and push as work develops. The remote branch provides collaboration and recovery, not just final publication.

```bash
git add <paths>
git commit -m "feat(auth): add refresh token rotation"
git push -u origin feat/token-refresh
```

### 3. Open a pull request

Explain the problem and the change. A draft pull request can be opened early for feedback; link related issues and include a test plan.

### 4. Address review

Push follow-up commits to the same branch. The pull request updates automatically. Repository rules may require approvals, status checks, or conflict resolution.

### 5. Merge

Merge only when the repository's review and branch-protection requirements are satisfied. The merge strategy—merge commit, squash, or rebase—is project policy rather than one universal GitHub Flow rule.

### 6. Delete the branch

Delete a completed branch after verifying the merge and retaining any needed recovery path. The pull request and repository history remain available.

## Deployment is a separate policy

The current GitHub Docs page does **not** define deployment as a GitHub Flow step. A project may deploy previews before merge, deploy after merge, require staged promotion, or not deploy at all. Record that choice in CI/CD documentation rather than attributing it to GitHub Flow.

## Historical variants

| Source | Historical wording | How to use it now |
|---|---|---|
| `githubflow.github.io` | `master` is always deployable; merge reviewed work, then deploy immediately | design history; replace `master` with default branch and do not treat deployment order as universal |
| archived GitHub Guides (2019 snapshot, older guide design) | create branch, add commits, open PR, discuss/review, deploy, merge | historical example of deploy-before-merge validation |
| current GitHub Docs | create branch, make/push changes, create PR, review, merge, delete branch | present-day normative source for this site |

The historical pages disagree about deploy-before-merge versus merge-then-deploy. That is evidence that deployment belongs to project policy, not a timeless flow invariant.

## dev-cli mapping

```bash
dev start api --task "token refresh" --base main
# commit and test in the task checkout
dev done --pr
```

`dev done --pr` publishes the branch and opens the pull or merge request, then deliberately leaves the task active during review. It does not claim the request is merged and does not mark the task DONE.

`dev` also permits a project-policy exception for one small safe commit directly on the trunk. That is a lightweight trunk-based choice, **not** GitHub Flow. Teams that require pull requests should always use a branch.

## Pull-request readiness checklist

- The branch contains one coherent change stream.
- The description explains why, behavior changes, and operational impact.
- Tests and manual verification are stated.
- Shared generated files and migrations have one owner.
- Required checks and reviewers have completed.
- The selected merge strategy matches the desired trunk history.
- Deployment and rollback follow the project's own policy.

## Sources

- [GitHub Docs: GitHub Flow](https://docs.github.com/en/get-started/using-github/github-flow)
- [Historical GitHub Flow site](https://githubflow.github.io/)
- [Archived “Understanding the GitHub Flow”](https://web.archive.org/web/20191104103724/https://guides.github.com/introduction/flow/)
- [`internal/help/topics/branching.md`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/help/topics/branching.md)
