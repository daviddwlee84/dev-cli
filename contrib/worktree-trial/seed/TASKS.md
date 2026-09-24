# Identical tasks for both routes

Implement each task in its own worktree/branch starting at `trial-seed`.
Use the same agent/model and prompt on both routes if comparing workflow effort.
Do not copy one route's solution into the other. Keep ordinary `go test ./...`
passing and add focused regression tests for your implementation.

## Task 1: JSON output

Add `--json`. Read stdin as before and emit one JSON object containing exactly
the integer keys `lines`, `words`, and `bytes`, followed by a newline. JSON key
order does not matter. No progress text belongs on stdout. Without the flag,
preserve the baseline text output. Unknown flags must still fail with stderr
and no stdout. Do not change word-counting semantics as part of this task.

Example: input `one two\nthree\n` gives
`{"lines":2,"words":3,"bytes":14}`. Empty input gives all zeroes.

Acceptance from the pilot root:
`python3 acceptance.py --task json --repo /absolute/path/to/this/worktree`

## Task 2: Unicode whitespace

Change the default word counter to treat Unicode White_Space characters as
separators, in addition to ASCII whitespace. Preserve the output format, raw
byte count, and LF-only line rules. Emoji and CJK text between separators count
as ordinary words, not individual letters. Reject invalid UTF-8 with nonzero
exit status, a diagnostic on stderr, and no stdout. This task is independent
of Task 1; it does not require `--json`.

Examples: `one\u00a0two\u2003三\n` has 1 line, 3 words, 15 bytes;
`你好 世界\n` has 1 line, 2 words, 14 bytes. `a\u2028b` has 1 line and
2 words because U+2028 separates words but is not LF.

Acceptance from the pilot root:
`python3 acceptance.py --task unicode --repo /absolute/path/to/this/worktree`
