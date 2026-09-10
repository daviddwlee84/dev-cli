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
worktree. `--all` scans canonical configured repositories. The TUI defaults to
its startup context: inside Git that is only the exact checkout, while outside
Git it is the accepted REPOS snapshot plus the ordinary startup directory.
Uppercase `A` switches both SKILLS and MCP to all accepted repositories for the
current TUI run. Global paths are scanned once.

In the SKILLS view, `e` opens the row's primary installed `SKILL.md`, or the lock file
for a missing row. The `y` copy menu offers its path (`p`), sanitized summary
(`s`), sanitized source URL (`u`), or the whole raw local file (`f`). Raw copy is
bounded to 1 MiB and does not contact a source, but the system clipboard receives
the file verbatim.

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

## Local transfers

`dev skill transfer plan <name> --from-agent universal --to-agent claude-code --mode mirror`
previews per-skill relative links from `.agents/skills`. Use `--mode copy` for an
independent tree, or `--mode move` for a reviewed source retirement. `--from-repo`
and `--to-repo` select exact checkouts. Existing different trees and foreign links
are conflicts. Skills containing known credential files or private keys are rejected.

`dev skill transfer apply --plan <id>` applies only the reviewed observations.
`transfer status` lists local ledgers; `transfer undo <id>` creates a reverse plan
that preserves intervening edits. `transfer refresh <id>` previews mirror refresh.
Private recovery payloads live under dev's state directory, never in plan JSON.
A symlink does not itself prove that an agent has loaded the skill.

For a cross-repository upstream install, use `transfer prepare <name>` with the
same source/destination selectors, then `transfer plan <name> --mode install
--prepared <id>`. Prepare is an explicit network action: only a trusted
`skills@1.5.23` executable runs, inside private staging. The source's native lock,
installed contents, staged contents, generated provenance, and provider version
must agree before publication. A changed upstream is an error, not a silent
upgrade. Unknown lock schemas, raw commit refs unsupported by the provider, and
unverifiable hash ordering require an explicit alternative such as copy.


# Skills management

`dev skill manage` opens an independent wizard. REPOS and SKILLS offer the same
workflow through Ctrl+O; it does not add another dashboard tab.

```bash
dev skill manage
dev skill manage --repo api
dev skill manage --all
dev skill list --all --check --json
```

Choose project, global, project plus global, or multiple repositories. The
repository picker includes checkouts with `skills-lock.json`, even when installed
skill folders are missing or ignored by Git. Global skills are processed once.
Use Space to select, Ctrl+A to select/clear visible items, and type to filter.

## Check and update

Checking explicitly contacts Git sources, groups identical source/ref pairs,
and compares upstream content with the lock. It does not run `skills`, npm, Node
or project code. Dated results are cached in `skill-checks-v1.json`; changing the
lock invalidates them. `update_checked_at` in skill-list JSON identifies the last
comparison. A cached result is evidence from that time, not a live remote query.

Update checks first, then preselects confirmed updates for review. Missing
installations, unsupported locks, failed checks and locally modified skill
contents are reported separately. The preview names each checkout, scope,
selected skill and command. Confirmation is required before execution.

Management mutations require a globally installed `skills` executable compatible
with the tested 1.5.23–1.5.25 contracts (supported 1.x versions from 1.5.23).
`dev doctor` reports the dependency; install or upgrade it with
`npm install -g skills`. dev does not install dependencies automatically or fall
back to npx. Listing and checking continue to work when the dependency is absent.

Commands run serially under dev's existing provider mutation lease. Lock,
installation and provider changes since preview reject the affected operation.
Results distinguish completed, failed, skipped, stale, canceled and unverified
items. A zero provider exit code alone is insufficient: dev rereads installations
and the resulting lock. Private JSON receipts live under
`<state_dir>/skills/runs/`. Raw Git and external installers remain outside dev's
coordination; native updates are not frozen artifact-transfer plans.

## Single-project native operations

The advanced choices appear for one project without global scope:

| Choice | Native command | Behavior |
|---|---|---|
| Restore from lock | `skills experimental_install` | Re-resolves recorded sources/ref into `.agents/skills`; may update the lock. Useful when `.agents` is gitignored or absent. It does not guarantee the old content hash. |
| Sync dependency skills | `skills experimental_sync --yes --agent …` | Reads skills from existing `node_modules` and installs for explicitly selected project agents. It does not install npm dependencies. |

The wizard checks native command support, project destination changes and local
content conflicts. Dependency skills with colliding names require individual
attention. Both operations retain native output and verify installed results;
neither is included in cross-repository batches in this version.

`dev skill install` and `dev skill sync` still manage dev's bundled skill. The
existing single-skill `dev skill update <skill> --project|--global` command remains
available for automation. Explicit transfer preparation keeps its separate pinned
provider and verified-payload contract.
