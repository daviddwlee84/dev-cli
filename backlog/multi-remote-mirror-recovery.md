# Multi-remote topology and mirror recovery targets

Status: research — P? · M

## Context

`dev repo list` already exposes remote topology, and repository/fleet workflows
can fetch or synchronize explicitly selected work. This is not a policy for
keeping several remotes equivalent, maintaining a mirror, or selecting a remote
from which every local object and file can be recovered.

The v0.3 command-family and Try-demotion work leaves search/open/browse/remote
behavior unchanged. Demotion retains the current local directory; it is not
remote restore or disk reclamation. The existing P1 verified-backup/local-
eviction item remains the prerequisite for describing clone removal as safe.

## Questions to resolve

- Which configured URLs are fetch sources, publication destinations, backups,
  or mirrors? Preserve Git's distinct fetch/push URLs and branch upstreams.
- Should a mirror preserve all refs, a reviewed subset, or only ordinary branch
  work? Distinguish Git mirroring from provider-side replication and avoid
  silently pruning or overwriting destination refs.
- How should users choose an explicit recovery target when refs exist across
  several remotes? A union of advertised names is not proof of independent,
  complete recovery.
- Which local-only data needs separate preservation: heads, tags, notes, stash,
  reflog-only objects, dirty/untracked/ignored files, LFS, submodules and nested
  repositories? What can be verified freshly without claiming remote durability
  beyond the evidence returned?

## Boundaries

- Passive local inventory and completion remain local. Network refresh is
  explicit, and cached observations never authorize synchronization or deletion.
- Remote changes require a preview identifying exact source/destination URLs,
  refs, direction and effects; destructive mirror/prune writes need their own
  explicit approval, not a generic ordinary-sync flag.
- Retain task, runtime, worktree, artifact and filesystem guards. A clean
  checkout or a synchronized current branch alone cannot authorize eviction.
- Keep backup receipts and recovery tests separate from user-visible remote
  names. Ordinary removal must not invent a trustworthy default such as origin.

## Before implementation

Inventory representative upstream/fork, two-publication-remote, backup-remote and
bare-mirror layouts. Compare read-only role reporting with an explicit reviewed
sync workflow before deciding on new commands. Reuse the P1 recovery proof design
for any later eviction; no mirror writer or local deletion is authorized by this
research item.
