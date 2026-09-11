# Hygiene snapshot inherits Git hook environment and changes the caller index

Symptoms during pre-release dogfood: `coverage: index_changed`, pre-commit's
`files were modified by this hook`, script modes changing from staged 100755 to
100644, and a shared non-bare repository's `core.bare` becoming true.

The scanner created an index-only private Git snapshot. Running `git -C <snapshot>`
was insufficient inside a real Git hook: exported `GIT_DIR`, `GIT_INDEX_FILE`,
object-directory and configuration variables still referred to the caller. A
snapshot `git init` or `update-index` could therefore change caller metadata.
The revalidation gate rejected the commit, but a detector must not cause the
mutation it detects.

The corrected boundary keeps the caller environment for index reads, including
`git commit --only` temporary indexes. Every temporary Git mutation uses a fresh
environment with inherited `GIT_*` values removed and controlled config/pager
settings. The gitleaks subprocess has the same isolation. ExecRunner's optional
UnsetEnv removes named inherited variables without changing other callers.

Regression tests retain an alternate index, executable modes and the entire
caller Git config byte-for-byte. The real hook test checks these invariants too;
a canary file with only mode 100644 would not expose the original bug.

This incident occurred before the hygiene feature was committed or released.
The canonical repository was known to be non-bare. Its `core.bare=false` was
restored with a private pre-repair config backup, and the three script modes were
re-staged from unchanged working files. Branch refs, worktrees, source bytes and
the canonical active transcripts were retained. Recovery must restore known prior
metadata; do not infer that an intentionally bare repository should be converted.
