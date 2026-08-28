# State, data, and cache locations

`dev` follows XDG and separates intent, durable observations, and regenerable
cache so cleanup cannot erase the wrong thing. `paths.state_dir` defaults to
`$XDG_DATA_HOME/dev` and may be overridden in config.

| Path | Contains | Reconstructible? |
|---|---|---|
| `$XDG_CONFIG_HOME/dev/config.toml` | user policy | no |
| `<state_dir>/tasks/*.toml` | state/owner/next/task note | partially |
| `<state_dir>/assets/*.toml` | catalog identity, metadata summary, experiment lifecycle | partially |
| `<state_dir>/stats.db` | Git backfill, session samples, WakaTime imports | not always |
| `<state_dir>/notes/<repo-id>/*.md` | multiple timestamped repository thoughts | no |
| `$XDG_CACHE_HOME/dev/notes.db` | note full-text search index | yes |
| `$XDG_CACHE_HOME/dev/remotes.json` | short-lived forge inventory | yes |
| `$XDG_CACHE_HOME/dev/gitignore/` | fetched GitHub templates | yes |

These note-like fields have different scopes:

- task `--next` is the next executable action;
- `dev park --note` records context on one task;
- `dev repo mark --note` stores one catalog metadata summary;
- `dev note` stores multiple durable observations attached to a repository.

Inspect paths:

```bash
dev config path
dev stats path
dev note path --all
dev cache path
dev cache list
```

Clear only regenerable cache:

```bash
dev cache clear remote
dev cache clear notes
dev cache clear gitignore
dev cache clear all
```

Clear durable stats only with an explicit scope and confirmation:

```bash
dev stats clear --repo api
dev stats clear --source git
dev stats clear --repo api --source wakatime
dev stats clear --all
```

`stats.db` is not called a cache because session samples and WakaTime imports
may not be reconstructible. Git-derived rows can be regenerated with
`dev stats backfill [--repo api]`.

Quick notes remain ordinary Markdown and can be synced or backed up as files,
but `dev` does not synchronize them. Synchronize `state_dir/notes` together
with the catalog assets when stable repository attachment must travel between
hosts. `notes.db` contains full note bodies for search. Note files and the index
use mode 0600 on Unix; Windows privacy follows the containing directory's ACL.
The index is still disposable: `dev cache clear notes` followed by
`dev note search ...` rebuilds it from Markdown.
