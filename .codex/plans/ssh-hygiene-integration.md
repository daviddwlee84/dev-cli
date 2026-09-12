# SSH integration and repository hygiene rollout

Approved implementation sequence (2026-09-12):

1. Merge current main into `feat/tailscale-ssh`, preserving both branches' work.
   Reconcile SSH/credential and Fleet/navigation behavior, validate all platforms,
   merge PR #22, release the next patch, mark merged and retire externally.
   Retain the branch and unrelated worktrees/sessions. The four target panes were
   observed as shells after the user stopped the foreground translator.
2. In a separate follow-up branch, refine password/fixture false positives,
   introduce reviewed `hygiene rules import --from machines` using bounded local
   Tailscale/LAN/Fleet caches only, and retain private rule values outside Git.
3. Add `dev hygiene manage` and REPOS batch setup. Reuse source-bound setup plans,
   preserve custom rules and hooks, migrate only recognized equivalent scanner
   hooks, deduplicate shared Git hook ownership, and report per-repository effects.
4. Extend SKILLS / `dev skill manage` with selected removal using exact native
   provider names/agent scopes or dev-owned install manifests. Revalidate locks,
   installed bytes and links; retain shared consumers and block unresolved owners,
   local edits, or active finalizer/hook dependencies.

The hygiene installation is a normal developer commit procedure, not a new agent
skill: Git -> pre-commit -> dev hygiene scan -> gitleaks and dev privacy policy.
Setup is not invoked recursively by the hook. Keep the agent-history-hygiene
finalizer/provenance/recovery path during this staged migration; dev's existing
artifact finalizer still calls its scripts. Do not delete its source repository.

Format-complete synthetic keys remain blocking until a reviewed exact
rule/path/value exception records a reason. Do not exempt tests, docs, entire
artifact roots, or an entire line beside a placeholder. Retain unquoted config
password detection while excluding program identifiers/expressions.

Validation includes SSH/Fleet/TUI and privacy integration, native Windows
credential/protocol tests, real hook checks with alternate/partial indexes,
malformed scanner failures, no-network cached imports, selected skill/shared-link
removal, stale plans, cancellation and partial batch receipts. Keep generated
skill help, CHANGELOG, and English/Traditional Chinese documentation synchronized.

Dogfood applies only to dev-cli first; other repositories receive previews.
Ongoing canonical SpecStory files remain untouched until their exact writer exits.
Published-history rewriting and wholesale legacy-finalizer replacement are not
part of this rollout. All work is performed by the current agent.

## Implementation checkpoint

PR #22 was merged and released as v0.2.30. The SSH task's remote-tracking base
was normalized to the identical local main under repository/task revision locks
before retirement; w12 and its linked checkout were retired, branch retained.
Canonical active transcripts were not edited.

The follow-up implements offline cache candidates, precise password literals,
reviewed batch setup and selected skill removal. Tests use synthetic credentials
and isolated provider executables. Windows required gates are distinct from
the broad advisory suite, whose success conclusion can hide failures.

Remaining migration boundary: agent-history-hygiene finalization/provenance
still owns its scripts; removing that skill remains blocked. Historical raw
transcript cleanup stays a manual post-writer task. First real setup dogfood is
dev-cli only; other repositories receive previews.
