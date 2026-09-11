# Feedback and repair during another task

Read this when dev panics, an internal error looks reproducible, or behavior and
help disagree. Ordinary usage errors, missing login and a safety refusal are not
by themselves dev bugs. For SSH failures first consult `dev help ssh`; diagnosis
is explicit network activity, not a passive inventory step.

## Preserve the original task and the user's token budget

1. Keep the failed command name, installed version, expected/actual behavior and
   smallest useful reproduction. Do not repeatedly rerun a failing operation or
   sweep environment, config, logs or conversation history.
2. Prepare a small local draft and an available source candidate, then offer
   report, repair, both or skipping the detour. Continue the original task through
   a safe alternative when possible; record its next action before a detour.
3. Deep investigation, dev-cli repair and an extra subagent need explicit consent
   covering their scope and added context/token cost. Do not launch a subagent
   merely because the provider supports one. If approved, use independent context
   and a separate repair checkout, with a bounded assignment and a short result.
   Existing scoped consent persists; do not ask repeatedly for an approved action.
4. Local repair consent does not grant issue/comment publication, fork/push/PR,
   binary replacement, merge or cleanup permission. Prepare concrete content and
   target identities before requesting any still-missing publication consent.

Use this compact proposal:

```text
Problem: observed behavior and minimal evidence
Original task: safe continuation or current blocker
Report: reviewed public draft location, exact target and operation
Repair: verified source, explicit base, planned isolated checkout
Extra work: proposed investigation/agent scope and cost, only if needed
```

## Local draft and reviewed GitHub operation

Check `dev feedback --help` for the installed binary's capability. An older or
broken binary must not block recovery: retain a manually prepared sanitized
Markdown draft and use explicitly authorized native gh steps if necessary.

```bash
dev feedback draft --title 'Short symptom' --body-file report.md --json
dev feedback issue <id> --json
# Only when related-issue network lookup is authorized:
dev feedback issue <id> --search --json
# Only after reviewing and authorizing this exact target/operation/revision:
dev feedback issue <id> --publish --yes --revision <revision> --json
```

The body should contain reproduction, expected/actual results and already-known
bounded evidence. `--diagnostic <file>` accepts local schema-v1 `ssh diagnose`
JSON and constructs a finite-code public attachment. Never attach raw doctor
stdout, SSH debug logs, complete config, key fingerprints, tokens or transcripts.
Redaction helps with known forms; inspect arbitrary names in free text manually.
Private `context.json` and rendered prompts are never publication bodies.

The default issue destination is `github.com/daviddwlee84/dev-cli`, independent
of cwd and GH_HOST. `--repo [host/]owner/name` changes it explicitly. Related
issues are candidates, not automatic comment targets. Use `--existing <number>`
to preview an exact comment, then approve that preview's revision. `--yes` is a
CLI expression of existing consent, not permission for the agent to invent it.

Missing gh, login, permission or network preserves the draft. An unknown POST
outcome must be reconciled by the exact marker before another attempt; do not
work around it by repeatedly creating new reports. Confirmed URLs and private
receipts are the recovery record. A report is durable state outside Git/cache.

## Verified local repair

```bash
dev feedback repair <id> --base main --json
dev feedback repair <id> --repo <source> --base <explicit-ref> --json
# After this exact plan is approved:
dev feedback repair <id> --apply --plan <plan-id> --yes --json
dev prompt render feedback-fix <id>
```

Source precedence is explicit path, configured `[feedback].source_repo`, then
REPOS discovery. A bad explicit/configured path is an error; multiple matches
need selection. An exact upstream remote is required, including for a fork.
Do not treat the folder name as identity or the current HEAD as a safe default
base. With no source, explicitly acquire a retained Try via the suggested
`dev try --clone` workflow, inspect any reused Try, and preview again.

The approved plan pins source filesystem identities, base ref/OID, relevant
configuration, new branch/task/path and report revision. Apply rechecks under the
repository lifecycle lock and calls the shared start service. It keeps source
edits in place and prepares a HOT worktree task without runtime, provisioning,
submodule acquisition or agent launch. Stale plans do nothing new; partial
failures retain the checkout and report the exact recovery location.

The current agent can consume the rendered prompt directly. Use `dev prompt open
feedback-fix <id> --agent <profile>` only for an explicitly approved additional
agent with that profile. It does not choose a default profile for this recipe.
A changed/locked/partial checkout or changed task binding blocks handoff.

Read the source AGENTS.md, reproduce, implement a focused fix, run meaningful
checks and synchronize the source's changelog/docs/help/skill. Deliver the diff,
test results, retained task/worktree/report and the original task's next action.
Load the private input only when needed; avoid spending context on whole logs.

## Optional contribution after the local fix

When separately authorized, use normal Git and gh to push the selected branch,
create a fork if necessary and open a draft PR with explicit repo/base/head and
an exact body file. `dev pr` currently lists requests; do not invent a `dev pr
create` command. Preserve confirmed URLs and reconcile an uncertain create before
retrying. Never auto-merge, force-push, overwrite the installed CLI or delete the
workspace after submission. Issue target, repair source and PR base/head are
separate identities and may refer to different repositories.
