# Remote fleet

See repository, task and runtime state from several machines without sharing
their filesystem paths or copying their dev configuration.

## Configure hosts

```bash
dev fleet config init
dev fleet config edit
dev fleet status
```

`$XDG_CONFIG_HOME/dev/remotes.toml` follows the same connection model as an
ordinary OpenSSH invocation. Prefer `ssh_alias`; explicit
`hostname`/`user`/`port`/`identity_file` fields are available when needed.
Password fallback supports `prompt`, `plain` and `bitwarden`, but key or agent
authentication is always attempted first.

## Inspect and open

```bash
dev fleet list
dev fleet list --host lab --repo api
dev fleet list --cached
dev fleet list --json
dev fleet open lab api
```

Every host runs its own `dev`, so its `config.toml`, scan roots, exact repo
paths, task registry and runtime remain authoritative. A missing remote `dev`
is `no-dev`, not a fleet-wide failure. Unreachable hosts reuse the last
successful private XDG snapshot when available.

The dashboard FLEET view hides this machine by default because REPOS already
shows its richer local state. Press `a` to include local rows. This is a TUI
display preference only: `dev fleet list` keeps returning local and configured
remote hosts.

Opening a remote repo prefers native Herdr remoting when an active remote Herdr
server is visible. It otherwise opens `ssh -t`, validates the repository path
through remote dev, changes directory and starts a login shell.

## Safely propagate a branch

```bash
dev fleet sync api --push
dev fleet sync api                 # HEAD must already equal fetched upstream
dev fleet sync api --host lab
```

The source must be clean and attached. Targets are matched by normalized Git
remote identity, not directory name. Each target fetches first; only a clean
checkout of the same branch that is strictly behind is advanced with
`merge --ff-only`. Different branches are not switched. Dirty, ahead,
divergent, ambiguous and unreachable targets remain untouched and make sync
return non-zero. Hosts without dev or without that repository are ignored and
reported explicitly.

There is no automatic rebase, force push, background hook or all-repository
pull. Resolve divergent work interactively on the machine that owns it.
