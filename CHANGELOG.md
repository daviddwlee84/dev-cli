# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases use [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Interactive `dev done` can now preserve a dirty canonical integration
  checkout while fast-forwarding: `stash+restore` captures staged, unstaged,
  and non-ignored untracked work under an exact stash OID, restores the index
  and worktree after integration, and drops only that exact stash entry. Dirty
  submodules and nested repositories remain blockers; restore conflicts retain
  the stash and keep the task active with an exact recovery command.

## [0.2.16] - 2026-09-04

### Added

- Interactive `dev done` can now recover from two common fast-forward blockers
  without dropping back to a raw error: it may close freshly revalidated
  non-caller Herdr panes whose agents are still exactly `idle`/`done`, and it
  can show canonical-checkout dirty paths before offering PR handoff, typed
  `DROP` discard, or cancel. Working/blocked/waiting/unknown agents remain
  non-closeable, and canonical discard is a guarded taskflow effect that
  revalidates exact status, HEAD, and occupancy before integration continues.

## [0.2.15] - 2026-09-04

### Added

- Bare interactive `dev done` now follows a successful managed-worktree merge
  with a cleanup wizard that previews covering runtime panes and agent states,
  then offers keep, retire while retaining the branch, or retire and delete the
  freshly contained branch. Caller-owned Herdr workspaces are handed to a new
  exact-pane external coordinator; active agents, mixed workspaces, stale
  authority, and unapproved unknown status still fail closed.
- Source checkouts include `mise.toml` pinning Go 1.26.4, matching the module
  toolchain used by development and CI.

### Changed

- The trusted shell wrapper now carries a narrow post-exit retirement action
  on a private side channel, allowing it to leave the target checkout before a
  fresh `dev retire` without evaluating ordinary command output as shell code.

## [0.2.14] - 2026-09-04

### Added

- REPOS now mirrors TRY's creation workflow: `n` suspends the dashboard into
  the existing clone-aware `dev repo new --handoff stay` wizard, then refreshes
  local TASKS/REPOS/TRY state without contacting remotes. It works even when
  REPOS is empty and is also available from the row action menu. REPOS quick
  note creation moves to `a`; `N` continues to open the notes browser.

## [0.2.13] - 2026-09-04

### Added

- The dashboard now supports cell-motion mouse input: left-click selects rows or
  switches visible tabs, the wheel moves three rows, and right-click opens the
  selected row's existing actions without making a click implicitly open it.
- SKILLS and MCP share a session-only `A` context/all scope toggle and can open
  the selected local configuration in `$VISUAL`/`$EDITOR` or copy its path,
  sanitized summary, and explicitly requested raw file. Raw file copy is bounded
  to 1 MiB but may put credentials and other declarations into the system
  clipboard. A configured `A` tool remains available on other views; capability
  views give the scope toggle precedence.

### Changed

- MCP caps its content-responsive `SERVER` column at 32 cells, keeping transport,
  state, and source columns together instead of pushing them to the far edge of a
  wide terminal.

## [0.2.12] - 2026-09-04

### Changed

- The TUI SKILLS and MCP views are now startup-context-first. From a Git
  checkout they scan only the exact current worktree plus global/user sources,
  matching what an agent launched there can read instead of listing unrelated
  projects. Outside Git they retain the cross-repository inventory and include
  every accepted REPOS target.

## [0.2.11] - 2026-09-04

### Added

- `dev pr list` shows the pull and merge requests you opened and the ones
  awaiting your review, and joins reported head branches to local task and
  checkout health. Account and per-repository surfaces remain explicit;
  personal per-repository queries may call once per requested role, engaged
  repositories bound the default local scope, and `--all-repos` widens it.
  `--actions` and schema-versioned `--json` emit reviewable `gh`/`glab` command
  strings; `dev` prints them and never runs them.
- `dev prompt list|render|run|open` provides a provider-neutral prompt handoff.
  Built-in `pr-triage`, `session-close`, and `workspace-closeout` recipes collect
  deterministic read-only context. `render` prints it, `run` launches a bounded
  batch command with no user stdin (10-minute default), and `open` launches a
  foreground conversation in the current terminal with no default timeout.
  There is no built-in vendor, agent, or launcher, and `dev` never parses the
  reply, loops, mutates lifecycle state, or changes permissions.
- Agent launchers use host-only nested configuration: `[[agent]]` selects a
  unique name/default, while `[agent.run]` and `[agent.open]` independently set
  direct `command` argv or static `shell`, `stdin|file|argv` input, plus optional
  batch timeout/shell-RC policy. `open` rejects stdin and timeout, prompts are never interpolated
  into shell text, and repository `.dev-cli/config.toml` cannot define agents.
- Agent profiles may include an optional description, and `dev prompt agents
  [--json]` exposes a sorted discovery inventory. The stable JSON objects contain
  name/description/default and redacted run/open capability metadata; neither
  output form reveals argv, shell source, executable directories, environment,
  prompt text, or config path.
- `dev sweep --ephemeral-worktrees` adds a schema-version-1, report-before-apply
  cleanup path for Claude Workflow worktrees. A bounded private-metadata adapter
  verifies exact workflow/agent/path/journal linkage without decoding prompts,
  scripts, logs, result bodies, or transcripts; strict V1 liveness, Git/task/
  artifact/runtime/caller checks, ignored-file and recursive-submodule checks,
  and locked fingerprint revalidation all fail closed. Apply is TTY-only with
  per-item confirmation and plain non-force removal. Branches survive by default;
  separately requested deletion needs an explicit unchanged base and safe `-d`.
  Claude Code 2.1.259 does not expose branch/HEAD plus a non-replayable
  registration generation, so its current claims report `provider-git-identity`
  as unknown and remain report-only rather than authorizing path-reuse deletion.

### Changed

- `dev doctor` now reports whether `gh` and `glab` are signed in, not only
  whether they are installed, with the exact login command for each.

### Fixed

- Pull-request `--repo` selectors now constrain both account rows and local
  query targets, and JSON reports the effective scope after non-open account
  requests narrow to local collection. Schema version 1 local joins distinguish
  `expected_branch` from `live_branch`, report checkout existence, worktree
  registration and status availability/errors independently, and include Git
  status only when the expected branch is actually checked out. Fork pull
  requests join against their source repository (resolving GitLab source project
  IDs when needed), canonical/unmanaged/external worktrees join without a task,
  duplicate local clones no longer suppress the remote query, and ambiguous
  same-branch local evidence fails closed instead of choosing a clone. Provider
  host identity is preserved so an enterprise remote is never queried against
  the public forge, partial failures appear in JSON `warnings`, and stale GitHub
  checks are not reported as passing.

## [0.2.10] - 2026-09-04

### Fixed

- REMOTE cloning now stays visibly pending with an animated status, lets `enter`
  clone and stay or `o` clone and open, and waits for a local-only repository
  refresh before marking the checkout as `repo` and making it searchable in
  REPOS. Clone, discovery, and runtime-open failures retain the last known true
  local state instead of reverting to a misleading `not cloned` row.
- The built-in `agent-ready` preset now generates one canonical, explicitly
  incomplete `AGENTS.md` starter with safe working rules, non-fabrication
  guidance, project TODOs, and concrete handoff requirements instead of pointing
  at undocumented checks. Its common ignore block now excludes only SpecStory's
  derived root `statistics.json` while leaving session histories and project
  config trackable; orphan salvage uses the same exact-path rule instead of
  discarding similarly named nested files.

## [0.2.9] - 2026-09-03

### Added

- Bare `dev repo clone` and the interactive `dev start` repository step now
  offer a configurable line picker. Clone candidates come from the existing
  private forge cache without an implicit network refresh; outside a checkout,
  start candidates use fast live discovery and are fully resolved after selection,
  while the in-repository current default remains immediate. `fzf` is the
  default when installed, a built-in Bubble Tea selector is the automatic
  fallback, manual entry and non-TTY behavior remain available, and optional
  Television/fzf compositions over `dev repo remote --cached --json` ship under
  `contrib/`.

## [0.2.8] - 2026-09-03

### Added

- Added the `dev ssh` command family for bounded static OpenSSH alias discovery,
  source-aware `list`/`show`, report-before-apply Include initialization,
  idempotent managed-host setup, fresh login probes, narrow removal, versioned
  JSON, and a six-column TSV selector contract. Setup supports explicit existing
  keys or Ed25519 generation, ProxyJump routes, POSIX and Windows OpenSSH targets,
  config-only/dry-run operation, and opt-in fleet registration after a fresh
  ordinary alias login succeeds.
- Added strict dev-owned fleet fragments beside the primary remote config
  (`remotes.d/ssh-<alias>.toml`, or the directory derived from `--remotes`) and
  `remote_os = "posix"|"windows"`. Windows fleet targets now run allowlisted
  hidden helpers through an encoded PowerShell launcher, with native Windows SSH,
  ACL, reparse-point, key-generation, cancellation, and fleet behavior enforced
  by a required CI gate plus Windows ARM64 compile coverage.

### Changed

- Fleet loading now merges the byte-for-byte user-authored primary
  `remotes.toml` with generated fragments in lexical order, applies defaults
  after the merge, includes `remote_os` in endpoint cache identity, and shows
  generated ownership in `dev fleet config show`. `config edit` continues to
  open only the primary file; `dev ssh setup --fleet` and
  `dev ssh remove --fleet` own generated registrations.

### Security

- OpenSSH remains connection authority and foreign `Host` blocks stay read-only.
  Dev writes only its dedicated `~/.ssh/dev.d/*.conf` namespace, validates
  path/file ownership before replacement, preserves the user's host-key policy,
  disables connection sharing for authentication proofs, and sends only one
  bounded public-key record to fixed remote installers. It never copies private
  keys, supplies passwords or host-key acceptance through `--yes`, removes
  `known_hosts` entries, or revokes/deletes remote authorized keys; interrupted
  installers and later failures are reported as resumable partial or unknown
  outcomes without unsafe rollback claims.

### Fixed

- Windows fleet config and generated-fragment publication now use exact held
  handles for guarded backup and no-replace publication, so validation handles
  do not block replacement and a concurrently created target wins safely.
- Windows SSH staging files now explicitly receive current-user ownership as
  well as a protected DACL, including when an elevated token would otherwise
  default new files to the Administrators group.
- A successful fleet SSH password retry is no longer changed into exit 255
  merely because the process completed without reading the optional askpass
  secret pipe.
- The Zellij runtime no longer fails every session listing when an exited
  session is left in Zellij's namespace. Zellij keeps exited sessions
  resurrectable, and `dump-layout` against one errors with `There is no active
  session!`, which previously turned `dev ls`, `dev doctor` and the TUI runtime
  join into an error. Exited sessions are now excluded from the live view, and
  `dev start` refuses to create over a name an exited session still owns
  instead of silently resurrecting its old layout at the old directory. If a
  live session exits between listing and layout inspection, creation now also
  fails closed rather than falling through to resurrection. Only Zellij's exact
  exited marker is recognized; a live session name containing `EXITED` remains live.
- An unauthenticated `gh` or `glab` no longer surfaces its full command line
  in the TUI REMOTE footer, in `dev repo remote` warnings, or in the cached
  provider status. A rejected credential now reads as, for example,
  ``glab is signed out — run `glab auth login --hostname gitlab.com` ``, with
  the original command and provider output preserved in the wrapped error.
  Rate limits, permission and network failures keep their full diagnostic text,
  because signing in again would not fix them. Classification inspects provider
  stderr rather than argv, so a title or description containing `auth login`
  cannot turn an unrelated failure into a sign-in warning. GitLab probing now honors
  `GLAB_HOST` as well as `GITLAB_HOST`, and reviewer inventory explicitly uses
  account scope instead of GitLab's authored-by-me default.
- Prompt `run`/`open` now resolve the global explicit/default/sole profile and
  requested launcher before recipe collection, never fall back to a different
  profile for a missing mode, and check a non-dry `open` TTY before querying
  forge/runtime evidence. Mode-aware `--agent` completion now honors parsed
  `--config`, sanitized descriptions, prefixes, and invalid-config degradation.
- Prompt handoffs now treat the invoking agent as an existing writer rather
  than excluding its pane, so one agent cannot launch another in the same
  checkout without the shared-checkout override. Batch timeout cancellation
  terminates the launcher process tree rather than leaving a descendant agent
  running after `dev` removes its prompt file.
- Session and workspace prompts now reuse retirement's pane/mixed-workspace and
  agent-status policy, Git branch relation logic, and read-only artifact
  reachability inspection. Missing or failed evidence remains unknown instead
  of being converted into a known blocker or safe zero value.

## [0.2.7] - 2026-09-03

### Added

- Agent skill inventory is now provider-independent and repository-aware. A
  versioned snapshot of the `skills@1.5.23` 77-agent path registry scans the
  current checkout, a selected repository, or every canonical repository plus
  global scope without executing Node; explicit source checks group shared
  Git/ref inputs once and keep local presence/integrity separate from upstream
  freshness.
- `dev mcp list` and a lazy MCP TUI view inventory static declarations for
  Claude Code, Codex, Cursor, Gemini CLI, and OpenCode. Output is scope-qualified,
  never claims runtime health/effective precedence, and strips command arguments,
  environment/header/OAuth values, URL credentials, and indirect file contents.
- Wide TASKS tables now show the repository for each row; compact terminals keep
  the previous layout and the detail pane continues to show the full repo/path.

### Fixed

- Read-only skill listing no longer treats the presence of `npx` as proof that
  the `skills` package is cached. It never runs `npx --no-install skills`, so npm
  cannot resolve a missing package from the registry and cancel the inventory.
- Repository-scoped skill mutations now require a directly installed `skills`
  executable, skip npm-local PATH shims in favor of a later trusted provider,
  validate against the effective canonical repository root, use the canonical
  lock key, reject option-like or source-less lock entries, and serialize provider
  processes across cooperating `dev` invocations. TUI post-update verification
  remains pinned to the confirmed checkout.
- Explicit skill source checks terminate Git option parsing, reject unsafe refs,
  and hash Git tree/blob objects without checking out remote content, so
  `.gitattributes` filters and `core.autocrlf` cannot execute or invent updates.
  Locale-dependent folder hashes with non-ASCII paths remain explicitly
  unverifiable instead of reporting false freshness.
- Native skill/MCP readers now open regular files non-blockingly before bounded
  reads, closing stat/open FIFO races. Target identity preserves explicit linked
  worktree aliases, canonicalizes missing suffixes below symlinks, attributes
  nested Claude projects to the most specific checkout, and no longer attributes
  project-only agents to global skill paths.
- MCP normalization now honors `CLAUDE_CONFIG_DIR`, Claude user/project/local/
  managed approval settings, exact local project paths, documented Gemini and
  Claude transports, provider-specific environment references, and Codex OAuth
  scope/resource facts. Null server members are diagnosed without producing
  phantom rows, and values remain redacted.
- Warning-only SKILLS/MCP diagnostics now keep fresh TUI snapshots instead of
  causing reload loops; visible dependent views resume after REPOS recovers,
  startup checkout identity is cached once, post-update skill reloads are traced,
  and long credential summaries stay within the terminal viewport.

## [0.2.6] - 2026-09-03

### Added

- `dev flow [repo]` adds a preview-labelled, full-screen repository lifecycle
  interface independent of the dashboard. It projects every registered worktree
  plus task-only records, separates persisted HOT/WARM/COLD/DONE intent from
  live local/manual remote evidence, and requires an exact revision-bound plan
  plus action-specific approval before managed lifecycle, unmanaged Adopt/Remove,
  or remote refresh effects. Apply revalidates authority, reports partial steps,
  preserves branches for unmanaged removal, and keeps DONE resources until
  explicit retirement.

### Fixed

- Existing park, resume, completion, retirement, unmanaged worktree removal, and
  sweep paths now share revision-bound taskflow plans instead of independently
  mutating lifecycle state. Git ref probe failures, unobserved runtime or writer
  occupancy, pending artifacts across checkout moves, changed remote/merge
  authority, and fork review results fail closed; successful DONE/MERGED
  completion retains runtime, worktree, and branch resources for explicit
  retirement.
## [0.2.5] - 2026-09-01

### Added

- `dev repo context [repo]` now has an additive schema-v1 `--json` report and an
  explicit `--refresh` path. Local checkout, Git, task, worktree, and runtime facts
  stay live and network-free; optional forge and configured-fleet observations carry
  source, age, freshness, completeness, and collection errors. Remote endpoints are
  structurally sanitized before public output, and readiness remains split by scope
  instead of collapsing unknown evidence into a global safe value. `dev status`
  reuses the same cheap local readiness projection without network probes.
- `dev fleet machine-id <host>` exposes the remote host's non-secret durable machine
  UUID and compares it with the optional `machine_id` pin in `remotes.toml`.
  `dev fleet files [repo-or-path] --to <host>` plans a one-way transfer of explicitly
  allowlisted ignored files and mutates only with `--apply`; both hosts must prove
  every exact path is untracked, ignored, regular, portable, bounded, and bound to the
  same fetch identity, attached branch, and commit; apply additionally requires the
  pinned target machine. Differing target bytes require a separate `--replace`;
  `--yes` never implies replacement.

### Fixed

- Task writes now carry an in-memory revision from read to write, so existing
  lifecycle callers cannot silently overwrite a concurrent process's newer task
  record. Park holds the task transaction through runtime/worktree cleanup, and a
  DONE direct-task ID starts a new compare-and-swap generation. Catalog create,
  update, import, and experiment reconciliation share one
  cross-process transaction lock, while corrupt task records make repository-context
  inventory explicitly incomplete instead of disappearing behind a warning.
- Portable-file retries now recover an interrupted store both before and after the
  first journal write. Source branch/HEAD/fetch identity is revalidated around payload
  reads; target sync/apply and remote aliases share the canonical clone lease. Durable
  publication provenance plus exact digest/mode prevents rollback from deleting a
  foreign identical file or retaining changed permissions. Target discovery errors fail
  closed, nested bare repositories are excluded, post-publication filesystem errors
  remove their own destination, and credential-bearing SCP-like tokens cannot enter
  catalog keys, protocol journals, or repository-context output.
## [0.2.4] - 2026-09-01

### Added

- Stable releases now render and publish `Formula/dev-cli.rb` to
  `daviddwlee84/homebrew-tap`. The release job requires a fine-grained
  `HOMEBREW_TAP_TOKEN`, fails visibly when the distribution update cannot be
  published, and has a manual workflow for retrying or backfilling an existing
  stable release without creating another tag. Homebrew installs remain
  package-manager-owned rather than letting `dev upgrade` overwrite a Cellar
  binary.

### Changed

- `dev upgrade` now delegates directly to the detected Homebrew, Scoop, or
  `go install` owner after confirmation, while standalone binaries retain the
  verified in-place replacement path.
- `dev doctor` now reports the running executable's install owner and resolved
  path, and warns when another `dev` binary on `PATH` could shadow it.

### Fixed

- Rendered-site documentation checks now reuse parsed HTML instead of eagerly
  reparsing the same target for every link.

## [0.2.3] - 2026-09-01

### Added

- The TUI can write an opt-in, versioned performance trace when
  `DEV_TUI_TRACE` names an absolute new file. Relative timings distinguish CLI
  load, initial view construction, requested-tab cache/live snapshots, aggregate
  row counts, producer completion, discarded stale generations, and key-update
  work without recording repository/task/host/tool names, paths, commands, key
  values, URLs, handles, or raw errors. Traces are bounded, private, local-only,
  and written after the alternate screen is restored; activity `stats.db`
  remains unchanged.
- TASKS, REPOS, and TRY now publish independently from one shared local load
  cycle. Each cycle reads task intent, runtime sessions, and repository discovery
  once, shares an eight-slot TASKS/REPOS enrichment limit, and lets FLEET and
  REMOTE reuse the accepted REPOS snapshot instead of rescanning local projects.

### Fixed

- TUI construction and rendering no longer wait for runtime auto-detection,
  project-root discovery, cache decoding, interactive shell tool probes, or the
  passive release refresh. Per-view generations reject late results, cancel
  superseded reads, retain usable rows on refresh failure, and distinguish a
  successful empty result from no result.
- Successful empty forge refreshes now replace obsolete cached repositories;
  fleet cache identity includes the SSH port; forge cache identity includes
  configured GH/GL hosts and Azure targets; GitLab inventory no longer infers its
  host from cwd; canceled refreshes cannot commit cache state; oversized or
  malformed forge/fleet caches are ignored; and cached repository names cannot
  escape `project_root` when cloning.

## [0.2.2] - 2026-08-31

### Added

- `dev start --run '<shell command>'` can dispatch an explicit command to the
  exact root pane returned for a newly created first-class Herdr worktree.
  `--focus` remains an independent opt-in for switching to that workspace;
  reuse, fallback, non-Herdr runtimes, non-worktree modes, and `--json` fail
  closed without guessing another pane.

### Changed

- The dashboard's FLEET view now hides this machine by default because REPOS
  already provides its richer local inventory. Press `a` to include local rows;
  the non-interactive `dev fleet list` contract still includes this machine.

### Fixed

- REMOTE keeps the top navigation bar visible while cached repositories refresh;
  its extra refresh banner is now included in the terminal-height budget.
- TASKS no longer opens a stale artifact directory or the canonical checkout for
  an unavailable worktree task. Missing/unregistered worktrees point to
  `dev sweep`, while a normal COLD worktree task points to explicit `dev resume`.

## [0.2.1] - 2026-08-30

### Added

- `dev repo new` now has a confirmed interactive repository-bootstrap wizard, with
  `dev repo create` as an alias; `dev repo clone` can optionally apply the same setup,
  and `dev repo setup` safely merges preset files into an existing clean checkout.
  A clear Git URL, local Git path, or owner/repository argument to `new`/`create` is
  acquired as a clone, preserving its history and remote. The no-argument wizard
  detects the same references. Its default-no “Customize preset and template options?”
  gate keeps the normal `agent-ready` path short while still exposing every detailed
  question on request.
  Presets live in versioned `scaffolds.toml`, support typed inputs, safe templates,
  phased hooks, project skills with setup entrypoints, optional GitHub/GitLab
  publication, and explicit stay/cd/runtime/start handoffs. The two recommended
  agent skills use reviewed built-in project initializers instead of executing
  newly downloaded setup code. Skills with the same source and agent targets are
  installed in one provider invocation; the history initializer also adds
  `.specstory/.gitignore` rules for machine-local identity/statistics while keeping
  session history trackable; existing custom content is preserved and missing managed
  rules are appended.
- `dev repo new` can seed a new Git history from a safe snapshot of a local directory,
  local repository, Git URL, or owner/repository reference. `--template-ref` selects a
  branch, tag, or commit and `--template-subdir` selects one confined folder; preset
  `template`, `template_ref`, and `template_subdir` fields support catalog repositories
  containing several starters. Source `.git` metadata is excluded, and symlinks,
  special files, and path traversal are rejected. Plans redact URL userinfo, preview
  selected paths, and warn when a local live snapshot is not pinned to a commit.
- Repository bootstrap now has `--check-in <auto|commit|stage|none>`. `stage` leaves
  generated changes staged for review and seeds lazygit's worktree-local pending
  message as a best-effort convenience for lowercase `c`; staged setup cannot be
  published, and the `start` handoff still requires committed setup. Presets can set
  `initial_check_in`, and project setup defaults can set `repo.setup.check_in`. A draft
  write failure is a warning after staging succeeds; it never rolls the index back.
- TTY text prompts in the repository, task-start, and finish wizards now use an inline
  editor with cursor movement, Home/End, Delete/Backspace, and Esc/Ctrl-C cancellation
  instead of inserting raw arrow-key escape sequences. Buffered non-TTY input retains
  its script/test behavior.
- Repositories may commit allowlisted `.dev-cli/config.toml` and
  `.dev-cli/scaffolds.toml` overrides. Host paths/runtime/state remain global, while
  executable project configuration is content-hash trusted locally before it can run;
  legacy `.dev.toml` worktree settings remain compatible.

- Releases now publish `windows/amd64` and `windows/arm64` `.zip` archives alongside the
  Unix `.tar.gz` set, all covered by `SHA256SUMS`. `dev` builds and runs on Windows; core
  repository, task and worktree commands work. There is no tmux, Zellij or Herdr there, so
  the runtime backend is always `none`, and `dev fleet open` starts a child shell rather than
  replacing the process. Shell integration is `dev shell-init powershell`, which passes the
  directory back through a `DEV_SHELL_CD_FILE` temp file since Windows cannot inherit
  file descriptor 3. CI now includes `windows-latest` (build, `go vet` and `skill sync --check`
  are enforced; the domain test suites run advisory-only until each POSIX assumption is guarded).
- `dev upgrade` downloads the newest release for this platform, verifies it against the
  release `SHA256SUMS`, and replaces the running binary with an atomic rename (Windows moves
  the live `.exe` aside and sweeps it on the next run). It defers to Homebrew, Scoop or
  `go install` when one of them owns the file. `dev upgrade --check` reports without changing
  anything.
- An interactive `dev` command prints one dim "newer release available" line at most once a
  day, read only from the day-old release cache — it never blocks on the network, and a
  best-effort background refresh keeps that cache warm. `[update] check = false` in
  `config.toml` (or `DEV_NO_UPDATE_CHECK`) disables it. `dev version --check` gained a
  `dev upgrade` hint and a `scoop update` hint on Windows.
- A Scoop manifest (`packaging/scoop/dev-cli.json`) with `checkver`/`autoupdate`. The release
  workflow refreshes its version and hashes, attaches it to the GitHub release, and pushes it
  to the bucket repo when `SCOOP_BUCKET_TOKEN` is configured.

## [0.2.0] - 2026-08-29

### Added

- Each command family's help carries an ASCII orientation diagram, and every command whose
  workflow has a quick-reference page ends its help with `See also: dev help <topic>`.
  `dev help <command>` now resolves a command name or alias to its topic, so `dev help wt`
  reaches the worktrees page instead of failing.
- `dev help tries` and `dev help skills` quick-reference pages.
- `dev version`, reporting whether this build is a published release or some number of commits
  past one, with an opt-in `--check` that asks GitHub for the newest release and caches the answer
  for a day. `dev doctor` reports the running version too; neither `dev --version` nor `dev doctor`
  touches the network.
- Releases publish `darwin/arm64`, `darwin/amd64`, `linux/amd64` and `linux/arm64` archives with a
  `SHA256SUMS` file, and take their notes from the matching `CHANGELOG.md` section. Previously a
  release published nothing but a GitHub release object with generated notes, so the only way to
  install `dev` was to build it.
- `e` in the dashboard's FLEET view edits `remotes.toml` — the file that view is about — instead of
  dev's own `config.toml`, reparsing it on return and refreshing the fleet only when it is valid.
- Semantic color now covers every human-readable surface. `dev fleet`, `dev skill`, `dev note`,
  `dev artifact`, `dev retire`, `dev park`, `dev resume`, `dev wt` and the `dev sweep` report were
  rendered entirely without it; fleet host state, skill update state and artifact intent state each
  gained a colorizer alongside the existing Git-status and task-state ones.
- `dev journal` and `dev summary` style their Markdown the way `dev help <topic>` does, and command
  help colors command names and flag specs rather than only its section headings.
- `--color` reaches the interactive dashboard, so `dev --color never` (and `NO_COLOR`, and
  `TERM=dumb`) render it without color.
- `dev sweep` reaps a task whose repository directory is gone. Such a record was unreachable by
  every command in the binary, because `done`, `resume`, `park` and `retire` all resolve the
  repository first, and neither existing reap rule covered it. A live runtime session rules the
  suggestion out, and reaping drops only dev's record of intent.
- `dev sweep` reports a task-recorded checkout that Git does not register and that holds nothing but
  agent artifact directories — the residue of a worktree removed while its transcript writer was
  still running. It offers removal only once every file inside is byte-identical to one the
  repository already has; anything else is reported as salvage work and never removed.
- `dev sweep` acts on a cold task whose worktree is still on disk, drift inventory has always
  computed for `dev ls` and the dashboard but sweep never consulted.

### Fixed

- An unknown command is reported instead of discarded. `dev` sets cobra's `SilenceErrors` and
  then also skipped printing any error beginning with `unknown command`, so `dev bogus` wrote
  nothing to either stream and exited 1. The message, cobra's suggestions, and a pointer to
  `--help` are now printed.
- A stray argument to a command family is an error. `dev wt bogus` printed `dev wt` help and
  exited 0, silently dropping the argument, because a family node has no `Run` of its own.
- Argument-count and flag errors print the failing command's usage block instead of one bare line.
- Removed the unparsed YAML frontmatter from the `retirement` help topic; `dev help` has no
  frontmatter handling, so those keys never affected anything.
- `dev retire <path>` reaps the matching task record. Only the by-task form set the task identity,
  so retiring the same checkout by path left the record behind.

## [0.1.11] - 2026-08-29

### Added

- `dev sweep` detects a branch-backed task whose branch Git no longer has — unfinishable by `done`,
  `resume`, or `retire` — and offers to reap the record.
- `dev artifact discard <intent> --yes` records that an armed handoff can never be finalized, so an
  intent whose transcript was never written, or whose HEAD is gone after a rebase, stops blocking
  integration and retirement. It refuses an intent that is still armed.

### Fixed

- Retirement no longer treats the `REBASE_HEAD` file Git leaves behind after a *completed* rebase as
  an in-progress operation, which had permanently blocked `dev retire` and
  `dev sweep --merged-worktrees` for any worktree that had ever been rebased.
- Development builds derive `--version` only from `vMAJOR.MINOR.PATCH` tags, so an unrelated tag in
  the repository can no longer be reported as the CLI version.

## [0.1.10] - 2026-08-29

### Added

- `dev journal` Markdown/JSON reports over calendar-day ranges, with author,
  repository, granularity, truncation and optional Git diff metrics controls.
- `dev summary` machine-wide Markdown/JSON snapshots with adaptive detail,
  attention filtering, recent commits, runtime controls and optional sizes.
- Fully paginated forge inventory with visibility filtering and a versioned,
  cache-first stale-while-revalidate REMOTE experience.

### Fixed

- Activity sampling now attributes sessions running in linked worktrees outside
  the canonical repository path.

## [0.1.9] - 2026-08-29

### Added

- Agent-safe retirement: `dev retire` closes covering runtime sessions and removes a linked worktree
  only from outside it, refusing active agents, mixed-purpose workspaces, dirty state, and in-progress
  Git operations; `dev sweep --merged-worktrees` reports and retires task-tracked and unmanaged
  worktrees whose branches are contained in the base.
- `dev prepare` and `dev artifact` arm and finalize exact agent transcripts after their writer exits,
  and `dev git uncommit/recommit/pull-rebase/amend-all/setup` add guarded Git transactions.
- `dev done --merged --base-ref <ref>` verifies a branch merged outside dev, with `--confirm-squash`
  as the explicit operator attestation for squash merges.

### Fixed

- `dev done` no longer closes the runtime, removes the worktree, or deletes the branch; it records
  MERGED and hands cleanup to `dev retire`, so a process can never delete the worktree it is running in.

## [0.1.8] - 2026-08-29

### Added

- An agent skill manager: `dev skill list/add/update` inventories project and global agent skills
  with their agents, sources, and update state, backed by a SKILLS dashboard view and a `dev doctor`
  check for the skill provider.

## [0.1.7] - 2026-08-29

### Added

- A remote repository fleet: `dev fleet list/status/sync/open` and `dev fleet config` inventory
  repositories, tasks, and live runtime state across SSH-reachable machines running their own `dev`,
  with a FLEET dashboard view, per-host degradation states, and a regenerable `fleet` cache.

## [0.1.6] - 2026-08-29

### Added

- An interactive `dev done` finish wizard that analyzes a dirty checkout against the base and offers
  commit, discard, or cancel, plus a `--dirty auto|fail|commit|discard` policy with `-m/--message`
  and `-y/--yes` for non-interactive callers.
- Semantic color for human-readable output, controlled by a global `--color auto|always|never` and
  disabled automatically for `NO_COLOR`, `TERM=dumb`, non-TTY writers, and `--json`.

## [0.1.5] - 2026-08-28

### Added

- Catalog-backed repository quick notes with durable Markdown storage, rebuildable full-text search, CLI commands, and TUI add/browse workflows.

## [0.1.4] - 2026-08-28

### Added

- Shared ASCII workflow orientation in `dev --help` and `dev help`, plus bilingual Mermaid diagrams for the full change-stream loop and lifecycle states.

## [0.1.3] - 2026-08-28

### Added

- Azure DevOps Services repository inventory, search, cloning, and opt-in forge configuration.

### Fixed

- Corrected command/config documentation to remove the unsupported `dev wt plan --json` example, include Azure forge configuration, and describe shell navigation without implying ordinary command output is evaluated.
- Corrected bundled skill reference coverage and agent-rule numbering.

## [0.1.2] - 2026-08-28

### Added

- A bilingual English/Traditional Chinese MkDocs knowledge site with strict source/site checks and GitHub Pages deployment.

## [0.1.1] - 2026-08-28

### Added

- Agent-ready repository context output, TUI copy actions, and an expanded worktree tree with per-checkout state.
- Exact-pane, fail-closed safeguards for launching parallel agents in newly created Herdr worktrees.
- An interactive, context-aware `dev start` wizard for managed task creation.
- Runtime session activation so opened or reused task surfaces can be focused or attached.

### Fixed

- Focused starts now activate their runtime session after the interactive wizard completes.

## [0.1.0] - 2026-08-28

### Added

- The HOT/WARM/COLD/DONE task lifecycle with direct, branch-only, and managed-worktree checkout modes.
- A TASKS/REPOS/TRY/REMOTE terminal dashboard backed by the same services as the non-interactive CLI.
- Try catalog identity, personal metadata, archive/restore, graduation, and guarded filesystem transitions.
- Rich Git inventory for upstream divergence, conflicts, staged, unstaged, and untracked paths.
- Managed worktree placement and provisioning with explicit ignored-file handling and per-ecosystem dependency strategies.
- Repository discovery, remote-risk inspection, logical size accounting, and durable activity statistics.
- Recursive machine bootstrap with non-destructive symlink indexing and guarded physical move plans.
- Adoption of existing worktrees, runtime sessions, and unmerged branches into the task registry.
- Managed `.gitignore` generation from GitHub templates plus project-local sections.
- XDG-based configurable repository, worktree, task, catalog, cache, and runtime layouts.
- An embedded and installable `dev-cli` agent skill with a generated command reference checked in CI.
- Homebrew, Go, and source installation paths, shell completion, and directory-changing shell integration.

<!-- 0.1.1 through 0.1.11 were tagged retroactively on 2026-08-29 from an already-linear history:
     the features had landed on main one branch at a time but were never released. Each tag sits on
     that feature's last commit, so the CHANGELOG at those commits still lists everything under
     [Unreleased]; this file at HEAD is the accurate record. -->

[Unreleased]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.16...HEAD
[0.2.16]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.15...v0.2.16
[0.2.15]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.14...v0.2.15
[0.2.14]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.13...v0.2.14
[0.2.13]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.12...v0.2.13
[0.2.12]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.11...v0.2.12
[0.2.11]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.10...v0.2.11
[0.2.10]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.9...v0.2.10
[0.2.9]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.8...v0.2.9
[0.2.8]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.7...v0.2.8
[0.2.7]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.6...v0.2.7
[0.2.6]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.5...v0.2.6
[0.2.5]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.4...v0.2.5
[0.2.4]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.3...v0.2.4
[0.2.3]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/daviddwlee84/dev-cli/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.11...v0.2.0
[0.1.11]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.10...v0.1.11
[0.1.10]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/daviddwlee84/dev-cli/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/daviddwlee84/dev-cli/releases/tag/v0.1.0
