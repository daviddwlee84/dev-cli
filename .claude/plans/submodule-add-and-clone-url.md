# Submodule addition and clone URL copy

Approved design, 2026-09-07. Implementation baseline: `72bb6e5` on
`feat/submodule-workspaces`. The original canonical checkout's autostash
conflict is unrelated and must remain untouched.

## Decisions

- First version: CLI addition plus TUI URL copy; no TUI addition wizard.
- Canonical command: `dev submodule add [source] [path]`; equivalent entry:
  `dev repo add-as-submodule [source] [path]`.
- Wizard offers pinned and remote-default-branch checkouts. Scripted default
  is pinned; `--ref` is pinned-only. Never assume the default is named main.
- Parent is the nearest exact checkout, or explicit `--parent`; destination
  paths are relative to its root, not cwd or the canonical checkout.
- Source selection combines known local remotes and existing forge cache.
  Choose an exact URL when names are ambiguous. Local-only repositories and
  adoption of existing destinations are out of scope.
- `--dry-run` is offline/read-only. Non-interactive mutation requires a source
  and `--yes`; `--json` never prompts and reports partial phases.
- Only `.gitmodules` and the new gitlink are staged. No commit, push, runtime,
  implicit task membership or remote-following initialization policy.

## Implementation

- A sealed addition Plan/Apply service in `internal/submodule` owns policy.
  CLI renders choices/results; `gitx` owns Git commands and portable source
  validation. Do not loosen ordinary repository-clone nesting protections.
- Clone into a private absorbed layout under the selected checkout's modules
  store, without changing the superproject's shared submodule config.
- Revalidate exact checkout, Git directories, branch/HEAD, index, metadata,
  destination parents, and occupancy under the hierarchy lifecycle lock.
  Recheck the new child's filesystem/Git/HEAD identities before publication.
- Preserve unrelated staged/dirty work. Reject modified or hidden-index
  `.gitmodules`, filters/encodings, conflicts, active operations, unsafe paths,
  existing destinations/stores, and direct self-references.
- Initialize only the newly added subtree, according to parent policy or
  explicit `--submodules`. Existing siblings are not initialized implicitly.
- Preserve partial clones and metadata; report exact phase/paths and manual
  next steps. Never reset a user's index or force-delete failed clones.
  Retirement recovery journals are not addition recovery journals.
- REPOS/REMOTE `y u` and row menus copy clone URLs. Local rows prefer origin
  and ask among remaining fetch URLs. Remote rows use CloneURL then SSHURL,
  never browser URL. Late/canceled lookup generations and stale selections
  cannot trigger copying. Existing copy bindings remain compatible.

## Validation and delivery

- Real Git fixtures cover canonical, linked and nested parents, non-main
  default branches, pinned refs, recursive initialization, empty remotes,
  retained partial results, stale authority and preservation of existing work.
- CLI fixtures cover alias, JSON, confirmation, cancellation, known-source
  deduplication and parent-root policy. TUI tests cover URL fallback, multiple
  remotes, errors, cancellation and stale lookup generations.
- Run focused/full race tests, vet, existing E2E, Linux/Windows crossbuild,
  generated command/skill checks and paired strict documentation builds.
- Sync README, help, changelog, English/zh-TW guides and bundled references.
  The generic skill-author lint has a pre-existing line-count failure:
  baseline SKILL.md was 782 lines, above its 700-line gate. Do not broaden this
  feature into an unrelated whole-skill restructuring.
- Commit independently after validation and staged secret scanning; no merge,
  push, tag, release, installation or original-main conflict resolution.

## Verification record

- Full `go test -race -timeout 20m ./...` passed (CLI: 565.181s). The first
  run reached Go's default ten-minute package timeout, not an assertion failure.
- Subsequent addition regressions passed with race detection, including CRLF
  metadata, provider-normalized source identity, hidden index flags, retained
  partial data and late/canceled URL lookup generations.
- Existing E2E, vet, build, generated skill check, paired strict MkDocs/source/
  site checks and Linux/Windows crossbuild passed. Crossbuild is not native
  Windows runtime validation. Staged secret scanning passed.
- The only outstanding generic lint result is the pre-existing main-skill
  length gate documented above; no unrelated skill restructuring was done.
