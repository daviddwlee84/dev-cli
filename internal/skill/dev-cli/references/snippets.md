# Small-file sharing

`dev snippet` lists/searches/opens/creates GitHub Gists and GitLab snippets.
`dev gist` fixes the provider to GitHub. Use native gh/glab authentication.

- Plain list/search reads account metadata. GitLab includes authored project
  snippets; --project selects one explicit project. Do not infer project from cwd.
- --content is explicit, bounded and in-memory. Treat complete=false / issues as
  incomplete observations, not proof of no matches. JSON never includes content.
- create publishes only explicit text files/stdin, or a reviewed editor draft.
  --dry-run shows target/filenames without publishing. Do not collect whole folders.
- GitHub secret means link-readable; GitLab private uses account/project access.
- Creation is one request to one provider. Unknown outcomes must be inspected,
  never blindly retried. Browser failure after creation does not mean no share exists.
- Cancelled/failed editor drafts are retained privately outside the repository.
- REMOTE → Ctrl+O → Show snippets opens the same service's lazy inventory.

Use `dev snippet <command> --help` and `dev help snippets` for current syntax.
Repository starters remain `dev repo new --template`; snippets are not task or
repository lifecycle records. Remote editing/deletion and global search are not
part of this interface.
