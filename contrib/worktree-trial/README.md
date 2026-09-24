# Herdr/dev and Sidecar worktree trial

This is a reproducible pilot, not an agent benchmark result. It creates a tiny
Go `textstat` project, one local bare origin, and two independent clones at
the same `trial-seed` commit. Both routes implement the same unfinished JSON
output and Unicode whitespace tasks. No external Go modules are needed.

Requires Python 3.11+, Git, and Go 1.22+ for the sample. Herdr, dev, Sidecar,
and an agent are only needed for the respective **manual** route. Nothing here
launches them automatically, initializes td, or creates a dev Task.

## Create the pilot

Choose a physical directory outside every Git checkout. Its parent must
already exist; the pilot path itself must not exist, even as an empty directory.
Symlink ancestors, `..`, existing files, and roots inside bare repositories
are refused. On macOS use the physical parent, not `/tmp` or another alias.

From the dev-cli source checkout:

```sh
python3 contrib/worktree-trial/trial.py create --root /absolute/external/pilot --dry-run
python3 contrib/worktree-trial/trial.py create --root /absolute/external/pilot
```

Both commands print one JSON object. Creation never overwrites/reuses a root.
A failed creation leaves its partial root for inspection; choose a fresh name
to retry. There is intentionally no persistent-pilot cleanup command.

The root contains `A-herdr-dev/`, `B-sidecar/`, `origin.git`, a retained `seed/`
checkout, isolated `dev/` and `sidecar/` configuration/state, `acceptance.py`,
route checklists, `feedback.json`, and `worktree-trial.json` with exact paths and
the seed commit. The feedback starts at `not_run` with unanswered fields null.
The manifest describes this generated fixture; it is not a new task registry.

Before running the following examples, set `PILOT` to the generated physical
root, for example `PILOT=/absolute/external/pilot`.

## Optional shared navigation source

If the chezmoi `dev-projects` helper is installed, use the generated dev config
as the source of the two trial repositories. `XDG_CONFIG_HOME="$PILOT"` selects
the existing `$PILOT/dev/config.toml`; it does not change the real dev config.

```sh
XDG_CONFIG_HOME="$PILOT" dev-projects list --json
```

Preview registration of **only B** in the isolated Sidecar configuration:

```sh
env -u TMUX TMUX_TMPDIR="$PILOT/sidecar/tmux" \
XDG_CONFIG_HOME="$PILOT" XDG_STATE_HOME="$PILOT/sidecar/state" \
dev-projects sidecar-import --config "$PILOT/sidecar/config.json" --repo "$PILOT/B-sidecar" --json
# After reviewing the add-only result, explicitly apply the same selection:
env -u TMUX TMUX_TMPDIR="$PILOT/sidecar/tmux" \
XDG_CONFIG_HOME="$PILOT" XDG_STATE_HOME="$PILOT/sidecar/state" \
dev-projects sidecar-import --config "$PILOT/sidecar/config.json" --repo "$PILOT/B-sidecar" --apply --yes --json
```

This is an alternative to the direct `project add` below; use one registration
route. An existing same-path project should be reported present. These are
manual examples, not a bulk import or a generator side effect. Do not omit
`--config` or the isolated state directory during the pilot.

## Run both routes manually

Set `PILOT` to the generated physical root in your shell. Use the same agent,
model, task prompt, and acceptance checks in both routes; record actual results
in `feedback.json`. Do not copy the first route's answer into the second.

First run `go test ./...` in each clone. The external acceptance checks are
expected to fail on the baseline: the two requested features are not implemented.

If a host reports that its Go compiler version does not match its source tree,
use `env -u GOROOT GOTOOLCHAIN=go1.26.4 go test ./...` for this trial. This was
needed on the initial macOS test host; it does not rewrite shell configuration.
The acceptance helper already drops an inherited `GOROOT` and builds outside
the checkout.

**A: Herdr plus task-free dev commands.** Use the generated config explicitly
on every dev invocation. Creation prepares a worktree and runtime surface;
open activates it. Launch your chosen agent manually after opening.

```sh
cd "$PILOT/A-herdr-dev"
dev --config "$PILOT/dev/config.toml" git worktree create trial/a-json --base trial-seed --no-provision
dev --config "$PILOT/dev/config.toml" git worktree open trial/a-json
```

Read `TASKS.md`, implement Task 1, and run the command below using the exact
worktree path printed by dev. For Task 2 repeat from `trial-seed` with branch
`trial/a-unicode`; use `--task unicode` in the acceptance command.

```sh
python3 "$PILOT/acceptance.py" --task json --repo /exact/worktree/path
```

Use `dev git worktree list` with the same config to rediscover the worktree.
Do not use `dev start`, `dev adopt`, or hot/warm/cold task bookkeeping.

**B: Sidecar worktree workflow.** The generated `{}` config lets Sidecar use
its native defaults; config and runtime state stay within this pilot. Register
the clone in this isolated config before opening it (skip `project add` if
the optional import above already registered it). The empty `sidecar/tmux`
directory isolates native tmux sockets; `env -u TMUX` avoids inheriting a
current tmux session. The generator does not start a tmux server.

```sh
cd "$PILOT/B-sidecar"
env -u TMUX TMUX_TMPDIR="$PILOT/sidecar/tmux" \
XDG_STATE_HOME="$PILOT/sidecar/state" \
sidecar --config "$PILOT/sidecar/config.json" project add B-sidecar --path "$PILOT/B-sidecar" --json

env -u TMUX TMUX_TMPDIR="$PILOT/sidecar/tmux" \
XDG_STATE_HOME="$PILOT/sidecar/state" \
XDG_DATA_HOME="$PILOT/sidecar/data" \
XDG_CACHE_HOME="$PILOT/sidecar/cache" \
sidecar --config "$PILOT/sidecar/config.json" --project "$PILOT/B-sidecar"
```

Use Sidecar's native workflow to create `b-json` and `b-unicode` from
`trial-seed`. Sidecar creates siblings of its main checkout; ensure the actual
paths remain under `$PILOT` and record them. This pilot does not assume an
arbitrary destination option. Run the same
acceptance checks with those paths. Keep this route's creation/cleanup owned
by Sidecar while evaluating it. If Sidecar requires a tmux surface or changes
focus under Herdr, record that friction. Companion browsing alone does not
establish that the integrated workflow was tested. td is optional and not
initialized by this fixture; evaluate it separately if useful.

The bare origin is **local only**. Push individual feature branches when
testing published-but-unmerged work. Do not push either route's local `main`
to it: each route can merge locally while the other remains at its seed.
There is no forge PR in this fixture. Treat “PR open” as **retain checkout**;
an unmerged pushed branch tests that Git boundary, not a live PR integration.

## Review and cleanup

Agent idle/done, td completion, and a pushed branch are not proof of integration
or stopped transcript writers. Stop the actual agent and recorder explicitly,
preserve the exact selected transcript after its final tail, then leave the
target checkout and its runtime before cleanup. Never select `history/*.md`
or the newest file as a substitute for exact session/file identity.

For A, after explicitly merging the chosen branch into **A's local main**, run
this from its canonical checkout and an outside runtime:

```sh
cd "$PILOT/A-herdr-dev"
dev --config "$PILOT/dev/config.toml" work sweep --merged-worktrees --base main --delete-branches
# Review the exact proposed paths, then separately apply:
dev --config "$PILOT/dev/config.toml" work sweep --merged-worktrees --base main --delete-branches --apply
```

The explicit `--delete-branches` choice removes contained local branches after
successful checkout cleanup, keeping Lazygit's branch list clear. Review the
branch deletion effects as well as the paths at the manual confirmation.
Remote branches are not deleted. The separate automated sanity case omits this
flag to verify ordinary branch preservation. `git worktree remove`/`dev wt rm` are direct actions, not
preview commands. Squash-merge recognition is **unresolved in this pilot**:
main ancestry containment does not prove a squash merge. Record the block
instead of using force or treating PR/td state as proof.

Use Sidecar's reviewed native cleanup for B and explicitly request local branch
deletion after integration; then inspect `git worktree list` and local branch
refs. A Sidecar cleanup failure is a result to record; do
not silently reroute it through dev and call B successful.

## Automated disposable fixtures

Build the current dev binary into an external temporary output and pass its
absolute path. The harness never searches for or closes a live runtime.

```sh
go build -o /absolute/temporary/dev ./cmd/dev
python3 contrib/worktree-trial/trial.py check --dev /absolute/temporary/dev --dry-run
python3 contrib/worktree-trial/trial.py check --dev /absolute/temporary/dev
```

`check` accepts **no cleanup root**. It creates its own temporary fixture,
verifies its manifest/root/clone identities before dev calls, supplies an
isolated HOME/XDG/dev config, and uses `--no-runtime --assume-no-runtime` only
because the harness launches no agents, sessions, or child servers. Do not
copy that runtime acknowledgement into real pilot cleanup.

The check proves these observable public-CLI outcomes:

- A merged clean worktree is removed; its branch remains.
- A separately merged worktree and its local branch are removed when branch
  deletion is explicitly requested; the first sanity case's branch remains.
- A pushed unmerged branch retains its worktree.
- Uncommitted product code remains intact.
- An exact synthetic ignored SpecStory file retains its final tail despite
  clean `git status`, with native archive policy configured and no receipt.
- A branch advanced after a preview remains on the later fresh sweep.

The last case uses **two CLI invocations**, each planning from current facts.
It does not test applying an old immutable plan; taskflow's existing Go tests
cover that separate stale-plan contract. Exit status alone is insufficient
because sweep can report blocked items successfully; the harness also checks
Git registrations, branch refs, and preserved bytes. Only its own temporary
fixture is removed afterwards.

Repository tests:

```sh
python3 -m unittest discover -s scripts -p test_worktree_trial.py
WORKTREE_TRIAL_DEV=/absolute/temporary/dev python3 -m unittest discover -s scripts -p test_worktree_trial.py
```

Automated checks do not measure human preference, actual agents, recorder exit,
Herdr/Sidecar UI behavior, real PR state, cross-machine sync, or squash merges.
Those remain explicitly unfilled in the manual feedback.
