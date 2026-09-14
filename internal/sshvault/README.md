# SSH vault key creation boundary

This package is the domain adapter, not CLI/TUI integration or an SSH agent.
`Plan` explicitly reads native provider metadata; `Apply` consumes a service-bound,
immutable, single-attempt plan. The caller reviews the destination and confirms
creation before Apply. Deserialized plans carry no authority. `ValidateRequest`
is a syntax-only, zero-provider-execution check for static dry-run previews; it
never establishes native-context readiness or substitutes for Plan/Apply.

## Provider modes

- **1Password:** native `op item create --category ssh` generates the default
  Ed25519 identity. Account/user/server/exact vault ID and CLI version are bound
  and revalidated. The returned item ID, title, category and vault must match;
  public material is fetched by that exact ID with a public-field selector.
  Private response fields are ignored, not returned or logged. The selected
  public-field JSON shapes remain native-unverified and covered by fixtures only.
- **Bitwarden endpoint-attested mode remains unsupported.** Its native status and
  read-only `config server` expose only a base/display URL, while effective API
  and identity endpoints may be independently overridden. US/EU cloud both expose
  a null base. A non-null self-hosted-looking URL is not stronger endpoint proof.
  Missing native-context approval returns `ErrUnsupportedContext`; it never
  authorizes guessed endpoints, native storage parsing, or configuration changes.
- **Optional Bitwarden native-profile mode:** callers must separately set both
  `Experimental` and `NativeContextApproved`. This explicitly delegates endpoint
  and internal configuration authority to the reviewed native CLI profile. It is
  not endpoint attestation. A session key is a decryption credential, not a server
  or account ID. Even unchanged session/user observations cannot prove native
  internal endpoints or settings did not change. This residual delegation must
  be visible in the preview and approval, not hidden behind a generic key choice.

## Frozen native execution

Native-profile planning captures the complete environment and working directory,
including `BW_SESSION`, HOME/XDG paths, explicit app-data overrides and PATH. Only
private comparison hashes and filesystem identities remain in a plan; the
session-bearing environment is released afterwards. No session or comparison
hash belongs in public JSON, logs, error text or a persisted plan.

Apply captures again, compares the reviewed context and revalidates before any
private generation. All metadata/create/postcheck calls use that same explicit
frozen child environment and cwd, never later ambient inheritance or `os.Setenv`
tricks. A custom runner without the frozen-execution contract fails closed.
Every Bitwarden command receives `--nointeraction` and a child-only
`BW_NOINTERACTION=true`; becoming locked cannot redirect key JSON into a native
master-password prompt. No login, unlock, config update or automatic retry occurs.

Existing profiles must resolve to current-user-owned private directories with
existing private regular `data.json` metadata; no vault contents are read.
Resolution is component-by-component: every requested path, intermediate symlink
and target directory, final path, filesystem identity and ACL observation is
captured and rechecked. Unsafe intermediate components are not erased by `..`
normalization or hidden behind a protected outer link and safe final target.
Relative explicit profile paths are refused. The selected canonical app-data
directory is pinned in the child.
The pinned CLI's portable `bw-data` beside `process.execPath` takes precedence
over even an explicit override, so its presence/absence is separately bound.
For Node distributions this means beside the bound Node binary, not the script.
The default macOS and Linux HOME/XDG mappings follow the pinned native source.

Darwin checks opened-inode extended security with `fgetattrlist`, using the
`getattrlist(2)` / SDK `sys/kauth.h` and `sys/acl.h` structures rather than parsing
`ls` or chmod text. Supported forms are absent/empty ACLs and well-formed deny-only
ACEs with non-null principal GUIDs, the DENY tag, optional INHERITED marker, and
only documented vnode rights from READ_DATA through TAKE_OWNERSHIP. ACL flags may
be zero or NO_INHERIT. The standard protective `everyone deny delete` home-style
ancestor ACL is therefore supported without exempting the ancestor from review.
Deny-only entries cannot grant access beyond the already-validated POSIX modes.

Allow/grant, audit/alarm, deferred or inheritance-control flags, generic/Windows
rights, unknown tags/bits/layouts, malformed/truncated data and unexpected owner/
group fields remain unsupported. Linux performs bounded ACL/security-xattr
metadata checks and currently accepts ACL absence only. Unsupported queries and
security-policy attributes fail closed. Every resolution component and existing
`data.json` is checked without reading vault contents; the entire accepted Darwin
metadata is privately fingerprinted and revalidated, so even a changed accepted
deny-only ACL invalidates the reviewed context. There are no trusted-ancestor
exemptions and no runtime ACL repair/removal. Owned temporary fixtures cover the
protective form, additional grants, metadata-only changes and malformed buffers;
no actual user's home ACL was inspected. Native desktop creation plus agent
selection remains the alternative for unsupported permission policies—do not
strip home protections to satisfy the adapter. ACL capture requires two equal,
safe metadata observations between inode/ownership/mode checks. Directory ctime/link-count
changes from unrelated children are not permission or identity changes; inode,
owner, mode and explicit ACL checks remain authoritative. Regular files and
symlinks retain their stricter ctime guard.

Supported execution surfaces are owned, protected native ELF/Mach-O entrypoints,
or the documented `#!/usr/bin/env node` entrypoint from an exact `@bitwarden/cli`
package. The latter binds the public package manifest/name/version/bin mapping,
installation directory, script, complete manifest-bin resolution path, and exact
native Node binary found through the frozen PATH/cwd. Common `build/bw.js` and
leading `./build/bw.js` bin forms are supported: only harmless leading `./` is
removed relative to the bound package directory, while the original manifest
value and actual path remain bound. Parent traversal, embedded dot cleanup,
absolute bin paths and ambiguous PATH forms are refused rather than silently
normalized. Clean absolute/relative PATH directories and an explicit current-
directory marker are resolved against the captured cwd. Execution uses the
bound Node binary with the pinned script prefix.
Unsupported script wrappers/shebangs and extra runtime-code loading through
NODE_OPTIONS/NODE_PATH/preload variables fail closed. Native executable checks bind
filesystem identity and ELF/Mach-O structure; they cannot distinguish every compiled
runtime shim from the intended runtime. The proof covers these named files/directories,
resolution components, ownership, modes and supported ACL observations, not executable
behavior, every dependency or the whole provider supply chain. Native Windows
profile execution is unavailable pending its identity/ACL contract.

## Generation and outcomes

Experimental Bitwarden generation creates Ed25519 only in process memory and
passes the pinned CLI `2026.3.0` type-5 schema through base64 stdin. It is limited
to the personal-vault schema with a unique operation marker. No existing key is
imported or deleted, and dev does not write private-key files. Owned buffers receive
best-effort cleanup, not a promise about Go runtime copies, dumps or OS swap.

Creation, native-context consistency, endpoints and SSH agent readiness are
separate facts:

- `status: created` means a matching creation receipt was verified.
- Bitwarden `native_context_status: observed_consistent` means the frozen context,
  tool/profile identities and native account observations remained consistent.
- Bitwarden always reports `endpoint_status: unverified` and keeps the legacy
  `binding_status: unknown`; it never turns native delegation into endpoint proof.
- None of these results proves that a provider agent lists or can sign with the key.

`CanSelectAgentKey(plan, result)` centralizes the provider-specific eligibility
matrix for matching a receipt to fresh agent inventory. It requires the unchanged,
source-bound result of successful Apply, including matching canonical public key
and fingerprint; fabricated, serialized or modified receipts cannot authorize it.
It performs no agent lookup and never substitutes for signer/authentication proof.

A wrong public-field type or failed lookup keeps the exact created item ID and
returns `ErrPublicKey`. An unexpected sharing scope, failed/changed postcheck or
other uncertainty retains any verified receipt with unknown context. A lost or
unverifiable creation response remains `status: unknown`; an item ID is never
invented. No creation is automatically retried, including interrupted operations.
There is no rollback, delete, import, login/unlock, agent-configuration or key-
revocation API. `Receipt.Destination` is reviewed intent; pre/post checks cannot
make native tools and remote services transactional.

The shared `sshcredential.NativeRunner` caps output, discards stderr, applies a
15-second command timeout and a two-second `WaitDelay` for inherited-pipe cleanup.
Failures remain sanitized. It does not terminate whole process groups; bounded
wait is not proof that every descendant exited.

## Validation status

No real vault/key creation or installed provider execution has been performed for
this implementation. Native-context tests use copies of their own harmless Go
test executable, inert Node entrypoints and synthetic profiles; they verify
frozen execution, guarded mutation and receipt handling, not live Bitwarden or
1Password integration. A real Bitwarden type-5 spike still requires separate user
consent and native unlock. Do not describe fixture success as live validation.

## Sources

- [Darwin ACL ABI and permission flags](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/kauth.h)
- [Darwin extended-security attribute packing](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/vfs/vfs_attrlist.c)
- [1Password CLI SSH generation](https://www.1password.dev/cli/ssh-keys/)
- [1Password item commands](https://www.1password.dev/cli/reference/management-commands/item/)
- [1Password exact vault selection](https://www.1password.dev/cli/reference/management-commands/vault/)
- [Pinned Bitwarden profile precedence](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/apps/cli/src/service-container/service-container.ts)
- [Pinned Bitwarden noninteraction flag](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/apps/cli/src/program.ts)
- [Pinned Bitwarden status](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/apps/cli/src/commands/status.command.ts)
- [Pinned configuration read/write split](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/apps/cli/src/platform/commands/config.command.ts)
- [Pinned cloud URL definitions](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/libs/common/src/platform/services/default-environment.service.ts)
- [Pinned SSH-key export schema](https://github.com/bitwarden/clients/blob/c20753e4dae029d6155565682dc79718f9aeb426/libs/common/src/models/export/ssh-key.export.ts)
