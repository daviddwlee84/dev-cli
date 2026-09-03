# Closure note (2026-08-31 UTC)

This worktree intentionally closes at the first independently useful vertical slice: shared assessment/machine/lease/filesystem/protocol foundations, additive `repo context --json/--refresh`, and explicit standalone `fleet machine-id` plus `fleet files` plan/apply. The distributed repository/task ownership transfer, setup journal, backup/reclaim/restore, and eviction phases below remain the design for a later milestone and are **not** implemented or claimed by this branch. Public help, docs, and changelog describe only the delivered slice.

# Context

現有 `v0.2.1` 的 `dev fleet` 已能透過 SSH 彙整多機器 snapshot，並以共享 Git remote 同步一個乾淨、attached、已提交的 branch：source 可先 push，target fetch 後只對既有且乾淨的同名 branch checkout 做 fast-forward（`internal/cli/fleet.go`、`internal/fleet/sync.go`）。它不是完整 project transfer：不會 clone target、建立 checkout、搬 task/catalog/note、搬 ignored files、交接 owner，也不能證明 source clone 可安全刪除。

這次要交付一條完整但分階段、可重試的主力開發機遷移流程：

1. 從一個 repo/linked worktree 看清 local、Git remote/forge web URL、configured fleet hosts 與各種 readiness。
2. 將目前 branch 與該 repo 所有非-DONE tasks，連同 catalog 與 quick notes，從 A 交接到明確的一臺 B。
3. 可選擇把 `.env`、`.mcp/**` 等明確 allowlist 的 ignored local files 一起帶過去，也可獨立做一次單向同步。
4. 對 A 做完整 backup/reclaim assessment、實際 restore drill，最後用獨立命令安全移除 clone/worktrees；transfer 本身絕不自動刪 A。

已確認的產品決策：repository scope = 目前 branch 加所有非-DONE tasks；sidecar 預設包含 tasks/catalog/全部 quick notes；各機器的 dev state store 彼此獨立；本版本包含實際 restore/evict；portable files 僅允許兩端皆為 untracked+ignored 的 regular files，target 不同內容預設阻擋，只有明示 replace 才可覆寫。

# Product contract and command surface

## One shared assessment vocabulary

所有 context、transfer、backup、reclaim、restore、evict 使用同一組 versioned evidence/gate types與 stable reason codes；不得輸出含糊的全域 `safe: true`。每個 scope 使用：

- `eligible`: 此 action 所需的所有 fresh evidence 均通過；
- `blocked`: 已知存在危險或衝突；
- `indeterminate`: probe 失敗、stale、unsupported 或 coverage 不完整；
- `not-applicable`: 該 resource/action 不存在或不適用。

至少分開顯示 `transfer-source`、`transfer-target`、`portable-files`、`remote-backup`、`linked-worktree-retirement`、`whole-clone-reclaim`、`restore-verified`、`whole-clone-eviction`。任何 eligibility 都是附 `observed_at` 和 fingerprint 的 point-in-time 結果；真正 mutation 前必須在 lock 下重驗。

## Commands

```text
dev status
  保持快速、純 local、以目前 checkout 為中心；改為重用 shared local facts，不做 network probe。

dev repo context [repo] [--json] [--refresh]
  既有完整單-repo surface。local 永遠 live；預設外部 facts 可來自 cache 並標 age/source；
  --refresh 才 live probe forge/fleet。不要新增重複的 `repo status`。

dev fleet files [repo-or-path] --to <host>
  [--file <portable-pattern>...] [--apply] [--replace] [--yes] [--json]
  一次、單向、單 source/target 的 portable-file plan/apply；無 watcher、雙向 merge、fan-out 或 delete propagation。

dev fleet transfer <repo-or-task> --to <host>
  [--scope auto|repository|task] [--remote <name>] [--with-local-files]
  [--apply] [--yes] [--transfer-id <id>] [--json]

dev fleet transfer status <transfer-id> [--refresh] [--json]
  reconcile durable source/target journals；遺失 response 時不猜結果。

dev fleet transfer abort <transfer-id> [--apply] [--yes] [--json]
  僅允許 source fence 前終止；清理 operation-owned target staging/reservation 並留下 terminal tombstone。

dev fleet transfer return <transfer-id> [--apply] [--yes] [--json]
  fence 後由 B 先 quiesce 並永久 invalidates 該 epoch 的 delayed accept，再由 A 驗證 receipt 後取回 ownership。

dev wt provision [task-or-path] [--apply]
  [--retry-indeterminate <step-id>|--mark-complete <step-id>] [--yes] [--json]
  顯示／執行 B-local setup journal；任意命令的 uncertain crash 不會自動重播。`dev resume` 對 transferred WARM checkout 進入同一服務。

dev backup [repo] --remote <name> [--push] [--json]
  預設 report-only；--push 只推 plan 中明確列出的 recovery refs，live reverify 後寫 receipt。

dev reclaim [repo] [--remote <name>] [--receipt <id>] [--json]
  永遠 report-only；逐 resource 說明可回收 bytes、recovery source、blocker/unknown。

dev restore <receipt> [--to <path>] [--apply|--verify] [--yes] [--json]
  --verify 在隔離 staging path 做真正 fetch/restore drill 並比對完整 manifest；一般 --apply 恢復到明確 path。

dev evict [repo] --receipt <id> [--apply] [--yes] [named discard acknowledgements] [--json]
  預設 report-only；只有 fresh reclaim + matching verified restore + final revalidation 才可移除。
```

`fleet transfer` 的 `auto` resolution 在 task/repo 同時可解析或模糊時拒絕並要求 `task:<id>` / `repo:<ref>`。Repository scope 去重目前 branch 與 task branches、搬所有非-DONE tasks；DONE 只報告、不 rehydrate。Task scope 只搬該 task branch，但仍搬同一 catalog identity 與 repo quick notes。

# Architecture

## Shared domains

- 新增 `internal/assessment`: schema/version、evidence provenance/freshness/completeness、gate/outcome/reason code、canonical fingerprint 與 outcome reduction；保持 presentation-neutral，讓 inventory/transfer/recovery/local-files 各自產生 evidence。
- 擴充 `internal/inventory/repo_context.go`: 保留每個 checkout 的 status error、runtime enumeration error、Git topology、catalog/note/artifact摘要、safe remotes、fleet observations 與 scoped gates；CLI/TUI 消費同一 aggregate，不能再以 `tui.RepoRow` 當 domain contract。
- 新增 `internal/lease`: 獨立於 task/transfer 的 cooperative operation authority；以 canonical Git identity + branch/repository scope 鎖定，保存 reservation/fence epoch，讓所有目前版本的 Git/worktree/artifact/local-file/task mutator 在任何 side effect 前共用同一 lock/guard。
- 新增 `internal/transfer`: portable task DTO、source/target plan、strict wire protocol、private object/sidecar staging、journal/store、phase transition、sidecar import、fencing/abort/return/reconciliation。
- 新增 `internal/localfiles`: project manifest resolution、ignored-file expansion、held-root snapshot、plan/conflict policy、bounded wire payload、target apply/rollback/immutable recovery copy/receipt；供 `fleet files` 與 `fleet transfer --with-local-files` 共用。
- 新增 `internal/recovery`: all-ref 與 Git-administrative manifest/artifact/receipt、reclaim assessment、restore drill/apply、eviction intent/reconciliation。
- 擴充 `internal/wt` provisioning service：以 checkout/OID + plan hash 為 key 的 private setup journal；content-addressed/idempotent steps 可重播，任意 command 逐步記錄 `pending/running/completed/indeterminate`。
- 新增 `internal/forge/weburl.go`: 從 live Git endpoint 或 exact forge match 保守產生 display-safe HTTPS URL；raw Git URL 與 public DTO 分離。
- CLI orchestration 拆到 `internal/cli/repo_context.go`、`fleet_files.go`、`fleet_transfer.go`、`backup.go`、`reclaim.go`、`restore.go`、`evict.go`；既有 `fleet.Transport` 只負責 transport，不承擔 policy。

Reuse rather than duplicate:

- repository/checkout identity: `gitx.Discover`, `repo.Resolve/Discover`, `pathx.Canonical/Contains`;
- local Git: `gitx.StatusOf`, `Worktrees`, `WorktreeFor`, `RecoveryTopologyOf`, `InProgress`, `AnalyzeFinish`, `Run`;
- fleet identity/transport: `catalog.NormalizeRemoteIdentity`, `fleet.Transport`, candidate matching/FF classification extracted from `fleet.ApplySync`;
- lifecycle safety: `retire.Inspect`, `CloseAndWait`, target revalidation, `runtime.All()`;
- target acquisition/provisioning: `repo.Acquire`, `wt.Manager`, target-local trusted settings;
- durable identity/state: catalog `WithLock`/locations, note revision + file lock, artifact intent/reachability;
- filesystem safety: extract held-`os.Root` traversal/stable reads/portable path validation from `internal/repotemplate/snapshot.go`, and use `lockx` plus note/experiment intent/atomic-write ordering;
- disk ownership: `diskusage.Target/Usage` for owned vs shared bytes.

## Stable machine and protocol identity

Create a private, durable machine UUID (independent state stores make this host-local) and expose it through a non-secret capability command. Add an optional expected machine ID to each `remotes.toml` host; any mutating transfer/files/restore operation requires it to be pinned and match, while list/status may merely warn. SSH host-key validation remains mandatory.

All new hidden commands use separate strict v1 envelopes, bounded stdin/stdout/stderr, `DisallowUnknownFields`, EOF checks, schema/capability negotiation, exact machine/repo/branch/OID binding and idempotency IDs. Add non-PTY `ssh -T` protocol mode and disable agent/X11/port forwarding; probe compatibility before sending note bodies or file content. Old/no-dev/incompatible targets fail before payload transmission.

# Repository context and safe remote links

Extend `dev repo context` rather than adding another status synonym. Human/JSON sections:

1. identity, selected checkout, generated time and source provenance;
2. canonical + linked worktrees, per-checkout Git/error/activity;
3. tasks, owner/fence, artifacts and every observed runtime backend;
4. every Git remote with role (current upstream/origin/upstream), sanitized endpoint identity, provider and safe plain HTTPS web URL when confidently derivable;
5. matching configured fleet hosts, path/branch/task/runtime summaries, live/stale/unreachable/incompatible state and snapshot age;
6. scoped readiness and stable blocker/remediation codes;
7. coverage/errors, including the explicit statement that fleet means configured hosts only.

`--json` is a new additive schema-v1 report; unknown/error values remain null/status entries, never zeroed or silently omitted. Raw credential-bearing Git URLs, note bodies, local-file hashes/content and secret query/fragment/userinfo never enter public output. `--refresh` may update caches, but cached forge/fleet facts are display-only and can never satisfy a mutating gate. TUI gets only the cheap local readiness summary and shared renderer; ordinary row rendering must not trigger SSH, forge refresh or all-object scans.

Web URL order: exact live forge record URL, strict Azure transform, literal recognized/configured GitHub/GitLab host transform, otherwise unavailable. Strip/reject userinfo, query, fragment, controls, ambiguous escaping and unsupported ports; SSH aliases do not become browser hosts. Render a plain URL (terminal may linkify it) rather than introducing OSC-8 width/security complexity.

# Portable local files

Add a new project-owned `[local_files]` section in `.dev-cli/config.toml` with explicit portable relative patterns, plus repeatable ad-hoc `--file`. Do not inherit existing `worktree.include` defaults: a repository may propose candidates, but only the explicit `fleet files` invocation or `transfer --with-local-files` authorizes off-machine export. Define a small portable glob grammar, expand it locally to sorted exact paths, and send no glob over the wire.

For every file, both hosts must prove it is untracked and ignored under their own Git configuration; tracked files travel through Git. Only bounded regular files are accepted—no directories, symlinks/reparse points, sockets, devices, FIFOs, `.git`, nested repositories or submodule boundaries. Initial hard ceilings: 128 files, 8 MiB/file, 32 MiB total, bounded path/component depth; host settings may lower but not raise compiled limits.

Plan states are `ready`, `current`, `blocked-conflict`, `blocked-ineligible`, `blocked-unsafe`, `missing`, `failed`. Source and target must be the same normalized repo identity, branch and exact OID. Apply preflights the complete set, reopens both roots through held handles, and journals the transfer:

- missing target: stage private temp (`0600`), stream/hash exact length, sync, then publish atomically without replacement;
- identical target: no-op/current;
- differing target: block unless `--replace`; replace requires the target digest observed in the displayed plan, private rollback copy, atomic swap, post-write rehash and rollback/reconciliation on failure;
- `--yes` never implies `--replace`; no deletion or last-writer-wins mode.

Target acceptance additionally retains each transferred file as an immutable, manifest-bound private recovery blob under the target transfer store; it materializes and rehashes that blob in staging. A source-deletion acknowledgement releases only ownership coordination: the blob, manifest and receipt reference remain immutable for the full recovery-receipt lifetime, including after A is gone and B’s working checkout is modified or evicted. They may be released only by a separately confirmed durable-receipt purge or after an independently restore-verified immutable backup supersedes them. B’s current-version replace/files/evict flows must honor this retention record. A must not treat a mutable working copy as deletion proof and must preserve its staged source if the target blob cannot be revalidated. Standalone `fleet files` may release the blob when source is retained, or use explicit `--retain-for-evict` to create recovery evidence.

Clamp final privacy to owner-only (`0600`, or `0700` only for explicitly executable source); do not copy ACL/xattr/owner/timestamps. Public human/JSON results contain path/size/state only—no content or hash. Native Windows targets remain capability-blocked before content is sent until the transport, ACL, reparse-point and atomicity contract is fully implemented and tested.

# Transfer protocol

## Target preparation

Separate branch/ref evidence from checkout evidence. For every selected task/current branch, A resolves `refs/heads/<branch>` directly through the shared Git common directory and captures exact OID/object format without requiring it to be checked out. Only branches with an existing checkout require conflict-free, operation-free, fully clean checkout evidence. COLD/unattached task refs must equal the freshly fetched/advertised remote tip before transfer and remain COLD on B with no checkout; this lets one repository transfer several serial branch-mode tasks without pretending they are all attached. Every published ref uses an explicit `<oid>:refs/heads/<branch>` ordinary push and the live remote tip must equal exactly that OID (not merely contain it).

Before A is fenced, B may only create a durable reservation, validate conflicts, and fetch expected objects into a private, non-discoverable transfer staging repository. Existing clones are reserved through the shared lease authority but no branch/worktree is moved; absent clones are staged under the private transfer directory rather than passed directly to `repo.Acquire`. Operation-owned incomplete staging can be verified and safely recreated/reused on retry, while any pre-existing or user-modified destination remains untouched.

After A’s fence is durable, B holds its repository/branch leases while publishing the target state. It matches all configured fetch/push identities and reuses exactly one clone; ambiguity blocks. If absent, it computes the final destination from B’s own path policy and materializes the verified staging clone without exposing a partial canonical checkout. It then creates/fast-forwards only the checkouts required by task state/mode. Ahead/diverged/dirty/live-agent targets block. Worktree tasks use B’s configured path and explicit expected ref; WARM worktree tasks get a checkout, COLD tasks do not. Branch/direct tasks may use canonical checkout only when unique and safe—never silently change mode. Acceptance may apply only deterministic, journaled portable files; it never runs `post_create`, dependency installers/copies, or other arbitrary provisioning inside the replayable distributed transaction. Transfer does not copy trust/credentials or open a runtime. Accepted HOT tasks become WARM; WARM/COLD stay as-is. After acceptance, B runs provisioning as a separate B-owned setup transaction through `dev resume` or `dev wt provision`; accepted existing WARM checkouts must enter this flow just like newly recreated worktrees. A private journal is bound to checkout identity/OID and provisioning-plan hash. Before each arbitrary command, fsync `running`; after success, fsync `completed`. A crash in `running` becomes `indeterminate` and is never auto-replayed—only explicit `--retry-indeterminate` or operator-attested `--mark-complete` can advance it. A journal cannot promise exactly-once for an external side effect, so automatic resume is limited to content-addressed/proven-idempotent steps. Context separately reports `transfer-complete` and `setup-pending/indeterminate/ready` so acceptance is never misrepresented as a fully provisioned environment.

## Sidecar rehydration

Portable task DTO contains stable ID, name/repo display, branch/base/mode/state/next/note/tags/timestamps and source provenance, but never A paths or runtime handles. On B, bind `RepoPath`/`WorktreePath` from B, clear runtime/agent hints, and import with exact-ID + revision conflict detection. Add task-store cross-process locking/CAS, but make the operation lease—not the task file—the authoritative fence so a selected branch with no task is still protected. Retrofit every current `dev` path that can mutate or expose a certified repository/branch/checkout/sidecar (`dev git`, lifecycle/TUI, adopt, worktree/repo/bootstrap/fleet setup, artifact finalization, local-file apply) to acquire/check the same lease before its first effect and hold it through Git/filesystem changes plus final CAS.

Catalog import preserves stable ID, metadata/tags/remote identity, adds B’s machine-specific location, and blocks pending move or conflicting ID/path/non-empty summaries. Note import preserves ID/repository ID/timestamps/tags/body; exact revision is idempotent, differing same-ID content blocks. Durable Markdown is written before disposable FTS rebuild; FTS failure is reported without rolling back durable notes. Note/file content travels only via bounded SSH stdin and is never echoed or cached.

## Idempotent no-two-writer handoff

Persist strict private source and target journals keyed by random transfer ID + manifest digest. Sequence:

1. **Plan**: live source/target assessment, no mutation.
2. **Publish**: create source intent; revalidate fingerprint; push/verify exact branch OIDs.
3. **Reserve/stage B**: durably reserve the target identities, validate sidecar/path conflicts and fetch objects into private staging only. Do not publish a working checkout, provision, write portable files, or expose target tasks.
4. **Fence A**: from an external coordinator, require caller/runtime outside all selected checkouts. Acquire all selected branch/repository operation leases in deterministic order, thereby drain already-started current-version mutators; while still holding them, revalidate Git/sidecar revisions, record B machine ID + fencing epoch durably, normalize HOT→WARM and then release the locks. Every later current-version source mutation sees the fence and fails. Keep every A byte.
5. **Accept/publish B**: under the same target reservations/leases, require the exact epoch/digest, revalidate staging and target state, publish/create the required checkouts, apply optional deterministic portable files, and import catalog/notes/tasks idempotently. Do not run arbitrary provisioning. Set B owner and write the acceptance receipt last; only then release the target reservation so ordinary B commands can see a writable accepted state and later run local `dev resume` provisioning.
6. **Complete/reconcile**: lost response is resolved through `fleet transfer status`; no distributed rollback. A remains present but fenced until explicit eviction or ownership return.

Same ID+digest resumes; same ID+different digest rejects. `abort` is terminal only before source fence and cleans solely journal-owned staging. After fence, `return` first acquires B’s leases, quiesces the exact manifest, writes a durable `returned` tombstone keyed by ID/digest/epoch that permanently rejects delayed prepare/accept replays, then lets A verify it and CAS-unfence the unchanged source while advancing the epoch. If B is unreachable or indeterminate, A remains safely fenced; generic `resume --force` cannot override a transfer fence. This permits a zero-writer window but never intentionally two cooperative writers. Document that raw Git and old binaries can bypass dev’s lease.

# Backup, reclaim, restore, and eviction

## Backup receipt

Add a fresh `gitx` recovery snapshot that inventories all local `refs/heads/*`, tags, notes, stash and other refs/OIDs; dirty/untracked/ignored/unreadable paths; Git operations/worktrees; LFS; submodules/nested repositories; shallow/partial clone, alternates and external/shared Git storage. Do not derive eviction cleanliness solely from `StatusOf`/`AnalyzeFinish`: enumerate index flags (`assume-unchanged`, `skip-worktree`, sparse-index state) without mutating the real index, then read/hash every materialized tracked path through held roots into a raw-byte manifest. The restore drill must independently materialize the receipt and compare exact working-tree bytes; hidden edits, unsafe sparse entries, clean/smudge-filter differences or missing external filter behavior must either enter an immutable recovery artifact or block/require a named discard. Repeat the flag/raw-byte proof during final eviction under the operation leases. Separately enumerate every private GitDir/common-dir administrative entry (config, hooks, info/exclude/attributes, rr-cache, reflogs and unknown files) through held roots. Classify only proven Git-managed/regenerable entries as reconstructible; preserve each remaining regular file in a private recovery artifact outside the eviction roots or require an item-scoped discard. Credential-bearing config is never uploaded to a Git remote. `RecoveryTopology` remains descriptive and is never promoted to proof.

`backup --push` authorizes one resolved effective push endpoint, not merely a remote name. Resolve Git rewrites plus every `pushurl`; if more than one effective destination exists, fail closed and require a dedicated single-endpoint recovery remote. Show the sanitized endpoint and exact ref mappings, bind both to the plan fingerprint, re-resolve immediately before push, refuse non-fast-forward/conflicting writes, and verify mappings at that same endpoint. Stash/custom refs require explicit receipt-scoped recovery mappings; unresolved reflog-only/unreachable objects remain blockers unless separately acknowledged for discard. Write an append-only private schema-v1 receipt only after verification; it records endpoint-set digest, exact ref/OID mappings, object/admin-artifact manifest digests, exclusions, source fingerprint and protocol versions—not credentials/raw URLs.

## Reclaim and restore proof

`reclaim` joins fresh evidence for every checkout/root, ref/object, Git-administrative entry, ignored file, immutable portable-file recovery blob, task/fence, catalog/note revision, artifact, all available runtime backends, caller containment, owned/shared bytes and configured fleet state. Each potentially unique item must map to an immutable recovery artifact/receipt, retained XDG state outside every deletion root, or a separately named item/category discard acknowledgement; a mutable live B working copy alone never qualifies. Stale/no-dev/unreachable evidence is indeterminate, never pass.

`restore --verify` restores into an isolated new staging directory, fetches every receipt mapping, verifies exact refs/OIDs and reachable object closure, restores and byte-verifies the classified Git-administrative artifact at the correct post-clone phase, and checks LFS/submodule proof where supported. It also materializes every portable-file recovery blob used by the eviction gate without exposing contents in output. Unsupported LFS/submodule/nested/external-object layouts block eviction rather than weakening the claim. Regular `restore --apply` uses the same plan/revalidation to reconstruct a real destination. A backup receipt without a successful matching restore drill cannot authorize eviction.

## Journaled actual eviction

`evict` never runs inside transfer. It defaults to the same report as reclaim and requires explicit `--apply`, exact target, matching receipt, current verified restore, and exact confirmation (`YES`; noninteractive `--yes` only with explicit target). Named acknowledgements such as `--discard-ignored`, `--discard-dirty`, `--discard-unreachable`, `--override-keep` are independent; `--yes` does not imply data loss and there is no generic `--force`.

Under recovery/task/catalog/Git operation leases, evict re-runs every check, closes only eligible runtime surfaces from outside them, waits and re-enumerates all backends, and freezes every reviewed checkout/Git root. Apply filesystem custody to **each** owned linked worktree before removal: write a per-root intent, use a Git-aware move into an unguessable same-parent private staging path so registration/common-dir remain valid, revoke traversal/new opens, completely enumerate file and directory descriptors into that staged inode tree, wait for existing handles to close, rerun that worktree’s index-flag/raw-byte proof, then remove the staged registration while preserving its branch/common-dir. A linked root that cannot be moved/probed/reproved blocks instead of falling back to ordinary `git worktree remove`.

After all linked roots are safely retired, apply the same sequence to the ordinary canonical clone: write/finalize its intent, atomically rename it into same-parent private staging, revoke new opens, drain all handles, and rerun the full raw-byte/Git-administrative manifest proof under custody before physical removal. If handle enumeration is unavailable/incomplete, any writer remains, or evidence changes at any root, preserve every affected staged root/intent and return indeterminate for later reconcile—never continue on an acknowledgement disguised as proof. External git dirs, alternates, cross-filesystem/shared-common-dir ambiguity, and platforms without the custody/open-handle contract block actual eviction.

Only after target bytes are absent: durably record source deletion completion on A, have B acknowledge it to release the transfer ownership hold (not the recovery blobs), remove A task records whose exact portable revisions were accepted by B (transfer receipt retains audit data), mark A catalog location `evicted` with recovery/restore reference, and clear the intent. If B/recovery evidence becomes unreachable after staging rename but before physical deletion, preserve the staged A root and reconcile rather than continue. Retain catalog metadata, quick-note copies, Git-administrative recovery artifacts, portable-file blobs, stats and transfer/recovery receipts outside clone roots for their receipt lifetimes; deleting those durable recovery assets is a future separately scoped, confirmed purge. Other ignored data blocks or needs its named discard acknowledgement.

# Ordered implementation

1. **Freeze contracts and fixtures**
   - Define schema-v1 public JSON, hidden protocols, phase machines, reason-code registry, outcome precedence, exit-code/stdout/stderr rules and golden fixtures before service code.
   - Add stable machine ID, host pinning and capability negotiation; harden `fleet.Transport` for non-PTY bounded protocol I/O and idempotent mutating calls.
   - Define the operation-lease key/order/state machine first, inventory every current `dev` mutation entry point, and add a shared guard API so fencing cannot be retrofitted piecemeal.

2. **Security/file foundations**
   - Extract held-root traversal, stable regular-file read, portable path validation and atomic no-clobber/replace primitives from `repotemplate`; extend Unicode/Windows collision validation.
   - Add safe remote endpoint/web URL and structural redaction; ensure existing/new JSON cannot leak URL userinfo/query/fragment/control characters.

3. **Assessment and unified repo context**
   - Implement assessment types/policy/fingerprints and cheap/deep profiles.
   - Enrich `inventory.RepoContext`, add `repo context --json/--refresh`, preserve all collection errors and provenance, make `dev status` a cheap projection, and reuse the local summary in TUI/copy rendering.

4. **Portable local files vertical slice**
   - Add `[local_files]`, exact expansion, local/remote planners, strict protocol, private journal, create/current/conflict/replace apply, immutable recovery blobs, standalone `fleet files`, security and fault-injection tests.
   - Compose it into transfer only during target acceptance after source fence and exact target OID; keep `--with-local-files` default off.

5. **Operation leases, transactional sidecars and transfer planning**
   - Implement repository/branch operation leases and retrofit all current-version Git/worktree/artifact/task/local-file mutators to acquire/recheck/hold them through side effects; add barrier tests proving in-flight operations drain before fence.
   - Add task lock/CAS, exact-ID catalog/note import APIs, private transfer/object staging and portable DTOs.
   - Add target capability/assessment endpoints and report-only `fleet transfer`; prove it mutates neither host nor Git remote.

6. **Transfer apply/reconciliation**
   - Implement publish → private target reserve/stage → source fence → deterministic target publish/accept → status reconciliation, including ref-only COLD tasks, operation-owned partial-stage retry and exact OID checks.
   - Keep arbitrary provisioning outside acceptance; add the durable B-local per-step setup journal and `wt provision` reconciliation UI, extend `dev resume` to use it for accepted existing WARM checkouts, and never auto-replay an indeterminate command.
   - Add pre-fence `abort` and post-fence `return` terminal transitions that invalidate delayed epochs; ensure every success/failure retains A and verify B-local paths, catalog ID, notes and ownership.

7. **Backup/reclaim/restore**
   - Implement recovery snapshot, index-flag/raw-tracked-byte manifest, single-effective-endpoint all-ref verification/push receipts, Git-administrative artifact inventory, complete report-only reclaim, restore apply and isolated byte-for-byte restore drill.
   - Add LFS/submodule/nested/alternate detection; support only cases with conclusive proof and report the rest indeterminate/blocked.

8. **Eviction and reconciliation**
   - Add reviewed target freezing, final live revalidation, runtime drain, linked-worktree removal, same-parent staged clone deletion, immutable B-blob retention handshake, task cleanup, catalog eviction receipt and crash reconciliation.
   - Test every race/failure point and prove no uncommitted/unacknowledged data is removed.

9. **Product synchronization**
   - Update `CHANGELOG.md` `[Unreleased]`, README, help topics, embedded skill and authored references; add paired English/zh-TW transfer/recovery docs and update remote-fleet, worktree provisioning, task lifecycle, storage, compatibility and sources/freshness claims.
   - Run `make skill-sync`, inspect generated commands, regenerate `docs/llms*.txt`, and correct existing done/retire lifecycle prose drift in touched pages.

# Verification

Focused tests:

- assessment outcome/freshness/reason codes and additive JSON fixtures;
- safe URL derivation/redaction across GitHub/GitLab/Azure/enterprise/local/SSH aliases and hostile values;
- portable file glob expansion, ignored-at-both-ends, limits, path/case/Unicode collisions, symlink/reparse races, special files, create/current/replace CAS, rollback, immutable blob retention, no secret in output/cache/receipts;
- operation-lease ordering plus barrier-driven races against every current mutator before/after fence; task CAS, portable path stripping, HOT→WARM, catalog exact-ID/location conflict, note revision import;
- exact ref/OID publication for current, COLD and multiple serial branch tasks; dirty/ahead/diverged/ambiguous targets, private partial-clone retry, duplicate retries and every timeout/crash boundary;
- proof that transfer acceptance never invokes `post_create`/dependency provisioning, plus one B-local resume provisioning run after acceptance and indeterminate local setup reporting;
- pre-fence abort and post-fence return, lost return receipt, delayed old-epoch prepare/accept rejection and inability of generic `--force` to bypass a fence;
- heads/tags/notes/stash/custom refs, assume-unchanged/skip-worktree/sparse-index and clean-filter byte differences, effective multi-push endpoint refusal, remote races, reflog/unreachable objects, every Git-administrative entry, ignored data, LFS/submodule/nested/partial/alternate/shared Git;
- restore drill fingerprint invalidation, exact restore after A eviction plus B working-copy modification/deletion/GC, caller/runtime reinspection, pre-existing writable file/directory descriptors across both linked-worktree and canonical staging moves, unsupported/incomplete handle probes, multi-root staged eviction rollback/reconcile and task/catalog finalization.

Build fake-SSH two-machine integration tests using one test binary with distinct `HOME`, XDG config/data/cache, project/worktree roots and independent task/catalog/note stores. Cover plan-no-mutation, capability before secret payload, new/existing target clone, COLD/unattached tasks, different paths, note/local-file transfer, password retry idempotency, lost acceptance/return responses, machine-ID mismatch, old/no remote dev, target/source drift, A retained until evict, target recovery blob retained through A deletion, and exact post-evict restore.

Run the full repository gates:

```bash
go test ./internal/assessment ./internal/lease ./internal/localfiles ./internal/transfer ./internal/recovery
go test ./internal/fleet ./internal/inventory ./internal/forge ./internal/gitx
go test ./internal/task ./internal/catalog ./internal/note ./internal/retire ./internal/cli ./internal/tui
go test -race ./...
go vet ./...
files="$(gofmt -l .)" && test -z "$files"
go build ./cmd/dev
GOOS=windows GOARCH=amd64 go build ./cmd/dev
make e2e
make skill-sync
make skill-check
uv sync --frozen --extra docs
uv run python scripts/check-docs.py --source
uv run mkdocs build --strict
uv run python scripts/check-docs.py --site site
git diff --check
```

Finally run a manual disposable two-host SSH smoke test with a private test remote and test `.env`/`.mcp` files: verify content never appears in output, A always remains after transfer failures/success, B receives exact code/tasks/catalog/notes/opt-in files, restore drill reconstructs every protected ref, and only explicit final `evict --apply` removes A.

# Critical files

- Existing command/domain seams: `internal/cli/fleet.go`, `internal/fleet/{sync,transport,types,config}.go`, `internal/inventory/repo_context.go`, `internal/cli/{repo,status,tui}.go`.
- Git/filesystem safety: `internal/gitx/{repo,status,topology,worktree,finish,transactions}.go`, `internal/repotemplate/snapshot.go`, `internal/pathx/pathx.go`, `internal/retire/{safety,service}.go`, `internal/diskusage/{usage,scanner}.go`.
- State/import: `internal/task/{task,store}.go`, `internal/catalog/{catalog,store,registry}.go`, `internal/note/{note,store,service}.go`, `internal/artifact/*`, `internal/projectconfig/*`.
- New vertical slices: `internal/assessment/*`, `internal/lease/*`, `internal/localfiles/*`, `internal/transfer/*`, `internal/recovery/*`, plus focused CLI files named above.
