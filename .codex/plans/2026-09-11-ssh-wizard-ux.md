# SSH wizard usability follow-up

The current SSH discovery/onboarding implementation was committed first as
`f6cae0e`. This follow-up remains a separate diff for review.

## Approved behavior

- List local public keys and current agent identities with `dev ssh key list`,
  supporting `--json`, `--no-agent`, and explicit `--alias` OpenSSH evaluation.
  The default catalog never evaluates a synthetic alias with `ssh -G`.
- Existing-key setup uses a searchable single picker with signer/source metadata,
  preserves the exact service-bound candidate, and retains manual paths and the
  existing separate key-generation choice. Repair-required private companions
  are distinct from public-only candidates. Apply revalidates selected sources.
- Registration uses existing built-in multiple selection: Fleet and Herdr,
  unchecked by default. Accepting none continues setup or finishes an explicit
  register action without effects. Esc cancels; required host picks stay required.
- Inspect canonical SSH permissions at wizard entry, before discovery or host
  prompts. Present concrete tightening, confirm separately, apply under the SSH
  operation lock, and verify. Inspect the selected key before registration prompts.
  Repair only explicitly bounded, owned, direct paths; no recursive chmod/chown,
  private-content reads, ACL guesses, or implicit repairs during dry-run/listing.
  Completed tightening remains in place after later cancellation or failure.
- Keep Tailscale, Herdr, and fzf optional. Multi uses built-in Bubble Tea; single
  choices keep respecting the existing configurable picker backend.

## Verification

Use isolated HOME and fake runners. Cover empty/canceled registration, local and
agent-only key selection, incomplete inventory, candidate/source replacement,
permission repair ordering, stale guards and retained partial results. Run
focused race suites, vet, formatting, Windows compilation, skill sync/check,
and strict source/site documentation checks after updating both locales.

Completed validation: full `sshhost` race suite; SSH/onboarding/registration CLI,
picker and sshflow race suites; full-project vet plus final sshhost vet; Windows
ARM64 CLI compilation and Windows/Linux AMD64 sshhost compilation; skill
sync/check, strict paired docs build/source/site checks, and isolated empty-HOME
key-list smoke. Native Windows/Linux execution remains the CI platform gate.

Review regressions additionally bind selected private identities to Unix ctime
or Windows ChangeTime, preserve ProxyJump's ambient agent while pinning the
target's selected agent, and reject an agent disappearing/changing context before
the first configuration effect. Explicit manual public-companion derivation
still has one derivation review and the existing final onboarding confirmation.
