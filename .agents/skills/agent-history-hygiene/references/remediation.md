# Remediation: a secret leaked into a commit

Load this doc when `scan-staged.sh` fires on a real leak, or when the
user says "I already pushed" / "I committed my `.env`" / "gitleaks
caught something on main".

## Core principle

> **Rotate first. Scrub second — only if cheap. Never rely on
> rewriting history to keep a secret secret.**

History rewrites sound like the obvious fix but they are fragile:

- The commit sits in every clone a teammate has pulled.
- It sits in GitHub's Pull Request views, in the GitHub Events API, in
  cached web pages, in `git reflog` everywhere.
- An attacker who scraped the commit before you scrubbed it still has
  the secret. The only act that revokes it is **rotation at the
  provider**.

Force-pushing a rewrite over a shared branch also destroys teammates'
local history and can silently re-introduce the leak when someone
merges their old copy back in. Avoid it.

## The runbook

### 1. Rotate at the provider (always)

Open the provider's console for every leaked credential and revoke /
rotate it:

| Provider       | URL                                                                             |
|----------------|---------------------------------------------------------------------------------|
| Anthropic      | https://console.anthropic.com/settings/keys                                     |
| OpenAI         | https://platform.openai.com/api-keys                                            |
| GitHub PAT     | https://github.com/settings/tokens                                              |
| HuggingFace    | https://huggingface.co/settings/tokens                                          |
| Supabase       | https://supabase.com/dashboard/account/tokens                                   |
| Linear         | https://linear.app/settings/api                                                 |
| WakaTime       | https://wakatime.com/api-key                                                    |
| Cursor         | https://cursor.com/settings (billing/api section)                               |
| Notion         | https://www.notion.so/profile/integrations                                      |
| Tailscale      | https://login.tailscale.com/admin/settings/keys                                 |

If the secret is something you can't rotate (DB password, a
pre-shared-key baked into clients), treat this as an incident and loop
in whoever owns that system before doing anything git-side.

### 2. Assess blast radius

Answer three questions before touching history:

1. **Is the commit pushed?** `git branch -r --contains <sha>`
2. **Which branch(es)?** `git branch --contains <sha>`
3. **Is the branch shared?** Anyone else checked it out? CI cached it?

Use this decision tree:

```
           Pushed?
          /       \
        no         yes
         │          │
   Unshared/      Branch shared?
   unpushed ──► amend/soft-reset
                    / \
                   no   yes
                   │     │
              feature    main/release
              branch        │
                 │     ── STOP ──
                 │     rotate, then
                 │     redact HEAD
                 │     in a NEW commit
                 │     and push forward
                 │
           filter-repo +
           force-with-lease
           (notify team)
```

### 3. If unshared + unpushed: amend/soft-reset

```bash
# Simple case: only the tip of the branch has the leak
git reset --soft HEAD~1
# Redact the file:
bash scripts/redact_secrets.py --fix --working-dir
# Or edit the file by hand to remove the secret.
git add <the file>
git commit -c ORIG_HEAD   # reuse previous commit message

# Slightly less simple: the leak is an older commit, not HEAD~1
git rebase -i <parent-of-bad-commit>    # mark the commit `edit`
# once stopped on that commit:
bash scripts/redact_secrets.py --fix --working-dir
git add -A
git commit --amend --no-edit
git rebase --continue
```

### 4. If shared + feature branch: filter-repo + force-with-lease

Use `git filter-repo` (not `filter-branch`; it's deprecated and slow).
Install with `brew install git-filter-repo` or `pipx install git-filter-repo`.

```bash
# One option: scrub a specific secret from every commit that touched it
git filter-repo --replace-text <(echo "THE_LEAKED_VALUE==>REDACTED")

# Another option: remove a whole leaked file from history
git filter-repo --invert-paths --path path/to/leaked.env

# Push with lease (safer than --force; fails if someone else pushed since your fetch)
git push --force-with-lease origin <branch>

# Tell your team to re-clone or hard-reset — their old clone still has
# the leaked blobs and will re-introduce them on any merge.
```

### 5. If shared + main/release branch: DO NOT rewrite

Rewriting `main` is almost always worse than the leak. Instead:

1. Redact the secret in a **new commit** on HEAD. Use
   `redact_secrets.py --fix --working-dir` on the affected file, or
   edit by hand.
2. Push the new commit.
3. Ask GitHub Support (or your hosting provider's equivalent) to purge
   cached views of the leaked commit. GitHub's
   [removing sensitive data](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)
   doc covers the cache-purge request.
4. Enable [Push Protection](https://docs.github.com/en/code-security/secret-scanning/introduction/about-push-protection)
   on the repo so the next accidental commit is blocked at push time.
5. Rotation (step 1 of this runbook) is still the real defense. The
   old bytes exist on everyone's hard drive forever; only the revoked
   credential is actually safe.

### 6. Audit downstream copies

- Any CI system that cached the repo between leak and rotation.
- Container images built from the bad commit — rebuild + redeploy.
- Teammate clones — have them `git fetch --all && git reset --hard origin/<branch>`
  or re-clone; their reflog still holds the leaked blob for 90 days
  otherwise.
- Mirror repos (GitLab mirror, backup bucket).

## What **not** to do

- `git push --force` without `--lease` on a shared branch. One of three
  outcomes: (a) teammate's work is silently destroyed; (b) teammate
  merges their old history back, undoing your rewrite; (c) CI cache
  still has the leaked objects — so you destroyed a colleague's work
  without actually removing the secret.
- `git commit --amend` on a commit that's already pushed. Creates a new
  SHA; the old SHA is still reachable via push force, reflog, and
  GitHub's Events API.
- Deleting the repo and re-creating it. You lose issue history, PR
  history, stars, forks, CI tokens — and the old blobs still exist in
  every clone and in GitHub's object storage.

## Cross-reference

- [`pre-commit-redaction-stack.md`](./pre-commit-redaction-stack.md)
  — the three-layer defense that should catch leaks before they hit
  disk.
- [`../scripts/scan-staged.sh`](../scripts/scan-staged.sh) — the
  pre-commit wrapper whose exit code triggers this runbook.
- [GitHub docs: Removing sensitive data](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)
