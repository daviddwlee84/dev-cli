# Repository scanning and redaction stack

## Current default: one shared dev policy

`dev hygiene setup` and `dev repo setup --enable agent-history-hygiene` install
an inspectable local pre-commit entry that calls `dev hygiene scan --scope staged`.
Gitleaks remains the optional external secret detector; dev owns policy, privacy
rules, safe output, scope and reviewed file transactions. Both dev and gitleaks
must be discoverable on PATH when the hook runs.

The hook checks actual index contents, including hidden paths and partial staging.
It does not rewrite or stage working files. Failed/missing scanners and malformed
reports fail closed. Root configuration and an effective hook are both necessary:
installed tools alone do not activate protection. Existing global/foreign hooks
are retained; unverified chains require manual integration.

Rule values from static SSH/local identity imports live in private state outside
Git. CI uses public rules only. The public report omits credential bytes and uses
opaque HMAC identifiers; finding counts require classification, not automatic
claims of confirmed leaks.

## Exceptions

A placeholder is inert data, never permission for another key on its line.
The bundled gitleaks sentinel allowlist matches the secret itself. Do not add
line-wide REDACTED/example allowlists or exempt entire transcript directories.
`.gitleaksignore` contains the scanner's exact finding fingerprints, not globs.
Shareable dev exceptions require a rule, path, anchored match pattern and reason;
private exceptions may bind a finding ID. `--audit` bypasses finding/inline
suppression, but scanner-config allowlists still require inspection.

## Legacy compatibility

`scan-staged.sh` emits masked locations by default. `--redact` selects its legacy
exit 10 and does not rewrite files; default candidate findings return 20. Missing
gitleaks returns 30; scanner or report/parser errors return 40. Never dump raw
scanner reports as a fallback.

The vendored `redact_secrets.py --fix` remains an explicit artifact utility. Its
console uses complete sentinels and scanner errors propagate. It may restage
files, so use `dev hygiene redact` for reviewed general-text changes and partial
staging. The separately published `agent-skills` remote hook has an independent
release cycle; installing dev does not update that remote implementation.

SpecStory native redaction is another provider-owned layer, not proof of complete
coverage. Additional unsupported artifacts, personal information and policy choices
remain the repository owner's responsibility. See the main skill for exact-session
handling and [remediation](remediation.md) for historical confirmed exposures.
