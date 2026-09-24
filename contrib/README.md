# Optional picker integrations

These files compose with dev's existing structured output. They are examples,
not installed product state: copy or symlink only the integration you want.

## Local repository navigation and workflow trial

`dev repo list --json` is the public source for local REPOS candidates. It is a
full local inventory, so consumers should request it on demand rather than at
editor startup or on every keystroke. Use existing, non-bare Git rows; preserve
distinct clones, identify entries by paths rather than display names, and
revalidate a selected path before opening it. Do not parse private dev caches.

The optional [dotfiles dev-projects integration](https://github.com/daviddwlee84/dotfiles/blob/main/docs/tools/dev-projects.md)
provides a standalone helper, an asynchronous `:DevRepositories` picker and a
`tv dev-repos` channel. It delegates to this JSON interface and is not a command
embedded in the dev binary. It can preview and explicitly add missing Sidecar
Projects; it does not mirror task data, rename/remove native projects, seed
usage histories, or create sessions during discovery. Native Sidecar settings,
editor sessions and zoxide history retain their own meaning.

[The worktree trial](worktree-trial/README.md) generates two independent Go
example repositories and isolated Sidecar settings outside any Git checkout.
It compares task-free Herdr/dev cleanup with Sidecar's complete workspace
workflow. Automated fixture checks and human workflow feedback are separate;
the trial does not formally deprecate task commands.

## Remote clone candidates

First populate the private forge inventory:

```bash
dev repo remote --refresh
```

## Television

Copy or symlink `television/dev-remote-repos.toml` into Television's cable
directory, then select an exact clone URL:

```bash
ref="$(tv dev-remote-repos)" && [ -n "$ref" ] && dev repo clone "$ref"
```

The channel requires `dev` and `jq`. It reads only
`dev repo remote --cached --json`; opening the channel never contacts a forge.

## fzf shell helper

Source `fzf/dev-repo-clone.bash`, then run:

```bash
dev-repo-clone-fzf
```

The helper requires `dev`, `jq`, and `fzf`. It does not use `eval` or `xargs`:
the selected canonical clone URL is passed to `dev repo clone` as one quoted
argument. Canceling fzf performs no clone.

Both integrations retain already-cloned rows because a second destination may
be intentional. `dev repo clone` remains responsible for destination and
nested-repository safety checks.
