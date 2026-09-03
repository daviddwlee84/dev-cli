# Optional picker integrations

These files compose with dev's existing structured output. They are examples,
not installed product state: copy or symlink only the integration you want.

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
