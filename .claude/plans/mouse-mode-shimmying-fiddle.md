# Context

The dashboard is keyboard-first today, so a user cannot click a visible tab or row and there is no common row-action surface. SKILLS and MCP correctly default to the exact startup context, but there is no session-level way to inspect every repository; MCP also lets the `SERVER` column consume all extra terminal width, which pushes `TRANSPORT`, `STATE`, and `SOURCE` far away on wide screens. Finally, capability rows expose configuration paths but cannot open or copy those files.

Add mouse input as an enhancement without weakening keyboard behavior, keep startup-context capability discovery as the default, offer a shared context/all toggle, compact MCP’s table, and give SKILLS/MCP explicit local-file open/copy actions. Per the requested local-tool behavior, raw configuration copying is supported as an explicit action; it never uploads or contacts a server, but the UI/docs will note that the entire file may contain credentials and enters the system clipboard.

## 1. Add shared mouse geometry and dispatch

- Start the dashboard with `tea.WithMouseCellMotion()` beside `tea.WithAltScreen()` in `internal/cli/tui.go`. This supports clicks, releases, drag, and wheel without enabling noisy all-motion tracking; Bubble Tea restores terminal mouse state on exit.
- Add focused mouse logic in a new `internal/tui/mouse.go` and route `tea.MouseMsg` from `Model.Update` with the same precedence as keyboard input:
  - active note/input/help/form/confirmation/stat/copy modes do not pass clicks through to the list;
  - the common action menu handles its own clicks and wheel;
  - an active clone still permits tab switching, row selection, and wheel navigation, but not context actions.
- Define the interaction contract:
  - unmodified left-button press on a rendered tab switches directly to it and calls existing `afterViewSwitch` so lazy loading and cursor clamping remain intact;
  - unmodified left-button press on a visible data row selects only;
  - unmodified right-button press selects the row and opens its action menu;
  - wheel up/down over the list body moves three rows through existing `setAt`; horizontal wheel, motion, releases, extra buttons, and modified clicks are ignored;
  - do not infer double-clicks or open rows on click—Enter/`o` and menu actions remain the explicit activation paths.
- Refactor `internal/tui/view.go:renderHeader` into one pure header-layout helper returning both the rendered line and visible tab cell spans. Compute spans with `lipgloss.Width` so full, summary-free, compact, and current-tab-only layouts cannot drift from hit testing.
- Derive row hitboxes from existing `listPreambleLines`, `listHeight`, and `window` results. Map only rendered data rows (`2 + listPreambleLines()` onward) back to `window.from + offset`; reject the header, loading banner, scroll note, detail, and footer. Do not persist coordinates from a prior `View`, so resize/filter/load changes recompute geometry from current model state.

## 2. Generalize the row action menu for every tab

- Evolve the Try-only menu in `internal/tui/overlay.go` into a generic full-screen row action overlay while retaining the existing form/confirmation overlays.
- Represent menu entries with a small `listAction` enum and fixed-size value arrays in `overlayState`; do not store closures, maps, or shared mutable slices in the value-copied Bubble Tea model.
- Pin the menu to a `selectionToken` containing the view and a stable row identity (task ID; canonical repo plus checkout path; host plus repo path; Try catalog ID; existing remote identity; Skill scope/checkout/path/name; MCP agent/scope/config path/project/plugin/name). Re-resolve before execution and fail closed if a refresh removed or changed the target instead of acting on the current cursor index.
- Extract menu-visible behavior from `updateList` into a shared `runListAction(actionID)` dispatcher. Keyboard bindings and right-click choices call the same action implementation; mouse code must not invoke domain callbacks directly or synthesize key messages.
- Build each menu from actions that are actually legal for that row, omitting unavailable choices rather than rendering misleading disabled entries. Reuse the existing operations across all tabs: open, notes/task lifecycle, worktree expansion/metadata/start/copy/stats, fleet open, Try lifecycle, remote clone/open, Skill add/check/update/config actions, and MCP config actions. Destructive or multi-step actions continue through their existing prompt/form/confirmation flow.
- In the menu, left-clicking an option selects and executes it, wheel moves the menu cursor by one, clicking outside closes it, and all keyboard `j/k`, Enter, and Escape behavior remains available.

## 3. Add a shared SKILLS/MCP context/all scope

- Add an exported value enum in `internal/tui` (startup context vs all repositories) and a `capabilityScope` field on `Model`, defaulting to startup context.
- Change `Actions.ReloadSkills`, `ReloadSkillsWithRepos`, `ReloadMCP`, and `ReloadMCPWithRepos` to receive that scope. Capture it in `reloadSkills`, `reloadUpdatedSkill`, and `reloadMCP` so every producer is bound to the generation and scope that started it.
- Bind uppercase `A` in both SKILLS and MCP to toggle the shared session-only scope; lowercase `a` remains Skill installation. Show `A scope:context` or `A scope:all` in both footers/help and reserve `A` in `internal/config/config.go` so an external tool cannot shadow it.
- On toggle, cancel and generation-bump both capability loads, clear old-scope rows/errors/warnings/check state and reset both cursors. Immediately load the current capability view; leave the other invalidated so it preserves existing lazy-load behavior and loads under the new scope when visited. If REPOS is still loading, use the existing waiting path; if it failed terminally, surface that dependency error. Late results from the old scope must be rejected by existing generation checks.
- Extend `internal/cli/tui.go:tuiCapabilityTargets` with scope policy:
  - startup context keeps current behavior—inside Git, only the exact startup checkout; outside Git, accepted non-Try REPOS targets plus the ordinary startup directory;
  - all repositories uses existing `agenttarget.WithCurrent(agenttarget.FromRepositories(repositories), current)`, still excluding Try rows and allowing each scanner to deduplicate global sources.
- Preserve the selected scope across manual refresh, configuration refresh, REPOS refresh, Skill update/check, and tab changes. Do not implement this as a post-scan row filter: target selection controls which project-local declarations are read.

## 4. Open and copy capability configuration files

- Add injected TUI action boundaries in `internal/tui/model.go` rather than performing filesystem/process work in rendering or mouse code:
  - `OpenFile(path)` returns an editor process for `tea.ExecProcess`;
  - `ReadFile(ctx, path)` returns bounded local regular-file contents for the existing clipboard callback.
- Wire them in `internal/cli/tui.go` using the existing `internal/cli/edit.go:editorProcess` and `internal/safefile.ReadRegular`. Validate that the selected path exists and resolves to a regular file; reject directories/devices/FIFOs and cap raw reads at 1 MiB. These actions perform no network access.
- Resolve one exact local target from the selected row:
  - an installed Skill uses `<Skill.Path>/SKILL.md`;
  - a missing/lock-only Skill uses `Skill.Lock.File` when present;
  - MCP uses `Declaration.ConfigPath` and never executes `Declaration.Command` or opens `Declaration.Endpoint`.
- Make `e` open that selected capability file in `$VISUAL`/`$EDITOR`. On successful return, reload only the originating SKILLS or MCP view under the current capability scope; do not run the dev `config.toml` reload path.
- Generalize `modeCopy`/copy helpers without changing existing REPOS chords:
  - SKILLS: copy selected file path, deterministic sanitized row summary, sanitized source URL, or raw selected file contents;
  - MCP: copy `ConfigPath`, deterministic sanitized declaration summary, or raw `ConfigPath` contents;
  - expose the same explicit choices in right-click menus.
- Keep safe summaries limited to normalized inventory fields. Raw copy is separately and clearly labeled “copy raw file”; for MCP it copies the entire source file, which may include other server declarations, tokens, headers, env/OAuth material, or helper commands. Report clipboard/read failures with capability-specific messages rather than the current REPOS-only `dev repo context` fallback.

## 5. Compact MCP’s responsive table

- Replace the unbounded `serverW = max(minimum, width-budget)` logic in `internal/tui/view.go:renderMCP` with a helper based on the widest server name in the full accepted MCP snapshot (not the filtered rows):
  - preferred width is the display width of `SERVER`/the widest name, clamped between the layout minimum and 32 cells;
  - actual width is further bounded by the space available at each existing wide/medium/narrow breakpoint;
  - retain left alignment and `fitCell` truncation.
- This keeps later columns adjacent to normal two-cell separators on very wide terminals, permits useful server names to expand, and avoids columns shifting when a filter is applied.

## 6. Tests

- Extend `internal/tui/tui_test.go` helpers with synthetic `tea.MouseMsg` input and cover:
  - row selection without activation; ignored releases/motion/modifiers/double presses;
  - full/compact/current-only tab hitboxes and lazy loading;
  - cached/loading preambles, viewport offsets, resize/filter changes, empty lists, and non-row regions;
  - three-row wheel movement and clamping;
  - right-click selection, all-tab menu availability, click/keyboard action equivalence, modal isolation, clone restrictions, and stale selection tokens.
- Add capability tests for default scope, `A` all/back toggles, callback scope values, clearing prior rows, stale generation rejection, REPOS wait/failure behavior, lazy loading of the other capability tab, scope preservation after refresh/update, and reserved-key collision.
- Expand `internal/cli/tui_capability_context_test.go` for context and all target sets, including exact linked/unlisted startup worktrees and outside-Git behavior.
- Test editor/raw-copy targets, installed versus lock-only Skills, MCP exact `ConfigPath`, capability-only reload after editor exit, the 1 MiB/regular-file guards, clipboard errors, sanitized summaries, and raw fixture contents (including proving no MCP command/endpoint is executed or contacted).
- Add MCP width regressions around breakpoints 93/94 and 111/112 plus a 200-cell terminal: lines fit, server width expands then caps at 32, later columns remain bounded/adjacent, and filtering does not shift them.
- Update `internal/tui/pty_unix_test.go` to exercise startup/quit with mouse cell tracking and verify terminal mouse mode is restored while existing keyboard responsiveness remains intact.

## 7. User-facing documentation

- Add the feature to `[Unreleased]` in `CHANGELOG.md`, including the new reserved `A` key and the raw-clipboard caveat.
- Update dashboard interaction, scope, and capability-file semantics in `README.md`, `internal/help/topics/{tui,skills,mcp}.md`, paired `docs/guides/tui-repos-bootstrap.md` and `.zh-TW.md`, `internal/skill/dev-cli/SKILL.md`, and `internal/skill/dev-cli/references/agent-capabilities.md`.
- Correct the currently stale help claims about capability startup scope while editing those pages. Document click-on-press behavior, no double-click activation, wheel step, right-click menus, terminal text-selection modifier caveat, context/all semantics, editor behavior, and the distinction between sanitized summary and raw local-file clipboard content.
- Cobra command syntax does not change, so do not regenerate the generated command reference. Regenerate `docs/llms.txt` and `docs/llms-full.txt` after authored docs change.

## 8. Verification

1. Format changed Go files with `gofmt` and run focused packages: `go test ./internal/tui ./internal/cli ./internal/config ./internal/safefile`.
2. Run repository gates: `go test -race ./...`, `go vet ./...`, the preserving format check, and `make build`.
3. Run `make skill-check`.
4. Regenerate/check docs: `uv sync --frozen --extra docs`, `uv run python scripts/check-docs.py --source --generate-llms`, `uv run python scripts/check-docs.py --source`, `uv run mkdocs build --strict`, and `uv run python scripts/check-docs.py --site site`.
5. Manually run the built dashboard from an exact linked project checkout and from outside Git, in narrow and very wide terminals: click every visible tab/row, scroll, exercise all-tab right-click menus, toggle `A` both directions in SKILLS/MCP, open files in the editor, copy path/summary/raw content, confirm MCP columns stay compact, and verify keyboard-only flows and terminal text selection still work after exit.