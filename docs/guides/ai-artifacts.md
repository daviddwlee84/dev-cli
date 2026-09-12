---
description: Choose where agent history belongs, preserve useful decisions and keep transcripts out of source distributions.
authority: project
status: evolving
verified_on: 2026-09-12
---

# AI artifacts: what to keep, archive, and ship

Keep useful decisions, choose where transcripts live, and ship product inputs.

## Decide by purpose

| Material | Purpose | Usual treatment |
|---|---|---|
| Code, tests, current specs and docs | Current behavior and contracts | Maintain with the project |
| Reviewed plans, decisions and pitfalls | Requirements, rationale and lessons | Keep concise, with status and context |
| Raw transcripts, draft plans and tool output | Evidence of the development process | Choose per repository: track, archive elsewhere, or keep local |
| Native sessions and tool databases | Resume state, indexes or curation records | Follow each tool's backup contract |

AI authorship alone does not determine where a file belongs. Promote useful
conclusions into the project's existing docs, decisions, backlog or pitfalls;
old transcripts are historical evidence, not current instructions. Do not ask
future contributors to read every exploratory conversation to understand code.

## Four independent choices

1. Does this material belong in the source repository's Git history?
2. Where is the durable copy, and how will another machine obtain it?
3. Should that copy be checked, redacted, or explicitly stored without scanning?
4. Does the material belong in a source distribution or installed product?

A private experiment can keep raw chats in Git. A public or long-lived project
can keep reviewed decisions in Git and transcripts in a separate private Git
repository. A private destination does not itself change hygiene policy: choose
it explicitly. An unscanned copy is preserved evidence, not a clean scan.

## SpecStory: capture is separate from publication

SpecStory is the current transcript integration. The originating agent is the
provider, for example `codex:<uuid>` or `claude:<uuid>`; SpecStory is the recorder.
Its Markdown export is not the native agent session and does not guarantee resume.

SpecStory can write per-worktree `.specstory/history/` files or use an explicit
`--output-dir`. Preserve project and session identity when combining histories.
A shared flat directory does not by itself retain project attribution. Tools
such as Lore have their own metadata and curation state; do not assume all of
their databases can be discarded and reconstructed from Markdown.

For the existing tracked-history workflow:

```bash
dev hygiene status
dev hygiene scan --scope staged --json
dev prepare --session codex:<uuid>
# After the exact recorder has stopped, from outside its checkout:
dev artifact finalize --intent <id> --writer-stopped
```

Preparation does not stop the writer. Never repeatedly redact a file that a
recorder is still rewriting from its native source. `dev hygiene redact` requires
an exact reviewed plan; artifact writes additionally require post-writer proof.
Private recovery and a stable byte snapshot do not prove that a process exited.

If history is ignored, ordinary code commits need not scan it. An already tracked
file remains tracked after adding `.gitignore`: removing it from the index is a
separate reviewed operation. Keep or archive ignored history before deleting its
worktree; ignored does not mean disposable or backed up.

## Choose and apply repository policy

No policy means existing behavior is unchanged. `track` retains the existing
source-commit finalizer, `archive` preserves selected evidence in a separate Git
checkout, and `unmanaged` leaves retention to the user. Sources are `specstory`
(recorder Markdown) or `files` (explicit source-relative exports/plans).

```bash
# Create the ordinary archive repository separately; configure its Git identity.
git init -b main /path/to/history
# Explicit private raw preservation; choose check or redact when wanted.
dev artifact setup --mode archive --source specstory --archive /path/to/history \
  --protection off --json
dev artifact setup --apply --plan <id> --yes
dev artifact status
# The same planner through repository setup:
dev repo setup --artifacts --mode unmanaged --json
```

Setup edits only reviewed policy, local binding, ignore and export files; it
never untracks, commits, pushes or installs tools. `repo setup --artifacts` is
separate from a scaffold plan. Apply there with `--artifacts --apply --plan <id>
--yes`. Review and commit shared policy changes yourself.

The shareable `.dev-cli/artifacts.toml` contains the stable project ID, retention
mode, source and literal paths. The archive path and protection binding stay in
private host state. Bind a cloned project to the desired archive on each host;
do not infer project identity from a folder basename or current branch.

`--capture project` keeps normal per-worktree recorder output. `--capture external`
sets a checkout-specific local SpecStory output directory outside Git. It requires
untracked native config and enabled local Markdown export; tracked/complex config
requires manual review. Run setup in each new external-capture checkout. Existing
cloud settings and native session stores remain under SpecStory's control.

## Preserve copies, then find them

```bash
dev artifact archive --session codex:<uuid> --json
# Inspect the private review directory, then after the recorder exits:
dev artifact archive --apply --plan <id> --yes --writer-stopped
dev artifact find --session codex:<uuid>
dev artifact find --commit <full-source-commit-id>
dev artifact find 'literal search text' --all
```

For `--source files`, select exact configured paths with repeated `--file`.
Inventory is bounded to 10,000 files and each operation to 256 selected files;
current snapshots are limited to 128 MiB per file. Oversized or unsafe inputs
fail explicitly. Search reads committed records on the archive's current branch;
`--all` includes its other projects. Results contain locations and line numbers,
not snippets; no network, native history crawl or agent resume occurs.

Archive commits contain ordinary snapshot files and metadata under `projects/`.
The metadata binds project/session, original and stored content digests and the
source commit. Source files and source index bytes stay untouched. The project
archive gets text/ident exclusions so CRLF and literal bytes survive Git; custom
content filters such as encryption, LFS or encoding transforms require the
archive's native workflow and are never silently bypassed. Existing Git hooks
still run. Archive access controls and off-host backups remain the operator's
responsibility.

`off` saves raw input without a scanner. `check` uses enabled source-repository
hygiene policy. `redact` proposes replacements on the copy and checks the result
again. Disabled categories remain disabled; skipped is not clean. Blocking
reports contain safe finding IDs for the existing `dev hygiene rules allow`
workflow. Exact fixture exceptions need review and a reason. Snapshot scans
support inline gitleaks rules and useDefault, not external rule-file includes.

Unchanged bytes reuse signed local scan evidence only when the path, policy,
exceptions, scanner inputs and executables match. Changed bytes or rules rescan.
Partial scans never pass. Review original/copy payloads privately; raw recovery,
reports and receipts stay under `paths.state_dir`, outside Git and cache clearing.
Filenames and metadata can still identify people or systems: review before sharing.

After committing product/policy changes, `dev prepare` also supports an archive
policy. Off/check finalization creates the exact archive copy after writer exit.
For redact, preview and review `artifact archive` first, then pass its ID to
`artifact finalize --archive-plan <id> --writer-stopped`. Reviewed plans should
be committed with product changes; prepared SpecStory archive handoffs select one
exact transcript. Old intents keep their original destination and requirements.
The tracked-history finalizer still uses the compatibility skill's scripts.

Retirement checks signed archive receipts and current ignored capture bytes.
New or changed evidence blocks cleanup until preserved. Local archive durability
is distinct from remote sync; ordinary status and readiness never query a remote.

## Explicit Git synchronization and original backups

```bash
dev artifact sync --push --json       # preview contacts the configured remote
dev artifact sync --apply --plan <id> --yes
dev artifact sync --pull --json       # separate pull preview; fast-forward only

dev artifact migrate --mode untrack --path .specstory/history --json
dev artifact migrate --apply --plan <id> --yes --writer-stopped

dev artifact migrate --mode split --path .specstory/history --json
dev artifact migrate --apply --plan <id> --yes
# Optional: publish the original named refs to a separate empty destination.
dev artifact backup <completed-migration-id> --remote <git-url> --json
dev artifact backup --apply --plan <backup-plan-id> --yes
```

Untrack saves original refs and selected working files, edits ignore rules and
removes only selected index entries. Other staged/unstaged work remains intact;
existing prepared handoffs must be resolved first. Untracking does not itself
configure future archive capture.

Split requires git-filter-repo and complete local history. It produces verified
`original.bundle`, `original.git`, `history.git`, `filtered.git`, both commit maps,
selected working snapshots in `current/`, and a migration manifest. Every mapped
tree is checked against the selected-path projection. Empty commits/merge topology
and message bytes are retained; rewritten signatures cannot remain valid. A
restore clone verifies the original bundle. Neither source refs nor remotes change.
Raw backups are not a hygiene audit. Other worktrees' uncommitted files, reflogs,
unreachable objects, LFS payloads and submodule repositories are outside coverage.
Detached tips need a recovery ref before migration.

Backup publication sends frozen named refs with empty-ref leases. Selected
uncommitted working snapshots stay in local `current/`; they are not in the Git
remote backup. Archive sync pins the reviewed destination and branch: push proves
ancestry before an exact lease, pull only fast-forwards. Resolve divergence with
native Git; dev does not force-overwrite or synthesize merges.

Private operation ledgers retain partial effects, output and recovery paths.
After interruption inspect those paths and the remote, then prepare a new plan;
never blindly replay a partial operation. Original-remote cutover, release/tag
migration and force-with-lease publication of filtered history are separate,
operator-coordinated actions.

## Distribution, untracking and history migration

| Action | Effect | Earlier commits |
|---|---|---|
| `.gitattributes` `export-ignore` | Exclude selected paths from `git archive` source packages | Unchanged |
| Ignore and untrack | Stop including new versions in source commits; retain working files | Unchanged |
| Filter a separate repository copy | Build replacement history without selected paths | Different commit IDs in that copy |

For example, this excludes chats from source archives while leaving them in Git:

```gitattributes
/.specstory export-ignore
/.specstory/** export-ignore
```

Verify the exported source still builds, including embedded resources. Packaging
systems have separate inclusion rules; an archive exclusion is not a universal
package filter. New rules affect new tagged snapshots, not old immutable tags.

When an experiment becomes a maintained project, first inventory and verify a
recoverable original. Build and validate filtered history in a separate copy,
retain commit mappings, and separately coordinate remote replacement, tags,
releases and other clones/worktrees. A bundle covers selected Git objects/refs,
not automatically dirty files, LFS objects or submodule repositories.

Sync, version control and backup are separate. A synchronized deletion is still
a deletion. Restoring a Markdown export is not restoring the native agent state.
Confirmed leaked credentials require revocation or rotation; filtering Git does
not revoke them or remove copies held by other people and services.

See `dev help hygiene` for scanning and reviewed redaction, `dev help retirement`
for post-writer handoffs, `dev help repositories` for setup, and `dev help storage`
for durable data versus caches.
