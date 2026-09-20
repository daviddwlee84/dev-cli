# Repository hygiene

`dev hygiene` joins optional gitleaks and pre-commit with repository policy,
private local identity rules and guarded text changes. It does not equate a
scanner finding with a confirmed credential leak.

## Inspect and configure

Install `gitleaks` and `pre-commit` first. Setup reports missing dependencies;
it does not install packages or replace a global hook silently.

```bash
dev hygiene status
dev hygiene status --check-remote              # explicit GitHub visibility query
dev hygiene setup --json
dev hygiene setup --apply --plan <id> --yes
```

Status reads the effective `core.hooksPath`, existing hook and project config.
A recognized hook chain is configuration evidence, not proof that every commit
was scanned. Hooks can be bypassed by raw Git or external tools.

Setup adds the blocking `dev-hygiene` pre-commit hook, safe gitleaks rules and
`.dev-cli/hygiene.toml`. It preserves other hooks and YAML comments. Unknown
hook chains and conflicting same-name hooks require manual integration. An
existing gitleaks config is retained by default. `--migrate-hooks` permits narrow
recognized rule migration; `--migrate-rules` proposes whole-config replacement.
Review either change against custom rules first.
`dev repo setup --enable agent-history-hygiene` uses this same service.

The hook checks index content. It never rewrites files or stages user changes.
A missing dev/gitleaks executable, invalid report or failed scan blocks it.
The hook runner must find a dev version that supports `hygiene` on PATH.

Setup writes project configuration. It first respects Git's effective
`core.hooksPath` and keeps a recognized existing hook, including a global one;
it does not install or modify a global hook. Only when no hooksPath is configured
and the common Git directory has no pre-commit hook does it install a repository
hook. Missing or unverified configured hooks require manual integration, not an
automatic local override. There is no `--global` setup option.

## Batch setup and gradual migration

The normal commit path is `Git -> pre-commit -> dev hygiene scan -> gitleaks +
private/public privacy policy`. Setup is a separate configuration operation;
commits do not call setup again and do not require an agent.

```bash
dev hygiene manage --all                 # choose repositories, preview, then apply
dev hygiene manage /path/to/a /path/to/b --json  # preview only
dev hygiene setup --migrate-hooks --json # one repository, narrow migration
```

REPOS → Ctrl+O → hygiene offers status, the latest stored scan report, explicit
worktree/staged/local-history scanning, and reviewed setup for the selected
checkout or filtered repository pool. A worktree row targets that exact checkout;
reports never fall back to a sibling checkout or trigger an automatic rescan.
Scanning uses the current policy and a 20-minute limit, without fetching refs or
capturing raw values. Operations suspend into the shared CLI workflow; press
Enter to return. Setup previews changes and asks for confirmation before apply.

The setup workflow is shared with the CLI. Repositories do not
need a skills lock. Plans show files, retained dependencies and blockers; choose
which prepared plans to apply. Shared Git repositories are processed once. A new
common-directory hook requires readable sibling worktree configurations, checked
again before installation. Unknown/custom scanner commands need manual review.

Migration replaces only recognized equivalent `gitleaks-system` hooks. It keeps
specialized artifact checkers, finalizers, provenance and unrelated hooks. The
recognized old password expression can be updated without replacing custom rules
or comments; scoped/custom expressions stay unchanged. `--migrate-rules` remains
the separate explicit whole-config replacement. Re-scan after rule updates;
changed detector IDs can require fresh fixture exceptions.

Setup and scanning require gitleaks 8.30.0 or newer compatible 8.x; CI pins 8.30.1.
Passive status checks presence without executing the scanner. Ensure the hook's
PATH resolves a dev version with hygiene support: running `./dev` alone does not
upgrade an older `dev` on PATH. Tools are not installed automatically.

Batch results retain per-repository completed, blocked, skipped, stale, partial
or unverified states and a private receipt. Completed setup is idempotent. An
interruption does not roll back other repositories or authorize a blind retry.
The JSON batch preview contains exact child plan IDs; apply one with the existing
`hygiene setup --repo PATH --apply --plan ID --yes` interface.

The staged checker itself does not mutate files. Pre-commit can temporarily stash
unstaged work, so wait for an artifact recorder to exit before committing its
checkout. This migration does not replace the entire agent-history-hygiene
lifecycle: `dev artifact finalize` still requires that skill's scripts for existing tracked-history handoffs; configured external archives use native snapshot checks. Keep those
installations and any active finalizer wiring until a separate replacement exists.

## Choose policy

Precedence is built-in defaults, user defaults (`$XDG_CONFIG_HOME/dev/hygiene.toml`),
repo policy, private local repo overrides, then explicit scan/status flags.
The recommended defaults are `secrets = "block"`, `known = "block"` and
`generic = "warn"`. Each category supports `block`, `warn` and `off`. A rule
may override its enabled category's disposition; category `off` disables it.

```toml
version = 1
secrets = "block"
known = "block"
generic = "warn"
```

Setting all three categories to `off` returns an explicit `skipped` result without
reading repository file contents or running the engine; it is not a clean scan.

Private visibility supplies advice only. It does not silently weaken policy;
unknown or unsupported visibility is never treated as private. GitHub lookup
requires `status --check-remote`; `--remote` selects its remote (default origin).

```bash
dev hygiene rules policy --known warn --generic off --json
dev hygiene rules apply --plan <id> --yes
```

Policy/rule changes are separate reviewed plans. One invocation's scan overrides
are not persisted. Obtain a fresh scan using the persistent policy before
creating a redaction plan.

## Local identity rules and exceptions

```bash
dev hygiene rules import --from ssh --json
dev hygiene rules import --from ssh --select <candidate-id> --json
dev hygiene rules import --from local --json
dev hygiene rules add --id private-network --kind cidr --value-file network.txt \
  --replacement 192.0.2.1 --json
dev hygiene rules apply --plan <id> --yes
```

Imports require explicit candidate selection. SSH uses the bounded static
OpenSSH Include closure, including literal aliases, HostName, User and key-path
references. It never executes `ssh -G`, `Match exec`, DNS or a remote command,
and never reads key material. An incomplete Include closure blocks import.
Common public names and generic accounts are recommendations for warnings.
Local import supplies home path, account, Git name and Git email candidates.

`rules import --from machines` reads existing Tailscale/LAN and Fleet inventory
caches only. IPs, hostnames and remote usernames become selectable private
candidates; credential stores, keys, discovery commands and remote connections
are never queried. Observation timestamps and stale markers remain visible.
Missing caches are reported without creating them; incomplete/corrupt caches
block imports. Source changes after preview invalidate the saved import plan.

Quoted password literals remain detectable in source files. Unquoted assignments
are scoped to configuration/script/prose formats, avoiding Go boolean fields and
variable expressions. Format-complete test/demo keys still block until a reviewed
exact exception is added. Do not exclude tests/docs or an entire line beside a
placeholder; prefer inert placeholders or construct detector fixtures at runtime.


Literal, CIDR and Go RE2 rules accept optional relative path globs. `directory/**`
selects descendants. Rule values come from a file or `--value-file -`; do not
put sensitive values in command arguments. Edit or disable existing private
rules through a reviewed same-ID rule plan or the private policy file.

Private rules, HMAC identifiers, scan records and recovery are stored outside
Git under `paths.state_dir/hygiene/repos/<native-repo-id>/`. Linked worktrees
share local policy but plans remain bound to an exact checkout. Windows uses
protected current-user/SYSTEM ACLs; Unix uses private ownership and permissions.
There is no automatic deletion or export of this state.

```bash
dev hygiene rules allow --report <report-id> --finding <finding-id> \
  --reason 'Reviewed synthetic test fixture' --json
```

Local exceptions bind an exact value/rule/path finding. Shareable TOML exceptions
require `rule`, `path`, an anchored `pattern` and a reason. Keep private values
out of shareable rules. `.gitleaksignore` contains finding fingerprints, not path
globs. `scan --audit` ignores local finding exceptions, that file and inline
`gitleaks:allow`; scanner-config allowlists still apply and must be reviewed.
Placeholders never automatically allow an unrelated key on the same line.

## Scan with an honest scope

```bash
dev hygiene scan --scope worktree --json
dev hygiene scan --scope staged --json
dev hygiene scan --scope history --audit --timeout 40m --json
dev hygiene --public-only --known off --generic off scan --scope history \
  --range <full-from-oid>..<full-to-oid> --json
```

Working-file scans include tracked files and nonignored untracked files. Already
tracked files remain in scope after a later `.gitignore` addition. Staged scans
use actual index bytes, including hidden files and partial staging. Selected
`--file` arguments must already belong to that scope.

An explicit range checks versions changed in its selected commits, leaving
untouched older files for the full audit. History freezes locally available refs
(or an explicit full-OID range), includes
merge-only content and removed files, and never fetches implicitly. It does not
scan reflogs, unreachable objects, uncommitted work in other checkouts or remote
submodule repositories. Nonregular entries and recognized binary formats are
listed as exclusions from text scanning. Unsupported text encodings, unreadable
or oversized files, changing sources/refs, shallow history, cancellation and
scanner failures produce incomplete results. The per-file text limit is 128 MiB;
large files are never silently called clean.

Public JSON schema 1 records status, scope, frozen refs, counts, safe finding
locations, dispositions, exclusions and coverage gaps. It omits source snippets
and credential values. Finding IDs and public file/plan digests use private HMAC
keys. `complete` describes the declared text scope; a blocking finding or
incomplete scan exits unsuccessfully. No findings is not a credential-validity
check. Reports may contain identifying filenames not covered by selected rules;
review them before sharing. Findings may add a `value_id`: a private-keyed
digest of the matched value within its rule, so identical values aggregate
across files without revealing them. Additive `file_id` identifies the exact
path with a private key, keeping distinct files separate even if their displayed
paths mask to the same text.

CI uses public repository rules, without global/personal values. It cannot
claim to have checked private SSH identities. PR/push scans select the changed
commit range; manual workflow dispatch audits all locally fetched history.

## Summarize a scan

```bash
dev hygiene report                                 # newest stored scan for this checkout
dev hygiene report --scope staged --by rule --top 5 --findings
dev hygiene report --report <report-id> --disposition block,warn --path 'docs/**'
dev hygiene report --rescan --scope history --range <full-from-oid>..<full-to-oid>
dev hygiene report --rule privacy-email --values  # masked values; implies --rescan
dev hygiene report --json                          # hygiene_summary schema 1
```

`report` summarizes one scan instead of printing every finding. By default it
reads the newest scan recorded for this checkout without scanning again, so the
pre-commit hook's staged scan is readable right after a blocked commit. Latest
pointers are per checkout: linked worktrees share hygiene state, but the default
lookup never selects a sibling's scan. Snapshot reports never become the latest.
`--scope` selects the newest scan of that scope, `--report ID` an exact report and
`--rescan` a fresh scan; `--range`, `--file`, `--timeout` and `--audit` require
`--rescan`. The output warns when policy changed since the scan or the report
came from another checkout. Stored reports are historical observations:
`checkout_current` compares checkout roots only, not current source bytes.
Stored-summary output shows the scan date and notes "source bytes were not
rechecked". Use `--rescan` for a new observation; it cannot combine with `--report`.

`--by` selects `severity`, `rule`, `file` and/or `category` groups (default
`severity,rule,file`). `--top N` limits each group (default 10, `0` shows all)
and counts omitted rows. `--disposition`, `--rule`, `--category` and repeatable
`--path <glob>` filter findings before aggregation; `--findings` lists them
individually. Rows order block, warn, then accepted, followed by occurrences
descending, findings descending and name ascending. A stored summary exits 0
whatever it contains; a rescan with coverage gaps prints its summary, then exits
unsuccessfully.

`--json` emits `kind: "hygiene_summary"`, `schema_version: 1`: `report_id`,
`report_kind`, `scope`, `status`, `created`, `policy_current`,
`checkout_current`, `audit`, `public_only`, `rescanned`, `sections`, `top`,
`filters`, `totals`, `file_counts_complete`, selected
`severities`/`rules`/`files`/`categories` (file rows include `file_id`),
optional `findings`, `gaps`, `skipped`, `omitted` counts and `values_shown`.
A rule reports `distinct_values` only when every finding has a `value_id`.
With `--values`, rules add masked `values` and `omitted_values`;
`values_truncated` warns when capture hit its limit. `values_status` is
`complete`, `truncated` or `failed` (empty/absent if not captured or legacy).
Legacy findings without file IDs make `file_counts_complete` false: file counts
then describe lower-bound masked-path groups. Private path metadata lets
`--path` match raw filenames while the echoed filter stays policy-masked; invalid
path globs are rejected. Agents should prefer
`dev hygiene report --json` over parsing `scan` output or tables.

`--values` adds masked distinct values per rule and implies `--rescan` unless
`--report` names a scan captured with values. Secrets keep their first and last
two characters plus length (length only below 12 characters); private rules show
`[private:N]`, emails `a•••@d•••.tld`, IPv4 `a.b.•.•`, IPv6 `first:•••` and
home paths `Users/x•••`. Raw values and line context never enter stdout, JSON or
scan records: they go only to a private 0600 `<report-id>.values.review.txt`, whose
location `dev hygiene review-path <report-id>` prints. Never paste that file into
chat, Git or CI logs. Capture allows 10,000 values and 20 samples per value,
with at most 64 KiB per raw value and 16 MiB combined raw/context/location/rule
metadata; the rendered review caps at 64 MiB. Oversized values are skipped whole,
not sliced, and limits set `values_truncated`. A failed values sidecar leaves a
saved partial report with a `values_capture_failed` gap and no private review-path
hint.

## Repair invalid UTF-8

Warning findings alone do not block a commit. A coverage gap such as
`unsupported_text_encoding` makes a scan incomplete even with zero blocking
findings. Encoding gaps retain that code and add an `encoding` object with
`reason`, zero-based `byte_offset`, one-based `line`, `invalid_bytes`,
`invalid_sequences` (contiguous invalid runs) and optional `nul_bytes`.
Historical gaps also identify the observed `commit`. Diagnostics contain no
source excerpts. Fixing a working file does not repair old Git objects.

```bash
dev hygiene repair-encoding --file .specstory/history/session.md --json
# Optional explicit deletion instead of the default replacement character:
dev hygiene repair-encoding --file notes.txt --invalid remove --json
# Review the private proposal; only after the exact artifact writer has exited:
dev hygiene repair-encoding --apply --plan <id> --yes --writer-stopped
# Review and stage only the intended repair, then check the actual index:
dev hygiene scan --scope staged --json
```

The default `--invalid replace` replaces each contiguous invalid byte run with
one `�`; `remove` deletes that run. All valid bytes, including existing `�`,
UTF-8 BOMs and line endings, remain unchanged. Explicit files may be selected
with repeated `--file`; each source and result must fit 128 MiB, with at most
256 files and 192 MiB of proposed output per plan. Valid text is a no-op. Known binary extensions,
NUL bytes and UTF-16/32 BOMs require separate encoding review; this command does
not guess a legacy encoding or recover the original character.

Repair uses signed plans, fresh file identity checks and private raw-byte
recovery through `dev hygiene restore`. It does not need a secret scanner.
Artifact edits require writer attestation and the live writer guard below,
including case variants and nested artifact directories. Encoding repair requires long
canonical path components without trailing dots/spaces or DOS short-name spellings. The hook
never repairs automatically. Apply changes working files only: a partially
staged file keeps its exact index contents, so a staged scan still fails until
you review and stage the repair. Do not broadly stage newly appended history.
Encoding repair does not redact secrets or prove a complete hygiene scan.

## Review and apply replacements

```bash
dev hygiene redact --report <report-id> --file notes.md --json
dev hygiene redact --report <report-id> --finding <finding-id> --json
dev hygiene review-path <id>                 # open privately; never paste contents
dev hygiene redact --apply --plan <id> --yes
# Only after the exact artifact writer exited:
dev hygiene redact --apply --plan <id> --yes --writer-stopped
```

All supported regular text files may be selected. Plans bind the checkout,
HEAD/branch, effective policy, scanner inputs, native file identity and content.
They are signed private records. An edited plan or stale input requires a new
preview. Overlapping matches prefer secret, then known, then generic rules;
within a category the longer span wins. The terminal output shows paths, opaque before/after digests and replacement
counts. `review-path <id>` locates a private full-content proposal for human
inspection. Its integrity is bound to the plan; never paste that file into chat,
Git or CI logs. The default output does not echo the original secret.

Apply checks the whole selection, then publishes individual guarded transactions
with original metadata and private recovery. It retains a partial ledger on
failure; never repeat an interrupted operation blindly. Apply and recovery errors
keep their underlying cause. On macOS the kernel tags every new file with the
writing process's `com.apple.provenance` and ignores attempts to copy it, so a
replacement carries the writer's tag rather than the source's. Every other
extended attribute must still round-trip, and a changed tag on the source file
still makes the operation stale. Index contents, Git history and installed
binaries are not changed. Native Windows protections are specific to these
opted-in text transactions; other configedit workflows retain their own platform
support contracts. Windows preserves owner/group/DACL semantics; files with
alternate streams, explicit integrity labels or special attributes require
manual preservation.

```bash
dev hygiene restore --receipt <receipt-id> --json
dev hygiene restore --receipt <receipt-id> --apply --yes
```

Restore refuses files replaced or edited since the recorded transaction. Artifact
recovery also needs `--writer-stopped`, and `restore --apply` guards the
receipt's exact paths. A disabled/unavailable runtime cannot prove process
absence. The post-writer attestation and source revalidation remain necessary,
and raw external writers remain outside dev-mediated locks.

## Artifact writer guard

Redaction, encoding repair, restore, batch `manage` and `dev artifact`
finalize/archive/migrate share one live-writer check over their reviewed target
files; archiving still leaves source bytes untouched. Any other recognized agent
covering the checkout blocks an artifact edit, whatever its status. The calling agent's own pane is exempt only when:

- Herdr reports its exact agent session ID (`agent_session` kind `id`, not a
  title) and every artifact target is a `.specstory/history/*.md` transcript
  with a valid UUID in its actual
  SpecStory-generated anchored preamble, proving a different session; or
- the caller passes global `--allow-shared-checkout`, asserting disjoint
  ownership after confirming the writer exited (for example plans, or
  transcripts without a provable preamble).

An identified caller-owned live transcript is refused even with the override.
Unknown caller identity needs the explicit disjoint-ownership attestation.
`--writer-stopped` still attests that the exact recorder exited; neither flag skips
source revalidation.

Use the [manual dogfood checklist](../reference/hygiene-dogfood.md) for this repo.
