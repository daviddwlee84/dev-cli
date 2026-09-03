---
description: Record dev-cli dependencies, upstream preview status, documentation constraints, and behavior that is intentionally incomplete.
authority: project-and-upstream
status: evolving
verified_on: 2026-09-03
tested_with: Claude Code 2.1.250
---

# Compatibility and known limitations

This page separates graceful degradation from real limitations. Reverify it whenever command/runtime code or version-sensitive Claude Code documentation changes.

## dev-cli capability matrix

| Capability | Dependency | Without it |
|---|---|---|
| core repository/task/worktree operations | Git | unavailable; Git is the only hard runtime dependency |
| rich grouped runtime and agent activity | Herdr server + CLI | `auto` tries tmux, then Zellij, then none |
| named terminal session | tmux or compatible Zellij | `none` preserves core behavior and shell navigation |
| GitHub pull requests/remotes | `gh` authenticated | branch still pushes when Git works; browser/manual flow may be needed |
| GitLab merge requests/remotes | `glab` authenticated | same graceful fallback |
| repository bootstrap publishing | authenticated `gh` or `glab` | local repository/scaffold still works; the wizard explains how to log in |
| remote repository snapshot template | Git plus network/authentication for the source | validation fails before the destination is created and rendered URL userinfo is redacted; local templates still work |
| native skill inventory | local filesystem plus the versioned `skills@1.5.23` path registry | always available; explicit add/update requires a directly installed `skills` executable and may use the network; repository-local npm bins are skipped and cooperating mutations are serialized |
| static MCP inventory | readable supported agent config files | missing sources are empty; malformed/unsupported sources produce fresh partial diagnostics, and runtime health is intentionally unavailable |
| setup-capable project skills | skills provider plus the entrypoint interpreter | unselected skills are skipped; selected required setup fails before scaffold mutation when its interpreter is unavailable; a clone acquired first is retained |
| staged lazygit message prefill | a lazygit version that reads `LAZYGIT_PENDING_COMMIT` | files remain staged and dev prints the suggested message; normal Git commit remains available |
| worktree dependency setup | ecosystem manager (`uv`, npm, Cargo, etc.) | plan reports the missing tool and keeps the checkout |
| remote portable-file plan/apply | Git, SSH target running a compatible `dev`, matching existing clone/branch/commit; apply also needs a verified machine UUID pin | fails before content is sent or leaves the target unchanged; no clone/task fallback is inferred |
| interactive dashboard | terminal input/output | bare `dev` prints the plain task list when piped |
| preview repository flow | terminal input/output | `dev flow` refuses non-TTY use and points to `dev repo context` / JSON inventory |
| interactive repository picker | terminal input/output; optional configured `fzf`-compatible selector | a missing selector uses dev's built-in picker; non-TTY input keeps line prompts |
| repository-note search | linked `modernc.org/sqlite` with FTS5 | no external `sqlite3` executable is required |
| static SSH alias discovery/completion | readable user OpenSSH config | unavailable/unsafe files are diagnosed; no `ssh` process or network is needed |
| SSH effective values, fresh probes, bootstrap, and fleet transport | system `ssh` client | static `ssh list`, dry-run, and local config plans remain available; effectful SSH operations fail with capability guidance |
| public companion derivation and Ed25519 generation | system `ssh-keygen` | an existing validated `.pub` can still be used; derivation/generation is unavailable |
| Windows OpenSSH target bootstrap/fleet helper | remote PowerShell + OpenSSH server | POSIX targets remain available; Windows-specific installer/launcher fails without PowerShell rather than using a shell fallback |
| terminal multiplexing on Windows | tmux/Zellij/Herdr (POSIX only) | Windows always uses the `none` backend; `dev shell-init powershell` still moves the shell |
| in-place self-update | standalone install (not Homebrew/Scoop/`go install`) | `dev upgrade` delegates to the package manager's upgrade command instead |

## Confirmed project limitations

### MCP inventory is static and intentionally incomplete

`dev mcp list` reads documented static files for Claude Code, Codex, Cursor, Gemini CLI, and OpenCode. An absolute `CLAUDE_CONFIG_DIR` relocates Claude user sources; local Claude rows retain their project key, and documented user/project/local/managed project approvals annotate declaration state. That narrow approval calculation is not a general runtime merge. The scanner does not start servers, execute helpers, contact endpoints, query health, or resolve credentials. Plugin caches, hosted connectors, remote organization configuration, inline `OPENCODE_CONFIG_CONTENT`, and command-line-only inputs are omitted. Keep the JSON `coverage` object, additive `local_project_path`, and scope-qualified duplicate rows when automating against this inventory.

### Note search and filesystem durability vary by text and platform

Latin note queries use term-wise prefix FTS and SQLite ranking. Non-ASCII queries use literal term-wise substring matching because SQLite's `unicode61` tokenizer does not segment arbitrary CJK substrings; those results do not use the same FTS ranking.

Note writes sync the file and atomically rename it on every supported platform. Unix also syncs the containing directory after rename/delete. The Windows implementation cannot provide that directory-fsync step, so a sudden power loss has a narrower durability guarantee there. Concurrent mutations by cooperating `dev` processes are serialized and each Markdown replacement is atomic; arbitrary external writers do not participate in that lock.

### A merged pull request does not retire its worktree

`dev done --pr` pushes and opens a pull/merge request, then leaves the task,
runtime, and worktree unchanged because review owns integration. `dev flow` can
run an explicit, run-local query for the exact head/base review and report only
portable existence, open/draft/merged/closed state, URL, provider, and observation
time. It does not query review decisions or checks, persist that evidence, or
turn it into DONE. Current `dev sweep` does not query the forge either. Verify
integration with exact local ancestry and finish deliberately.

`dev pr list --scope local --state merged` now reports which requests the forge considers merged, and which local checkout each belongs to. `dev sweep` still does not consult it, and that is deliberate: a squashed merge produces a commit that is not an ancestor of the local branch, so a forge saying "merged" cannot prove the work is recoverable from the remote. `dev sweep --merged-worktrees` proves containment locally with `git merge-base --is-ancestor`, and `dev done --merged` requires an explicit `--confirm-squash` attestation. Treat the pull-request list as a prompt to look, not as permission to delete.

### Pull-request inventory is limited by the provider surfaces

GitHub's `gh search prs` account surface cannot report a head branch, review decision, or check status, so those rows are `detail: "summary"` and cannot be joined to a worktree. An absent field on a summary row means the surface could not report it, not that the value is empty. GitLab's account list does carry branch/merge detail and therefore returns full rows, but its list surfaces report neither pipeline checks nor a normalized review decision.

Personal per-repository inventory may make one paginated query per requested role for each repository (author and reviewer by default). It covers repositories `dev` has a task for unless `--all-repos` widens the scan. `--repo` filters both account rows and local targets. Account search cannot distinguish merged from closed, so requesting merged/closed/all narrows an account/all request to local collection; JSON reports that effective scope.

The schema-version-1 local join reports expected and live branches, checkout existence, worktree registration, status availability/error, and whether the expected branch is actually checked out. Git details are omitted unless those live checks succeed. Azure DevOps pull requests are not listed at all; a configured target is reported as unsupported rather than failing the command.

### Prompt open uses the current terminal, not runtime placement

`dev prompt open <recipe>` runs one configured child in the foreground of the
terminal/TTY that invoked it. It does not create, focus, reuse, or inject into a
Herdr, tmux, or Zellij pane. Inside Herdr it naturally remains in the current
pane. To use another Herdr pane, create or focus it manually, enter the exact
checkout, and run `prompt open` there.

This is intentionally separate from `dev start --run`, whose dispatch target is
only the exact root pane returned for a newly created first-class Herdr worktree.
`prompt open` neither supplies a fresh runtime surface nor weakens that
exact-pane proof. `run` is the non-interactive alternative; it receives no user
stdin and defaults to a 10-minute timeout, while `open` reserves stdin for the
conversation and has no default timeout.

The configured child retains its own permission policy. Recipe instructions are
read-only guidance, not a sandbox; `dev` does not add permission flags, answer
approval prompts, or parse the response into an action.

### Prompt closeout reports do not authorize cleanup

`session-close` computes runtime-closure evidence only. An `idle` or `done`
covering agent can satisfy that one activity gate, but does not prove that work
is committed, artifacts are finalized, review is complete, or task intent is
done. Caller-contained, mixed-purpose, active, unknown, and unrecognized
sessions remain blocked or unknown as their evidence requires.

`workspace-closeout` performs the broader read-only audit: target kind,
registration/path, status availability, clean Git state, no in-progress Git
operation, known base and containment, task completion, artifact
reachability/finalization, and runtime eligibility. A merged pull request is only
evidence. Even an `eligible` audit is advisory; `dev retire` recollects and
revalidates fresh state before any mutation. Neither recipe closes a runtime,
changes Git/task state, deletes a branch/worktree, or grants permission to do so.

### Zellij exited sessions are closed but keep their names

Zellij keeps exited sessions available for resurrection. `dev` recognizes only
the exact `(EXITED - attach to resurrect)` marker, omits those sessions from live
coverage, and refuses to create over their names; reclaim one with
`zellij delete-session <name>`. A live session name containing `EXITED` is not an
exit marker. If a session exits between native listing and layout inspection,
open fails closed and asks for a retry rather than resurrecting an old layout at
the requested checkout.

### Forge CLI sign-in is reported, never retried

`gh` and `glab` are optional and independent, and `dev` never authenticates on your behalf. When a provider's stored credential is missing or rejected, `dev` reports which provider and the exact login command — for example ``glab is signed out — run `glab auth login --hostname gitlab.com` `` — and continues with whatever the other provider returned. `dev repo remote` and the TUI REMOTE view render the partial result and warn; `dev pr` fails only when no provider is authenticated at all. `dev doctor` probes sign-in state, so an installed but signed-out CLI is visible before a command needs it.

Only a missing or rejected credential is reported this way. A rate limit, a permissions or scope failure, and a network error keep their full diagnostic text including the command that failed, because signing in again would not fix them. The original command and provider output remain in the wrapped error in every case.

### Agent session capture is reserved, not wired

The task schema has an `AgentSession` field and Herdr inventory can expose live agent session IDs. The production start/park/resume path does not yet capture or attach that ID. Treat the field and live inventory as observability/future integration, not a promise that `dev resume` restores the coding-agent conversation.

### Built-in forge cache TTL differs from generated config

`dev config init` writes `forge.cache_ttl = "15m"`. With no config file, the current built-in `Forge.CacheTTL` zero value means an existing valid cache is not rejected by age; explicit `r` refresh still replaces it. Freshness also requires a source fingerprint matching the configured GH/GL hosts and Azure targets, so an endpoint change is never hidden by the zero TTL. Legacy source-less caches remain available only through explicit `--cached` and are reported stale. Run `config init` or set the TTL when freshness matters.

Older generated configs may contain `forge.remote_limit = 100`. The field is
still accepted, but complete forge inventories are now paginated and it no
longer caps synchronization. `dev config init` no longer writes it. The
`--limit` flag on `dev repo remote` limits rendered matches after the complete
inventory has been searched.

### Lazygit staged-message prefill is best effort

`repo --check-in=stage` writes a pending message in the exact worktree Git
directory. [Lazygit v0.59.0 uses that file for lowercase
`c`](https://github.com/jesseduffield/lazygit/blob/v0.59.0/pkg/gui/controllers/helpers/working_tree_helper.go#L191-L216), but it is a
lazygit implementation detail rather than a Git interface; uppercase `C` and
plain `git commit` do not consume it. Dev never overwrites a different existing
draft. A draft-write failure is warning-only: staging remains successful and
is never rolled back. If the integration changes upstream, the staged index
and printed message remain the recovery path.

### Portable files are not project transfer or backup

`dev fleet files` moves only explicitly selected, bounded files that both hosts
prove are untracked and ignored. It does not acquire a missing clone, transfer
task ownership/catalog/notes, run worktree provisioning, watch for later changes,
propagate deletion, remove source bytes, produce a remote-backup receipt, verify
restore, or authorize eviction. Private transaction staging is not a user-facing
backup/restore promise.

The source and target must already match by fetch identity, attached branch, and
exact commit. Apply additionally needs a verified `machine_id` pin. Native
Windows payloads remain disabled until ACL, reparse-point, held-root, and atomic
replacement guarantees are implemented and tested there; machine identity
diagnostics remain content-free.

### Windows has native SSH support and a smaller runtime surface

`dev` compiles and runs on `windows/amd64` and `windows/arm64`, and every release ships a `.zip` for each. Core repository/task/worktree operations and the SSH host domain are native: static discovery, protected-DACL managed fragments, reparse-point rejection, native `ssh-keygen.exe`, Windows Job Object cancellation, and POSIX/Windows remote bootstrap are covered. Fleet can also target Windows OpenSSH through its encoded PowerShell launcher. What still differs:

- There is no tmux, Zellij or Herdr, so the runtime backend is always `none`. Grouped runtime/agent activity and named sessions are unavailable; the `cd` directive and PowerShell wrapper still work.
- Shell integration is `dev shell-init powershell`. POSIX shells hand the directory back on file descriptor 3; PowerShell cannot inherit it, so the wrapper passes a temp-file path in `DEV_SHELL_CD_FILE` instead.
- `dev fleet open` starts a child shell (`%COMSPEC%`) rather than replacing the process, because Windows has no `exec(2)`.
- `dev fleet machine-id` can perform its content-free `_capability` probe, but native `fleet files` plan/apply payload helpers are denied before content is sent.
- CI keeps an advisory broad Windows suite for unrelated POSIX assumptions, but `internal/sshhost`, `internal/fleet`, and the SSH/fleet/doctor CLI contracts run in a separate required native `windows-latest` gate. Affected tests/packages and the CLI are also compiled for `windows/arm64`.

### SSH host management is intentionally narrow

Static `dev ssh list` is a provenance/candidate scanner, not a second OpenSSH evaluator. Dynamic `Match`, unsupported Include expansion, cycles, and scan bounds produce `complete: false`; they can block mutation even when plain `ssh` would eventually choose a value. `dev ssh show` and setup route resolution use plain `ssh -G`, so configured resolver and `Match exec` behavior may run. Fresh probes preserve `KnownHostsCommand`, `UpdateHostKeys`, and host-key policy rather than forcing a convenient answer.

Setup requires an explicit key or explicit Ed25519 generation and installs public material only. It supports bounded ProxyJump forms plus fixed POSIX and Windows OpenSSH installers; `ProxyCommand`, custom `AuthorizedKeysFile`, forced-shell policy, and ACL-incapable filesystems require manual remediation. Once a remote installer starts, cancellation/failure is `unknown` because the key may have been appended. Dev retains local config/generated keys and never attempts key revocation, private-key deletion/copying, `known_hosts` cleanup, password storage/fallback, or automatic credential rollback. Alias rename/adoption, bulk onboarding, arbitrary directives, and an SSH TUI are also deferred.

### Direct mode has a smaller lifecycle

A direct task uses the canonical checkout and cannot go COLD, because cold cleanup would remove a directory the repository needs. Use branch-only or worktree mode for cross-machine reconstruction.

### Flow intentionally omits expert overrides

`dev flow` offers normal managed lifecycle actions, exact unmanaged metadata-only
Adopt/clean branch-preserving Remove, and explicit remote evidence. It does not
offer dirty commit/discard, WIP, shared-writer, ownership takeover, force, unknown-
runtime, or assume-no-runtime choices. A blocked plan shows remediation and the
compatible CLI fallback; existing command flags and structured contracts remain
available.

Task-backed lifecycle and exact unmanaged linked-checkout actions share
`internal/taskflow`. Explicit unmanaged path retirement remains an isolated
compatibility implementation, while `sweep` retains record-only reaping, orphan
salvage, and other narrow reconciliation paths. Do not assume every cleanup path
has the same planner. Raw Git and configured external tools also bypass dev's
PlanID, locks, revalidation, and result ledger.

## Current behaviors that are implemented

These were historical gaps and should not be reintroduced as limitations:

- `dev repo new|create`, `repo clone`, and `repo setup` share a preset-driven bootstrap pipeline. A plain explicit `repo new NAME` remains minimal, while a clear Git URL, local Git path, or owner/name routes through clone acquisition and preserves source history/remote. The no-argument new-repository wizard detects the same reference in its first field; for a new repository, a default-no customization gate keeps the normal `agent-ready` flow concise. Bare `repo clone` selects exact URLs from the existing forge cache; outside a checkout, bare `start` selects a fast-discovered local repository, while the in-repository path keeps its immediate current default. Both retain manual entry. A configured line selector is optional, with a built-in fallback. Text fields use a TTY inline editor, so cursor keys edit rather than inserting raw escape bytes; non-TTY readers retain line-oriented behavior.
- `repo new` can snapshot a local directory/repository or Git source at an optional branch, tag, or commit and confined subdirectory into a fresh history. An unpinned local Git tree includes tracked plus untracked non-ignored files; a non-Git directory includes its full current tree. Source `.git` metadata is excluded, URL userinfo is redacted, unsafe file types/paths fail before destination creation, and held root/file handles confine mutable-path races. Human plans preview paths and warn when the snapshot is live; presets can select catalog-repository subfolders.
- Repository setup supports `--check-in=commit|stage|none` (`auto` for preset compatibility). Staged setup runs before-commit setup and `git add -A` without an `after_commit` phase, cannot publish or hand off to `start`, and may prefill lazygit lowercase `c` as described above.
- `agent-ready` uses one explicitly incomplete, non-fabricating `AGENTS.md` starter across built-in and native setup paths. Its common top-level ignore excludes only `.specstory/statistics.json`; history, project identity, and config remain visible. Selected skills with matching source and agent targets share one installer invocation. The optional `agent-history-hygiene` initializer additionally writes pre-commit/gitleaks policy and merges missing machine-local `.project.json`/`statistics.json` rules into `.specstory/.gitignore`; custom content and transcript history remain trackable.
- Project `.dev-cli/config.toml` and `.dev-cli/scaffolds.toml` are constrained to portable setup policy. Executable project configuration is keyed to the canonical Git common directory and an exact content hash; a changed hash is untrusted until approved again.
- `dev ssh init/list/show/setup/probe/remove` provides explicit OpenSSH host onboarding without a separate host database. Dev owns only its dedicated Include, canonical `dev.d` fragments, and opt-in generated fleet registrations; all public SSH JSON is one versioned object, and TSV listing is a documented six-field selector.
- Fleet merges user-authored primary `remotes.toml` with strict generated `remotes.d` fragments, tracks `remote_os`, and uses a hidden-helper-only encoded PowerShell launcher for Windows targets. Primary duplicate aliases remain compatible; any generated alias collision fails closed.
- `dev repo context --json` exposes additive schema-v1 local/remote evidence with source, age, freshness, completeness, null/error preservation, and scoped readiness; external probes happen only with `--refresh`. `dev status` reuses the cheap local readiness projection without network access.
- `dev fleet machine-id` reports an observed UUID without changing configuration. `dev fleet files` is report-only by default, uses a separate `[local_files]` allowlist, negotiates downward-only limits before content, and requires explicit apply/replacement controls plus a matching target pin.

- `dev start --focus` activates the runtime after non-JSON creation.
- `dev start --run '<shell command>'` dispatches only to an exact root pane from
  a newly created first-class Herdr worktree. It is incompatible with `--json`,
  non-worktree modes, and non-Herdr runtimes, and does not wait for command exit.
- TUI navigation refuses to open a missing COLD checkout and directs the user to `dev resume`.
- Preview-labelled `dev flow [repo]` is an independent full-screen TTY model. It resolves canonical/linked cwd to the exact surface, uses a picker outside Git, shows every registered worktree plus task-only records, and makes every mutation Enter-to-plan then approve. Apply revalidates task revision and exact repository/worktree/ref/runtime/artifact authority and retains partial step results. Local `r` is network-free; `R` explicitly selects fetch/query/both and keeps minimal review evidence run-local.
- `DEV_TUI_TRACE` starts at `cli.Execute`; it cannot include OS process loading. `tui.initial_view_returned` measures model construction, not renderer flush or physical terminal paint. Use it for same-profile comparisons rather than universal hardware/network guarantees.
- Runtime handles now record backend provenance and are revalidated before cleanup.
- `auto` runtime selection includes Zellij between tmux and none.

- `dev done` opens an interactive finish wizard on a TTY when `--ff`/`--pr` are both omitted, analyzing a dirty checkout against the base (commit, discard, or cancel) instead of rejecting any uncommitted change outright; a non-interactive caller still passes an explicit `--dirty` policy and `--yes` for a destructive discard.
- Human-readable output now carries semantic color (`--color auto|always|never`), automatically disabled when `NO_COLOR` is set, `TERM=dumb`, or stdout/stderr is not a terminal.
- `dev done` records MERGED only: it never closes the invoking runtime, removes a worktree, or deletes a branch. Cleanup moved to `dev retire`, which runs from outside the target workspace, refuses active agents and mixed-purpose workspaces, and revalidates Git state after every runtime closure. `dev done --delete-branch` is now an error pointing at `dev retire --delete-branch`, and `--keep-worktree` warns as a no-op.
- `dev sweep --merged-worktrees` enumerates linked worktrees from Git rather than from the task registry, so unmanaged worktrees whose branches are contained in the base become retirable. Containment alone is never permission; dirty state, unfinalized artifacts, in-progress Git operations, and runtime refusals all still block it, and branches survive unless `--delete-branches` is passed.
- `dev sweep` reports a branch-backed task whose branch Git no longer has as dead and offers to reap the record. Such a task cannot be finished, resumed, or retired, because every one of those paths resolves the branch first; the suggestion stays report-only until `--apply`.
- An unknown command is reported instead of discarded. `dev` silences cobra's own error printing and previously also skipped printing anything whose message began with `unknown command`, so a mistyped command produced no output at all on either stream. The message, cobra's "Did you mean this?" suggestions, and a pointer to `--help` are now printed to stderr with exit status 1.
- A stray argument to a command family is an error rather than a silent help render. `dev wt bogus` used to print `dev wt` help and exit 0 because a family has no `Run` of its own; every family node now reports the unknown subcommand and exits 1, while a bare family still prints its help and exits 0.
- Argument-count and flag errors print the failing command's usage block. `--color` still governs whether that block is colorized.
- Each command family's help carries an ASCII orientation diagram and a `See also: dev help <topic>` pointer, and `dev help <command>` resolves a command name or alias to its topic, so `dev help wt` reaches the worktrees page.
- Semantic color covers every human-readable surface, including the interactive dashboard: `dev --color never`, `NO_COLOR` and `TERM=dumb` now disable dashboard color too, which they previously did not.
- `dev sweep` reaps a task whose repository directory no longer exists. Such a record was unreachable by every command in the binary: `done`, `resume`, `park` and `retire` all resolve the repository first, the dead-branch rule excludes direct mode, and the stale-worktree rule requires a recorded worktree path. A live runtime session rules the suggestion out, and reaping removes only dev's record of intent.
- `dev sweep` reports a task-recorded checkout that exists but Git does not register and that holds nothing but agent artifact directories. Removal is offered only when every file inside is byte-identical to one already in the repository; anything else is reported as salvage work and is never removed, including under `--apply`.
- `dev sweep` acts on a cold task whose worktree is still on disk. Inventory has always computed that drift for `dev ls` and the dashboard, but sweep never consulted it, so it was displayed and not actionable.
- `dev retire <path>` reaps the matching task record. Only the by-task form set the task identity, so retiring the same checkout by path left the record behind; the DONE-state and identity checks are unchanged.
- `dev version` reports whether the running build is a published release, and `dev doctor` carries the same line plus the install owner, invoked/resolved executable path, and warnings for distinct `dev` copies on `PATH`. Nothing in the tool answered "am I current?" before, and `go install ...@latest` resolves to the newest tag, so an untagged feature was invisible to anyone installing it.
- A release publishes platform archives and `SHA256SUMS` and takes its notes from `CHANGELOG.md`. Earlier releases published a GitHub release object and nothing else, so their assets are absent by construction.
- A release publishes Windows `.zip` archives alongside the Unix `.tar.gz` set, refreshes an in-repo Scoop manifest attached to the release (and pushed to the bucket when a token is configured), and publishes the matching source formula to `daviddwlee84/homebrew-tap`. Homebrew publication is a required release step and fails visibly when its repository token is missing or rejected. A manual workflow can retry or backfill the formula for an existing stable release without creating or moving a tag.
- `dev upgrade` downloads the current release for this platform, verifies it against the release `SHA256SUMS`, and replaces the running binary with an atomic rename (Windows moves the live `.exe` aside and sweeps it on the next run). It detects and runs Homebrew, Scoop or `go install` when one of them owns the file; Homebrew users receive releases through the automatically advanced tap instead of an in-place Cellar overwrite.
- An interactive `dev` command prints one dim "newer release available" line at most once a day, read from the day-old release cache; it never blocks on the network. For the TUI, a stale-cache background refresh starts only after the initial view returns. `[update] check = false` or `DEV_NO_UPDATE_CHECK` disables it.

## Claude Code status matrix

| Surface | Status on 2026-08-28 | Compatibility note |
|---|---|---|
| core agentic loop/tools | official | exact tools depend on surface/model/policy |
| worktree isolation | official, rapidly evolving | cleanup, baseRef, resume, and safety details are version-sensitive |
| subagents | official | fork/background defaults and naming changed across 2.1.x |
| Agent view | research preview | retain manual sessions/worktrees fallback |
| agent teams | experimental, disabled by default | no automatic worktree isolation; resumption/task/shutdown limitations |
| Dynamic Workflows | versioned feature | requires v2.1.154+; availability/config and limits vary by release |
| Agent SDK | official SDK | language SDK parity and event availability can differ |

`TeamCreate` and `TeamDelete` are historical tools removed in v2.1.178. The old `Task` worker tool name was replaced by `Agent`; do not confuse either with Task-list metadata tools.

## Documentation-stack limitations

- `mkdocs-static-i18n` reports that `zh-TW` is not a Lunr language, so Chinese search does not receive a dedicated Lunr stemmer/segmenter. Navigation and pages still build.
- `mkdocs-llmstxt` skips pages after static-i18n remaps them. This project replaces that empty output with a deterministic local generator and runs strict builds again.
- The copy-to-LLM plugin's button labels remain English on zh-TW pages; this is cosmetic.
- MkDocs and Material are pinned below their next major versions because the selected plugin stack targets MkDocs 1.x/Material 9.x.

## Reporting a compatibility change

Update the owning guide, both languages, this matrix, and [Sources and freshness](sources-freshness.md). Include the tested binary/version and a code/test or official-source link. Never preserve a known false statement for backward documentation compatibility.

## Sources

- [`internal/runtime/runtime.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/runtime/runtime.go)
- [`internal/cli/flow.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/flow.go)
- [`internal/taskflow`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/taskflow)
- [`internal/flowtui`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/flowtui)
- [`internal/cli/done.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/done.go)
- [`internal/cli/sweep.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/sweep.go)
- [`internal/task/task.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/task/task.go)
- [`internal/forge/cache.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/forge/cache.go)
- [`internal/note/index.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/index.go)
- [`internal/note/store.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/store.go)
- [`internal/note/sync_windows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/note/sync_windows.go)
- [`internal/cli/upgrade.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/upgrade.go)
- [`internal/cli/version.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/version.go)
- [`internal/cli/prompt_command.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/prompt_command.go)
- [`internal/handoff`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/handoff)
- [`internal/closeout`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/closeout)
- [`internal/retire/audit.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/retire/audit.go)
- [`scripts/update-homebrew-formula.sh`](https://github.com/daviddwlee84/dev-cli/blob/main/scripts/update-homebrew-formula.sh)
- [`internal/scaffold`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/scaffold)
- [`internal/projectconfig`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/projectconfig)
- [`internal/cli/fleet_exec_windows.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_exec_windows.go)
- [`internal/cli/ssh.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/ssh.go)
- [`internal/sshhost`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/sshhost)
- [`internal/fleet/managed.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/managed.go)
- [`internal/fleet/transport.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/fleet/transport.go)
- [`internal/cli/fleet_files.go`](https://github.com/daviddwlee84/dev-cli/blob/main/internal/cli/fleet_files.go)
- [`internal/localfiles`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/localfiles)
- [`internal/repocontext`](https://github.com/daviddwlee84/dev-cli/tree/main/internal/repocontext)
- [`.github/workflows/ci.yml`](https://github.com/daviddwlee84/dev-cli/blob/main/.github/workflows/ci.yml)
- [`.github/workflows/release.yml`](https://github.com/daviddwlee84/dev-cli/blob/main/.github/workflows/release.yml)
- [`.github/workflows/publish-homebrew.yml`](https://github.com/daviddwlee84/dev-cli/blob/main/.github/workflows/publish-homebrew.yml)
- [Claude Code parallel agents](https://code.claude.com/docs/en/agents)
