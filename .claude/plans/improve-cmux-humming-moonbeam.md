# Make blocked LAN discovery visible instead of "ready, 0 candidates"

## Context

On macOS 26, `dev ssh discover --source lan` and the `dev ssh` wizard reported
`lan discovery: ready` with zero candidates on a /24 that has 4 hosts with TCP/22
open. Verified cause: macOS Local Network privacy denied the Go binary's dials
(`connect: no route to host` / EHOSTUNREACH) while Apple's `nc` connected.
From Herdr (a launchd-started CLI, not an `.app`) it never works; from cmux the
first wizard run raced the permission prompt.

Two product defects turned this into a silent failure:

1. `internal/sshdiscovery/lan.go:359-361` `probeLANEndpoint` returns `nil` for
   every dial error, so a fully blocked scan is `Complete`/`StatusReady` with no
   evidence of why nothing was found.
2. `internal/cli/ssh_machines.go:283-289` `runSSHDiscovery` reuses any fresh
   same-scope cached LAN report, including an empty one, and skips the
   "Discover LAN" line. The second wizard run therefore failed instantly with
   the generic `no candidates; check discovery source status`
   (`internal/cli/ssh_onboard.go:537`). The TUI dialog path
   (`internal/cli/ssh_tui_discovery.go:57`) always rescans, which is why the TUI
   later succeeded.

Goal: an empty LAN scan explains itself (probe outcome counts + a finite
warning code + macOS Local Network hint), and an empty cached report never
masks a retry. Status semantics and existing JSON fields stay unchanged.

## Changes

### 1. `internal/sshdiscovery` — classify probe outcomes (domain)

- `types.go`: add additive, optional fields to `Report`:
  - `Probes *ProbeSummary \`json:"probes,omitempty"\`` with
    `Attempted, Open, Refused, Timeout, Unreachable, Other int` (snake_case tags).
  - `Warnings []string \`json:"warnings,omitempty"\``; one finite code
    `WarningNoReachableEndpoints = "no_reachable_endpoints"`.
  - Keep `Status` values untouched: `validateReport` (`cache.go:224-235`)
    and old binaries reject unknown statuses.
- `lan.go`:
  - `lanResult` gains an `outcome` class; `probeLANEndpoint` returns
    `(*Candidate, outcome)`. Classify with a small unexported
    `classifyDialError(err)`: timeout (`context.DeadlineExceeded` or
    `net.Error.Timeout()`), refused (`syscall.ECONNREFUSED`, plus Windows errno
    10061 like `internal/sshhost/diagnose.go:405-424`), unreachable
    (`syscall.EHOSTUNREACH`, `syscall.ENETUNREACH`), else other. Don't export or
    move the sshhost helper; it has no unreachable case and different codes.
  - Aggregate counts in the existing result loop (`lan.go:327-341`); set
    `report.Probes` once the plan is prepared.
  - After completion: if `Complete && len(Candidates)==0 && Unreachable > 0`,
    append `WarningNoReachableEndpoints`. Status stays `ready`.
- New exported helper (same package, e.g. `guidance.go`):
  `LANGuidance(report Report, goos string) string`. It returns "" when there is no
  warning; otherwise a one-line summary such as
  `no LAN endpoint accepted a connection (254 probed: 0 open, 1 refused, 240 unreachable, 13 timed out)`.
  On `goos == "darwin"` it appends `; macOS may be blocking Local Network access for this terminal app — allow it in System Settings > Privacy & Security > Local Network, then retry`.
  Both CLI and TUI use it so wording lives in one place.
- `cache.go` `validateReport`: reject negative probe counts and unknown warning
  codes (the cache is untrusted input). Old caches without the fields still
  validate. `MergeReports` needs no change (struct copy keeps fields).

### 2. `internal/cli` — stop masking retries, surface the reason

- `ssh_machines.go:286`: reuse a cached LAN entry only when
  `entry.Complete && len(entry.Candidates) > 0 && !entry.Stale`; otherwise
  rescan and print the "Discover LAN" line. Cache write rule stays as is so
  inventory still shows `lan=ready` for an empty completed scan.
- `renderSSHDiscovery` (`ssh_machines.go:304-315`): after the header, if
  `LANGuidance(report, runtime.GOOS) != ""`, call `app.warnf` with it. JSON output
  gains `probes`/`warnings` automatically through the embedded `Report`.
- `ssh_onboard.go:484-489`: for the `lan` source, if the report has zero
  candidates and `LANGuidance` is non-empty, return that as the error instead of
  falling through to the generic message at :537.

### 3. `internal/tui` — explain an empty scan in the dashboard

- `ssh_discovery.go:312`: when `len(report.Candidates)==0` and
  `sshdiscovery.LANGuidance(report, runtime.GOOS) != ""`, set `m.status` to that
  text (and keep `Esc shows all`). Other status branches are unchanged.

### 4. Docs, skill, changelog (required by AGENTS.md)

- `CHANGELOG.md` `[Unreleased]` → `### Fixed`: empty LAN scans now report probe
  outcomes and a Local Network hint on macOS; the wizard no longer reuses an
  empty cached LAN scan.
- LAN discovery paragraph + structured-output note, English and zh-TW as a pair:
  `docs/guides/ssh-hosts.md` (~:188, :423-436) and `docs/guides/ssh-hosts.zh-TW.md`
  (~:192, :398). Mention additive `probes`/`warnings`, that `ready` means
  "every endpoint finished", and the macOS Local Network requirement. Note that
  a launchd-started terminal/multiplexer may not be grantable, so run from an
  `.app` terminal.
- `docs/reference/compatibility.md` (~:517) and `compatibility.zh-TW.md` (~:459):
  additive fields; status values unchanged.
- `internal/skill/dev-cli/references/ssh-hosts.md` (~:463, :488) and
  `internal/help/topics/ssh.md` (~:380): same facts, brief.
- No Cobra flag/Use/Short change, so `make skill-sync` isn't required;
  `make skill-check` still runs as a guard.

## Tests

- `internal/sshdiscovery/lan_test.go`: new test using the `DialContext` seam.
  Return `&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}`
  for all but one address, and `ECONNREFUSED` for that one. Assert
  `Status==ready`, `Complete`, zero candidates, exact `Probes` counts, and the
  warning code. Add a case with one open banner endpoint: no warning, and
  `Open==1`. Add a timeout case counted as `Timeout`.
- `LANGuidance` table test: no warning gives "", darwin vs linux wording,
  counts rendered.
- `internal/sshdiscovery/cache_test.go`: round-trip of `probes`/`warnings`;
  reject an unknown warning code and negative counts; an old-format record
  without the fields still reads.
- `internal/cli`: test that `runSSHDiscovery` rescans (dial seam called) when a
  fresh cached same-scope LAN report is empty, and reuses it when it has
  candidates. Use `sshNativeTestApp` from `ssh_tui_discovery_test.go:20-40`
  with `XDG_CACHE_HOME` and a real clock (cache written with `ObservedAt` now).
- `internal/tui/ssh_workflow_test.go`: an empty LAN result carrying the warning
  sets the guidance status.

## Verification

```bash
gofmt -l . ; make vet
go test ./internal/sshdiscovery ./internal/cli ./internal/tui
go test -race -timeout 20m ./...
make skill-check && make build
uv sync --frozen --extra docs && uv run python scripts/check-docs.py --source \
  && uv run mkdocs build --strict && uv run python scripts/check-docs.py --site site
```

Manual, on this Mac:
- From Herdr (permission denied): `./dev ssh discover --source lan --cidr 192.168.31.0/24 --refresh`
  prints the warning with unreachable counts. `--json` shows `probes` and
  `warnings`, and `status` is still `ready`.
- Run `./dev ssh` → LAN twice in a row: the second run prints "Discover LAN"
  again and fails with the guidance, not the generic message.
- From cmux with permission granted: the same commands list .27/.52/.67/.138 and
  print no warning.

## Landing

This is a standalone fix, so AGENTS.md calls for a patch release (`v0.2.37`).
Commit on a branch; confirm with the user before pushing, merging or tagging.
