---
description: Manage project and global skills with scoped checks, reviewed updates, and native lock restoration.
authority: project
status: evolving
verified_on: 2026-09-10
tested_with: skills 1.5.23 and 1.5.25 source contracts; isolated provider fixtures
---

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


Provider source contracts: [update](https://github.com/vercel-labs/skills/blob/v1.5.25/src/update.ts), [restore](https://github.com/vercel-labs/skills/blob/v1.5.25/src/install.ts), [dependency sync](https://github.com/vercel-labs/skills/blob/v1.5.25/src/sync.ts).

## Bundled dev-cli skill lifecycle

The bundled `dev-cli` skill follows the installed binary separately from skills
managed by the external `skills` provider:

```bash
dev skill install                 # install or explicitly replace bundled files
dev skill install --check         # local content comparison; nonzero on drift/absence
dev skill install --if-installed  # refresh only an existing installation
dev skill uninstall --dry-run     # preview exact owned files and matching links
dev skill uninstall              # confirm and remove those files and links
```

`dev doctor` and `dev upgrade --check` report whether the default installation
matches the running binary, without changing it. After a successful `dev upgrade`,
the **new executable** refreshes an already installed
`~/.agents/skills/dev-cli`. Homebrew and Scoop use their stable installation paths;
other `dev` copies on PATH are not selected. An absent skill stays absent. A
normal upgrade also repairs an existing skill when the binary is already current.
Skill refresh failure is reported separately after the binary update succeeds.

Installation records content hashes in `.dev-cli-install.json`. Automatic refresh
refuses recorded local edits, restores missing files, and removes unchanged
obsolete files recorded by earlier installs. Explicit `skill install` replaces
bundled files; unrelated files remain. It preserves existing foreign agent links
and directories. Legacy installs without a manifest are recognized by their
`dev-cli` frontmatter; their first refresh replaces known bundled files and records
ownership. Preserve personal edits before that first migration.

Uninstall previews and revalidates the recorded files plus agent symlinks still
pointing to the exact installation. Modified managed files block removal;
unrelated files and foreign links remain. It never recursively deletes the
skills directory or changes native skills locks. `--yes` confirms the displayed
removal for automation. Legacy installs need one explicit `skill install` before
uninstall can verify ownership.

Custom `--dir` installations require `skill install --dir PATH` to refresh and
`skill uninstall --dir PATH` to remove. Direct package-manager upgrades and
upgrades initiated by binaries older than v0.2.23 do not run the new refresh hook;
run `dev skill install` once with the updated binary in those cases.
