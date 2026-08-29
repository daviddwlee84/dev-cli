# Release the backlog, close the lifecycle gaps, and make `dev` explain itself

## Context

The previous plan (land five finished worktrees, backfill docs, retire the worktrees) is
**complete**: `main` is linear, 38 commits past `v0.1.0`, every worktree retired, `make all` /
`make e2e` / `make skill-check` green. This plan covers what that work exposed.

Four problems, all verified against the code and the running binary:

1. **Nothing has been released since `v0.1.0` (2026-08-28).** `main` holds 38 commits and ~20
   `[Unreleased]` feature entries. `go install …@latest` resolves to the newest *tag*, so a
   downstream user today gets a binary predating quick notes, fleet, retirement, journal and
   summary. `release.yml` publishes a GitHub Release object and **nothing else** — no binaries,
   no checksums, no tap bump. `dev doctor` never reports its own version. There is no update
   check anywhere. `AGENTS.md` documents *how* to release but never says *when*, which is
   exactly how 38 commits accumulated unreleased.

2. **Two task records are stuck, one of them permanently.**
   - `agent-skills__feat-exact-agent-history-selectors` — branch gone, worktree deregistered.
     `dev sweep` R6 reaps the *record*, but nothing removes the orphaned directory
     `~/Worktrees/agent-skills/feat-exact-agent-history-selectors/` (artifact-only: `.specstory/`
     with one transcript).
   - `repo__main` ("demo") — `repo_path` is a deleted `mktemp` directory. It is **unreachable by
     every command in the binary**. R6 (`internal/cli/sweep.go:234-236`) is the only rule that
     does not require `state == done`, and it explicitly excludes `EffectiveMode() ==
     ModeDirect`. Nothing anywhere stats `t.RepoPath`. `dev retire` refuses it (`retire.go:104`
     requires DONE). Only `rm ~/.local/share/dev/tasks/repo__main.toml` works.

   There are exactly **two** `Tasks.Delete` call sites in the whole binary: `sweep.go:241` and
   `retire/service.go:89`.

3. **Unknown subcommands fail silently.** `Execute()` (`internal/cli/root.go:175-187`) skips
   printing any error whose message starts with `"unknown command"`, under a comment claiming
   *"cobra already printed usage errors in a readable form"* — but `SilenceErrors: true` on line
   80 means cobra printed nothing. Cobra's "Did you mean…?" suggestions are computed and thrown
   away. Verified: `./dev bogus` → zero bytes on both streams, exit 1. The same guard silences
   every `cobra.NoArgs` site (`./dev status extraneous` → zero bytes, exit 1).

   Group commands are worse: `./dev wt bogus` prints `wt` help, **exits 0**, and silently drops
   the argument — byte-identical to bare `./dev wt`. Cause: cobra `legacyArgs` returns nil for a
   non-root command, `wt` has no `Run`, so `execute()` returns `flag.ErrHelp`, and `ExecuteC`
   maps that to `(cmd, nil)`. Affects `wt`, `repo`, `note`, `fleet`, `skill`, `tries`, `artifact`,
   `cache`, `git`, `config`, `stats`, `tui`.

4. **Colour and help are half-finished.** Two table constructors exist: `app.newTable`
   (`color.go:122`) carries the style; exported `NewTable` (`render.go:20`) hands back a
   zero-value `cliStyle`, so its header is never coloured. `fleet`, `skill`, `note` and
   `artifact list` use the uncoloured one and contain **zero** style calls. `retire`, `park`,
   `resume`, `journal`, `summary` and the `sweep` report body are fully plain. `--color` is not
   plumbed into the TUI at all, so `--color never` does not disable TUI colour. `renderCobraHelp`
   repaints only 7 section headings — never command names, flag names or the `Long` body, which
   is why `dev help fleet` looks rich and `dev fleet --help` looks bare.

   `workflowTLDR` (`root.go:29-43`) is the **only** ASCII diagram in the CLI. No subcommand family
   has one. No `Long` string anywhere mentions that `dev help <topic>` exists, and `dev help wt`
   fails outright because topic lookup does not know command names.

Intended outcome: every past feature is a tagged, installable release; a downstream user can tell
at a glance whether their binary is current; no task record can become unreachable; and an unknown
command, a bare command family, and every command's output all say something useful.

### Decisions taken

- **Retroactive per-feature tags, as `v0.1.x` patch releases** in merge order; this round's work
  then closes as **`v0.2.0`**.
- **All three freshness mechanisms**: a `dev doctor` version row, published release binaries with
  checksums, and an opt-in network update check.
- **All four fix groups** are in scope for this round.

### Explicitly deferred

`dev land` and the generic `[[gate]]` rule engine (items 4a/4b of the previous plan), and an MCP
inventory. MCP has **zero** code anywhere in the repo — `internal/agentskill` shells out to the
external `skills` CLI over a JSON contract and has no vendor abstraction to extend
(`agentskill.go:192-246`), and nothing in the tree has ever parsed a vendor config file. An MCP
view is a new package, not an extension; it needs its own plan.

---

## Phase 1 — Release the backlog

### 1a. Tag boundaries

Eleven patch releases, each at the last commit of a feature's docs tail. History is already
linear, so every one of these is a real ancestor of `main`.

| Tag | Commit | Contents |
|---|---|---|
| `v0.1.1` | `ca37594` | repo context copy + worktree tree, parallel-agent isolation, `dev start` wizard, runtime session attach |
| `v0.1.2` | `4f11927` | bilingual MkDocs knowledge site + Pages deployment |
| `v0.1.3` | `bfefe7a` | Azure DevOps remote repositories |
| `v0.1.4` | `db55e64` | lifecycle flows in help and docs |
| `v0.1.5` | `2b59242` | catalog-backed repository quick notes |
| `v0.1.6` | `fb76149` | interactive `dev done` finish wizard + semantic CLI colours |
| `v0.1.7` | `e540af3` | remote repository fleet |
| `v0.1.8` | `6a6d281` | agent skill manager |
| `v0.1.9` | `828c5b0` | agent-safe retirement + `sweep --merged-worktrees` |
| `v0.1.10` | `2e50b5f` | development journal + machine-wide summary |
| `v0.1.11` | `f2316e6` | version-tag derivation fix, `REBASE_HEAD` fix, `dev artifact discard`, transcript salvage, dead-branch reap |

`v0.1.11` is fixes-plus-two-small-features; patch is the honest level for it.

### 1b. Order of operations (the workflow constrains this)

`release.yml` runs `git merge-base --is-ancestor "$GITHUB_SHA" origin/main`, so **`main` must be
pushed before any tag is pushed**.

1. Restructure `CHANGELOG.md`: split `[Unreleased]` into the eleven dated sections above, add
   eleven comparison links, leave `[Unreleased]` empty for this round's work.
2. Update `AGENTS.md` (1c), `README.md:59` (drop the `@v0.1.0` pin example), and `AGENTS.md:91`'s
   `v0.1.0` baseline sentence. Commit as one `docs:` change at the tip.
3. **`git push origin main`** — confirm with the user first. Watch CI (`ci.yml` runs on push to
   `main`, ubuntu + macos).
4. Push the eleven tags **one at a time**, checking each release run before the next.
5. **Never push `backup/*` or `rescue/*`.** Delete them locally once the release tags verify.
   Ten of the eleven tagged commits predate the `--match 'v[0-9]*'` fix (`3b477c7`), so their
   `make build` uses the old bare `git describe --tags`; a backup tag reachable from those
   commits would break the workflow's `test "$(./dev --version)" = "dev version $VERSION"`
   assertion. Tagging exactly at each commit keeps `describe` returning the exact tag.

**Known skew, accepted:** the CHANGELOG commit lands at the tip, so the tagged commits themselves
still show everything under `[Unreleased]`. GitHub release notes come from `--generate-notes` over
the commit range and are accurate regardless; `CHANGELOG.md` is correct at `HEAD`. The alternative
— rewriting 38 commits to interleave release commits — is technically available (`main` is
unpushed) but not worth the risk. Add one line to `CHANGELOG.md` recording that `v0.1.1`–`v0.1.11`
were tagged retroactively on 2026-08-29.

### 1c. Close the alignment gap in `AGENTS.md`

The user's question — *"這些是否在 CLAUDE.md 中有，以免後續不 align"* — the answer is **partially**.
`AGENTS.md:89-106` documents the release mechanics but has two gaps and one stale claim:

- **No cadence rule.** Add: *a landed feature branch that stands alone is released immediately as
  a patch (`v0.1.x` during 0.x); a landed branch belonging to an open milestone accumulates in
  `[Unreleased]` and the milestone closes with a minor bump.* This is the rule that would have
  prevented the 38-commit backlog, and it matches what this plan actually does.
- **No downstream-freshness contract.** Add: every release publishes binaries and `SHA256SUMS`;
  `dev doctor` reports the running version.
- **Stale bullet.** `AGENTS.md:93` still says `git describe --tags --always --dirty`; the Makefile
  has used `--match 'v[0-9]*'` since `3b477c7`. Correct it.

### 1d. Freshness mechanisms (all three)

- **`dev doctor` version row** — `internal/cli/doctor.go`. Local only, no network. Report
  `versionFromBuild()` and, for a dev build, its distance from the last release tag.
- **Publish binaries** — `.github/workflows/release.yml`. Add a cross-compiled matrix
  (`darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`), `SHA256SUMS`, and
  `gh release create --notes-file` sourced from the CHANGELOG section instead of
  `--generate-notes`. Today the built `./dev` is used only for the `--version` assertion and then
  discarded.
- **`dev version --check`** — new `internal/cli/version.go`. Queries the GitHub releases API,
  caches under `$XDG_CACHE_HOME/dev` with a 24h TTL, degrades silently offline. **Opt-in only** —
  never on bare `dev --version`, never during `PersistentPreRunE`.

Applies only from `v0.2.0` forward; a tag push replays the workflow file *at that commit*, so the
`v0.1.x` releases keep the current no-artifact behaviour. That is expected.

---

## Phase 2 — Four fix branches

One branch per group, each landed with the loop that worked last time: rebase onto `main` →
gates → `merge --ff-only`. All four accumulate in `[Unreleased]` and close together as `v0.2.0`
(Phase 3), per the milestone half of the 1c rule.

### 2a. `feat/unknown-command-help` — make failure and discovery legible

**Root cause fix** (`internal/cli/root.go:175-187`). Switch `Execute()` to `root.ExecuteC()`,
which returns the failing command, and delete the `strings.HasPrefix(msg, "unknown command")`
guard along with its incorrect comment:

```go
cmd, err := root.ExecuteC()
if err != nil {
    style := styleForWriter(os.Stderr, colorModeFromArgs(os.Args[1:]))
    fmt.Fprintln(os.Stderr, style.danger("dev:")+" "+err.Error())
    if cmd != nil && isUsageError(err) {
        fmt.Fprint(os.Stderr, renderCobraHelp(cmd.UsageString(), style))
    }
    return 1
}
```

Keep `SilenceUsage`/`SilenceErrors` — `dev` owns the formatting. Cobra's unknown-command message
already embeds its suggestions; they simply have to be printed.

**Group commands exiting 0.** `Args: cobra.NoArgs` will **not** work here — verified in
`cobra@v1.10.2/command.go:955`, `if !c.Runnable() { return flag.ErrHelp }` runs *before*
`ValidateArgs` at line 968, and `ExecuteC:1152` maps `ErrHelp` to `(cmd, nil)`. The fix is a
shared `RunE` applied to all twelve group commands:

```go
// groupRunE gives a command-family node a real Run so cobra stops treating a
// stray argument as a request for help. No args still prints help; an unknown
// one is an error with suggestions, like every other command.
func groupRunE(cmd *cobra.Command, args []string) error {
    if len(args) == 0 {
        return cmd.Help()
    }
    return unknownSubcommand(cmd, args[0])  // uses cmd.SuggestionsFor (exported)
}
```

**Discovery.** Per-family ASCII TL;DR blocks appended to the `Long` of `wt`, `tries`, `note`,
`fleet`, `skill`, `retire`/`artifact`, and `journal`/`summary`. Extend
`root_help_test.go:47-60`'s `assertWorkflowTLDR` (which rejects any byte ≥ 0x80) to cover every
new diagram. Add a command↔topic map so each `Long` ends with `See also: dev help <topic>`, and so
`help.Get` (`internal/help/help.go:49-75`) resolves command names and aliases — `dev help wt`
fails today. Add the three missing topics `tries.md`, `skills.md`, `config.md`, and drop the dead
YAML frontmatter in `retirement.md:1-4` that `help.load()` never parses.

### 2b. `feat/color-coverage` — finish what the colour branch started

- Delete the exported `NewTable` (`render.go:20`) and route its six callers through
  `app.newTable`: `skill.go:121`, `fleet.go:420,648,746`, `artifact.go:226`, `note.go:410`.
  Verified internal-only, so removing it breaks nothing outside the package.
- Add semantic styling to the commands with zero style calls: `fleet` (all subcommands), `skill`,
  `note`, `retire`, `park`, `resume`, non-interactive `done` and `start`, `wt create/open/rm/
  provision`, the `tries` mutations, and the `sweep` report body (`sweep.go:86-128` — only its
  `--apply` prompt is styled today).
- `journal` and `summary` emit Markdown for piping to an agent. Route them through the same
  `renderMarkdown` that `dev help <topic>` uses (`help.go:40`); `--color auto` already disables on
  a non-TTY, so piped output stays clean.
- Extend `renderCobraHelp` (`color.go:126-142`) past the 7 headings to command names and flag
  names, using the existing ANSI-aware width helpers in `render.go:73-115,133-164`.
- Plumb `--color` into the TUI. `internal/tui/styles.go:15-32` hardcodes 256-colour codes with no
  `lipgloss.SetColorProfile`/`termenv` call anywhere, so `dev --color never` currently has no
  effect on the dashboard.
- Unify `renderMarkdown` (`markdown.go:11-33`) onto the `cliStyle` roles; it hardcodes
  `ansiBold`/`ansiCyan`/`ansiDim` today.

### 2c. `feat/task-lifecycle-gaps` — no record can become unreachable

New rules in `suggestFor` (`internal/cli/sweep.go:145-266`):

- **Repository path gone** → reap the record. Predicate: `t.RepoPath != ""` and `t.RepoPath` does
  not exist. **Safety:** only suggest when the *parent* directory exists, so a repository on an
  unmounted volume (whose mount point is also absent) never triggers it. This is the rule that
  catches `repo__main`, and it makes the `ModeDirect` exclusion in R6 harmless rather than fatal.
- **Cold drift** → `inventory.go:69-71` already computes "cold but the worktree is still on disk";
  `suggestFor` never consults `StateDrift()`, so it is displayed and never actionable.
- **Artifact-only orphan directories** → new `internal/retire/orphan.go`. A path that (a) is
  recorded by a task or sits under `paths.worktree_path`, (b) is not a registered git worktree,
  and (c) contains only artifact directories (`.specstory/`, `.claude/`, `.cursor/`, `.DS_Store`).
  Offer removal **only after proving every transcript is reachable in the base branch**;
  otherwise classify it as an orphan needing salvage and refuse. This is item 4c of the previous
  plan, and there is now a second live instance under `~/Worktrees/agent-skills/`. Surface it in
  `dev sweep` and `dev retire --path`.
- **`retirementTargetForPath`** (`retire.go:130-150`) never sets `TaskID`, so retiring by path
  leaves the record behind. Attach it via `Tasks.FindByWorktree(path)`.

Both live leftovers are the regression tests: a direct-mode task whose repo path is gone, and a
worktree-mode task whose branch is gone and whose path is an artifact-only shell.

### 2d. `feat/tui-fleet-config` — smallest of the four

`e` in the FLEET view opens `remotes.toml` instead of `config.toml`, then revalidates and
refreshes. Reuses the `tea.ExecProcess` editor pattern already used at `model.go:1772` (note
edit) and `model.go:1579-1591` (config edit).

- Extract an editor-process helper from `newFleetConfigEditCmd` (`fleet.go:868-889`) and inject it
  as `Actions.EditFleetConfig`, mirroring `Actions.EditConfig` (`model.go:115`, wired at
  `cli/tui.go:529-532`).
- On return, re-run `fleet.LoadConfig` — it treats unknown TOML fields as a hard error
  (`fleet/config.go:83-85`), so surface that as `m.err` rather than a silent no-op — then
  `reloadFleet()` (`model.go:498-506`).
- Re-run `fleet.CheckPrivateMode` (`fleet/config.go:187-206`): `remotes.toml` is 0600 and may hold
  a plaintext password.
- Update the FLEET footer hint (`view.go:1094-1097`) and the help overlay line
  (`overlay.go:299`), which currently advertise only `enter`.

---

## Phase 3 — Close the milestone as `v0.2.0`

Move this round's `[Unreleased]` entries into `## [0.2.0] - <date>`, update comparison links,
update the `AGENTS.md` baseline sentence, push, tag `v0.2.0`, and confirm the release job uploads
binaries and `SHA256SUMS`. Verify `dev version --check` against the freshly published release.

---

## Synchronization required by `AGENTS.md`

Every branch in Phase 2 touches the Cobra tree, so each one runs `make skill-sync` then
`make skill-check`, and hand-updates:

- `internal/skill/dev-cli/SKILL.md` — Everyday commands, numbered Rules for agents, Reference files.
- `internal/help/topics/` — three new topics (2a) plus edits to `fleet.md`, `retirement.md`, `worktrees.md`.
- `docs/reference/commands-config.md`, `compatibility.md`, `sources-freshness.md` — **and every
  `.zh-TW.md` sibling**, with byte-identical `authority`/`status`/`verified_on` frontmatter.
  `verified_on` is compared against **UTC**, not local time.
- `README.md`, `CHANGELOG.md`, and `docs/llms*.txt` via
  `uv run python scripts/check-docs.py --source --generate-llms`.

---

## Verification

Per branch, before `merge --ff-only`:

```bash
files="$(gofmt -l .)" && test -z "$files"
go vet ./... && go test -race ./...
make skill-sync && make skill-check
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
```

Behavioural checks that must change from today's output:

```bash
./dev bogus;            echo "exit=$?"   # was: 0 bytes, exit 1 -> error + suggestions + usage
./dev statu;            echo "exit=$?"   # was: silent -> "Did you mean this? status"
./dev wt bogus;         echo "exit=$?"   # was: wt help, exit 0 -> error, exit 1
./dev wt;               echo "exit=$?"   # must still print help, exit 0
./dev status extraneous                  # was: silent -> arity error + usage
./dev fleet list --color always | cat -v # was: no ANSI -> coloured header and cells
./dev --color never                      # TUI must render without colour
./dev help wt                            # was: 'no help topic "wt"' -> worktrees topic
./dev wt --help                          # must show a TL;DR block + "See also: dev help worktrees"
./dev doctor                             # must include a version row
./dev version --check                    # opt-in; silent no-op offline
./dev sweep                              # must offer to reap the "demo" record
```

End to end: `make all && make e2e`, then `dev sweep` reports nothing outstanding, both leftover
task records are gone, and `~/Worktrees/agent-skills/feat-exact-agent-history-selectors/` is
removed only after its transcript is confirmed present in the `agent-skills` base branch.

Release: each `v0.1.x` tag produces a GitHub release; `v0.2.0` additionally carries four platform
binaries and `SHA256SUMS`; `go install …@latest` resolves to `v0.2.0`.

## Risks

- **Historical commits have never run CI.** `main` was never pushed, so `v0.1.1`–`v0.1.11` will
  hit ubuntu + macos for the first time. A Linux-specific failure in an old commit blocks that
  release (the tag survives; no release object is created). Decide at that point between skipping
  the number and fixing forward — do not weaken CI to make an old tag pass.
- **`TestPosixShellWrapperFallsBackFromStaleTMPDIR/zsh` is load-flaky** (it spawns a real `zsh`)
  and CI runs `-race` on two OSes eleven times. Expect at least one spurious red; re-run rather
  than chase it.
- **Eleven tag pushes are eleven full CI runs.** Push serially and verify each.
- **Deleting exported `NewTable`** is a breaking change for any external importer. Verified no
  caller exists outside `internal/cli`, and `internal/` is not importable from outside the module.
- **The repo-path-gone reap deletes user data** if the safety predicate is wrong. The
  parent-directory guard plus `sweep --apply`'s per-item confirmation are the two protections;
  neither may be skipped.


---

## Outcome (2026-08-29)

Executed in full. `v0.1.1` through `v0.1.11` and `v0.2.0` are published; `main` is linear and
in sync with `origin/main`; all four fix branches plus the freshness work landed.

Three deviations from the plan as written, each for a reason found during execution:

1. **The repo-gone reap uses a live-session guard, not a parent-directory guard.** The record the
   rule exists for lives under a deleted `mktemp` directory whose parent is also gone, so the
   planned guard would have missed the one case that motivated it. A live runtime session is proof
   the directory is there, and reaping drops only dev's record of intent — the branch, commits and
   remote are untouched — so per-item confirmation carries the rest.
2. **A single automatic CI retry was added to the tag sequence.** `v0.1.5` failed on Linux with
   `TestPosixShellWrapperFallsBackFromStaleTMPDIR/bash` and passed unchanged on re-run, confirming
   the flake the risk section predicted. Six tags remained, so retrying once and stopping on a
   second failure replaced eleven possible interruptions. No other tag needed it.
3. **The orphan sweep tests were rewritten after failing on Linux.** They built the orphan through
   `dev wt rm`, which clears the recorded worktree path, so the state under test never existed;
   they passed on macOS only because the harness puts `HOME` under a temp directory that Git
   reports through `/private`, defeating dev's own path match. They now remove the worktree with
   raw Git, the way the real incident happens, and assert that precondition explicitly.

Left deliberately undone:

- `~/Worktrees/agent-skills/feat-exact-agent-history-selectors/` still holds a 1.6 MB transcript
  that is not in the `agent-skills` repository. `dev sweep` correctly refuses to remove it and its
  task record is the only thing still pointing at it. Salvaging into another repository is the
  owner's call.
- `backup/*` and `rescue/*` tags remain local and were never pushed. `backup/main-pre-integration`
  sits on the same commit as `v0.1.5`, which would have broken that release's version assertion.
- `dev land`, the generic `[[gate]]` rule engine, and an MCP inventory remain deferred.
