# UX notes: provisioning a remote Mac with dev (2026-09-25)

Field notes from provisioning `ts_david_mac` (Tailscale macOS host behind a
filtering network) with `dev ssh`, `dev fleet`, herdr, lazyclash and chezmoi.

## Worked well

- `dev ssh probe <alias>` gave a one-line ready/ready answer; no guessing
  whether the user-owned alias in `~/.ssh/config` was usable.
- `dev ssh manage --action register --to fleet|herdr` plan/apply split was
  clear, and herdr registration was one command once the remote had herdr
  >= 0.9.1 (`herdr --machine <alias> workspace list` worked immediately).
- `dev work start --task … --base … --json` produced exactly the fields needed to
  continue in the worktree; herdr worktree panes opened automatically.
- `dev repo list -l` / `--json` located checkouts quickly.

## Friction

- **No sudo credential path** — see [remote sudo credentials](remote-sudo-credentials.md).
  Everything privileged (chezmoi apply, lazyclash system setup) needed a
  temporary `NOPASSWD` drop-in the user had to install by hand.
- **Abandoning an empty task** took `dev done --merged <task>` followed by
  `dev retire`; `dev retire` alone refuses a hot task even with zero commits.
  A direct path for a never-used worktree (no commits, clean tree) would help,
  or the refusal message could suggest `done --merged` when the branch has no
  commits beyond its base.
- **`dev fleet status` shows `no-dev` / "remote command exited 127"** for a
  freshly registered host without dev; a hint such as "dev is not on the remote
  PATH (install via dotfiles personal tools)" would save a lookup. Non-login
  SSH PATH there was `/usr/bin:/bin:/usr/sbin:/sbin` (no `/opt/homebrew/bin`,
  no `~/.local/bin`), which is the usual cause.
- **Herdr registration prerequisites** are only discovered at apply time; the
  plan could state the remote herdr version found and whether it meets the
  controller's protocol.
