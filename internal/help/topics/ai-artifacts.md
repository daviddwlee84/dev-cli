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

The default `product-first` handoff requires committed product changes and an
empty index; it does not stage the still-changing transcript:

```bash
dev git hygiene status
dev git hygiene scan --scope staged --json
dev agent artifact prepare --session codex:<uuid>
# After the exact recorder has stopped, from outside its checkout:
dev agent artifact finalize --intent <id> --writer-stopped
```

Preparation does not stop the writer. Never repeatedly redact a file that a
recorder is still rewriting from its native source. `dev git hygiene redact` requires
an exact reviewed plan; artifact writes additionally require post-writer proof.
Private recovery and a stable byte snapshot do not prove that a process exited.

The shared artifact writer guard covers hygiene redact/repair-encoding/restore/
manage and artifact finalize/archive/migrate. Other recognized live agents
covering the checkout always block, including idle/done agents. The calling
agent is exempt when Herdr's exact session ID (`agent_session` kind `id`, not a
title) differs from every target artifact's valid UUID in its actual
SpecStory-generated anchored preamble. Only `.specstory/history/*.md` supplies
this proof, not filenames or UUIDs later in the text. Plans or unprovable transcripts require global
`--allow-shared-checkout`: an explicit assertion of disjoint ownership after the
writer exits, never an automatic retry flag. Unknown caller identity needs
that explicit attestation. An identified caller-owned live transcript is refused
even with the override. Post-writer proof and source revalidation remain
required; restore checks the receipt's exact paths.

If history is ignored, ordinary code commits need not scan it. An already tracked
file remains tracked after adding `.gitignore`: removing it from the index is a
separate reviewed operation. Keep or archive ignored history before deleting its
worktree; ignored does not mean disposable or backed up.

## Exact selection and co-commit closeout (v0.2.40)

`dev agent artifact prepare --specstory-path PATH` optionally selects one exact Markdown file
when several exports share a session. The persisted selection must match the
provider and UUID in its strict SpecStory preamble and stay within the configured
capture scope: `.specstory/history/` for source commits, or the archive policy's
capture root. Traversal, symlinks and mismatched identities are refused. Without
this selector, ambiguous discovery still blocks. Finalization rechecks the
selection and live writer guard; `--writer-stopped` never overrides a live writer.
Native intent updates use locked revision compare-and-update (CAS), including
archive handoffs. `artifact finalize --revision HASH` can require an exact reviewed
native revision instead of acting on a changed record.

Co-commit is an explicit alternative to product-first, not its new default. It
places reviewed staged product changes and the exact final transcript/optional
plan in one normal commit. It currently supports `claude:UUID` and in-checkout
SpecStory capture, not the external-archive lane. The canonical
`agent-history-hygiene` v2 source is maintained separately in `agent-skills`;
installing dev does not bundle, install or upgrade the helper. Co-commit requires
an installed helper with compatible v2 capabilities. Missing/unsupported tools,
helper capabilities or changed helper/tool fingerprints block rather than trigger
an automatic install. Native Windows co-commit is unsupported pending a verified
native backend. Existing v1 journals are not silently upgraded.

1. Explicitly launch the real canonical v2 wrapper **without `--allow-commit`**
   (manual outer finalization). Dev neither launches nor closes the agent.
   Invented environment variables, session IDs or receipts cannot replace the
   real wrapper lifecycle.
2. Stage only reviewed product files. Leave the transcript and selected plan out
   of the index, and prepare a base message file without managed provenance
   trailers. Select exactly one `--plan` or explicitly `--no-plan`.
3. From that wrapped agent, queue the exact handoff:

   ```bash
   dev agent artifact prepare --closeout co-commit --session 'claude:<uuid>' \
     --specstory-path .specstory/history/session.md \
     --plan .claude/plans/task.md --message-file /path/to/commit-message.txt --json
   ```

   Use `--closeout-helper /path/to/agent-history-hygiene/scripts` only to select
   an installed canonical helper explicitly. A new untracked transcript over
   2 MiB needs reviewed `--allow-large` consent.
4. Preserve the command output in the real recorder/session. JSON has schema 1,
   kind `co_commit_handoff`, and the canonical v2 `queue_ack` object; human output
   retains that ACK as a standalone compact JSON line. This is actual queue
   evidence, not text for the agent to reconstruct. Report **"finalization
   queued"**, then exit normally with **no further repository or index operations**.
   `queued_but_not_bound` also requires exit: retain the request ID and repair only
   that exact binding externally with the same selection and `--run-id`.
   Binding repair creates neither a new queue request nor a new ACK. Unknown
   queue outcomes must be inspected, never blindly requeued.
5. After the real wrapper finishes, an external coordinator reviews the handoff
   and explicitly authorizes finalization:

   ```bash
   dev agent artifact list --json
   dev agent artifact finalize --intent '<id>' --allow-commit --json
   ```

   Optionally require `--revision HASH`. Dev first delegates canonical
   prepare-only work, then rechecks its own live writer/policy/source guards and
   native CAS revision before one commit-capable helper call bound to the exact
   helper journal revision. A commit with uncertain outcome is reconcile-only,
   never a reason to retry the commit. `committed_but_not_recorded` needs receipt
   reconciliation, not another commit. Proof covers the exact parent, prepared
   tree, full normalized message and unique request identity, not trailers alone.

The v2 helper defaults to no-cloud across run, sync and export. Finalization
requires successful child exit, process-group quiescence, exact native export
digest and request/session linkage; idle state, mtime or stable bytes alone are
not proof. All eligible staged products are scanned read-only; sanitation is
limited to the selected artifacts. Before changes, durable private beforeimages
and receipts bind every finding occurrence plus source/index/tool/policy identity.

For a blocked sanitation review, inspect without applying:

```bash
dev agent artifact finalize --intent '<id>' --preview-review --json
```

The preview cannot combine with `--allow-commit`, `--review-file` or
`--rotation-confirmed`. Raw findings and recovery remain private, outside Git.
After explicit per-finding review, the external finalizer may use
`--allow-commit --review-file /absolute/path/to/review.json`, optionally with the
preview's `--revision`. `reviewed_noncredential` is distinct from actual credential
rotation: fixture review releases only the exact already-sanitized run's rotation
gate. It never restores secret bytes, creates blanket exceptions or bypasses Git
hooks. Use `--rotation-confirmed` only after actual required credential rotation;
unknown or incomplete findings stay blocked. This workflow does not authorize
recovery of any existing real run automatically.

`artifact list --json` uses schema 1, kind `artifact_handoffs`. Each row keeps the
persisted native Intent fields separate from `co_commit_observation` or
`observation_error`. List, status and lifecycle readiness are read-only: a helper
observation never silently finalizes or reconciles the native record. Incomplete
or failed proof remains a blocker even when the runtime says done.

## Choose and apply repository policy

No policy means existing behavior is unchanged. `track` retains the existing
source-commit finalizer, `archive` preserves selected evidence in a separate Git
checkout, and `unmanaged` leaves retention to the user. Sources are `specstory`
(recorder Markdown) or `files` (explicit source-relative exports/plans).

```bash
# Create the ordinary archive repository separately; configure its Git identity.
git init -b main /path/to/history
# Explicit private raw preservation; choose check or redact when wanted.
dev agent artifact setup --mode archive --source specstory --archive /path/to/history \
  --protection off --json
dev agent artifact setup --apply --plan <id> --yes
dev agent artifact status
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
dev agent artifact archive --session codex:<uuid> --json
# Inspect the private review directory, then after the recorder exits:
dev agent artifact archive --apply --plan <id> --yes --writer-stopped
dev agent artifact find --session codex:<uuid>
dev agent artifact find --commit <full-source-commit-id>
dev agent artifact find 'literal search text' --all
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
reports contain safe finding IDs for the existing `dev git hygiene rules allow`
workflow. Exact fixture exceptions need review and a reason. Snapshot scans
support inline gitleaks rules and useDefault, not external rule-file includes.

Unchanged bytes reuse signed local scan evidence only when the path, policy,
exceptions, scanner inputs and executables match. Changed bytes or rules rescan.
Partial scans never pass. Review original/copy payloads privately; raw recovery,
reports and receipts stay under `paths.state_dir`, outside Git and cache clearing.
Filenames and metadata can still identify people or systems: review before sharing.

After committing product/policy changes, `dev agent artifact prepare` also supports an archive
policy. Off/check finalization creates the exact archive copy after writer exit.
For redact, preview and review `artifact archive` first, then pass its ID to
`artifact finalize --archive-plan <id> --writer-stopped`. Reviewed plans should
be committed with product changes; prepared SpecStory archive handoffs select one
exact transcript. Old intents keep their original destination and requirements.
The tracked-history finalizer still uses the compatibility skill's scripts.

Retirement checks signed archive receipts and current ignored capture bytes.
New or changed evidence blocks cleanup until preserved. Local archive durability
is distinct from remote sync; ordinary status and readiness never query a remote.

For an intent not matched to the checkout path, readiness checks canonical Git
common-directory identity before requiring a branch to resolve a moved intent.
Pending or finalized records proven to belong to another repository therefore do
not block a detached checkout; detached HEAD is normal for pinned submodules.
Relevant pending intents, ambiguous repository or moved-intent identity, and an
unreadable intent store still block. Exact finalized-receipt requirements are
unchanged. Failed child artifact observations report their underlying cause,
never a false clean result.

Empty gitlink children have separate path-based task/artifact claim checks,
without inheriting the parent's Git identity. Any matching non-discarded artifact
intent, even finalized, conservatively blocks recursive empty-child removal;
discarded records still bind the reviewed plan's authority.

## Explicit Git synchronization and original backups

```bash
dev agent artifact sync --push --json       # preview contacts the configured remote
dev agent artifact sync --apply --plan <id> --yes
dev agent artifact sync --pull --json       # separate pull preview; fast-forward only

dev agent artifact migrate --mode untrack --path .specstory/history --json
dev agent artifact migrate --apply --plan <id> --yes --writer-stopped

dev agent artifact migrate --mode split --path .specstory/history --json
dev agent artifact migrate --apply --plan <id> --yes
# Optional: publish the original named refs to a separate empty destination.
dev agent artifact backup <completed-migration-id> --remote <git-url> --json
dev agent artifact backup --apply --plan <backup-plan-id> --yes
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

For dev-cli v0.2.40 release source archives, `.gitattributes` excludes the root
`.specstory` directory and only these agent-plan roots: `.claude/plans`,
`.codex/plans`, `.cursor/plans` and `.opencode/plans`. This is a publication
boundary, not Git untracking or history rewriting; other agent configuration and
skills are not excluded by these rules.

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

### Go module distribution boundary

From v0.2.41, release source archives and Go module ZIPs both omit the existing
SpecStory and agent-plan evidence directories. Git archives use `export-ignore`;
Go uses nested `go.mod` boundary markers in those evidence-only roots. Preserve
all embedded help, skills, rules and generated build inputs. Verify both real
payloads with `python3 scripts/check-distribution.py --version v0.3.2` after
committing the packaging changes. This does not untrack evidence or shrink Git
clones, and older immutable tags retain their original package contents.
