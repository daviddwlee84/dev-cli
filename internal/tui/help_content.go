package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// helpEntry is presentation metadata, never an instruction to dispatch an
// action. Descriptions include the prerequisites that may hide a menu action.
type helpEntry struct {
	ID, Group, Key, Title, Description, Topic string
	View                                      View
	Guide                                     bool
}

func helpTLDR(view View) string {
	switch view {
	case ViewRepos:
		return "Browse local repositories → select a checkout → open it or choose actions."
	case ViewFleet:
		return "Browse configured machines → inspect their observations → open a repository on its host."
	case ViewTries:
		return "Create a scratch experiment → resume it → archive, restore or graduate when ready."
	case ViewRemote:
		return "Find a repository on your forge → open its local checkout or explicitly clone it."
	case ViewSkills:
		return "Inspect installed agent skills → check source freshness when needed → manage one skill."
	case ViewMCP:
		return "Inspect static MCP declarations → read their scope and settings → open the source file."
	default:
		return "Open a task → work and checkpoint → park with a next action, or finish through actions."
	}
}

func helpRelatedTopics(view View) []string {
	switch view {
	case ViewRepos:
		return []string{"repositories", "git-status", "worktrees", "notes", "storage", "adopting", "tui"}
	case ViewFleet:
		return []string{"fleet", "ssh", "git-status", "tui"}
	case ViewTries:
		return []string{"tries", "storage", "git-status", "tui"}
	case ViewRemote:
		return []string{"repositories", "bootstrap", "pull-requests", "tui"}
	case ViewSkills:
		return []string{"skills", "interop", "agents", "tui"}
	case ViewMCP:
		return []string{"mcp", "interop", "agents", "tui"}
	default:
		return []string{"parking", "retirement", "worktrees", "branching", "commits", "agents", "tui"}
	}
}

func (m Model) helpKeyEntries(view View) []helpEntry {
	var entries []helpEntry
	add := func(id, key, title, description, topic string, available bool) {
		if !available {
			description += " This action is unavailable in this dashboard session."
		}
		entries = append(entries, helpEntry{ID: view.String() + ":" + id, Group: "This view", Key: key, Title: title, Description: description, Topic: topic, View: view})
	}
	add("actions", "Ctrl+O / right-click / click selected", "Show available actions", "Click a different row to select it; click the selected row to open its menu. Opening the menu executes nothing. Available actions depend on the row, its current observations and this session's integrations.", "tui", true)
	stats := m.actions.LoadStats != nil || m.actions.Stats.Read != nil
	copyAvailable := m.actions.Copy != nil
	switch view {
	case ViewTasks:
		add("open", "Enter / o", "Open the selected task", "Requires a task with an eligible checkout. Cold or missing work needs the explicit resume/recovery action.", "parking", m.actions.Open != nil)
		add("task-actions", "Ctrl+O", "Show task actions", "Finish, resume, retire, inspect or filter task state through the selected task's action menu. Space has no action on this flat list.", "retirement", true)
		add("park", "p", "Park warm", "For a selected HOT or WARM task, enter its next action; parking closes its runtime and retains its checkout.", "parking", m.actions.Park != nil)
		add("next", "c", "Edit next action", "Edit the selected task's next-action reminder.", "parking", m.actions.SetNext != nil)
		add("note", "n", "Add a repository thought", "Attach a durable quick note to the selected task's canonical repository.", "notes", m.actions.Notes.Add != nil)
		add("notes", "N", "Browse repository thoughts", "Read, search and manage the selected repository's notes; note deletion has its own confirmation.", "notes", m.actions.Notes.List != nil)
		add("history", "a", "Include or hide DONE tasks", "Toggle finished tasks in this list; explicit task-state filters are cleared.", "retirement", true)
		add("stats", "H", "Show repository activity", "Read the selected task's repository activity heatmap.", "journal", stats)
	case ViewRepos:
		add("open", "Enter / o", "Open the selected checkout", "A repository row opens the main checkout; an expanded worktree row opens that exact checkout. Missing or prunable worktrees cannot be opened.", "repositories", m.actions.OpenRepo != nil || m.actions.OpenCheckout != nil)
		add("worktrees", "Space", "Expand or collapse worktrees", "For a repository with known linked worktrees, reveal each checkout's Git, runtime and task state. Space on a child collapses its parent.", "worktrees", true)
		add("new", "n", "Create or clone a repository", "Open the repository wizard, including when the list is empty. Completion returns to the dashboard and refreshes local inventory.", "repositories", m.actions.Repos.Create != nil)
		add("start", "s", "Start a worktree task", "Select a repository main row, then choose the task and explicit base. Creates a managed checkout outside the repository; prepares a runtime surface.", "worktrees", m.actions.Start != nil || m.actions.Workflow != nil)
		add("direct", "d", "Start a direct task", "Select a repository main row to record work on its current branch without creating a branch or worktree.", "branching", m.actions.StartDirect != nil || m.actions.Workflow != nil)
		add("note", "a", "Add a repository thought", "Attach a durable quick note to the selected canonical repository, including when a linked worktree is selected.", "notes", m.actions.Notes.Add != nil)
		add("notes", "N", "Browse repository thoughts", "Read, search and manage the selected repository's notes.", "notes", m.actions.Notes.List != nil)
		add("metadata", "m", "Edit tags and summary", "Edit catalog metadata for a repository main row; this summary is separate from its multiple quick notes.", "repositories", m.actions.Repos.Patch != nil)
		add("copy", "y", "Choose repository data to copy", "Requires a selected row. Choose context, path, branch or clone URL from the copy controls; copying does not contact a remote.", "repositories", copyAvailable)
		add("sort", "O / R", "Cycle or reverse ordering", "Uppercase O cycles activity, latest, name, Git, size and task ordering. Uppercase R reverses it. Column-header sorting is available separately.", "tui", true)
		add("stats", "H", "Show repository activity", "Read the selected repository's activity heatmap.", "journal", stats)
	case ViewFleet:
		if m.hostFleetEnabled() {
			add("open", "Enter / o", "Navigate to the host or repository", "The local host opens REPOS. Remote navigation uses its exact host/session and preserves the current-client boundary; no extra client is opened inside Herdr. Enter never expands the tree.", "fleet", true)
			add("expand", "Space", "Expand or collapse host repositories", "Space changes only the tree. On a repository child it collapses and selects the parent host. Search expansion overrides are temporary; clearing the query restores saved expansion.", "fleet", true)
			add("host-actions", "Ctrl+O", "Show host actions", "Read the shared local Herdr catalog and derive this host's SSH, Herdr and dotfiles actions. Multiple saved profiles have a profile picker before exact enable, disable or remove actions. The menu remains available when no rows match.", "fleet", true)
			add("local", "a", "Show or hide the local host", "Local is hidden initially. Showing it adds a collapsed host last, independent of sorting. Search and coverage include local repositories only while the host is shown; local rows reuse REPOS without another scan.", "fleet", true)
		} else {
			add("open", "Enter / o", "Open repository on its host", "Requires a repository row and the configured host connection. A host error/status row has no checkout to open.", "fleet", m.actions.OpenFleet != nil)
			add("local", "a", "Include or hide this machine", "Local fleet rows are hidden by default because REPOS has the richer local inventory.", "fleet", true)
		}
		add("config", "e", "Edit fleet configuration", "Open remotes.toml in the configured editor, then validate and reload it after the editor exits.", "fleet", m.actions.EditFleetConfig != nil)
	case ViewTries:
		add("new", "n", "Create a Try", "Open the scratch experiment form, including when the list is empty. A Try can be a non-Git directory.", "tries", m.actions.Tries.Apply != nil)
		add("open", "Enter / o", "Open the selected Try", "Requires a Try present on this host with no incomplete move. Archived or missing Tries need their own restore or recovery action.", "tries", m.actions.Tries.Apply != nil)
		add("try-actions", "Ctrl+O", "Show experiment actions", "Edit metadata, deprecate, archive, restore or graduate through the menu. Space has no action on this flat list.", "tries", true)
		add("history", "a", "Include or hide Try history", "Toggle deprecated, archived, evicted and graduated entries.", "tries", true)
		add("sort", "O / R", "Cycle or reverse ordering", "Uppercase O cycles activity, name, phase and size ordering. Uppercase R reverses it.", "tui", true)
	case ViewRemote:
		add("open", "Enter / o", "Open an existing local checkout", "Requires a matched local clone. Opening an uncloned row does not clone it.", "repositories", m.actions.OpenRemote != nil)
		add("clone", "c", "Clone the selected repository", "For an uncloned row, preview the destination. Enter clones and stays; o clones and opens. A leftover failed destination is labelled inspect for manual review.", "repositories", m.actions.CloneRemote != nil)
		add("copy", "y", "Choose a repository URL to copy", "Requires a selected remote repository; choose its clone URL using the copy controls.", "repositories", copyAvailable)
		add("note", "n / N", "Add or browse repository thoughts", "Requires a matched local repository; a Try clone is excluded from repository quick notes. Lowercase n adds a thought; uppercase N browses them.", "notes", m.actions.Notes.Add != nil || m.actions.Notes.List != nil)
		add("cancel", "q / Ctrl+C", "Cancel an in-progress clone", "While cloning, request cancellation and wait for its result. This does not automatically delete a partially created destination.", "repositories", m.actions.CloneRemote != nil)
	case ViewSkills:
		add("scope", "A", "Switch context or all repositories", "Uppercase A changes the shared SKILLS/MCP project scope for this TUI run. Global sources remain included; old rows clear before the new scope loads.", "skills", true)
		add("add", "a", "Open the skill installer", "Choose the skill, agent and install scope in the interactive installer; listing skills does not run it.", "skills", m.actions.AddSkill != nil)
		add("check", "c", "Check skill sources", "After the local inventory is loaded, explicitly compare upstream sources. This read-only check may use the network and keeps installed rows visible.", "skills", m.actions.CheckSkills != nil)
		add("update", "u", "Manage or update the selected skill", "Requires a selected skill with a supported update path. Preview/confirmation and the native manager's ownership rules still apply.", "skills", m.actions.UpdateSkill != nil || m.actions.Workflow != nil)
		add("file", "e", "Open the primary skill file", "Requires a locally present primary SKILL.md and a configured editor.", "skills", m.actions.EditFile != nil)
		add("copy", "y", "Choose skill data to copy", "Copy a path, safe summary or source URL. Raw-file copy is a separate explicit choice and may include private content.", "skills", copyAvailable)
	case ViewMCP:
		add("scope", "A", "Switch context or all repositories", "Uppercase A changes the shared SKILLS/MCP project scope for this TUI run. User/global declarations stay included.", "mcp", true)
		add("file", "e", "Open the declaration's source file", "Requires a source config path and a configured editor. The dashboard does not start an MCP server.", "mcp", m.actions.EditFile != nil)
		add("copy", "y", "Choose declaration data to copy", "Copy a config path or sanitized declaration summary. Raw-file copy is an explicit separate choice; it is not the sanitized summary.", "mcp", copyAvailable)
	}
	if view != ViewSkills && view != ViewMCP && view != ViewFleet {
		add("config", "e", "Edit dashboard configuration", "Open this session's config file in the configured editor and reload supported settings when it closes.", "tui", m.actions.EditConfig != nil)
	}

	refresh := "Reload configuration and local observations. Local repository refresh does not fetch Git remotes."
	switch view {
	case ViewFleet:
		refresh = "Refresh the shared local Herdr catalog and the selected host's repositories (a child selects its parent). These observations are independent; old data remains explicitly stale on failure. Update all configured hosts is a separate menu entry; authentication has an explicit terminal handoff."
	case ViewRemote:
		refresh = "Reload configuration and explicitly refresh repositories through configured forge CLIs; this may contact the network."
	case ViewSkills:
		refresh = "Reload local installed skills. Use c separately for the explicit upstream source check."
	case ViewMCP:
		refresh = "Reload local static declarations only; no server is started or health-checked."
	}
	for _, e := range []helpEntry{
		{ID: "move", Key: "j/k / ↓/↑ / Ctrl+N/P", Title: "Move selection", Description: "Move one row down or up. The wheel moves three rows; a click selects a visible row."},
		{ID: "page", Key: "PgDn/PgUp / Ctrl+D/U", Title: "Move one page", Description: "Move the selection down or up by the visible page size."},
		{ID: "ends", Key: "g / G / Home / End", Title: "First or last row", Description: "Lowercase g or Home selects the first row; uppercase G or End selects the last."},
		{ID: "views", Key: "1–7 / Tab / Shift+Tab / h/l / ←/→", Title: "Switch dashboard view", Description: "Choose TASKS, REPOS, FLEET, TRY, REMOTE, SKILLS or MCP. Click a visible top tab for the same action."},
		{ID: "filter", Key: "/", Title: "Filter the list", Description: "Type a query; Enter keeps it and Esc clears it. View-specific structured terms are explained in Guide."},
		{ID: "clear", Key: "0", Title: "Clear filters", Description: "Clear the text query and explicit task-state filter, then select the first row."},
		{ID: "column-sort", Key: "Click column heading", Title: "Sort a column", Description: "Cycle ascending, descending, then default order. The heading's ↑ or ↓ shows this ordering; it is separate from Git divergence."},
		{ID: "refresh", Key: "r", Title: "Refresh this view", Description: refresh},
		{ID: "help", Key: "? / click Help", Title: "Open contextual help", Description: "Start with this view's Keys, then read its Guide or a complete dev help Manual topic."},
		{ID: "quit", Key: "q / Ctrl+C / Esc", Title: "Quit or clear narrowing", Description: "q or Ctrl+C quits the dashboard. Esc first clears a query/state filter; with none it quits. While a clone runs these controls request cancellation."},
	} {
		e.ID, e.Group, e.View, e.Topic = view.String()+":"+e.ID, "Navigation", view, "tui"
		entries = append(entries, e)
	}

	for i, tool := range m.actions.Tools {
		if tool.Key == "" || tool.Name == "" || len(tool.Command) == 0 {
			continue
		}
		if helpToolKeyReserved(view, tool.Key) {
			continue
		}
		availability := "available"
		if tool.Probe != nil {
			switch tool.Availability {
			case ToolUnknown:
				availability = "availability not yet known"
			case ToolUnavailable:
				availability = "unavailable"
			}
		}
		description := "Known status: " + availability + ". Requires a selected checkout on disk; returns to the dashboard after the tool exits. Help does not probe or launch tools."
		if view == ViewFleet || view == ViewSkills || view == ViewMCP {
			description += " This view has no local checkout target for tools."
		}
		entries = append(entries, helpEntry{ID: fmt.Sprintf("%s:tool:%d", view, i), Group: "Custom tools", Key: tool.Key, Title: tool.Name, Description: description, Topic: "tui", View: view})
	}
	return entries
}

func helpToolKeyReserved(view View, key string) bool {
	if _, reserved := config.ReservedKey(key); reserved {
		return true
	}
	// Some keyboard aliases are intercepted by the model even when the config
	// validator accepts them. Do not advertise such a binding as a tool launch.
	switch key {
	case "enter", "esc", "ctrl+c", "down", "up", "ctrl+n", "ctrl+p", "ctrl+d", "pgdown", "ctrl+u", "pgup", "home", "end", "right", "left", "shift+tab", "u":
		return true
	case "A":
		return view == ViewSkills || view == ViewMCP
	default:
		return false
	}
}

func helpGuideEntries(view View) []helpEntry {
	var entries []helpEntry
	add := func(id, group, title, description, topic string) {
		entries = append(entries, helpEntry{ID: view.String() + ":guide:" + id, Group: group, Title: title, Description: description, Topic: topic, View: view, Guide: true})
	}
	add("flow", "Using this view", "Workflow", helpTLDR(view)+" Click the selected row or press Ctrl+O to discover the actions currently offered.", helpRelatedTopics(view)[0])
	add("selection", "Colors and symbols", "Cyan/blue selection and ▸ cursor", "The selected row is bold cyan/blue and begins with ▸. Selection color overrides its normal row color; it does not mean clean, safe, active or synchronized. Color names describe the normal palette; terminal themes and --color can change their appearance.", "tui")
	add("freshness", "Observations", "Unknown, loading and cached data", "Loading, failed and stale observations are not clean or closed facts. Existing rows can stay visible during refresh; read the loading/status text and detail pane. A standalone ? means unknown in Git/runtime/task/size columns, while ?N in Git means N untracked paths. A ~ Git prefix is a previous repository snapshot; … means loading or truncated text depending on its column.", "tui")
	if view == ViewTasks || view == ViewRepos || view == ViewFleet || view == ViewTries {
		add("git", "Colors and symbols", "Git: ⇡N ⇣N ⇕ =N +N !N ?N", "⇡N and ⇣N count commits ahead of and behind the locally recorded upstream. ⇕ means both directions diverged. =N counts conflicted paths, +N staged paths, !N unstaged paths and ?N untracked paths. Staged/unstaged categories can overlap on one path. These observations do not fetch or prove remote durability.", "git-status")
		add("clean", "Colors and symbols", "Git: clean, local and —", "clean means no reported changes or divergence relative to a configured upstream in this observation. local means no reported changes and no tracking upstream; it does not mean there is no remote configured. In REPOS, — is Git not applicable to a bare repository; in TRY it can mean a non-Git directory. An error or missing checkout is separate from these states.", "git-status")
	}
	switch view {
	case ViewTasks:
		add("states", "Colors and symbols", "Task intent: 🔥 HOT · 🌤 WARM · ❄️ COLD · ✅ DONE", "HOT uses orange/coral, WARM amber, COLD blue and DONE green. These are persisted lifecycle intent: working, parked with checkout retained, parked reconstructibly without requiring a checkout, and finished. They do not directly assert whether a runtime is live. READY, REVIEW, MERGED and RETIRED are action/result milestones, not extra task states.", "parking")
		add("columns", "Columns", "TASK · STATE · REPO · BRANCH · GIT · AGE · NEXT", "TASK is the task title; STATE is recorded intent; REPO appears when space permits; BRANCH identifies its branch. AGE uses the newer of its last commit and task-record update, not a live-agent timer. NEXT is your recorded next action; — means none. Git no dir means its checkout is absent; ? means status could not be read. Detail retains the full repository/path and runtime context.", "tui")
		add("lifecycle", "Using this view", "Park, finish and retire", "Warm parking keeps the checkout and records the next action. Finishing, retirement and branch deletion are separate operations. Use the actions menu to inspect the available guarded transitions; task mode, state and fresh observations determine eligibility.", "retirement")
	case ViewRepos:
		add("colors", "Colors and symbols", "Orange dirty rows; gray quiet or external rows", "Without selection color, orange means the stored Git status reports uncommitted paths. A main repository row is gray when it has no recorded tasks and no live runtime; gray alone cannot prove Git clean. For linked worktrees, dirty orange takes priority, then external/ephemeral ownership is dimmed. Other rows retain the normal text color. Pending rows can still carry the old snapshot's color.", "git-status")
		add("tree", "Columns", "REPO · BRANCH · ▸/▾ worktree tree", "REPO names the canonical repository. ▸ before its name means collapsed linked worktrees; ▾ means expanded. Children use ├─/└─ and name the exact checkout, with external/ephemeral ownership labels where applicable. BRANCH is its current branch, (detached) a child without one, and (bare) a bare main repository.", "worktrees")
		add("runtime", "Columns", "LIVE: runtime, agent status, closed, — or ?", "LIVE can show herdr:idle, herdr:working or another reported agent status; N live counts multiple runtime sessions. A parent summarizes sessions across its checkouts, while a child describes that checkout only. — means no live runtime was observed for the parent; closed is no observed child session; ? means unavailable and … means loading. An idle/done/unknown recognized agent still occupies a checkout.", "agents")
		add("tasks", "Columns", "WT · TASKS · NOTES", "WT counts registered linked worktrees, excluding the main checkout; — is zero or not applicable to a child. TASKS shows per-state counts: 🔥 HOT, 🌤 WARM, ❄️ COLD, ✅ DONE; — is no recorded task and ? an unavailable task read. NOTES counts durable quick notes for the canonical repository, shared by its children; — is none.", "notes")
		add("size", "Columns", "SIZE: logical owned bytes and +S", "SIZE measures logical checkout bytes plus private Git metadata. Shared Git storage is shown separately in detail and marked +S; removing one checkout does not reclaim it. This is not physical disk allocation or a deletion estimate. … is measuring, ? a measurement error and — no applicable target; partial measurements are identified in detail.", "storage")
		add("latest", "Columns", "LATEST: latest local activity", "For the repository, LATEST is the newest observed changed-file modification time, commit author time or task-record update across its checkouts/tasks. A child uses that checkout's observed file/commit activity. m/h/d/w/mo are approximate elapsed units; — means no known timestamp. It is not a runtime heartbeat or a freshness guarantee.", "tui")
		add("optional", "Columns", "REMOTE · CATEGORY · PATH", "Optional configured columns show remote/recovery topology, repository grouping and checkout path. REMOTE ? is a failed topology observation; … is loading. CATEGORY — is uncategorized. A ~ path prefix abbreviates the home directory and is unrelated to the ~ Git snapshot marker. Long cells truncate with …; detail/copy retains available full values.", "repositories")
		add("filter", "Using this view", "Filter and order local repositories", "Filter by repository, branch, task, checkout or catalog text; structured terms include tag:, remote: and size:. Click a column heading for ascending, descending and default order. Startup-repository selection changes focus without changing the configured order.", "tui")
		add("discovery", "Using this view", "Add a startup repository to discovery", "When a fully observed startup Git repository is outside configured discovery, REPOS offers Add this repo (repo_paths) or Scan parent directory (scan_roots). Exact paths suit isolated repositories; a parent scan includes eligible siblings. Preview the actual config and scope, then confirm; existing comments/order are preserved.", "repositories")
	case ViewFleet:
		add("columns", "Columns", "Hosts with repository children", "Remote hosts start collapsed; local is hidden until a and stays last when shown. HERDR remains visible on narrow terminals and reports this client's saved registration/enabled state, not connectivity. Repository children leave HERDR blank. OS is in host details; wider tables add BRANCH, GIT, LIVE and TASKS. Not loaded remains unknown.", "fleet")
		add("herdr", "Observations", "Herdr catalog is independent of SSH", "A shared local machine list is read on each FLEET visit, explicit refresh, host menu and completed Herdr action. There is no polling. Only a successful catalog without a matching alias means not added. Failed reads preserve previous states with a stale marker; --no-runtime is not checked. Profile labels and sessions are searchable without IO.", "fleet")
		add("colors", "Colors and symbols", "Cached observations are dimmed", "Green marks an observed live runtime only for a current snapshot. Cached rows are dimmed and retain their observation time; an old clean Git status is not fresh proof. Errors stay attached to their host without hiding usable cached repositories.", "fleet")
		add("network", "Using this view", "Local first, bounded background updates", "Descriptors and caches load without SSH. When background_refresh is enabled, after five seconds or an earlier visit to FLEET, stale or missing host snapshots update one at a time. Explicit host loads take priority. Filtering searches only known host metadata and cached or loaded repositories; matching children appear without changing saved expansion or making network requests.", "fleet")
	case ViewTries:
		add("columns", "Columns", "TRY · PHASE · WHERE · GIT · LAST · SIZE · TAGS", "PHASE is durable active/deprecated/graduated intent. WHERE is this host's location: present, archived, evicted, missing, unavailable or other-host. Missing means an expected location was not found; unavailable means the read failed. LAST uses experiment activity; SIZE is logical owned data. Narrow screens retain fewer columns; detail provides more context.", "tries")
		add("colors", "Colors and symbols", "Gray history; orange dirty; green live", "A non-present location or non-active phase is dimmed first. Otherwise dirty Git is orange, then an observed live runtime is green. These priorities apply before cyan selection. A gray row can still contain uncommitted work; read WHERE, Git and detail before a lifecycle action.", "tries")
		add("marks", "Colors and symbols", "! important · ◆ keep · · unmarked", "The leading intent marker is ! for an important tag, otherwise ◆ for a keep tag, otherwise ·. It is independent of Git !N, which counts unstaged paths. Git — can mean a non-Git Try; repo ? means its Git status is unknown; error means discovery failed.", "tries")
		add("lifecycle", "Using this view", "Archive, restore, graduate and recover", "Use actions for reversible archive/restore, metadata edits and graduation into a durable project. Phase and host location are separate. Incomplete moves or missing paths require inspection/recovery, not an assumption that cleanup succeeded. Filter with tag:, phase:, where:, git:, remote: or size:.", "tries")
	case ViewRemote:
		add("columns", "Columns", "FORGE · REPOSITORY · VIS · UPDATED · LOCAL · DESCRIPTION", "FORGE identifies the provider; REPOSITORY is its full name; VIS is provider visibility. UPDATED is the provider's timestamp, not local activity or cache age. LOCAL repo/try/yes means a known local match, — no matched clone, inspect a failed destination needing review, and an animated marker an in-progress clone/local refresh.", "repositories")
		add("colors", "Colors and symbols", "Green local/clone rows; coral inspect; gray archived", "An existing local clone or an in-progress clone is green. Otherwise inspect is coral, then archived forge repositories are dimmed. Selection overrides these colors. Green does not mean clean, synchronized or that a clone has finished; LOCAL and status distinguish those cases.", "repositories")
		add("network", "Using this view", "Cached discovery and explicit clone", "Authenticated forge inventory loads lazily; cache rows can remain during refresh. Opening requires an existing local clone; c explicitly previews a clone. A successful clone stays pending until REPOS accepts it. Filter by forge, repository and descriptive text, or visibility such as vis:private.", "repositories")
	case ViewSkills:
		add("columns", "Columns", "REPO · SCOPE · SKILL · INSTALL · UPDATE · AGENTS · SOURCE", "REPO identifies a project; SCOPE distinguishes project/global. INSTALL describes local presence and bundled integrity (present, missing, verified or drifted). UPDATE is a separate source comparison. AGENTS lists detected consumers, with +N for additional names. SOURCE is recorded origin or the owning manager; — means no value was available.", "skills")
		add("colors", "Colors and symbols", "Coral attention; green current; gray unverifiable", "Update available, upstream missing and check failed are coral attention states. Current is green for the last completed comparison. Unverifiable is dimmed; unchecked stays normal. These colors follow UPDATE, not INSTALL: green does not guarantee all local files are present or unchanged. See the checked time and integrity in detail.", "skills")
		add("scope", "Using this view", "Context first; explicit source checks", "Context mode inside Git scans the exact startup checkout plus global sources. Outside Git it includes accepted repositories and the startup directory. A toggles both SKILLS/MCP to all accepted repositories. Listing/r only reads local state; c explicitly checks upstream sources. Filters include repo:, scope:, agent:, update:, presence: and integrity:.", "skills")
	case ViewMCP:
		add("columns", "Columns", "REPO · SCOPE · AGENT · SERVER · TRANSPORT · STATE · SOURCE", "Each row is a declaration from an agent's local configuration. SCOPE and SOURCE describe where it came from, including plugin sources; TRANSPORT describes the declared connection. STATE is enabled, disabled or unknown configuration state. Narrow screens hide columns; detail retains safe endpoint, policy and credential-reference information.", "mcp")
		add("colors", "Colors and symbols", "Green enabled; gray disabled; normal unknown", "Green means the declaration explicitly says enabled. Gray means explicitly disabled. Normal text means enablement is unknown. Enabled is not a claim that the server is running, reachable, trusted or healthy; this inventory does not start or probe servers. Selection color overrides declaration color.", "mcp")
		add("scope", "Using this view", "Static declarations and safe summaries", "A shares the SKILLS context/all-repositories scope switch; r rereads static files. Filters include repo:, agent:, scope:, transport:, managed: and state:. Summaries redact argument values and credentials; policy/credential names are references only. Reading Help performs no provider or MCP execution.", "mcp")
	}
	return entries
}

// Shared classifiers keep the renderer's row colors and Help's interpretation
// tied to the same priority rules. Unknown observation state remains separate.
type helpRowColor uint8

const (
	helpColorNormal helpRowColor = iota
	helpColorDirty
	helpColorQuiet
	helpColorExternal
	helpColorAttention
	helpColorCurrent
	helpColorUnknown
	helpColorEnabled
	helpColorDisabled
)

func (c helpRowColor) style() lipgloss.Style {
	switch c {
	case helpColorDirty:
		return styleDirty
	case helpColorQuiet:
		return styleClean
	case helpColorExternal, helpColorUnknown, helpColorDisabled:
		return styleDim
	case helpColorAttention:
		return styleDrift
	case helpColorCurrent, helpColorEnabled:
		return styleLive
	default:
		return lipgloss.NewStyle()
	}
}

func repoItemColor(item repoItem) helpRowColor {
	if checkout, child := item.checkout(); child {
		if checkout.Status.Dirty() {
			return helpColorDirty
		}
		if checkout.Ownership == inventory.CheckoutExternal || checkout.Ownership == inventory.CheckoutEphemeral {
			return helpColorExternal
		}
	} else if item.Repo.Status.Dirty() {
		return helpColorDirty
	} else if len(item.Repo.Tasks) == 0 && !item.Repo.Live {
		return helpColorQuiet
	}
	return helpColorNormal
}

func skillRowColor(row agentskill.Skill) helpRowColor {
	switch row.UpdateStatus {
	case agentskill.UpdateAvailable, agentskill.UpdateMissing, agentskill.UpdateFailed:
		return helpColorAttention
	case agentskill.UpdateCurrent:
		return helpColorCurrent
	case agentskill.UpdateUnknown:
		return helpColorUnknown
	default:
		return helpColorNormal
	}
}

func mcpRowColor(row agentmcp.Declaration) helpRowColor {
	if row.Enabled == nil {
		return helpColorNormal
	}
	if *row.Enabled {
		return helpColorEnabled
	}
	return helpColorDisabled
}

func repoItemStyle(item repoItem) lipgloss.Style          { return repoItemColor(item).style() }
func skillRowStyle(row agentskill.Skill) lipgloss.Style   { return skillRowColor(row).style() }
func mcpRowStyle(row agentmcp.Declaration) lipgloss.Style { return mcpRowColor(row).style() }

func (m Model) helpSelectionColor() string {
	if item, ok := m.currentRepoItem(); ok {
		switch repoItemColor(item) {
		case helpColorDirty:
			return "Orange: the stored Git observation reports uncommitted paths; check freshness below."
		case helpColorQuiet:
			return "Gray: no recorded task and no observed live runtime. Gray alone does not prove clean Git."
		case helpColorExternal:
			return "Gray: external/ephemeral checkout ownership. Gray alone does not prove clean Git."
		default:
			return "Normal text: no special row style applies; read the Git/runtime observations."
		}
	}
	if row, ok := m.currentSkill(); ok {
		switch skillRowColor(row) {
		case helpColorAttention:
			return "Coral: source comparison needs attention (" + string(row.UpdateStatus) + ")."
		case helpColorCurrent:
			return "Green: current at its last source comparison; local INSTALL/integrity is separate."
		case helpColorUnknown:
			return "Gray: source freshness is unverifiable, not proven current."
		default:
			return "Normal text: source freshness has not been established by a completed check."
		}
	}
	if row, ok := m.currentMCP(); ok {
		switch mcpRowColor(row) {
		case helpColorEnabled:
			return "Green: declaration enabled in configuration; running state and health are not checked."
		case helpColorDisabled:
			return "Gray: declaration explicitly disabled in configuration."
		default:
			return "Normal text: declaration enablement is unknown; running state and health are not checked."
		}
	}
	if row, ok := m.currentTask(); ok && row.Task != nil {
		colors := map[task.State]string{task.Hot: "Orange/coral", task.Warm: "Amber", task.Cold: "Blue", task.Done: "Green"}
		return colors[row.Task.State] + ": recorded " + row.Task.State.Label() + " intent; this is separate from current runtime state."
	}
	if row, ok := m.currentFleet(); ok {
		if row.Repository != nil && row.Repository.Live {
			return "Green: a live runtime was recorded in this host observation; a cached row may be stale."
		}
		if row.State != "ok" {
			return "Coral: host state needs attention (" + string(row.State) + ")."
		}
	}
	if row, ok := m.currentTry(); ok {
		switch {
		case row.Where() != string(catalog.LocationPresent) || row.Item.Phase != catalog.PhaseActive:
			return "Gray: the Try is not active/present on this host; this does not imply clean Git."
		case row.Item.Live.Status != nil && row.Item.Live.Status.Dirty():
			return "Orange: the stored Git observation reports uncommitted paths."
		case row.Live:
			return "Green: a live runtime was observed for this Try."
		}
	}
	if row, ok := m.currentRemote(); ok {
		switch {
		case row.Cloned() || m.remoteCloneTargets(row):
			return "Green: local checkout matched or clone in progress; read LOCAL/status for completion."
		case row.CloneProblemPath != "":
			return "Coral: a failed clone destination needs inspection."
		case row.Repo.Archived:
			return "Gray: this repository is archived on the forge."
		}
	}
	return "Normal text: no special row color applies."
}

// helpSelectionSnapshot freezes display text at entry time. It never invokes
// callbacks, rescans sources, refreshes runtime state or asks a tool to probe.
func (m Model) helpSelectionSnapshot() string {
	state := m.viewLoad(m.view)
	lines := []string{"Captured when Help opened · " + strings.ToUpper(m.view.String())}
	provenance := "Source: " + dashCell(string(state.source)) + " · freshness: " + dashCell(string(state.freshness))
	if !state.hasSnapshot {
		provenance += " · no complete view snapshot accepted"
	}
	if state.loading {
		provenance += " · loading; displayed observations may be older"
	}
	lines = append(lines, provenance)
	if err := m.viewError(m.view); err != nil {
		lines = append(lines, "Observation error: "+err.Error())
	}
	if status := m.viewStatus(m.view); status != "" {
		lines = append(lines, "View status: "+status)
	}
	if _, ok := m.currentSelectionToken(); !ok {
		return ansi.Strip(strings.Join(append(lines, "No selected row. Loading or an empty/filtered list is not evidence that every source is empty."), "\n"))
	}
	title, path := m.selectionHeading()
	lines = append(lines, "Selected: "+title, "Cyan/blue and ▸ mark selection, overriding the normal row color.", "Underlying row color: "+m.helpSelectionColor())
	if path != "" {
		lines = append(lines, path)
	}
	if item, ok := m.currentRepoItem(); ok {
		if item.Repo.Pending != "" {
			lines = append(lines, "Repository observation: "+item.Repo.Pending+"; old Git facts and color are not a fresh clean/closed proof.")
		}
		if !item.Repo.ObservedAt.IsZero() {
			lines = append(lines, "Repository observed: "+item.Repo.ObservedAt.Local().Format("2006-01-02 15:04:05"))
		}
		for _, name := range []string{"branch", "git", "live", "latest", "worktrees", "tasks"} {
			lines = append(lines, strings.ToUpper(name)+": "+m.repoItemColumnValue(item, name))
		}
	}
	if row, ok := m.currentFleet(); ok && row.FromCache {
		lines = append(lines, "Host observation: cached/stale; host details and Git/runtime state are not fresh proof.")
	}
	// Help is entered from list mode. Explicitly use it for the captured detail
	// so an input widget can never leak into a future caller's snapshot.
	m.mode = modeList
	if detail := strings.TrimSpace(ansi.Strip(m.renderDetail())); detail != "" {
		lines = append(lines, "", detail)
	}
	return ansi.Strip(strings.Join(lines, "\n"))
}
