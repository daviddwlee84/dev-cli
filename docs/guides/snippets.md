---
description: Find and share GitHub Gists and GitLab personal or project snippets from the CLI and REMOTE dashboard.
authority: project-and-upstream
status: stable
verified_on: 2026-09-22
---

# Gists and snippets

Use `dev snippet` for small text files you want to share or retrieve without
creating a repository. `dev gist` offers the same commands restricted to GitHub.
The providers use the authenticated `gh` and `glab` accounts; they do not choose
a destination from your current Git checkout.

## Find and open

```bash
dev snippet list
dev gist search python
dev snippet search migration --forge gitlab
dev snippet list --project team/service
dev snippet open github:abc123 --print
dev snippet open https://gitlab.com/team/service/-/snippets/42
dev snippet list --json
```

The default inventory includes snippets authored by your accounts. GitLab's
account endpoint includes your personal and project snippets; `--project`
explicitly lists the snippets visible in that project. dev does not crawl all
your projects. Host selection follows `GH_HOST` and `GITLAB_HOST`/`GLAB_HOST`,
with github.com and gitlab.com as defaults.

Ordinary search matches all query words against metadata, including title,
description and filenames, without downloading file contents. Results are
sorted by update time. `open` without an argument offers an interactive picker;
a bare ID needs an explicit forge (or the GitHub-only `gist` command).

```bash
dev snippet search "retry timeout" --content
```

Content search inspects at most 1 MiB of text per file and 16 MiB per search,
with four concurrent reads and a 60-second operation deadline. These text
budgets do not bound the provider CLI's HTTP transport bytes. Both providers'
multi-file snippets are searched. Truncated responses, missing files and network
failures produce incomplete coverage, retaining confirmed matches. JSON includes
`complete` and `issues`, not file contents. The text is not cached. Metadata
requests also have a 60-second deadline and paginate in pages of 100.

This searches the selected account/project inventory, not GitHub's global Gist
search. Provider failure is distinct from an empty inventory.

## Create a share

```bash
dev gist create demo.py notes.md --description "Small reproduction"
cat demo.py | dev gist create - --filename demo.py
dev snippet create demo.py --forge gitlab --project team/service --title "Repro"
dev snippet create demo.py --forge github --dry-run --json
dev snippet create
```

Explicit file arguments publish those UTF-8 text files. Directories are not
recursively uploaded; conflicting basenames are rejected. The total text limit
is 16 MiB, and GitLab accepts at most ten files. `--filename` names stdin or an
editor draft. `--title` defaults to the first filename; on GitHub it supplies the
description when `--description` is omitted.

With no files in a terminal, dev asks for the destination and a filename, opens
`--editor` / `$VISUAL` / `$EDITOR` (then nvim/vim/vi), and shows a final publication
review. GitLab may target a personal snippet or an explicitly named project.
A fully specified non-interactive command publishes directly; stdin requires
`-`, `--filename`, and an explicit platform unless using `dev gist`.

| Platform | Default | Sharing behavior |
|---|---|---|
| GitHub | `secret` | Unlisted; anyone with its link can read it. |
| GitLab personal | `private` | Only the owner can read it. Use public for a public link. |
| GitLab project | `private` | Access is governed by project membership. |

`--visibility public` publishes with the provider's public setting. GitLab
`internal` is available only on hosts that support it; GitLab.com does not.
GitHub secret is not equivalent to GitLab private. Neither command adds an
automatic expiry time.

Editor drafts live privately outside the repository under
`paths.state_dir/snippets/drafts`. Cancellation or failure retains the draft and
prints its path. Successful publication removes the helper's draft. Dry-run
prints filenames, byte counts and destination metadata without publishing text.

Creation makes one request to one selected platform. An uncertain response is
reported as `unknown` and is never automatically retried; inspect the inventory
before choosing to create again. A successful create returns its ID and URL.
If `--web` cannot open a browser afterwards, the publication remains successful.

## Dashboard

REMOTE starts with repositories. In `Ctrl+O`, choose **Show snippets** or
**Show repositories**. Snippets have their own selection and search state, and
load only when selected. Ordinary `/` filters metadata; file-content search is
a separate explicit action. The menu also selects platform/project scope,
refreshes, opens or copies a URL, and starts the same CLI creation wizard.
Snippets never receive repository clone, task or worktree actions.

## Sources and alternatives

- [GitHub Gist API](https://docs.github.com/en/rest/gists/gists) and
  [Gist visibility](https://docs.github.com/en/get-started/writing-on-github/editing-and-sharing-content-with-gists/creating-gists).
- [GitLab snippets](https://docs.gitlab.com/user/snippets/) and
  [project snippets API](https://docs.gitlab.com/api/project_snippets/).
- [PrivateBin](https://privatebin.info/) is an alternative for instances offering
  expiration or burn-after-reading; it is not a dev provider.

Repository starters remain `dev repo new --template owner/starter`. Gists and
snippets are independent sharing surfaces, not repository lifecycle records.
