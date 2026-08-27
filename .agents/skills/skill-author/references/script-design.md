# Script design for agent skills

Condensed from <https://agentskills.io/skill-creation/using-scripts>. Read this
before adding anything to `scripts/` in a skill.

The headline difference from "regular" CLI design: **the caller is an LLM that
reads your stderr to decide what to do next.** Optimize for that.

## Table of contents

1. [One-off commands vs bundled scripts](#one-off-commands-vs-bundled-scripts)
2. [Pin versions for `uvx` / `npx` / `pipx`](#pin-versions)
3. [Self-contained scripts (PEP 723 etc.)](#self-contained-scripts)
4. [Agentic CLI design rules](#agentic-cli-design-rules)
5. [Error message style](#error-message-style)
6. [Structured output](#structured-output)
7. [Idempotency, dry-run, exit codes](#idempotency-dry-run-exit-codes)
8. [Bash 3.2 compatibility (macOS)](#bash-32-compatibility-macos)
9. [Quick script checklist](#quick-script-checklist)

---

## One-off commands vs bundled scripts

If a published tool already does the job and the invocation is short, **don't
bundle a script** — reference the tool directly:

```bash
uvx ruff@0.8.0 check .
npx eslint@9.0.0 --fix .
```

Bundle a script when:

- The invocation grows past ~3 flags or needs computed arguments.
- You need to compose multiple tools.
- The logic must be tested independently.
- You want to give the agent a stable interface even if the underlying tool
  changes.

State runtime prerequisites in `SKILL.md` ("Requires Node 18+", "Requires
`uv`"). Don't assume the agent's environment.

---

## Pin versions

Unpinned `uvx`, `npx`, `pipx` invocations behave inconsistently as upstreams
release new versions. Always pin:

```bash
# Good
uvx ruff@0.8.0 check .
npx eslint@9.0.0 --fix .
pipx run 'black==24.10.0' .

# Bad — silently breaks when upstream ships a breaking change
uvx ruff check .
npx eslint --fix .
```

`@latest` is fine **if** the skill explicitly says "tracks upstream latest" and
the user understands that.

---

## Self-contained scripts

When a script needs dependencies, prefer **inline declarations** over a
sibling `requirements.txt` / `package.json`. The agent can run the script with
one command and no setup.

### Python — PEP 723

```python
#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "beautifulsoup4>=4.12,<5",
# ]
# ///
"""Extract <p class="info"> from an HTML file."""
import sys
from bs4 import BeautifulSoup

with open(sys.argv[1]) as f:
    html = f.read()
print(BeautifulSoup(html, "html.parser").select_one("p.info").get_text())
```

Run with `uv run scripts/extract.py file.html`. `uv` creates an isolated
environment, installs declared deps, and runs — every time, deterministically.

For full reproducibility, `uv lock --script scripts/extract.py` produces
`scripts/extract.py.lock`.

### Deno

```typescript
#!/usr/bin/env -S deno run
import * as cheerio from "npm:cheerio@1.0.0";
// ...
```

### Bun

```typescript
#!/usr/bin/env bun
import * as cheerio from "cheerio@1.0.0";
// ...
```

If a `node_modules/` exists anywhere up the tree, Bun's auto-install is
disabled. Mention this in the skill if relevant.

### Ruby

```ruby
require 'bundler/inline'
gemfile do
  source 'https://rubygems.org'
  gem 'nokogiri', '~> 1.16'
end
# ...
```

---

## Agentic CLI design rules

### No interactive prompts, ever

Agents run in non-interactive shells. A script that calls `read`, `input()`,
or any TTY prompt **hangs indefinitely**. There is no recovery.

```
# Bad
$ python scripts/deploy.py
Target environment: _

# Good
$ python scripts/deploy.py
Error: --env is required. Options: development, staging, production.
Usage: python scripts/deploy.py --env staging --tag v1.2.3
```

Accept all input via flags, env vars, or stdin (non-blocking — check `isatty`).

### `--help` is the primary documentation

The agent reads `--help` to learn the script. Make it concise but complete:

```
Usage: scripts/process.py [OPTIONS] INPUT_FILE

Process input data and produce a summary report.

Options:
  --format FORMAT    Output format: json, csv, table (default: json)
  --output FILE      Write output to FILE instead of stdout
  --verbose          Print progress to stderr
  --dry-run          Show what would happen without doing it

Examples:
  scripts/process.py data.csv
  scripts/process.py --format csv --output report.csv data.csv

Exit codes:
  0  success
  1  invalid arguments
  2  input file not found
  3  processing failed
```

The exit-codes block matters — the agent uses them to branch retry behavior.

---

## Error message style

When an agent gets an error, the message **directly shapes its next attempt**.
Opaque errors waste turns.

```
# Bad
Error: invalid input

# Good
Error: --format must be one of: json, csv, table.
       Received: "xml"
```

Pattern: **what was wrong + what was expected + what to try.**

For "field not found" style errors, include the available alternatives in the
message — this lets the agent self-correct without re-querying the system:

```
Error: Field 'signature_date' not found.
       Available fields: customer_name, order_total, signature_date_signed.
```

---

## Structured output

Prefer JSON / CSV / TSV / NDJSON over whitespace-aligned columns. Structured
output is parseable by both the agent and standard tools (`jq`, `cut`, `awk`).

```
# Bad
NAME          STATUS    CREATED
my-service    running   2025-01-15

# Good
{"name": "my-service", "status": "running", "created": "2025-01-15"}
```

**Separate data from diagnostics.** Send structured data to stdout; send
progress, warnings, debug logs to stderr. The agent can then `jq` stdout
cleanly while still seeing diagnostics if it wants.

Bash convention:

```bash
log() { printf '%s\n' "$*" >&2; }   # diagnostics → stderr
log "Loading config from $CONFIG"
printf '{"status":"ok"}\n'           # data → stdout
```

---

## Idempotency, dry-run, exit codes

### Idempotency

Agents retry. "Create if not exists" beats "create and fail on duplicate".
"Apply migration if not applied" beats "apply migration".

### `--dry-run`

For destructive or stateful operations, `--dry-run` lets the agent preview
and the user confirm. Implement it as a real preview (show what would change),
not a no-op:

```
$ scripts/deploy.sh --dry-run --env staging --tag v1.2.3
[dry-run] Would deploy v1.2.3 to staging.
[dry-run] Would update 3 services: api, worker, scheduler.
[dry-run] Would run post-deploy hook: scripts/healthcheck.sh.
```

### Exit codes

Use distinct codes for distinct failure types. Document them in `--help`.
This lets the agent branch retry/recover behavior:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | invalid arguments / usage |
| 2 | required input missing or not found |
| 3 | external system failure (network, auth, dependency) |
| 4 | validation failed |
| ≥10 | reserved for skill-specific conditions |

### Predictable output size

Many agent harnesses truncate tool output past ~10-30K characters. If your
script can produce large output:

- Default to a summary or a sane limit
- Provide `--offset` / `--limit` for pagination
- Or require `--output FILE` so the agent has to opt in to stdout

---

## Bash 3.2 compatibility (macOS)

Stock macOS ships bash 3.2. If the script may run on macOS without Homebrew
bash, avoid:

| Don't use | Use instead |
|---|---|
| `mapfile -t arr < file` | `while IFS= read -r line; do arr+=("$line"); done < file` |
| `${var,,}` (lowercase) | `tr '[:upper:]' '[:lower:]' <<< "$var"` |
| `${var^^}` (uppercase) | `tr '[:lower:]' '[:upper:]' <<< "$var"` |
| `[[ -v VAR ]]` | `[ "${VAR+x}" = "x" ]` |
| Associative arrays `declare -A` | indexed array + key parser, or switch to Python |
| `wait -n` (wait for any) | `wait` (waits for all) or track PIDs manually |
| `&>` redirection | `>file 2>&1` |

Lead every script with:

```bash
#!/usr/bin/env bash
set -euo pipefail
```

`set -u` catches typos in variable names. `set -e` aborts on first error.
`set -o pipefail` makes pipelines fail if any stage fails (otherwise only the
last stage's exit code matters).

If you need bash 4+ features, either:

- Switch to Python with PEP 723 (recommended for anything non-trivial).
- Add an explicit version check at the top:

```bash
if (( BASH_VERSINFO[0] < 4 )); then
  echo "Error: requires bash >= 4. macOS ships 3.2; install via 'brew install bash'." >&2
  exit 1
fi
```

---

## Quick script checklist

- [ ] Has `#!/usr/bin/env bash` (or equivalent) shebang
- [ ] Is `chmod +x`
- [ ] Starts with `set -euo pipefail` (bash) or equivalent strict mode
- [ ] Responds to `--help` with usage + flags + examples + exit codes
- [ ] No interactive prompts; all input via flags / env / stdin
- [ ] Errors say *what + expected + try this*
- [ ] Data → stdout (structured), diagnostics → stderr
- [ ] `--dry-run` for any destructive operation
- [ ] Exit codes are distinct and documented
- [ ] Pinned versions for any `uvx` / `npx` / `pipx` invocations
- [ ] Python helpers use PEP 723 inline deps (not external `requirements.txt`)
- [ ] Bash 3.2 compatible if it might run on stock macOS
- [ ] Idempotent where possible
