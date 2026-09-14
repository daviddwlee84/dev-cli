# SSH key setup you can find: visible prompt options, key picker, dashboard key wizard → v0.2.37

## Context

After the LAN diagnostics fix (commit `920895c` on `fix/lan-discovery-diagnostics`), the user hit three
usability gaps while setting up hosts:

1. `dev ssh setup <alias> --generate-key` asks `Remote OS for <alias> [posix]:` — `prompter.choice`
   (`internal/cli/prompt.go:63-75`) only shows the default; valid values appear after a wrong answer.
2. The dashboard can't set up keys from Ctrl+O. "set up / import connections…" opens a blank form
   (`internal/tui/ssh_view.go:474-475`), there is no key action for configured profiles, and the form's
   key is a free-text path plus a separate Generate toggle (`ssh_discovery.go:484-498`).
3. `dev ssh setup <alias>` without `--key/--generate-key` errors (`internal/cli/ssh.go:729-731`) even
   interactively, unlike the user's old `ssh-setup-remote [user@]host` (one key menu incl. "+ create a new key").
   `dev ssh` help/menu never mention keys.

User decisions: merge the wizard's "existing key" and "generate" auth items into one key picker; for the
dashboard, offer key choice both while adding a connection and as a dedicated key wizard for configured
profiles (UX below). Then push, open a PR, merge, tag v0.2.37.

## UX design (dashboard)

One shared **key picker** sub-dialog (kind `keys`), fed by a new `SSHActions.ListKeys` callback:
```
Choose SSH key for lab
› ~/.ssh/id_ed25519            ed25519 · private file + agent
  ~/.ssh/work_rsa              rsa · public only; no signer found
  + Generate a new key         ~/.ssh/id_ed25519_dev_lab
  Enter a path manually…
```
- **Add SSH connection form** (Enter on a candidate / "set up this discovered target…"): fields
  Alias, Host, Remote user, Port, Authentication `config · existing · [key]`, **Key**, Remote OS
  `[posix] · windows`, Fleet, Herdr. The old free-text "Key path" + "Generate key" fields become one
  read-only **Key** field showing `~/.ssh/id_ed25519` or `new: ~/.ssh/id_ed25519_dev_lab`. Enter/Space on
  Key opens the picker; picking a key sets Authentication to `key`. "Enter a path manually…" opens a
  one-field `keypath` dialog that returns to the form.
- **Install SSH key wizard** (Ctrl+O on a configured profile → "set up / install an SSH key…"):
  choose profile (existing `profiles` dialog when several) → key picker opens immediately → short form
  titled `Install SSH key for lab (user@host:port)` with Key, Remote OS, Fleet, Herdr (connection fields
  are not shown or editable) → Ctrl+S review → Enter apply. Esc in the picker returns to the form.
- Choice fields render every option with the current one bracketed; ←/→/Space cycle both directions.

## Changes

### A. Prompts list their options (`internal/cli/prompt.go`)
- Add `choiceOf(label, fallback string, options []string, choices map[string]string)` rendering
  `? <label> (posix/windows) [posix]:`, re-asking with `enter one of: posix, windows` on bad input.
- Use it at `ssh.go:1237` (Remote OS), `ssh_manage.go:267`, `ssh_tui.go:267`, `submodule_add.go:81,91`,
  `repo_create_wizard.go:235`, `start_wizard.go:271`; for `repo_create_wizard.go:396,522,534,547,554`
  drop the hand-written `(a/b)` from the label and switch to `choiceOf`. Leave done_*/start_wizard.go:189
  (labels already list letter shortcuts).
- `ssh_onboard.go:369` `line("Target OS (posix/windows)")` → `choiceOf("Target OS", …)` (adds validation).

### B. One interactive key picker for CLI setup (`internal/cli/ssh_keys.go`, `ssh.go`, `ssh_onboard.go`)
- Rename/extend `selectSSHWizardKey` → `chooseSSHKeyInteractive(ctx, app, service, alias, options, generateDefault)`:
  catalog keys + `+ Generate a new key` (prompts `New key path` with `generateDefault`) + `Enter a key path…`;
  clear `key/keyCandidate/keyPlan/generateKey/keyPath` before each pick; keep the `prepareSSHWizardKey` retry loop.
- Wizard auth picker `ssh_onboard.go:602-617` and its copy `ssh_fleet_import.go:302-325`: items become
  config / existing / `Install an SSH key (existing or new)` → `chooseSSHKeyInteractive`; drop the separate
  generate branch and the duplicate `prepareSSHWizardKey` call.
- `validateSSHSetupFlags` gains `interactive bool`; the explicit-key requirement applies only when not
  interactive (`full setup requires --key or --generate-key outside an interactive terminal`).
- `runSSHSetupOperationObserved` (before key planning, `ssh.go:833`): if interactive, not config-only/dry-run,
  no key flags → `chooseSSHKeyInteractive(... filepath.Join(service.Paths().SSHDir, "id_ed25519_dev_"+alias))`.
- Honour a prepared plan: if `options.keyPlan != nil`, `service.RevalidateKeySelection(ctx, *options.keyPlan)`
  (returns only an error) and use that plan instead of calling `planSSHSetupKey` again (avoids a second
  derive confirmation). JSON/non-interactive contracts unchanged.

### C. Discoverability (`ssh.go:169-176`, `ssh_manage.go:294-319`, `ssh_keys.go:28`)
- `dev ssh` Long: one line pointing to `dev ssh setup <alias>` for key install (picker) or `--key/--generate-key`.
- `dev ssh key` Short: "Inspect local SSH keys and agent identities (install one with dev ssh setup)".
- No-arg `dev ssh` menu: add `Set up or install an SSH key for a host` → `sshPick` over `ConnectionHints`
  aliases (`user@host:port`) + "Enter an alias…" → run setup via `cmd.Find({"setup"})` with that alias.
- No new `dev ssh key install` command. Cobra text changed → `make skill-sync` + `make skill-check`.

### D. Dashboard (`internal/tui`, `internal/cli/ssh_tui*.go`, `internal/sshflow/onboard_ui.go`)
- **Choice rendering** (`ssh_dialog.go`): `sshFieldChoices(key)` drives cycling and render
  (`config · existing · [key]`, toggles `[x]`), ← cycles back; hint "←/→ changes choices · Enter on Key lists keys · Ctrl+S reviews".
- **Menu** (`list_actions.go:84-95`, keep new IDs inside the SSH range checked at `:645`):
  `listActionSSHSetupTarget` "set up this discovered target…" when the row has LAN/Tailscale candidates →
  `openSSHOnboarding(row)`; `listActionSSHInstallKey` "set up / install an SSH key…" when the row has profiles.
  Keep "set up / import connections…" (test pins it). Profile resolution mirrors `runSSHAction`
  (current child / only profile / `openSSHProfiles("install-key", row)`; `profiles` Enter branch routes
  `install-key` to the key wizard instead of `Workflow`).
- **Prefill**: `openSSHOnboarding` sets `User` from a row profile whose HostName matches the candidate.
- **Request**: `sshflow.OnboardRequest` gains `Profile *ConnectionProfile` (key install for an existing
  alias; connection fields read-only). Replace free-text key/generate fields with the Key field bound to
  `request.KeyPath` + `request.GenerateKey`.
- **Callback**: `SSHActions.ListKeys func(ctx, alias string) ([]SSHKeyChoice, error)`,
  `SSHKeyChoice{Label, Description, Path string; Generate bool}`. CLI (`ssh_tui.go` wiring) builds it from
  `localSSHKeyCatalog` + `sshKeyLabel`/`sshKeySigner`, appending the generate item with the default path.
- **Picker dialog**: saves the form as `parent`, loads keys asynchronously (`sshEventMsg` kind `keys`,
  generation-checked so background discovery events can't replace it), `keys` / `keypath` kinds handled
  before the generic Esc close.
- **CLI adapter** `prepareSSHTUIOnboarding`: when `request.Profile != nil` require `Alias == Profile.Alias`,
  `Auth == "key"`, a key, no `Candidate`; `revalidateSSHTUIProfile`; build options **without**
  hostName/user/port, `*Changed` and `connectionChanged` so foreign aliases get bootstrap-only and managed
  ones only gain `IdentityFile` (review shows it). Existing validation otherwise unchanged.
- **Hints/help**: detail line `ssh_view.go:351`, footer `view.go:1617`, `help_content.go:111-118` entries.

### E. Docs, skill, changelog (both locales)
`docs/guides/ssh-hosts.md` (+zh-TW: setup 83-159, menu :230, wizard 386-574, SSH view 748-807),
`docs/guides/tui-repos-bootstrap.md` (+zh-TW SSH section), `internal/help/topics/ssh.md` + `tui.md`,
`internal/skill/dev-cli/references/ssh-hosts.md`, README SSH sections (293-324, 939-951),
`docs/reference/commands-config.md` (+zh-TW 104-131), `docs/reference/sources-freshness.md` (+zh-TW SSH row →
v0.2.37). CHANGELOG `[Unreleased]`: `### Added` (interactive key picker for setup, dashboard target setup and
key wizard, `dev ssh` key entry) and extend `### Fixed` (prompts list options; Target OS validated). Regenerate
`docs/llms-full.txt`.

## Tests
- `prompt_test.go`: `choiceOf` render `(posix/windows) [posix]`, re-ask, letter alias.
- `ssh_internal_test.go` / `ssh_keys_test.go`: interactive `setup <managed alias>` without key flags reaches
  `SSH key for lab` (fake `pickerSelect`), sets IdentityFile, derive confirmation once; generate item default path;
  foreign alias bootstrap-only; non-interactive still usage error; wizard tests updated for merged auth item.
- `ssh_entry_test.go`: menu has key entry and reaches the key picker.
- `internal/tui` (`ssh_workflow_test.go`, `ssh_view_test.go`): choice render + backward cycle; target menu
  prefills candidate + user; install-key with several profiles → profiles dialog → picker → reduced form sends
  `Profile`; picker Generate/manual set Key; Esc returns to form; adapt
  `TestSSHConfigOnlyFormCannotRequestKeyGeneration` to set `GenerateKey` via the Key field.
- `internal/cli/ssh_tui_discovery_test.go`: Profile + key keeps managed HostName/User; foreign → bootstrap
  only, no connection-field error; Profile + Candidate rejected; ListKeys includes generate item.

## Verification
```bash
export PATH=/Users/zhouhanru/.local/share/mise/installs/go/1.26.4/bin:$PATH   # homebrew go 1.27.1 mismatches GOROOT
gofmt -l internal cmd; go vet ./internal/cli ./internal/tui ./internal/sshflow
go test ./internal/cli ./internal/tui ./internal/sshflow ./internal/sshhost
go test -race -timeout 20m ./...
make skill-sync && make skill-check && make build
uv run python scripts/check-docs.py --source --generate-llms && uv run python scripts/check-docs.py --source \
  && uv run mkdocs build --strict && uv run python scripts/check-docs.py --site site
```
Manual: `./dev ssh setup <alias>` shows the key picker and `Remote OS for <alias> (posix/windows) [posix]`
(cancel at review); `./dev ssh` menu key entry; `./dev` dashboard SSH view: Ctrl+O on a LAN row → set up this
target (prefilled), Ctrl+O on a profile → install key wizard (picker → review, then Esc).

## Landing
1. Commit the feature (specific files only; not `.claude/plans` or `.specstory`).
2. Release prep commit on the branch: `## [0.2.37] - 2026-09-14` from `[Unreleased]`, compare links
   (`CHANGELOG.md` bottom: `[Unreleased]` → `v0.2.37...HEAD`, add `[0.2.37]`), `README.md:114,158` pins,
   `AGENTS.md:128` baseline, regenerate llms; rerun docs checks.
3. `git push -u origin fix/lan-discovery-diagnostics`, `gh pr create` (body ends with the Claude Code
   attribution), `gh pr checks --watch`; fix failures.
4. When CI is green, merge with a merge commit (repo convention, e.g. PR #29) via `gh pr merge --merge`.
5. `git switch main && git pull --ff-only`; `git tag -a v0.2.37 -m v0.2.37` on the merge commit (ancestor of
   `origin/main` as `release.yml` requires); `git push origin v0.2.37`; `gh run watch` the release run and
   confirm assets + Homebrew formula.
