# Dotfiles

Find chezmoi configuration, choose a repository and delegate explicit native operations.

```bash
dev dotfile                         # passive configuration/source/Git observation
dev dotfile status --json
dev dotfile setup                   # review setup or keep the existing source
dev dotfile setup --preset david    # optional author-maintained platform preset
dev dotfile setup --repo https://github.com/you/dotfiles.git
dev dotfile diff
dev dotfile apply
dev dotfile update
dev fleet dotfile status --host lab --json
```

Chezmoi owns templates and deployment; the repository owns prompts and package
installation. Dev does not install chezmoi or execute downloaded bootstraps.
Setup outside a terminal is report-only unless `--yes` is supplied; `--apply`
separately requests deployment. Native repository prompts remain native.

The optional david preset selects Unix (macOS/Linux/WSL), native Windows, or the
experimental Termux/iSH/OpenWrt repository. Existing sources are never replaced
by a recommendation. Use `--chezmoi-config PATH` for a custom local config.

Passive status does not invoke chezmoi, hooks, template evaluation or fetch.
Source directory, source-state directory and Git working tree are distinct;
deployment drift remains unknown. Invalid/ambiguous config does not mean clean
or unconfigured. Native diff/apply/update can execute hooks; dry-run does not
sandbox chezmoi hooks. Keep advanced operations in chezmoi itself.

Remote status reads only the selected host's conventional configuration through
its own dev. No remote apply, config copying or legacy fleet migration occurs.
FLEET host actions expose the same observation without a repository selection.
The author's dotcfg and appsrc remain independent optional helpers.
