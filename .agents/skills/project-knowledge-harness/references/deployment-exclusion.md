# Deployment exclusion cheatsheet

`TODO.md`, `backlog/`, and `pitfalls/` are repo metadata for maintainers,
not files to ship to users. Make sure they are excluded from whatever
packaging or deployment mechanism the host project uses.

The bundled `scripts/init.sh` does NOT modify ignore files automatically
(too high blast radius). It prints the lines you should add. Reference
table:

| Mechanism | File | Lines to add |
|---|---|---|
| chezmoi | `.chezmoiignore.tmpl` | `TODO.md`<br>`backlog/**`<br>`pitfalls/**` |
| Python (setuptools) | `MANIFEST.in` | `exclude TODO.md`<br>`recursive-exclude backlog *`<br>`recursive-exclude pitfalls *` |
| npm | `package.json` `files` allowlist OR `.npmignore` | `TODO.md`<br>`backlog/`<br>`pitfalls/` |
| Docker | `.dockerignore` | `TODO.md`<br>`backlog/`<br>`pitfalls/` |
| Generic | confirm `.gitignore` does NOT ignore them | (they should be tracked) |

For Python wheels, also confirm your `pyproject.toml` `[tool.setuptools]`
`packages` / `include` configuration does not pick up these directories.
