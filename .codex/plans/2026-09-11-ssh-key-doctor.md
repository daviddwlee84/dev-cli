# SSH key doctor

User approved a standalone doctor plus shared setup preflight after observing
generic public/private key permission warnings. Read-only diagnosis on this host
found a private companion with mode 0700 where dev expects 0600. No real SSH
permissions were changed during implementation.

## Behavior

- `dev ssh key doctor` is a metadata-only bounded diagnosis. Default discovery
  covers public companions and exact standard identity names under ~/.ssh.
  Repeatable `--key` restricts scope to selected paths plus canonical setup paths;
  arbitrary private-only filenames require explicit selection.
- `--fix` previews the exact union of mode changes, confirms once (or uses
  `--yes`), applies via the existing source-bound permission transaction, and
  diagnoses again. Incomplete/blocked scope never authorizes an automatic fix.
  Successful tightening remains after partial failure; Windows ACLs remain manual.
- Setup keeps canonical/selected-key preflight and shares PermissionPlan/Apply
  and permission renderers. It does not auto-repair every unselected key.
- Catalog error codes stay compatible while messages explain actual safe causes.
  Missing inferred companions are distinct from malformed content/permissions;
  duplicate unreadable paths are reported once. Doctor fixes metadata, not key
  contents, passphrases, or remote authentication.
- Doctor never queries ssh-agent, evaluates ssh -G, reads private contents,
  follows links or chmods unrelated traversed directories. Only linked recognized
  candidates block scan completeness; unrelated links remain out of scope.

## Verification

Isolated HOME fixtures cover read-only default, single-confirmation aggregate
repair, explicit scope, idempotence, stale/unsafe/incomplete refusal, no agent or
remote calls, unchanged valid public modes and unrelated directories, and safe
diagnostic messages. Run focused CLI races, full sshhost races, vet, platform
compilation, skill sync/check and strict paired documentation checks.

Completed checks: focused SSH key/doctor/wizard CLI race tests, full sshhost race
suite, full-project vet, Windows ARM64 CLI and Windows AMD64 sshhost compilation,
formatting, skill sync/check, source generation/check and strict bilingual
MkDocs/site checks. Live read-only doctor found exactly one planned change:
azure_vm1 private identity 0700 to 0600. It did not apply that repair.
