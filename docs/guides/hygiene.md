---
description: Inspect repository hooks, scan secrets and personal data, and apply reviewed text replacements with private recovery.
authority: project
status: evolving
verified_on: 2026-09-12
---

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

## Batch setup and gradual migration

The normal commit path is `Git -> pre-commit -> dev hygiene scan -> gitleaks +
private/public privacy policy`. Setup is a separate configuration operation;
commits do not call setup again and do not require an agent.

```bash
dev hygiene manage --all                 # choose repositories, preview, then apply
dev hygiene manage /path/to/a /path/to/b --json  # preview only
dev hygiene setup --migrate-hooks --json # one repository, narrow migration
```

REPOS offers the same single/filtered-repository workflow. Repositories do not
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
review them before sharing.

CI uses public repository rules, without global/personal values. It cannot
claim to have checked private SSH identities. PR/push scans select the changed
commit range; manual workflow dispatch audits all locally fetched history.

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
failure; never repeat an interrupted operation blindly. Index contents, Git
history and installed binaries are not changed. Native Windows protections are
specific to these opted-in text transactions; other configedit workflows retain
their own platform support contracts. Windows preserves owner/group/DACL
semantics; files with alternate streams, explicit integrity labels or special
attributes require manual preservation.

```bash
dev hygiene restore --receipt <receipt-id> --json
dev hygiene restore --receipt <receipt-id> --apply --yes
```

Restore refuses files replaced or edited since the recorded transaction. Artifact
recovery also needs `--writer-stopped`. Runtime observations reject recognized
writers; a disabled/unavailable runtime cannot prove process absence. The
post-writer attestation and source revalidation remain necessary, and raw external
writers remain outside dev-mediated locks.

Use the [manual dogfood checklist](../reference/hygiene-dogfood.md) for this repo.
