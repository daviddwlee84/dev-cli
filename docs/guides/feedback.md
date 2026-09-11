---
description: Prepare private local feedback, publish reviewed GitHub issues, and hand a verified isolated repair checkout to an explicitly selected agent.
authority: project
status: evolving
verified_on: 2026-09-11
---

# Feedback and isolated repair

When dev fails, preserve enough evidence to explain the problem before deciding
whether to report it or repair it. `dev feedback` provides a small terminal form;
its explicit subcommands are intended for agents and scripts. Nothing publishes
an issue, creates a repair checkout or starts an agent merely because a command
failed.

## Keep the original task moving

An agent should distinguish a usage or environment failure from a suspected dev
bug. For SSH transport trouble, use [connection diagnosis](ssh-hosts.md#ssh-diagnosis)
when authorized. A timeout, missing gh login or safety refusal is not by itself an
upstream defect.

For a panic, unexpected internal error or behavior/documentation mismatch, first
prepare a minimal local draft and identify a possible source checkout. Offer
reporting, repair, both or skipping the detour. Continue a safe alternative for
the original task when available. Deep investigation, repair and an additional
subagent require explicit consent covering the added scope and cost. An approved
subagent uses an independent context/checkout and returns a short result. Existing
scoped consent persists; there is no need to ask repeatedly for the same action.

## Prepare and review a local draft

```bash
dev feedback
dev feedback draft --title 'SSH menu panic' --body-file report.md --json
dev feedback draft --title 'SSH transport diagnosis' --body-file report.md \
  --diagnostic diagnosis.json --json
dev feedback issue <id> --json
```

`--body-file -` reads a bounded Markdown body from stdin. Include the minimal
reproduction and expected/actual behavior. The CLI adds only version, OS/arch and
installation-method facts. It does not collect doctor output, environment,
configuration or session history automatically. A supplied SSH diagnostic is
projected to finite public stage codes; its endpoint/user/key paths never enter
the attachment.

Reports live under configured `paths.state_dir/feedback/<id>/` (by default the
XDG dev data directory). `public.md` is the editable public draft; `context.json`
retains the explicitly supplied private evidence. Report and operation records
are durable state, not cache. POSIX permissions are owner-only; Windows uses
verified protected current-user-and-SYSTEM ACLs. No automatic cleanup occurs.

Known credential, URL, endpoint, path and SSH-option forms are sanitized again
at preview/publication. Review free text for arbitrary identifying names: automatic
redaction cannot recognize every private name. The preview prints the exact
operation, repository, body and content/target revision. Edits change that revision.
A broken dev config still permits drafting under default storage, with a warning.

## Explicit GitHub publication

```bash
dev feedback issue <id> --search --json
dev feedback issue <id> --publish
# For an already reviewed and authorized exact revision:
dev feedback issue <id> --publish --yes --revision <revision> --json
# Preview a comment on an existing thread:
dev feedback issue <id> --existing 123 --json
```

The default issue target is `github.com/daviddwlee84/dev-cli`. `--repo
[host/]owner/name` explicitly changes it; cwd and GH_HOST do not choose the target.
`--search` is a separate network action and only lists candidates. `--existing`
selects a positive issue number for a comment; it never silently chooses a thread.
Review that preview's revision before using `--publish --yes`.

Publication uses optional gh authentication and an argv-only API request with
exact JSON stdin. Bodies never enter shell source or error argv. Missing gh,
missing login, rejected permissions and unknown remote outcomes remain separate
results. Drafts remain usable offline.

An operation receipt is saved as unknown before a POST. If the response is lost,
a repeat invocation searches for its exact report/revision marker before doing
anything else. An incomplete or empty reconciliation remains unknown and never
authorizes another POST. Confirmed issue creation returns the existing URL on
retry; a changed draft can be submitted as an explicitly reviewed comment.

## Plan a repair checkout

```bash
dev feedback repair <id> --base main --json
dev feedback repair <id> --repo ~/Projects/dev-cli --base main --json
dev feedback repair <id> --apply --plan <plan-id> --yes --json
```

Source precedence is explicit `--repo`, `[feedback].source_repo`, then the same
local discovery used by REPOS. A configured source must expand to an absolute
path. Invalid explicit/configured paths are errors, multiple matches are returned
for selection, and a directory name is never identity proof. A local fork needs
an exact dev-cli upstream remote; a fork origin alone is insufficient.

```toml
[feedback]
source_repo = "~/Projects/dev-cli"
```

Without a source, the preview suggests explicitly acquiring a retained Try clone
with the existing `dev try --clone` workflow, then repeating the preview with that
checkout. Inspect a reused Try's identity and existing work. Never use an
automatically deleted temporary clone for unique fixes.

A base ref is required explicitly. The saved plan pins its commit, canonical
repository identities, target branch/path, relevant configuration and report
revision. Apply re-observes under the Git lifecycle lock and refuses stale plans.
It uses the shared start service to create a HOT worktree task. Source edits are
kept in place; runtime opening, provisioning, submodule downloads and agent launch
are disabled for this preparation. A partial failure retains its checkout and
reports the task/store recovery problem instead of deleting work.

## Continue with an agent

```bash
dev prompt render feedback-fix <id>
# Only after approving this additional agent/profile:
dev prompt open feedback-fix <id> --agent <profile>
```

The current agent can read the rendered prompt and work in the returned checkout.
A new process requires an explicit profile even when a default profile exists.
The prompt binds to the report's prepared checkout/task and rejects a changed,
locked, missing or partial binding. It is private context and is never an issue
body.

Read the source AGENTS.md, reproduce the problem, make a focused change, run the
relevant tests and synchronize changelog/help/docs/skill. Deliver the diff summary,
test results, retained workspace and next action for the original task. Fork,
push and draft-PR publication require their own applicable consent; use existing
Git/gh tools with exact body files. This workflow does not auto-merge, replace the
installed binary or retire its workspace.
