# Evaluate unified REPOS and FLEET browsing

Status: research — P? · M

## Context

FLEET's host tree makes SSH, Herdr saved-machine controls and remote repository
inventory available in one place. Showing all local repositories there also
duplicates REPOS, which already has richer Git/worktree/task/notes actions.
The current decision is to hide the local FLEET host by default. The `a` key and
action menu can reveal it at the end of the host list for the current session.

## Question to answer

Would a shared host/repository browser make navigation simpler, or would it
obscure the distinction between a local working checkout and a remote observation?
Evaluate shared projections or a host selector before committing to a single
dashboard view. Retaining separate views is an acceptable outcome.

## Constraints and tradeoffs

- Keep the fast local startup and reuse the accepted REPOS snapshot. A unified
  browser must not rescan local repositories or wait for remote SSH.
- Preserve local Git/worktree/task/notes actions and exact checkout identities;
  a cached remote row is not permission to execute a local action.
- Keep each host's loading, error, cache age and authentication independent.
  Search must state which hosts are covered and avoid implicit extra connections.
- Herdr registration/enabled state belongs to the executing host's client
  catalog. It is distinct from repository state and live remote sessions.
- Keep repository actions, host connection actions and saved-machine management
  recognizable without duplicating every menu in both views.
- Preserve keyboard navigation, selection on refresh, and explicit local/remote
  path semantics. CLI fleet JSON and existing host-local state remain compatible.

## Before implementation

Compare three representative workflows: local-only work, opening a known remote
repository, and searching for a repository across several partially available
hosts. Prototype the smallest shared projection or host switcher, then assess
whether it reduces navigation while retaining clear action ownership. Do not
couple this research to the shipped Herdr enable/disable/remove controls.
