# Agent skills

An inventory of the skills the coding agents on this machine can already use,
at two scopes: skills committed to the current project, and skills installed
globally for your user.

## Reading the inventory

```bash
dev skill list                # both scopes
dev skill list --project      # only this repository
dev skill list --global       # only this user
```

Each row reports the scope, the skill name, which agents recognise it, where it
came from, and whether an update is available. The SKILLS view in the dashboard
shows the same data; `r` reloads it and `c` checks for updates.

## Where the data comes from

`dev` does not scan for skills itself. It asks the external `skills` provider —
found on `PATH`, or run through `npx` — and merges the answer with the lock
files that record what was installed from upstream. If the provider is not
installed, `dev skill list` reports that as an actionable error rather than
silently returning an empty inventory, and never downloads the provider as a
side effect of a read.

`dev doctor` reports whether the provider is available.

## Updating

```bash
dev skill add                 # install one interactively
dev skill update <name> -g    # refresh a provider-managed skill (scope required)
```

Only skills the provider manages can be updated this way. A skill that was
written by hand or vendored into the repository shows as externally managed and
is left alone.

## dev's own skill is different

The `dev-cli` skill is compiled into the binary. It updates when `dev` updates,
not through the skill provider, and `make install` links it into the agent
directories. Handing it to `dev skill update` would be a category error, so the
inventory marks it as directly managed.
