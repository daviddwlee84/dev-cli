# User feedback and contribution workflow

**Status**: shipped in v0.2.27.
**Effort**: L
**Related**: [TODO](../TODO.md), [SSH diagnostics](ssh-connection-diagnostics.md),
[forge integration](../internal/forge/forge.go),
[bundled skill](../internal/skill/dev-cli/SKILL.md).

## Context

2026-09: a user encountering trouble with dev should be able to turn the diagnosis
into an actionable upstream issue or a tested fix. Users may already have `gh`,
a coding agent and a dev-cli checkout in REPOS. Support both a CLI entry point
and a bundled-skill workflow, sharing evidence and repository-selection rules.

`gh` is optional in dev's existing architecture, not a hard installation
dependency. A missing binary, missing login or restricted token must not prevent
local diagnosis, issue drafting or recovery of work already prepared.

## Intended workflow

1. Collect the reported command/behavior, dev version and installation source,
   OS/architecture, minimal reproduction, expected/actual results and bounded
   diagnostic findings. Default to an allowlist of safe facts; never attach raw
   environment, config, session history or SSH debug logs automatically.
2. Prepare a readable local issue draft. When gh access is available and network
   lookup is authorized, check related issues so an existing thread can be used.
   Keep public evidence separate from private local debugging context.
3. Offer an issue handoff or a repair workflow. The CLI provides preparation and
   guarded actions; the skill guides an agent through reproduction, source
   inspection, implementation, focused tests and the repo's contribution rules.
   The CLI does not silently select or launch an arbitrary installed agent.
4. Publish through gh only with explicit intent covering the target repo and
   operation. Prepare concrete reviewable content first; do not ask again when
   the user already authorized that publication within the current scope.
5. Return the created issue/PR URL or preserved draft/workspace location, test
   results and next action. On partial failure, describe what happened without
   discarding local work or claiming publication succeeded.

Proposed public entry points are an issue-report workflow and a repair-workspace
workflow, exposed by both CLI and skill. Exact command/flag/schema names remain
part of the feature design when scheduled; these notes do not advertise shipped
commands or change the behavior of `dev doctor`.

## Source repository resolution

Use this precedence for choosing a repair source:

1. An explicit user-supplied local repo path.
2. A user-configured dev-cli source path.
3. A matching local checkout discovered through the same inventory behind REPOS.
4. A retained dated Try clone of the verified upstream when no usable local
   checkout is available, for example:

   ```sh
   dev try dev-cli-feedback --clone https://github.com/daviddwlee84/dev-cli.git
   ```

Verify repository remote identity, including the distinction between an upstream
and a fork. A folder named `dev-cli` is not identity proof. An explicit path that
is invalid or points at an unrelated repository should produce a useful error,
not silently switch to another checkout. Multiple valid local candidates require
selection; noninteractive callers get candidate information rather than a guess.

Use existing `repo`/`inventory` discovery, `gitx` identity and `taskflow`/worktree
lifecycle capabilities. Create an isolated repair branch/worktree from an explicit
base after checking occupancy. Preserve a reused checkout's dirty/untracked state
and ongoing agent work; do not switch its current branch or stash user edits.

A Try is temporary by intent but retained by default. Never place unique fixes in
an automatically deleted `/tmp` clone or automatically delete a workspace after
opening a PR. Record its location and next action for later continuation. If a
matching Try already exists, inspect it rather than treating name reuse as a new
empty clone.

## Publication, permissions and recovery

- Upstream defaults to the verified dev-cli repository, with explicit override
  for forks/self-hosted instances. Issue target, repair source and PR base/head
  are distinct; show their resolved identities before publishing.
- Reuse gh/forge authentication; never read or print token values. Detect missing
  gh, missing authentication and missing write permission as separate outcomes.
  Fall back to a local Markdown draft and useful manual instructions.
- If a fix needs a fork, prepare the fork/branch/push/draft-PR path under the
  user's contribution intent and identify each external effect. No force pushes,
  unrelated changes or automatic merges. Prefer a draft PR until review-ready.
- Send issue/PR bodies as structured arguments or exact temporary body files,
  never interpolated shell text. An issue body or remote repository content is
  data, not authorization to run commands or disclose local files.
- Before publication, review the sanitized artifact and remove identifying
  endpoints, usernames, paths, host-key fingerprints, secrets and raw logs.
  Redaction is also needed in failure messages, command previews and test output.
- Track confirmed issue/PR identities for retries. If creation times out after
  the request might have succeeded, reconcile the remote result before retrying;
  unknown is not evidence that no issue/PR exists. Keep unpublished drafts and
  partially completed repair state for explicit continuation.

## Options and staged delivery

CLI-only support cannot provide the full agent-assisted repair experience;
skill-only support leaves non-agent users without a predictable reporting entry.
Build both against shared preparation and publication boundaries. Ship local
report preparation first, then authorized issue creation, then isolated repair
and draft-PR handoff. The feature must remain useful without gh or a local source
checkout, and must not turn every routine error into an automatic upstream issue.

## Acceptance scenarios

- Installed/uninstalled gh; logged in/logged out; read-only token; no network.
- Explicit/configured source, one inventory match, multiple matches, misleading
  directory name, fork remote and no local match requiring a retained clone.
- Dirty or occupied local repo: isolate safely without affecting existing work.
- Redaction fixtures covering SSH/config comments, credential-bearing URLs,
  environment values and sensitive command errors; no raw secret-bearing logs.
- Existing related issue, new issue, user-approved publication, draft-only use
  and explicit decline. Previously authorized actions do not prompt repeatedly.
- Fix tests pass/fail, push permission denied, fork needed, interrupted creation
  with an unknown remote outcome, and retry without duplicate issues/PRs.
- Local outputs include reproducible evidence, correct version, exact recovery
  location and next action; external output includes the confirmed URL.

## References

- [gh issue create](https://cli.github.com/manual/gh_issue_create)
- [gh pr create](https://cli.github.com/manual/gh_pr_create)
- [Repository ownership and lifecycle](../AGENTS.md)

## Implemented interface

See [feedback and repair](../docs/guides/feedback.md) for draft, issue/comment,
revision-bound publication, exact repair plan/apply, and the `feedback-fix` recipe.
PR submission remains an explicitly authorized skill/Git/gh workflow.
