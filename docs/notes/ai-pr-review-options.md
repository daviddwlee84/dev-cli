---
description: Compare Claude Code Review, the Claude Code GitHub Action, Codex code review, and CodeRabbit by trigger phrase, workflow requirement, prerequisites, and cost.
authority: anthropic-docs-and-project-policy
status: evolving
verified_on: 2026-09-01
tested_with: Claude Code Review research preview, claude-code-action v1, Codex GitHub integration, CodeRabbit docs as of 2026-09-01
---

# AI pull-request review options

Four products review pull requests with a model. They differ in one structural
way that decides most of the choice: whether you maintain a workflow file.

!!! info "Freshness"
    **Authority:** vendor documentation plus a labeled local recommendation ·
    **Status:** evolving — these products change quickly ·
    **Verified:** 2026-09-01. Re-check trigger phrases and plan tiers before
    relying on them; every row below was read from the vendor's own docs on
    that date.

## Decision table

| | Workflow file? | Trigger | Prerequisites | Cost model |
|---|---|---|---|---|
| **Claude Code Review** | No | `@claude review` as a top-level PR comment, or automatic | Team or Enterprise plan; research preview; unavailable with zero data retention | Token usage, billed separately from plan usage; roughly $15–25 per review |
| **Claude Code GitHub Action** | **Yes** | `@claude` in a comment (configurable), or a `prompt` input for unattended runs | Repository admin; `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` | GitHub Actions minutes plus API tokens, or your subscription via an OAuth token |
| **Codex code review** | No | `@codex review` in a PR comment, or the *Automatic reviews* setting | GitHub connected in Codex | Codex usage |
| **CodeRabbit** | No | Automatic on PRs against the primary branch; `@coderabbitai review` or `@coderabbitai full review` | GitHub App installed from the CodeRabbit dashboard | Per plan; an OSS plan exists |

## The distinction that matters

**A managed GitHub App** — Claude Code Review, Codex code review, CodeRabbit —
receives a webhook and reviews. There is nothing in your repository to maintain,
and nothing to keep up to date when the vendor changes.

**A GitHub Action** — `anthropics/claude-code-action@v1` — runs inside your CI.
You maintain the workflow file, you pay for the runner minutes, and in exchange
the agent can do things a reviewer cannot: run your tests, push a fix, open a
follow-up PR. It is not a reviewer that also edits; it is an agent that happens
to be reviewing.

Pick by what you want to happen *after* the finding. If the answer is "a human
reads it", use a managed app. If the answer is "fix it and push", you want the
Action.

## Getting the trigger right

The most common failure is a phrase that looks right and does nothing.

- Claude Code Review needs the comment to **begin with `@claude review`**, as a
  top-level comment rather than an inline one. The PR must be open and not a
  draft, and the commenter needs owner, member, or collaborator access.
  `@claude please review this` is not the documented trigger.
- The Claude Code GitHub Action responds to `@claude` as a **complete word** —
  not `/claude`, not `@claude-bot` — and only from a user with write access.
  The default phrase is configurable with `trigger_phrase`.
- The two are separate products that share a GitHub App. Installing the app
  does not enable Code Review, and enabling Code Review does not give you the
  Action. A `👀` reaction with no review usually means the app is installed but
  the product you expected is not enabled.
- CodeRabbit distinguishes `@coderabbitai review` (only what changed since its
  last review) from `@coderabbitai full review` (everything, ignoring its own
  prior comments).

## Where `dev` fits

`dev` does not install an App, write a workflow, or review anything. It tells
you which requests are waiting and renders the comment that triggers whichever
reviewer you chose:

```bash
dev pr list                    # what is open, and which worktree it belongs to
dev pr list --actions          # the gh/glab commands, including `pr comment`
```

Posting the trigger is then an ordinary comment:

```bash
gh pr comment 12 --repo owner/name --body '@claude review'
gh pr comment 12 --repo owner/name --body '@codex review'
gh pr comment 12 --repo owner/name --body '@coderabbitai full review'
```

`dev` prints those and never runs them. Requesting a paid review is a decision,
and at $15–25 per Claude Code Review it is not one to make on someone's behalf.

## What this note does not claim

- Review *quality* is not compared here. It depends on the codebase, the diff,
  and the repository guidance file more than on the vendor.
- Prices and plan tiers move. The figures above are the vendor's own, read on
  the verification date, not a quote.
- A widely repeated claim that CodeRabbit requires manual triggering on public
  repositories below a star threshold could not be confirmed in CodeRabbit's
  documentation and is deliberately omitted.

## Sources

- [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions) — setup, `@claude` trigger, `trigger_phrase`, secrets, who can trigger runs, cost model.
- [Set up Code Review for Claude Code](https://support.claude.com/en/articles/14233555-set-up-code-review-for-claude-code) — Team/Enterprise research preview, `@claude review` preconditions, frequency settings, token billing.
- [Review GitHub pull requests with Codex](https://developers.openai.com/codex/integrations/github) — `@codex review`, the Automatic reviews setting.
- [CodeRabbit commands](https://docs.coderabbit.ai/guides/commands) — `review` versus `full review`.
- [CodeRabbit FAQ](https://docs.coderabbit.ai/faq) — GitHub App install, automatic review on the primary branch, no workflow required.

## Related pages

- [Pull request inbox](../guides/pull-request-inbox.md)
- [GitHub Flow](../git/github-flow.md)
- [Sources and freshness](../reference/sources-freshness.md)
