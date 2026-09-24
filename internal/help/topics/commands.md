# Command families and shortcuts

Use `dev --help` for the 17 primary entrypoints and `dev help --tree` for the
canonical command tree. The default depth is two; `--depth 0` expands everything.
Append a command path to focus the tree, or add `--aliases` to include shortcuts.

```bash
dev help --tree
dev help --tree --depth 0
dev help --tree agent artifact
dev help --tree --aliases
```

The main families are work, repo, tries, git, agent, activity and self.
Independent entrypoints are snippet, ssh, fleet, dotfile, pr, summary, triage,
status, tui and help. Command help describes syntax; `dev help <topic>` describes
the workflow. `dev help tries` covers graduation, demotion, archive and Trash.

| Family | Operations |
|---|---|
| work | list, start, park, resume, done, adopt, retire, sweep |
| repo | repository operations, bootstrap, note, flow |
| tries | try, open, list, graduate, demote, retention |
| git | transactions, ignore, worktree, submodule, hygiene |
| agent | skill, mcp, instructions, prompt, artifact |
| activity | journal, stats |
| self | config, cache, doctor, version, upgrade, completion, shell-init, feedback |

Existing top-level commands remain permanent shortcuts with the same parameters,
output and exit behavior, without deprecation warnings. For example `dev start`
is `dev work start`, `dev prepare` is `dev agent artifact prepare`, and `dev edit`
is `dev self config edit`. `dev try archive` still creates or opens the experiment
named archive; `dev tries archive <ref>` performs archiving. `dev gist` keeps its
GitHub-only provider selection.

Root completion highlights primary entrypoints; shortcut argument/flag completion
remains available. Reload shell integration from `dev self shell-init <shell>`
after upgrading. Task states, persisted formats, JSON contracts and dashboard tab
names are unchanged. Grouping an operation does not change when it applies or
which safety checks it requires.
