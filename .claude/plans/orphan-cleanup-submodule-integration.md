# Orphan cleanup and submodule integration

Approved on 2026-09-10. This delivery plan supersedes the earlier feature's
no-merge/no-publish boundary: the user explicitly requested integration and
release after reviewing the existing worktrees.

- Preserve all nine Claude Workflow worktrees in private, restore-verified
  archives and independent Git bundles. Commit their dirty content to local
  backup branches, retain original branch tips, and remove only unoccupied,
  clean checkouts without force. Do not integrate these unrelated snapshots.
- Preserve the submodule feature checkout, its two committed changes, task
  record, and dirty release-document edits before any cleanup. The dirty edits
  proposed v0.2.18 and belong only in the backup, not the new release.
- Merge the feature into an isolated checkout based on current main. Preserve
  ancestry, existing canonical dirty files and ongoing transcript output.
- Keep current version-2 retirement handoffs, workflow handoff callbacks,
  exact caller/process consent and preview authority. Add recursive disposal
  and child graph/intent/artifact authority to the same revalidation flow.
- Preserve both conflicting historical artifact versions as separate files;
  retain feature plans and run staged secret scanning before committing.
- Regenerate command/skill and llms output. Maintain English/zh-TW pairs.
  Restore the already-tagged v0.2.21 changelog section before releasing the
  submodule feature as v0.2.22 (or the next unused patch version).
- Verify targeted stale-child handoff and foreground-authority regressions,
  full race tests, vet, E2E, platform crossbuilds, skill checks and strict docs.
- Repair the v0.2.21 release gate: empty extended-attribute maps must retain
  their meaning through recovery JSON, so unchanged SSH/fleet files can be
  restored without weakening identity checks. Cover absent, empty-value and
  nonempty attributes. Isolate the shared lifecycle test fixture's StateDir.
- Push main before the immutable release tag. Verify binaries, checksums and
  Homebrew publication, then mark the original feature task merged and retire
  its checkout from outside. Retain original and backup branches.
