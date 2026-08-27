# Tests for agent-history-hygiene

Run the full suite:

```bash
# From repo root
make test-skill

# Or directly
uv run --extra dev pytest skills/local/agent-history-hygiene/tests/ -q
bash skills/local/agent-history-hygiene/tests/test_scan_staged.sh
```

## Layout

- `fixtures/` — paired `.md` files exercising gitleaks rules + allowlist.
  Each filename encodes its expected behavior (e.g. `real_anthropic.md`
  should fire, `example_shapes.md` should be allowlisted when staged
  inside an agent-artifact directory).
- `conftest.py` — shared pytest fixtures; loads the PEP 723 script
  `assets/redact_secrets.py` as an importable module.
- `test_redact_secrets.py` — unit tests for pure functions in the
  redactor (`redact_secret`, `filter_by_prefixes`,
  `find_private_key_files`, `redact_file`, `redact_private_keys`).
- `test_gitleaks_corpus.py` — golden-corpus tests: stages each fixture
  in a throwaway git repo and asserts `gitleaks git --staged` returns
  the expected findings (or lack thereof). Skipped if `gitleaks` is
  not on `PATH`.
- `test_scan_staged.sh` — exit-code contract for
  `scripts/scan-staged.sh`: `0` clean, `20` leak, `30` missing
  gitleaks, `2` not a git repo.

## Test-vector hygiene (why the fixtures carry `gitleaks:allow`)

The firing fixtures (`real_anthropic.md`, `real_openai.md`, `private_key.md`,
`webhook_urls.md`) hold deliberately-fake test vectors. Most use realistic
secret shapes plus an inline marker so a repo-root, downstream, or
`npx skills add`-installed gitleaks scan **skips them by default**:

```
<SECRET> <!-- gitleaks:allow -->                      # markdown fixtures
"-----BEGIN RSA PRIVATE KEY-----\n"  # gitleaks:allow  # test_redact_secrets.py
```

The Stripe webhook fixture is stricter: GitHub's provider-level scanner does
not honour gitleaks markers, so its source uses
`__SYNTHETIC_STRIPE_WEBHOOK_SECRET__`. The corpus test expands that placeholder
inside its throwaway repo. The other corpus + shell tests **strip the marker
before staging** (`_GITLEAKS_MARKER_RE` in `test_gitleaks_corpus.py`; the `sed`
in `stage_fixture` in `test_scan_staged.sh`), so the secret bytes have the exact
shape the rules expect and the assertions still fire. Principle: **fake test
vectors are out of scan scope by default, and only "fire" when a test re-plants
them.**

Invariant — **the strip must be byte-identical**: never change a secret's length
or the length-sensitive rules (`sk-proj-…{80,}`, `sk-ant-api\d{2}-…{93}AA`, the
webhook regexes) stop matching. When adding a new firing fixture, append the
marker with a single leading space (`<SECRET> <!-- gitleaks:allow -->`); the
strip regex reclaims exactly that separator. `clean.md` / `example_shapes.md`
carry no marker (the strip is a no-op) and must keep their must-not-fire
behavior. GitHub-native secret scanning ignores the marker; it's excluded
separately via the repo's `.github/secret_scanning.yml`. This replaced a former
repo-root `.gitleaksignore` whose `file:rule:line` fingerprints silently drifted.

## Why these three levels

The skill already surfaced three regressions during initial hand
verification — each one would have been caught here:

| Regression                                                  | Caught by                |
|-------------------------------------------------------------|--------------------------|
| Allowlist defaulted to `OR` between paths + regex           | `test_gitleaks_corpus`   |
| `[allowlist]` singular clashed with `[[allowlists]]` plural | `test_gitleaks_corpus`   |
| `gitleaks dir` silently skipped hidden paths                | `test_redact_secrets`    |
| JSON report `[]` treated as "leaks found"                   | `test_scan_staged.sh`    |

## Requirements

- `uv` (for `uv run --extra dev pytest`)
- `gitleaks` on `PATH` (corpus tests + shell test skip gracefully when
  missing — the pytest file xfails with a clear message, the shell
  test exits 0 for the "no gitleaks" case)
- `git` configured with a user identity (the shell test uses
  `-c user.email=...`)
