package tui

func refreshActionDescription(view View) string {
	switch view {
	case ViewFleet:
		return "Refresh the shared local Herdr catalog and the selected host's repositories. Old data remains stale on failure; update all configured hosts is a separate action."
	case ViewRemote:
		return "Refresh the selected REMOTE content: repositories or snippet metadata/content-search results. This is an explicit network action."
	case ViewSkills:
		return "Reload local installed skills. Use c separately for explicit upstream source checks."
	case ViewMCP:
		return "Reload local static declarations only; no server is started or health-checked."
	case ViewSSH:
		return "Reload local SSH/fleet/Herdr records, machine mappings and discovery caches. No Tailscale refresh, LAN scan, authentication or remote repository fan-out."
	default:
		return "Reload configuration and local observations. Local repository refresh does not fetch Git remotes."
	}
}

// Authored guidance lives alongside the registrations; it never dispatches an action.
func actionGuidance(view View, id listAction) (string, string) {
	switch view {
	case ViewTasks:
		switch id {
		case listActionOpen:
			return "Open the selected task", "Requires a task with an eligible checkout. Cold or missing work needs the explicit resume/recovery action."
		case listActionPark:
			return "Park warm", "For a selected HOT or WARM task, enter its next action; parking closes its runtime and retains its checkout."
		case listActionEditNext:
			return "Edit next action", "Edit the selected task's next-action reminder."
		case listActionAddNote:
			return "Add a repository thought", "Attach a durable quick note to the selected task's canonical repository."
		case listActionBrowseNotes:
			return "Browse repository thoughts", "Read, search and manage the selected repository's notes; note deletion has its own confirmation."
		case listActionToggleHistory:
			return "Include or hide DONE tasks", "Toggle finished tasks in this list; explicit task-state filters are cleared."
		case listActionStats:
			return "Show repository activity", "Read the selected task's repository activity heatmap."
		}
	case ViewRepos:
		switch id {
		case listActionOpen:
			return "Open the selected checkout", "A repository row opens the main checkout; an expanded worktree row opens that exact checkout. Missing or prunable worktrees cannot be opened."
		case listActionToggleWorktrees:
			return "Expand or collapse worktrees", "For a repository with known linked worktrees, reveal each checkout's Git, runtime and task state. Space on a child collapses its parent."
		case listActionRepoCreate:
			return "Create or clone a repository", "Open the repository wizard with n or the action menu, even when the list is empty or the selected row is still refreshing. An active clone still blocks creation. Completion returns to the dashboard and refreshes local inventory."
		case listActionStartWorktree:
			return "Start a worktree task", "Select a repository main row, then choose the task and explicit base. Creates a managed checkout outside the repository; prepares a runtime surface."
		case listActionStartDirect:
			return "Start a direct task", "Select a repository main row to record work on its current branch without creating a branch or worktree."
		case listActionAddNote:
			return "Add a repository thought", "Attach a durable quick note to the selected canonical repository, including when a linked worktree is selected."
		case listActionBrowseNotes:
			return "Browse repository thoughts", "Read, search and manage the selected repository's notes."
		case listActionRepoMetadata:
			return "Edit tags and summary", "Edit catalog metadata for a repository main row; this summary is separate from its multiple quick notes."
		case listActionRepoDemote:
			return "Move a graduated project back to Tries", "Preview the exact move, then confirm. Preserves current files, Git history, remotes and catalog identity. Task, runtime or pending artifact claims must be resolved first."
		case listActionCopy:
			return "Choose repository data to copy", "Requires a selected row. Choose context, path, branch or clone URL from the copy controls; copying does not contact a remote."
		case listActionCycleSort:
			return "Cycle or reverse ordering", "Uppercase O cycles activity, latest, name, Git, size and task ordering. Uppercase R reverses it. Column-header sorting is available separately."
		case listActionStats:
			return "Show repository activity", "Read the selected repository's activity heatmap."
		}
	case ViewFleet:
		switch id {
		case listActionOpen:
			return "Navigate to the host or repository", "The local host opens REPOS. Remote navigation uses its exact host/session and preserves the current-client boundary; no extra client is opened inside Herdr. Enter never expands the tree."
		case listActionFleetToggle:
			return "Expand or collapse host repositories", "Space changes only the tree. On a repository child it collapses and selects the parent host. Search expansion overrides are temporary; clearing the query restores saved expansion."
		case listActionFleetLocal:
			return "Show or hide the local host", "Local is hidden initially. Showing it adds a collapsed host last, independent of sorting. Search and coverage include local repositories only while the host is shown; local rows reuse REPOS without another scan."
		case listActionSettings:
			return "Edit fleet configuration", "Open remotes.toml in the configured editor, then validate and reload it after the editor exits."
		}
	case ViewSSH:
		switch id {
		case listActionSSHConnect:
			return "Connect through SSH", "Choose an exact profile when a machine has several aliases; source fingerprints and alias identity are revalidated before native SSH starts."
		case listActionSSHSetup:
			return "Set up connections", "Complete the native connection form, review configuration and optional authentication / fleet / Herdr effects, then apply the exact plan."
		case listActionSSHSetupTarget:
			return "Set up a discovered target", "Select a LAN or Tailscale row, open Ctrl+O and choose set up this discovered target. The form starts from that observation; Enter on Key lists local keys or generates one."
		case listActionSSHInstallKey:
			return "Install an SSH key", "Select a configured profile, open Ctrl+O and choose set up / install an SSH key. Pick an existing key or generate one, choose the remote OS, then review. Connection settings stay unchanged; foreign aliases receive only the public key."
		case listActionSSHDiscover:
			return "Discover Tailscale or LAN hosts", "Press c, then select LAN or Tailscale. Uppercase L defaults to the lazygit tool. LAN discovery shows a bounded on-link range and ports; listing and refresh do not scan."
		case listActionSSHProbe:
			return "Test connectivity or authentication", "Review quick network checks or full SSH verification for an exact profile, machine or filtered group. Results are dated observations; tests do not change recent-use ordering."
		case listActionSSHToggle:
			return "Expand connection profiles", "Keep one machine row, then expand exact aliases. Configured machines sort first, followed by recent connection use. Discovery-only endpoints can be added with Enter."
		case listActionSSHMappings:
			return "Manage machine mappings", "Adopt candidates or preview linking, unlinking, and merging canonical machine identities. Provider configuration stays owned by its original source."
		case listActionSSHCopy:
			return "Copy machine data", "Choose machine ID, aliases, or a safe source summary; copying does not contact the host."
		}
	case ViewTries:
		switch id {
		case listActionTryCreate:
			return "Create a Try", "Open the scratch experiment form, including when the list is empty. A Try can be a non-Git directory."
		case listActionOpen:
			return "Open the selected Try", "Requires a Try present on this host with no incomplete move. Archived or missing Tries need their own restore or recovery action."
		case listActionToggleHistory:
			return "Include or hide Try history", "Toggle deprecated, archived, evicted and graduated entries."
		case listActionCycleSort:
			return "Cycle or reverse ordering", "Uppercase O cycles activity, name, phase and size ordering. Uppercase R reverses it."
		}
	case ViewRemote:
		switch id {
		case listActionRemoteContent:
			return "Show snippets or repositories", "REMOTE starts with repositories. Switch to snippets to query GitHub Gists and GitLab snippets lazily; each mode keeps its own rows, query and selection."
		case listActionSnippetProvider:
			return "Choose snippet provider", "Query all configured providers, GitHub Gists, or GitLab snippets. Provider errors remain visible with any partial results."
		case listActionSnippetProject:
			return "Choose GitLab snippet scope", "Enter an exact GitLab project path, or leave it blank for personal snippets. The submitted scope starts an explicit network query."
		case listActionSnippetSearch:
			return "Search snippet file contents", "Submit a query to explicitly fetch and search bounded snippet contents. Ordinary / filtering searches only the loaded metadata and filenames."
		case listActionSnippetClearSearch:
			return "Return to snippet metadata", "Clear the content search and reload snippet metadata for the selected provider/project scope."
		case listActionSnippetCreate:
			return "Create a snippet", "Open the shared CLI editor wizard. It previews the exact provider, visibility and content and asks for confirmation before publication."
		case listActionSnippetCancel:
			return "Cancel snippet loading", "Cancel the current request and ignore late responses. Previously accepted rows remain available; refresh retries explicitly."
		case listActionOpen:
			return "Open an existing local checkout", "Requires a matched local clone. Opening an uncloned row does not clone it."
		case listActionRemoteClone:
			return "Clone the selected repository", "For an uncloned row, preview the destination. Enter clones and stays; o clones and opens. A leftover failed destination is labelled inspect for manual review."
		case listActionCopy:
			return "Choose a repository URL to copy", "Requires a selected remote repository; choose its clone URL using the copy controls."
		case listActionAddNote:
			return "Add or browse repository thoughts", "Requires a matched local repository; a Try clone is excluded from repository quick notes. Lowercase n adds a thought; uppercase N browses them."
		}
	case ViewSkills:
		switch id {
		case listActionCapabilityScope:
			return "Switch context or all repositories", "Uppercase A changes the shared SKILLS/MCP project scope for this TUI run. Global sources remain included; old rows clear before the new scope loads."
		case listActionSkillAdd:
			return "Open the skill installer", "Choose the skill, agent and install scope in the interactive installer; listing skills does not run it."
		case listActionSkillCheck:
			return "Check skill sources", "After the local inventory is loaded, explicitly compare upstream sources. This read-only check may use the network and keeps installed rows visible."
		case listActionSkillUpdate:
			return "Manage or update the selected skill", "Requires a selected skill with a supported update path. Preview/confirmation and the native manager's ownership rules still apply."
		case listActionCopy:
			return "Choose skill data to copy", "Copy a path, safe summary or source URL. Raw-file copy is a separate explicit choice and may include private content."
		}
	case ViewMCP:
		switch id {
		case listActionCapabilityScope:
			return "Switch context or all repositories", "Uppercase A changes the shared SKILLS/MCP project scope for this TUI run. User/global declarations stay included."
		case listActionCopy:
			return "Choose declaration data to copy", "Copy a config path or sanitized declaration summary. Raw-file copy is an explicit separate choice; it is not the sanitized summary."
		}
	}
	return "", ""
}
