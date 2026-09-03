# Agent skills

A provider-independent inventory of agent skills in the current checkout, any
selected repository, every configured repository, and global agent paths.

## Reading the inventory

```bash
dev skill list                         # current checkout + global
dev skill list --repo api --project    # one repository/checkout
dev skill list --all                   # canonical repositories + global once
dev skill list --all --check --json    # explicit upstream freshness check
```

Project/global copies of one skill remain separate rows. Repository identity,
logical installations, compatible agent paths, local presence/integrity, source,
and update freshness are reported independently.

## Where the data comes from

Reads are native. `dev` scans a versioned snapshot of all 77 paths in the
`skills@1.5.23` registry plus project/global lock files. It never runs `skills`,
Node, npm, `npx`, agent detectors, or project code to list inventory. Shared
paths such as `.agents/skills` report registry compatibility; they do not claim
the corresponding agent executables are installed.

Project scope defaults to the exact current checkout, including a linked
worktree. `--all` scans canonical configured repositories. The TUI reuses its
accepted REPOS snapshot and additionally includes the startup checkout when it
is a distinct linked worktree. Global paths are scanned once.

A missing lock is normal. Malformed, unreadable, oversized, unsupported, or
ambiguous lock data is diagnosed without hiding valid neighboring rows. Absolute
`$XDG_STATE_HOME/skills/.skill-lock.json` is the global lock when configured;
otherwise `~/.agents/.skill-lock.json` is used.

## Freshness and integrity

Listing is local-only. `--check` (or TUI `c`) is the explicit network operation:
it groups equal Git source/ref pairs and compares Git tree/blob object bytes with
the lock-recorded hash without checking out remote content or modifying installed
files/locks. Checkout filters and `core.autocrlf` therefore cannot execute or alter
the comparison. Provider folder hashes containing non-ASCII paths are reported
unverifiable because upstream `localeCompare` ordering is locale-dependent.

`current` therefore means upstream still matches the lock record. It does not
prove installed bytes are intact. Presence is tracked separately, and only the
embedded `dev-cli` skill can verify that every bundled file matches; additional user files are not treated as drift.

## Updating

```bash
dev skill add
dev skill update <name> --project [--repo api]
dev skill update <name> --global
```

Only explicit add/install/update operations invoke the provider to change
skills. They require a directly installed `skills` executable and may access the
network; `dev doctor` may separately run that trusted executable's `--version`
probe. `dev` never invokes `npx` from a repository checkout, so a local
`node_modules/.bin/skills` cannot substitute for the provider; an ineligible PATH
shim is skipped when a later trusted executable exists. Unknown/source-less lock
entries are not mutation-eligible, and cooperating `dev` processes serialize the
entire provider run. Updates require one explicit scope and confirmation (or
`--yes`); no bulk update is implied by inventory.

`dev doctor` reports native inventory separately from the optional mutation
provider.

## dev's own skill is different

The `dev-cli` skill is compiled into the binary. It updates when `dev` updates,
not through the upstream provider, and `make install` links it into agent
directories. The inventory marks it as directly managed and can verify its local
content against the embedded files.
