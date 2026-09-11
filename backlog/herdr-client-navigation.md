# Herdr navigation scoped to the calling client

Status: research — P? · M

## Current behavior

Fleet uses Space to expand/collapse and Enter to navigate. Inside Herdr, dev
ensures an explicitly chosen saved profile, prepares a remote repository
workspace without focus, and reports the native sidebar target. Outside Herdr,
it prepares without focus and attaches with an explicit session. Both routes
leave the final workspace selection to the user.

This is intentional for Herdr 0.9.0. Native machine add/enable connects open
clients but does not select their machine. `herdr --remote` launches another
client and supports an explicit session, but no initial workspace/path selector.
Public `workspace focus` changes session-wide focus and can move other clients.
`HERDR_ENV=1` identifies the environment, not the caller's client identity.
The client socket is an attach socket, not a public existing-client control API.

## Evidence to revisit

- [Remote argument parser](https://github.com/herdrdev/herdr/blob/v0.9.0/src/remote/args.rs):
  remote launch arguments and session parsing.
- [Launch validation](https://github.com/herdrdev/herdr/blob/v0.9.0/src/main.rs):
  remote attach cannot be combined with other launch commands.
- [Server client views](https://github.com/herdrdev/herdr/blob/v0.9.0/src/server/headless/client_views.rs):
  session-wide workspace focus.
- [Native endpoint navigation](https://github.com/herdrdev/herdr/blob/v0.9.0/src/client/shell/endpoint_navigation.rs):
  client-local navigation exists internally, but is not exposed as a public CLI.

## Required upstream contract

1. Stable caller-client identity, scoped to the current client connection and
   authenticated to that connection. Do not infer it from selected profiles,
   workspace IDs, process names or the session's attach socket path.
2. An explicit machine/profile, server session and workspace activation API that
   moves only that client. IDs such as `w1` are meaningful only within their
   server/session; responses must bind all of those identities.
3. Capability discovery and errors that distinguish unsupported, disconnected,
   stale identity and rejected activation, without opening a replacement client.
4. An outside attach option for an exact initial workspace, with no change to
   existing clients' focus.

## Integration constraints

Keep native profile Plan/Apply approvals, endpoint/profile revalidation, exact
repository identity and explicit session isolation. Activation is a final stage
after registration/enable and workspace preparation; a failure retains those
completed effects and returns an honest partial result. Never fall back to SSH
automatically after a chosen Herdr operation, write private catalog/socket
protocols, inject sidebar keystrokes, clear HERDR_ENV or enable nested clients.

Before implementing, inspect the installed public schema and upstream release
source. Test with two clients on one session plus two machines whose workspace
IDs overlap; activating one must leave the other client's view unchanged. A
client disconnect or profile removal between preparation and activation must
fail without selecting a substitute. Preserve the current sidebar workflow for
versions lacking the new capability.
