---
description: Manual post-writer dogfood and the separate impact assessment for already-published repository history.
authority: project
status: evolving
verified_on: 2026-09-11
---

# Manual hygiene dogfood

This checklist keeps live agent writers separate from the final cleanup. A new
agent without SpecStory does not stop an older SpecStory writer. Do not terminate
unrelated sessions to make a scan or redaction pass.

## Before changing real artifacts

1. Build the feature binary and run the native hygiene tests and
   `python scripts/test-hygiene-hooks.py --dev <absolute-binary>` in its isolated
   temporary HOME. That checks real pre-commit blocking with a synthetic key,
   hidden paths, same-line placeholders, an alternate index, executable modes,
   unchanged Git config and a clean follow-up commit.
2. Run `dev hygiene status`, preview setup, and verify the effective hook chain.
   Put the new binary on PATH for the test session; never overwrite a
   package-managed installed binary manually.
3. Preview `rules import --from ssh` and `--from local`. Review which candidates
   are private and which are public/common names before selecting IDs. Keep the
   resulting private policy outside Git.
4. Run working-file and history scans. Preserve scope, exclusions and gaps.
   Classify candidates before adding narrow, reasoned exceptions. A finding
   count is not a count of confirmed live credentials.

## Final manual trigger

After the exact provider/wrapper exits and finishes the transcript, use an
external terminal. A separately authorized agent may use a terminal without a
SpecStory writer, but it still needs fresh proof about the original writer.

```bash
dev hygiene scan --scope worktree --json
dev hygiene redact --report <fresh-report-id> --file <reviewed-file> --json
dev hygiene redact --apply --plan <plan-id> --yes --writer-stopped
dev hygiene scan --scope worktree --json
git diff --check
# Review and stage exact files; normal hooks remain enabled.
```

Only select reviewed findings/files. Known live agents, changed source bytes,
metadata or plan signatures remain blockers. Keep partial results and recovery
receipts for manual inspection; restore only after reviewing the affected paths.
Never set writer-stopped merely because bytes happened to be stable briefly.

Run focused tests for any changed source/config files, then skill/doc checks.
Commit the verified HEAD corrections forward. A transcript still being written
stays explicitly pending rather than being repeatedly rewritten.

## Initial audit evidence

The planning audit found installed pre-commit/gitleaks and an executable global
hook, but no repository pre-commit/gitleaks configs. Thus ordinary commits skipped
those gates; manual artifact finalization was a separate path.

The local reachable-history gitleaks audit produced 416 candidates, including
384 broad password-rule matches and 21 findings in test files. These remain
candidate classifications, not a declaration of 416 live leaks. A lexical local
SSH comparison found six configured IP values at 163 occurrences in current
files. Common account/host tokens also produced many non-private matches.

The history contained about 717 MB of unique blob data, including a roughly
70 MB transcript. An initial three-minute run timed out. The completed audit
covered the selected gitleaks patch scope; the new implementation additionally
tests merge-only content and reports unsupported/excluded data explicitly.

Detailed audit files stay in private local storage. Do not add raw reports,
private dictionaries, recovery images or credential excerpts to Git or CI logs.

## Implementation dogfood result

The new setup applied the three repository configuration files while retaining
this machine's existing global hook. The feature's real commit gate completed
with 70 staged files, zero blocking findings and 36 generic privacy warnings.
Synthetic hook tests also proved rejection without changing caller Git config,
index entries or executable modes.

A new-CLI full reachable-history audit on this host hit its explicit 40-minute
limit and saved a `partial` receipt; it did not establish a clean history. Earlier
CI range validation also identified existing transcripts containing invalid UTF-8
or embedded NUL bytes. Those are explicit coverage gaps, not ignored successes.
Commit-range scans now restrict themselves to changed versions; the full audit
retains the historical gaps. The final manual phase must classify the retained
candidates and resolve or explicitly scope unsupported artifacts before claiming
that real history has been covered.

## Separate published-history assessment

Before considering any history change, produce a private table mapping each
confirmed item to its kind, current-HEAD presence, introducing/containing commit,
reachable local branches/tags and known remote copies. Fetch/query remote facts
explicitly; local refs alone cannot prove a complete list of public copies.

| Option | Effect and remaining exposure |
| --- | --- |
| Forward HEAD correction and prevention | Preserves commit/tag identities; older objects, PR views, clones and caches may still contain the data. |
| Credential revocation/rotation | Invalidates an exposed credential at its provider; does not erase its old bytes. Document completion without publishing the value. |
| Coordinated history cleanup assessment | Would change commit identities and signatures, affect PRs/clones and require a separate release/distribution migration plan. No rewrite is authorized by ordinary redact/setup. |
| Provider-assisted removal | Availability depends on the provider and type of sensitive data; cannot be claimed complete from a force push. |

For dev-cli, immutable release tags are CLI version authority, and releases are
also consumed through the Go module ecosystem, Homebrew/Scoop and downloaded
archives. Existing published tags must not be moved or reused. Inventory affected
releases and downstream artifacts before proposing a migration; a new version
cannot remove previously downloaded copies.

Git author names/emails and identifying filenames are assessment items; text
redaction does not rewrite commit metadata or rename files. GitHub describes
rotation-first handling and the limits of force pushes, forks, PR references and
caches in its [sensitive-data removal guidance](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository).
