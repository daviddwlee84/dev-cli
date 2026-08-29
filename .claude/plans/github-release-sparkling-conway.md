# Windows release builds + CLI self-update

## Context

Two gaps prompted this work:

1. **No Windows binaries.** `.github/workflows/release.yml` builds only
   `darwin/{arm64,amd64}` and `linux/{amd64,arm64}`. A Windows user has no
   `go install`-free path to `dev`. The binary itself already cross-compiles
   cleanly for `windows/amd64` and `windows/arm64` (verified with Go 1.26.4),
   and the filesystem/lock packages already carry `_windows.go` variants, so the
   groundwork is mostly done — what is missing is a release target, one
   test-file build tag, and a few POSIX-only assumptions in `internal/cli`.

2. **`dev` cannot update itself.** `dev version --check` (internal/cli/version.go)
   is the whole story: it hits the GitHub releases API, compares the tag to the
   build, and prints `brew upgrade` / `go install @latest` hints. There is no
   `dev upgrade`, no proactive nudge on ordinary commands, and no Scoop manifest
   even though README documents a Homebrew tap.

Intended outcome: releases publish Windows archives; `dev upgrade` replaces the
running binary in place (deferring to a package manager when one owns the
install); a Scoop manifest exists for Windows users; ordinary commands surface a
one-line "newer release available" hint from cache.

## Part 1 — Windows release + compatibility

### Release workflow (`.github/workflows/release.yml`)

- Extend the build loop with `windows/amd64` and `windows/arm64`.
- For Windows targets, build `dev.exe`, package as `dev-cli_${VERSION}_windows_${arch}.zip`
  (`zip` is present on `ubuntu-latest`), and keep non-Windows on `.tar.gz`.
- Feed both `*.tar.gz` and `*.zip` into `sha256sum *.tar.gz *.zip > SHA256SUMS`
  and the `gh release upload` list.

### CI workflow (`.github/workflows/ci.yml`)

- Add `windows-latest` to the `matrix.os` list.
- Guard the e2e step (`scripts/e2e.sh` is bash + POSIX) with
  `if: runner.os != 'Windows'`. Keep `gofmt`/`go vet`/`go test -race`/`go build`
  and `skill sync --check` on all three.
- The only file that fails to compile on Windows today is
  `internal/cli/shell_internal_test.go` (`golang.org/x/sys/unix.Dup`). Split the
  fd-3 side-channel test into `internal/cli/shell_internal_unix_test.go` with
  `//go:build unix`; leave any OS-agnostic assertions in the original file.
  (`go vet` for `GOOS=windows` is already clean for every non-`internal/cli`
  package.)

### `internal/cli` POSIX assumptions

- **`fleet.go:104` `syscall.Exec`** — returns `EWINDOWS` on Windows. Extract a
  `replaceProcessWithShell(shell string) error` helper split as
  `fleet_exec_unix.go` (current `syscall.Exec`) and `fleet_exec_windows.go`
  (`exec.Command(shell)` with inherited stdio via `cmd.Run()`, then
  `os.Exit(code)`).
- **`shell.go` — PowerShell wrapper.** Add a `powershellInit` template and
  accept `powershell`/`pwsh` in `ValidArgs` and the `switch`. Because Windows
  shells do not inherit fd 3 the way `posixInit` relies on, the PowerShell
  wrapper sets `DEV_SHELL_CD_FILE` to a temp path it creates, runs the real
  binary, then reads/`cd`s/deletes it.
- **`app.go` `cdDirective`** — before the `DEV_SHELL_CD_FD` branch, honor a
  `DEV_SHELL_CD_FILE` env var: if set, `os.WriteFile(path, append([]byte(dir), 0), 0o600)`
  and return. Keeps the fd path unchanged for POSIX shells.
- **`doctor.go`** — when `runtime.GOOS == "windows"`, add a `checkWarn` line
  stating tmux/Zellij/Herdr multiplexers are unavailable and `dev` runs in the
  no-multiplexer backend; the shell-integration check should point at
  `dev shell-init powershell` on Windows.

### Docs / skill sync

- `make skill-sync` after the `shell-init` arg change; inspect
  `internal/skill/dev-cli/references/commands.md`; `make skill-check`.
- README `## Install`: add a Scoop subsection and a short "Windows support"
  note (core commands work; multiplexer + POSIX shell integration limited).
- `docs/reference/compatibility.md` + `.zh-TW.md`: Windows support matrix row.
- `internal/skill/dev-cli/references/runtime-herdr.md` and any skill page that
  asserts POSIX-only: note the Windows no-multiplexer fallback.
- `CHANGELOG.md` `[Unreleased]`.

## Part 2 — `dev upgrade` self-update

### New command `internal/cli/upgrade.go` (`newUpgradeCmd`, registered in `root.go`)

Flags: `--check` (report only), `--force` (skip the "already latest" short-circuit),
`--yes` (no prompt).

Flow:
1. Resolve latest tag via the existing `latestRelease(ctxOf(), true)` in
   `version.go`. Compare with `buildDescription(versionFromBuild())`. Exit early
   if current (unless `--force`).
2. **Install-method detection** — `detectInstallMethod()`:
   - `self, _ := os.Executable()`; `EvalSymlinks`.
   - Path under a Homebrew prefix (`/Cellar/dev-cli/`, `/opt/homebrew/`,
     `$(brew --prefix)`) → `methodHomebrew`.
   - Path contains `scoop{/,\}apps{/,\}dev-cli` → `methodScoop`.
   - Path under `go env GOPATH`/`GOBIN` → `methodGo`.
   - else → `methodStandalone`.
   For every method except `methodStandalone`, print the matching command
   (`brew upgrade dev-cli`, `scoop update dev-cli`,
   `go install github.com/daviddwlee84/dev-cli/cmd/dev@latest`) and stop —
   do not self-replace a package-manager-owned file.
3. **Download + verify** (standalone only):
   - Asset name from `runtime.GOOS`/`GOARCH`:
     `dev-cli_<tag>_<os>_<arch>.(tar.gz|zip)`.
   - GET `https://github.com/daviddwlee84/dev-cli/releases/download/<tag>/<asset>`
     and `.../SHA256SUMS` (5s-ish timeout `http.Client`, reuse the pattern in
     `version.go`).
   - `sha256` the archive, match the line in `SHA256SUMS`; abort on mismatch.
   - Extract `dev` / `dev.exe` from the archive to a temp file **in the same
     directory as the target** (same filesystem → atomic rename), `chmod 0755`.
4. **Atomic replace** — `replaceBinary(newPath, targetPath)`:
   - Unix: `os.Rename(newPath, targetPath)`.
   - Windows (`replace_binary_windows.go`): rename the running exe to
     `targetPath + ".old"`, `os.Rename(newPath, targetPath)`, best-effort
     `os.Remove` the `.old` (and a startup sweep of a stale `dev.exe.old` next to
     `os.Executable()` early in `Execute()`).
   Keep this hand-rolled — no new dependency; `go.mod` is deliberately lean.
5. Print old → new version and, on Windows, note the old file is cleaned up on
   next run.

`--check` output should also feed the same "latest" line that `version --check`
prints (share a small `renderUpgradeHint` helper).

### Proactive nudge (cache-only, no network in the hot path)

- In `root.go` `PersistentPreRunE`, after `app.Load()`, call
  `app.maybeNoteNewerRelease(cmd)`:
  - skip when not `interactive()`, when the command is `version`/`upgrade`/
    `completion`/`__complete`, or when `app.Cfg.UpdateCheck` is false.
  - `readReleaseCheck()` (cache only — never fetch here). If the cached tag is
    newer than `buildDescription(versionFromBuild())`, print one dim line to
    `app.Err`: `dev: v0.3.0 available — run 'dev upgrade'`.
  - rate-limit to once/day via a `nudged_at` field added to the
    `release-check.json` struct (or a sibling file).
- After a successful command in `Execute()` (or a `PersistentPostRunE`), if the
  cache is older than `releaseCheckTTL` and `UpdateCheck` is on, refresh it in a
  detached goroutine with a short timeout so the *next* invocation has fresh
  data. Must not block or delay process exit — fire-and-forget with a hard
  context timeout; acceptable to skip if the process exits first.

### Config

- `internal/config`: add `[update] check = true` (default true), surfaced as
  `Config.UpdateCheck`. Document in config reference (both locales) and
  `dev config` help. CI can set `check = false` or `DEV_NO_UPDATE_CHECK`.

## Part 3 — Scoop manifest

- Add `packaging/scoop/dev-cli.json`:
  - `version`, `architecture.64bit.url`/`hash`, `architecture.arm64.url`/`hash`
    pointing at the release `.zip`s,
  - `bin: "dev.exe"`,
  - `checkver: "github"`, `autoupdate` block with the release URL template and
    `hash.url` = `SHA256SUMS`.
- `release.yml`: after the release is created, rewrite `version` + `hash` fields
  in `packaging/scoop/dev-cli.json` from the built `SHA256SUMS`. If a
  `SCOOP_BUCKET_TOKEN` secret is present, push the updated manifest to
  `daviddwlee84/scoop-bucket`; otherwise just attach it to the release and
  commit it back to `main` (or skip the push and log). Keep the step
  non-fatal — a manifest problem must not fail an otherwise-good release.
- README + `docs/reference/compatibility.*`: `scoop bucket add ...` +
  `scoop install dev-cli` instructions.
- Homebrew formula auto-bump is **out of scope** here (the tap has no
  automation today); note it as a follow-up.

## Critical files

| File | Change |
|---|---|
| `.github/workflows/release.yml` | Windows targets, `.zip` packaging, SHA256SUMS, Scoop manifest step |
| `.github/workflows/ci.yml` | `windows-latest` matrix, skip e2e on Windows |
| `internal/cli/shell_internal_test.go` → `shell_internal_unix_test.go` | `//go:build unix` split |
| `internal/cli/fleet.go` + `fleet_exec_{unix,windows}.go` (new) | portable process replacement |
| `internal/cli/shell.go` | `powershellInit` template + `powershell`/`pwsh` args |
| `internal/cli/app.go` | `DEV_SHELL_CD_FILE` path in `cdDirective` |
| `internal/cli/doctor.go` | Windows multiplexer/shell notes |
| `internal/cli/upgrade.go` + `replace_binary_{unix,windows}.go` (new) | `dev upgrade` |
| `internal/cli/version.go` | share `latestRelease`/hint helpers; add `scoop update` hint on Windows |
| `internal/cli/root.go` | register `newUpgradeCmd`; `maybeNoteNewerRelease` in `PersistentPreRunE`; stale `.old` sweep |
| `internal/config/config.go` (+ paths) | `[update] check` / `Config.UpdateCheck` |
| `packaging/scoop/dev-cli.json` (new) | Scoop manifest |
| `internal/skill/dev-cli/references/commands.md` | regenerated via `make skill-sync` |
| `README.md`, `docs/reference/compatibility.{md,zh-TW.md}`, config reference (both locales), `CHANGELOG.md` | docs |

## Reuse

- `latestRelease`, `readReleaseCheck`/`writeReleaseCheck`, `releaseCheck`,
  `buildDescription`, `versionSummary`, `releaseCheckPath` — all in
  `internal/cli/version.go`, already exactly what `dev upgrade` and the nudge
  need. Do not re-implement release polling.
- `config.CacheHome()` for the cache file; `app.outStyle()` / `style.dim` /
  `style.warning` for hint rendering (matches `version.go`).
- `interactive()` (root.go) for the terminal check.
- Existing `_windows.go` / `_other.go` build-tag pattern in
  `internal/experiment`, `internal/note`, `internal/lockx`.

## Implementation notes (done 2026-08-29)

- All three parts landed. `go build`, `go vet` and `go test ./...` pass on darwin;
  `GOOS=windows` build + vet pass for amd64 and arm64. `make skill-check`, `make e2e`
  and the strict docs checks pass.
- Deviation 1: the whole POSIX shell-wrapper test set moved to
  `shell_internal_unix_test.go` (`//go:build unix`), not just the `unix.Dup` test —
  those tests are meaningless on Windows and running them under git-bash on the
  runner was an avoidable risk.
- Deviation 2: the Windows CI test run is `continue-on-error: true` (advisory).
  Build, `go vet` and `skill sync --check` are hard requirements on Windows; the
  domain suites still assume POSIX in places and are triaged from the first run.
- `writeReleaseCheck` is now an atomic temp-file rename, since the background
  refresh goroutine can be killed mid-write.

## Verification

1. `PATH` to Go 1.26.4, then:
   `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/dev.exe ./cmd/dev`
   and `GOARCH=arm64` — both must succeed (they do today).
2. `GOOS=windows GOARCH=amd64 go vet ./...` — clean after the test-file split.
3. `go test -race ./...` on macOS/Linux unchanged; on a Windows runner
   `go build` + `go test ./...` (minus the unix-tagged files) pass.
4. `make skill-sync && make skill-check` — command reference matches the tree
   after the `shell-init` arg + `upgrade` command.
5. `make build && ./dev upgrade --check` on a dev build → reports the newer
   published release and the correct per-install-method command.
6. Standalone-install path: `cp ./dev /tmp/bin/dev`, `PATH=/tmp/bin`,
   `dev upgrade --yes` → downloads the latest release archive, verifies against
   `SHA256SUMS`, replaces `/tmp/bin/dev`, `dev version` shows the new tag.
7. Homebrew/Scoop/go path: point `os.Executable()` at a fake Cellar/scoop path
   (test seam) → `dev upgrade` prints the package-manager command and makes no
   network write.
8. Nudge: seed `release-check.json` with a higher tag, run `dev ls` in a TTY →
   one dim "vX available — run 'dev upgrade'" line on stderr; `dev --json`-style
   and non-TTY output unaffected; second run within a day stays quiet.
9. Tag a test release on a fork (or dry-run the workflow) → `.zip` artifacts +
   `SHA256SUMS` contain the Windows entries; `scoop install` from the generated
   manifest yields a working `dev.exe`.
10. Docs: `uv run mkdocs build --strict` + `scripts/check-docs.py` after the
    compatibility/config page edits; regenerate `docs/llms*.txt` if they drift.
