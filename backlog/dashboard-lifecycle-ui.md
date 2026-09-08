# Deferred dashboard lifecycle UI alternatives

Status: deferred by user choice, 2026-09-08.

The shipped approach uses Ctrl+O/Space/right-click row actions and suspends the
inventory Dashboard into shared CLI workflows. It retains the full done/retire
wizard and avoids adding a second implementation of lifecycle policy.

Two alternatives remain explicitly deferred:

1. Dashboard-native forms (P3, L): render taskflow Plan conditions/effects,
   required input, exact approval tokens and partial result ledgers inside
   `internal/tui`. Do not reproduce lifecycle policy in handlers. Cover stale
   revisions, blocked/unknown observations, cancellation and post-exit handoffs.
2. Focused dev flow handoff (P3, M): open the independent `internal/flowtui`
   model at an exact repository/task/checkout, including missing-checkout rows.
   Define a typed launch target and structured return. Preserve flow's preview
   restrictions rather than silently adding CLI expert overrides.

Both alternatives must preserve the action target across refreshes and keep
`flowtui` independent of the dashboard model. Before implementation, assess
whether the shared workflow bridge already provides sufficient usability.

Related: TODO's verified backup receipts and safe local eviction remain separate
from explicit Try Trash/permanent disposal.
