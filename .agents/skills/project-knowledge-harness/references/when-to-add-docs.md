# When to add a backlog or pitfall doc, and the upgrade path

## Backlog: when an entry needs `backlog/<slug>.md`

Add a backlog doc when **any** apply:

- `P?` priority (record what was tried so it doesn't need re-investigation)
- Captures paused troubleshooting **that you intend to fix later** (preserve
  the error trace + root-cause analysis before context evaporates)
- Multiple options were considered (record trade-offs, not only the winner)
- `L` or `XL` effort (architectural; needs design before code)

`S` items rarely need a backlog doc — a file path in the TODO line is usually
enough.

## Pitfalls: when a debugging session deserves `pitfalls/<slug>.md`

Add a pitfall doc when you've spent more than ~15 minutes on something that
wasn't googleable, AND any of:

- The symptom is non-obvious from the root cause (silent state, weird side
  effect, behaviour change without error)
- The fix is "do nothing different but in a specific order" (sentinel writes
  must come after process completion, etc.)
- The same trap could be hit by a new agent / new machine / new contributor
- An upstream bug exists with no ETA — workaround needs to outlive memory
- A specific tool version is required or forbidden, and failure at the wrong
  version is silent / confusing

Skip a pitfall doc when:

- Trivially googleable (next person solves in 30 seconds)
- Already covered as part of normal config docs in `docs/<tool>.md` —
  cross-link from `pitfalls/README.md`'s "Cross-referenced pitfalls" table
  instead of duplicating
- Already a Hard invariant in `AGENTS.md` — those have higher enforcement
  (cross-link only)
- One-off transient (network glitch, machine-specific config rot)

## Disambiguation: pitfall vs backlog vs TODO entry vs invariant

| Situation | Goes in |
|---|---|
| "We hit X, debugged it, applied workaround, moving on" | `pitfalls/` |
| "We hit X, debugged it, but the real fix is queued" | `pitfalls/` (capture trace) AND `TODO.md` (queue the fix) — link both ways |
| "We thought about doing X but deferred" | `TODO.md` (P2/P3) |
| "We thought about doing X, did a 2-day spike, deferred" | `TODO.md` (P?) + `backlog/` |
| "X is a rule everyone must follow forever" | `AGENTS.md` Hard invariant |

## Upgrade path: pitfall → Hard invariant

A pitfall **graduates** to a Hard invariant in `AGENTS.md` (or the project's
agent contract file) when:

- It recurs across machines / agents / sessions despite being documented
- The trap silently corrupts state (no error message, just wrong behaviour)
- The workaround is non-obvious enough that "remember to do X" is unsafe

When graduating:

1. Add the rule to `AGENTS.md` Hard invariants section
2. Link from the invariant back to `pitfalls/<slug>.md` for context
3. Leave the pitfall doc as historical record (don't delete — it explains
   *why* the invariant exists)
