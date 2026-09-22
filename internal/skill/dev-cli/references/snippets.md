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

## REMOTE statistics and sorting

Repository rows show GitHub/GitLab stars, forks, open issues and open PRs/MRs.
Issues exclude pull requests; the `PRS` column includes open draft PRs and GitLab
MRs. Inventory appears first, then background GraphQL batches of at most 25
resources add statistics. Entering REMOTE can enrich missing/stale statistics
without reloading fresh inventory; `r` refreshes both. Rate limiting stops that
provider's statistics requests until a later refresh. Sorting and `/` filtering
use loaded data and never contact providers.

Click a column to cycle ascending, descending and default order, or use
**Ctrl+O → sort columns** for all fields, including columns hidden on narrow
screens. Numeric values sort numerically; unknown values stay last in both
directions. Default ordering and the selected resource are preserved. Details
show statistics and their observation time. `0` means measured zero, `?` means
unknown/failed, `—` means unavailable, and `~` marks a retained stale count.
Statistics failures do not invalidate a successfully refreshed inventory.
Azure repository statistics are unavailable in this version.

GitHub Gists add stars, forks and comments. GitLab snippets retain their existing
metadata; this version does not query their comment connections. Snippets can
also sort by file count: a `+` suffix denotes an incomplete file list, which is
not sorted as an exact total. Repository statistics share the existing private
cache and `forge.cache_ttl`; snippet statistics stay in the dashboard session.
Repository and snippet sorting remain independent.
