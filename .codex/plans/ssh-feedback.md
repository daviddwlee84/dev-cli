# SSH diagnostics and feedback implementation

Approved plan: deliver independent SSH-entry context hotfix, then explicit
three-platform layered diagnostics, then agent-first feedback and local repair.

- Hotfix: inherit the Cobra execution context before direct child RunE dispatch;
  regress through the interactive root, preserving deadline and cancellation.
- Diagnostics: explicit `ssh diagnose`, 60s total bounded stages; native
  macOS/Linux/Windows routes; fresh strict OpenSSH; optional evidenced QoS
  comparison; private local JSON and a distinct allowlisted public projection.
- Feedback: durable local draft, reviewed optional GitHub issue/comment, guarded
  isolated repair plan/apply, and an existing-agent prompt recipe. Reuse start,
  repository identity, task/worktree policy and optional gh. Keep unknown remote
  outcomes retry-safe. No automatic agent launch, push, merge or cleanup.
- Agent workflow: prepare minimal evidence without interrupting the original
  task; request explicit consent before deep investigation, repairs or added
  subagents. Existing scoped consent persists. This implementation is performed
  by the current agent only, per the user's explicit preference.
- Sync changelog, embedded help/skill, paired EN/zh-TW docs and backlog status.
  Run focused tests, vet/race suite, generated skill checks and strict docs.
  Land/release each stage independently when remote access is available.

Current limitations are recorded in implementation results, not silently treated
as successful publication or platform support. Keep workspaces and branch history
for recovery; preserve the original canonical checkout's active transcript and
unrelated generated statistics.
