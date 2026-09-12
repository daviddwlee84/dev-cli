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
