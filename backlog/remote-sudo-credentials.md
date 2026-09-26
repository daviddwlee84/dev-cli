# Remote sudo credentials for fleet and managed setup

Status: research (P?, M). Motivated by provisioning `ts_david_mac` on 2026-09-25.

## Problem

Several unattended workflows need administrator rights on a remote macOS/Linux
host that only allows password sudo:

- dotfiles `chezmoi apply` (ansible become, Homebrew casks),
- `lazyclash setup` for system-scope clients (Verge service helper, launchd
  rollback watchdog),
- ad-hoc fixes during `dev fleet` operations.

Today the only non-interactive options are a temporary `NOPASSWD: ALL` sudoers
drop-in (what we did, then removed) or re-running each tool in a terminal and
typing the password several times. dev already stores SSH *login* passwords via
the system keychain or Bitwarden (`--password-store`), but it has no concept of
a remote *sudo* password, so the operator repeats a secret dev could hold.

## Direction

A `dev ssh sudo <alias>` credential context reusing the existing provider layer:

- Store: same providers and consent flow as the SSH password save (Yes/No/Never,
  system keychain default, Bitwarden optional); context key = alias + remote user.
- Deliver: never argv, environment or ordinary files. Either
  1. a one-shot `sudo -S -v` over the existing SSH session with the password on
     stdin, refreshing the timestamp for a bounded window (`timestamp_timeout`
     permitting), or
  2. an askpass bridge: a fixed remote helper reading one line from a private
     forwarded fd, used via `SUDO_ASKPASS` for a single command.
- Consumers: `dev fleet` actions, and an exported contract other tools
  (lazyclash `runPrivilegedScript`, dotfiles `apply_with_sudo.sh`) can call, for
  example `dev ssh sudo-exec <alias> -- <fixed argv>` with the password piped by
  dev only.
- Verification: detect wrong password vs. policy refusal vs. `requiretty`;
  never retry a wrong password automatically; never cache beyond the sudo
  timestamp on the remote.

## Open questions

- Is timestamp refresh reliable when tools open fresh SSH sessions
  (`tty_tickets`/per-tty timestamps on macOS)? If not, the askpass bridge is required.
- Should a temporary NOPASSWD drop-in be a first-class, self-expiring dev
  action (install, then remove plus verify), since it is what operators actually do?
