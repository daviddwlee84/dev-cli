# State, data, and cache locations

`dev` follows XDG and separates intent, durable observations, and regenerable
cache so cleanup cannot erase the wrong thing.

| Path | Contains | Reconstructible? |
|---|---|---|
| `$XDG_CONFIG_HOME/dev/config.toml` | user policy | no |
| `$XDG_DATA_HOME/dev/tasks/*.toml` | state/owner/next for tasks | partially |
| `$XDG_DATA_HOME/dev/stats.db` | Git backfill, session samples, WakaTime imports | not always |
| `$XDG_CACHE_HOME/dev/remotes.json` | short-lived forge inventory | yes |
| `$XDG_CACHE_HOME/dev/gitignore/` | fetched GitHub templates | yes |

Inspect paths:

```bash
dev config path
dev stats path
dev cache path
dev cache list
```

Clear only regenerable cache:

```bash
dev cache clear remote
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
