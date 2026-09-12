# AI artifacts and SpecStory history policy

Approved 2026-09-13. Current agent only; keep canonical live transcripts and all
unrelated sessions untouched. Base main at 4570ce2b547b975f1175ea13babdeb1cbb843637.

Delivery sequence:
1. Exclude .specstory from source packages; portable ai-artifacts help and paired
   public guides. Prove exported sources build; publish a standalone patch.
2. Add agenthistory domain and artifact status/setup/archive/find; explicit
   source=specstory|files, per-repo track/archive/unmanaged policy, in-place or
   external capture, off/check/redact copy protection. Preserve native source
   files and source index. Use ordinary external Git archives and reviewed plans.
   Integrate repository setup and prepare/finalize/retire receipts. Preserve old
   intents; source code hygiene never changes implicitly with archive policy.
3. Add untrack and split-history migration: verified original backup, separate
   filtered copy, original/new commit mapping, exact-path coverage and recovery.
   Optional backup remote, separate publication; never rewrite original remote
   or release tags automatically. Dogfood only isolated copies.

Identity and privacy: local native identity guards are separate from portable
project/session identity. No raw secrets in console/JSON. Explicitly unscanned
private archives need no scanner. Checked/redacted snapshots reuse scans only
when content/path/engine/rules/policy match; incomplete scans never pass.

Help command names stay artifact; source is the recorder, provider is the agent.
dev help ai-artifacts, artifact and specstory share the new policy topic.
retirement still owns writer exit and lifecycle safety. No new agent launcher,
mandatory SpecStory/cloud dependency, automatic knowledge extraction or disposal.

Verification: meaningful native archive/policy/recovery/lifecycle tests, large
and changing files, immutable-source redaction, stale-plan refusal, isolated
history migration restore checks, source archive build, skill synchronization,
paired strict documentation and relevant CI. Keep partial results honest.

## Delivery checkpoint

- Packaging/help PR #24 merged at b2a614a7f7745f6b9057a91ebf2dc229cea26365.
  v0.2.32 is published; all six platform archives and Homebrew verified.
  Actual GitHub source download: 3,495,618 bytes, no .specstory. SHA256 matches
  the formula: 62fd4e9bfe03f95ef69c9668e7136bd2e7e8eb107dd7060c2f5bb45e902bf806.
- Active follow-up adds agenthistory, immutable hygiene snapshots, policy/setup,
  archive/find, sync/backup, migration and legacy lifecycle integration. Local
  focused domain/CLI and isolated filter/restore tests pass. Complete native
  review, full checks, docs/skill synchronization and frozen-copy dogfood remain
  required before landing the follow-up. Canonical live histories stay untouched.
