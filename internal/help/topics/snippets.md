# Gists and snippets

Find or share a few text files through GitHub Gists and GitLab snippets.

    dev snippet list
    dev gist search python
    dev snippet list --project team/service
    dev snippet search "retry timeout" --content
    dev snippet open github:abc123 --print
    dev gist create demo.py --description "Reproduction"
    cat demo.py | dev gist create - --filename demo.py
    dev snippet create demo.py --forge gitlab --project team/service
    dev snippet create

`gist` fixes the platform to GitHub. `snippet` lists both authenticated accounts
unless --forge or --project selects a narrower scope. GitLab account listings
include authored personal and project snippets; explicit project listing does
not traverse other projects. Host/authentication follow the native forge CLIs
and configured host environment, independently of the current checkout.

Ordinary search checks metadata, including descriptions and filenames. --content
explicitly reads multi-file contents: 1 MiB/file, 16 MiB/search, four concurrent
reads and 60 seconds. Partial results retain matches and report incomplete
coverage. --json returns metadata, complete and issues, never contents.

Create accepts explicit UTF-8 files or stdin; no recursive directory uploads.
Duplicate basenames are rejected. GitLab supports ten files; dev limits total
text to 16 MiB. --filename names stdin/editor text. --dry-run previews the target
and filenames. A complete non-interactive create publishes directly.

No files in a terminal opens VISUAL/EDITOR and reviews the account, platform,
project, files and visibility before publishing. Drafts outside Git are private,
retained on cancellation/failure, and removed after successful creation.
GitHub secret is link-readable. GitLab private personal snippets are owner-only;
private project snippets use project membership. These are not automatic-expiry
shares. A timed-out publication is unknown and never automatically retried.
Inspect list before attempting another create. --web failure after a confirmed
create does not undo the returned ID/URL.

REMOTE → Ctrl+O → Show snippets switches the dashboard's independent inventory.
The same menu offers provider/project scope, content search and creation.
Show repositories restores the normal repository list. Snippets do not own
local checkouts, tasks or worktrees.
