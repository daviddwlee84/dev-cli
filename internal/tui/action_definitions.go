package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

// Add actions here with their target and capability predicates. Neither Help
// nor the key dispatcher needs a second copy of these associations.
func newActionRegistry() []ActionSpec {
	var specs []ActionSpec
	add := func(view View, id listAction, label, keys string, applies func(ActionContext) bool, ready func(ActionContext) string) *ActionSpec {
		title, description := actionGuidance(view, id)
		specs = append(specs, ActionSpec{ID: id, Views: []View{view}, Label: label, Keys: keys, Applies: applies, Ready: ready, Topic: helpRelatedTopics(view)[0], HelpTitle: title, Description: description, Run: func(m Model, o actionOption) (tea.Model, tea.Cmd) { return m.executeListAction(o.action) }})
		return &specs[len(specs)-1]
	}
	workflow := func(c ActionContext) string { return requires(c.model.actions.Workflow != nil, "Shared workflow") }
	copyReady := func(c ActionContext) string { return requires(c.model.actions.Copy != nil, "Clipboard") }
	parent := func(c ActionContext) bool { return c.repo != nil && !c.repo.child() }
	file := func(c ActionContext) bool { _, err := c.model.capabilityFilePath(); return err == nil }
	editFile := func(c ActionContext) string { return requires(c.model.actions.EditFile != nil, "File editor") }
	tryApply := func(c ActionContext) string { return requires(c.model.actions.Tries.Apply != nil, "Try operations") }
	sshWorkflow := func(c ActionContext) string { return requires(c.model.actions.SSH.Workflow != nil, "SSH workflow") }
	sshTests := func(c ActionContext) string {
		return requires(c.model.actions.SSH.Test != nil || c.model.actions.SSH.Workflow != nil, "SSH connectivity tests")
	}
	sshProfiles := func(c ActionContext) bool { r, ok := c.model.currentSSH(); return ok && len(r.Profiles) > 0 }
	sshOnboard := func(c ActionContext) string {
		return requires(c.model.actions.SSH.PrepareOnboarding != nil, "SSH setup")
	}
	sshRegistry := func(c ActionContext) string {
		if c.model.ssh.Sources["registry"] == "unavailable" {
			return "Machine mappings unavailable · open issues and suggested actions"
		}
		return sshWorkflow(c)
	}

	add(ViewTasks, listActionDone, "finish task…", "", func(c ActionContext) bool {
		return c.task != nil && (c.task.Task.State == task.Hot || c.task.Task.State == task.Warm) && taskOpenBlocker(*c.task) == nil
	}, workflow)
	add(ViewTasks, listActionRetire, "retire task (keep branch)…", "", func(c ActionContext) bool { return c.task != nil && c.task.Task.State == task.Done }, workflow)
	add(ViewTasks, listActionResume, "resume task…", "", func(c ActionContext) bool {
		return c.task != nil && (c.task.Task.State == task.Warm || c.task.Task.State == task.Cold)
	}, workflow)
	add(ViewTasks, listActionSweep, "inspect and recover this task…", "", nil, workflow)
	add(ViewTasks, listActionOpen, "open task", "enter o", nil, func(c ActionContext) string {
		if err := taskOpenBlocker(*c.task); err != nil {
			return err.Error()
		}
		return requires(c.model.actions.Open != nil, "Opening tasks")
	}).Description = "Open an eligible task checkout. Cold or missing work needs explicit resume or recovery."
	add(ViewTasks, listActionEditNext, "edit next action", "c", nil, func(c ActionContext) string { return requires(c.model.actions.SetNext != nil, "Next-action editing") })
	add(ViewTasks, listActionPark, "park warm", "p", func(c ActionContext) bool {
		return c.task != nil && (c.task.Task.State == task.Hot || c.task.Task.State == task.Warm)
	}, func(c ActionContext) string { return requires(c.model.actions.Park != nil, "Task parking") })

	add(ViewRepos, listActionRepoCreate, "new repository…", "n", nil, func(c ActionContext) string {
		return requires(c.model.actions.Repos.Create != nil, "Repository wizard")
	}).Scope = actionView
	add(ViewRepos, listActionOpen, "open repository", "enter o", nil, func(c ActionContext) string {
		if c.repo.child() {
			checkout, ok := c.repo.checkout()
			if !ok || !checkout.Exists || checkout.Worktree.Prunable {
				return "Worktree checkout is missing or prunable"
			}
			return requires(c.model.actions.OpenCheckout != nil, "Opening worktrees")
		}
		return requires(c.model.actions.OpenRepo != nil, "Opening repositories")
	}).Title = func(c ActionContext) string {
		if c.repo.child() {
			return "open worktree"
		}
		return "open repository"
	}
	add(ViewRepos, listActionToggleWorktrees, "expand or collapse worktrees", "space", func(c ActionContext) bool { return c.repo != nil && (c.repo.child() || c.repo.Repo.Worktrees > 0) }, func(c ActionContext) string {
		if c.repo.Repo.Context.WorktreeErr != nil {
			return "Worktree observations are unavailable"
		}
		return ""
	})
	add(ViewRepos, listActionRepoMetadata, "edit repository metadata", "m", parent, func(c ActionContext) string {
		return requires(c.model.actions.Repos.Patch != nil, "Repository metadata editing")
	})
	add(ViewRepos, listActionRepoDemote, "demote back to a Try…", "", parent, func(c ActionContext) string {
		asset := c.repo.Repo.Asset
		if asset == nil || asset.Kind != catalog.KindRepository || asset.Experiment == nil || asset.Experiment.Phase != catalog.PhaseGraduated {
			return "Only a previously graduated Try can be demoted"
		}
		if asset.MoveIntent != nil {
			return "Resolve the pending catalog move first"
		}
		return workflow(c)
	})
	add(ViewRepos, listActionStartWorktree, "start worktree task", "s", parent, func(c ActionContext) string {
		return requires(c.model.actions.Workflow != nil || c.model.actions.Start != nil, "Worktree task creation")
	})
	add(ViewRepos, listActionStartDirect, "start direct task", "d", parent, func(c ActionContext) string {
		return requires(c.model.actions.Workflow != nil || c.model.actions.StartDirect != nil, "Direct task creation")
	})
	add(ViewRepos, listActionCopy, "copy repository data…", "y", nil, copyReady)
	add(ViewRepos, listActionCopyCloneURL, "copy clone URL", "", nil, func(c ActionContext) string {
		if reason := copyReady(c); reason != "" {
			return reason
		}
		return requires(c.model.actions.CloneSources != nil, "Clone URL lookup")
	})

	add(ViewFleet, listActionOpen, "open repository on host", "enter o", func(c ActionContext) bool {
		return c.fleet != nil && (c.model.hostFleetEnabled() || c.fleet.Repository != nil)
	}, func(c ActionContext) string {
		if c.model.hostFleetEnabled() {
			return requires(c.model.actions.NavigateFleet != nil || (c.fleet.Local && c.fleet.Repository == nil), "Host navigation")
		}
		return requires(c.model.actions.OpenFleet != nil, "Fleet repository opening")
	})
	add(ViewFleet, listActionFleetToggle, "expand or collapse repositories", "space", func(c ActionContext) bool { return c.model.hostFleetEnabled() }, nil)
	add(ViewFleet, listActionFleetRefresh, "refresh this host", "", func(c ActionContext) bool { return c.model.hostFleetEnabled() }, nil)
	add(ViewFleet, listActionFleetRefreshAll, "update all configured hosts over SSH", "", func(c ActionContext) bool { return c.model.hostFleetEnabled() }, nil).Scope = actionView
	s := add(ViewFleet, listActionFleetLocal, "show or hide the local host", "a", nil, nil)
	s.Scope = actionView
	s.Title = func(c ActionContext) string { return c.model.fleetLocalToggleLabel() }

	add(ViewTries, listActionTryMark, "edit tags and note", "", nil, tryApply)
	add(ViewTries, listActionTryDeprecate, "deprecate (metadata only)", "", func(c ActionContext) bool { return c.try.Item.Phase == catalog.PhaseActive }, tryApply)
	add(ViewTries, listActionTryReactivate, "reactivate", "", func(c ActionContext) bool { return c.try.Item.Phase == catalog.PhaseDeprecated }, tryApply)
	add(ViewTries, listActionTryArchive, "archive locally (reversible move)", "", func(c ActionContext) bool {
		return c.try.Item.Phase != catalog.PhaseGraduated && c.try.LocationState() == catalog.LocationPresent
	}, tryApply)
	add(ViewTries, listActionTryRestore, "restore from local archive", "", func(c ActionContext) bool {
		return c.try.Item.Phase != catalog.PhaseGraduated && c.try.LocationState() == catalog.LocationArchived
	}, tryApply)
	add(ViewTries, listActionTryGraduate, "graduate into a project", "", func(c ActionContext) bool {
		return c.try.Item.Phase != catalog.PhaseGraduated && (c.try.LocationState() == catalog.LocationPresent || c.try.LocationState() == catalog.LocationArchived)
	}, tryApply)
	add(ViewTries, listActionOpen, "open Try", "enter o", func(c ActionContext) bool { return c.try.Present() }, tryApply)

	add(ViewRemote, listActionRemoteContent, "Show snippets", "", nil, nil).Scope = actionView
	specs[len(specs)-1].Title = func(c ActionContext) string {
		if c.model.snippets.enabled {
			return "Show repositories"
		}
		return "Show snippets"
	}
	snippetMode := func(c ActionContext) bool { return c.model.snippetsActive() }
	snippetLoad := func(c ActionContext) string { return requires(c.model.actions.Snippets.Load != nil, "Snippet listing") }
	for _, entry := range []struct {
		id    listAction
		label string
	}{
		{listActionSnippetProvider, "choose snippet provider…"},
		{listActionSnippetProject, "GitLab project / personal scope…"},
		{listActionSnippetSearch, "search snippet contents…"},
	} {
		add(ViewRemote, entry.id, entry.label, "", snippetMode, snippetLoad).Scope = actionView
	}
	add(ViewRemote, listActionSnippetClearSearch, "clear content search", "", func(c ActionContext) bool { return c.model.snippetsActive() && c.model.snippets.query.Content }, snippetLoad).Scope = actionView
	add(ViewRemote, listActionSnippetCancel, "cancel snippet request", "", func(c ActionContext) bool { return c.model.snippetsActive() && c.model.snippets.loading }, nil).Scope = actionView
	add(ViewRemote, listActionSnippetCreate, "create snippet…", "", snippetMode, func(c ActionContext) string {
		return requires(c.model.actions.Snippets.Create != nil, "Snippet wizard")
	}).Scope = actionView
	for _, id := range []listAction{listActionSnippetAll, listActionSnippetGitHub, listActionSnippetGitLab} {
		s := add(ViewRemote, id, "", "", snippetMode, snippetLoad)
		s.Scope, s.Menu = actionView, menuAdapter
	}
	add(ViewRemote, listActionCopy, "copy repository URL…", "y", nil, copyReady).Title = func(c ActionContext) string {
		if c.model.snippetsActive() {
			return "copy snippet URL"
		}
		return "copy repository URL…"
	}
	add(ViewRemote, listActionCopyCloneURL, "copy clone URL", "", func(c ActionContext) bool { return c.remote != nil }, copyReady)
	add(ViewRemote, listActionOpen, "open local checkout", "enter o", func(c ActionContext) bool {
		if c.model.snippetsActive() {
			_, ok := c.model.currentSnippet()
			return ok
		}
		return c.remote != nil && c.remote.Cloned()
	}, func(c ActionContext) string {
		if c.model.snippetsActive() {
			return requires(c.model.actions.Snippets.Open != nil, "Snippet browser")
		}
		return requires(c.model.actions.OpenRemote != nil, "Opening local clones")
	}).Title = func(c ActionContext) string {
		if c.model.snippetsActive() {
			return "open snippet in browser"
		}
		return "open local checkout"
	}
	add(ViewRemote, listActionRemoteClone, "clone repository…", "c", func(c ActionContext) bool { return c.remote != nil && !c.remote.Cloned() }, func(c ActionContext) string {
		return requires(c.model.actions.CloneRemote != nil, "Repository cloning")
	})

	for _, view := range []View{ViewSkills, ViewMCP} {
		label := "open primary skill file"
		pathLabel, summaryLabel, rawLabel := "copy primary skill file path", "copy safe skill summary", "copy raw primary skill file"
		if view == ViewMCP {
			label, pathLabel, summaryLabel, rawLabel = "open MCP config", "copy MCP config path", "copy safe declaration summary", "copy raw MCP config file"
		}
		add(view, listActionOpenCapabilityFile, label, "e", file, editFile)
		add(view, listActionCopyCapabilityPath, pathLabel, "", file, copyReady)
		add(view, listActionCopyCapabilitySummary, summaryLabel, "", nil, copyReady)
		add(view, listActionCopyCapabilityRaw, rawLabel, "", file, func(c ActionContext) string {
			if reason := copyReady(c); reason != "" {
				return reason
			}
			return requires(c.model.actions.ReadFile != nil, "Source file reading")
		})
		add(view, listActionCopy, "choose data to copy", "y", nil, copyReady).Menu = menuAdapter
		add(view, listActionCapabilityScope, "switch context or all repositories", "A", nil, nil).Scope = actionView
	}
	add(ViewSkills, listActionCopySkillSourceURL, "copy skill source URL", "", func(c ActionContext) bool { return c.skill.SourceURL != "" }, copyReady)
	add(ViewSkills, listActionSkillManage, "manage selected skill…", "", nil, workflow)
	add(ViewSkills, listActionSkillRemove, "remove skills in this scope…", "", nil, workflow)
	add(ViewSkills, listActionSkillRemoveAll, "remove skills across repositories/global…", "", nil, workflow).Scope = actionView
	add(ViewSkills, listActionSkillRepo, "manage this project's skills…", "", func(c ActionContext) bool { return c.skill.Scope == agentskill.ScopeProject }, workflow)
	add(ViewSkills, listActionSkillVisible, "manage project + global skills…", "", func(c ActionContext) bool { return c.skill.Scope == agentskill.ScopeProject }, workflow)
	add(ViewSkills, listActionSkillGlobal, "manage global skills…", "", nil, workflow).Scope = actionView
	add(ViewSkills, listActionSkillUpdate, "update selected skill…", "u", func(c ActionContext) bool { return agentskill.CanUpdate(*c.skill) }, func(c ActionContext) string {
		state := c.model.viewLoad(ViewSkills)
		if state.loading || !state.hasSnapshot {
			return "wait for local agent skills to finish loading"
		}
		return requires(c.model.actions.Workflow != nil || c.model.actions.UpdateSkill != nil, "Skill update")
	})
	add(ViewSkills, listActionSkillAdd, "add skills…", "a", nil, func(c ActionContext) string { return requires(c.model.actions.AddSkill != nil, "Skill installer") }).Scope = actionView
	add(ViewSkills, listActionSkillCheck, "check skill sources", "c", nil, func(c ActionContext) string {
		state := c.model.viewLoad(ViewSkills)
		if state.loading || !state.hasSnapshot {
			return "wait for local agent skills to finish loading"
		}
		return requires(c.model.actions.CheckSkills != nil, "Skill source checks")
	}).Scope = actionView

	add(ViewSSH, listActionSSHDetails, "full machine / source details", "", nil, nil)
	add(ViewSSH, listActionSSHConnect, "connect through an exact SSH profile…", "enter o", func(c ActionContext) bool {
		return c.ssh != nil && len(c.ssh.machine.Profiles)+len(c.ssh.machine.LAN)+len(c.ssh.machine.Tailscale) > 0
	}, func(c ActionContext) string {
		r, _ := c.model.currentSSH()
		if len(r.Profiles) == 0 && len(r.LAN)+len(r.Tailscale) > 0 {
			return requires(c.model.actions.SSH.PrepareOnboarding != nil || c.model.actions.SSH.Workflow != nil, "SSH setup")
		}
		return sshWorkflow(c)
	})
	add(ViewSSH, listActionSSHProbe, "test connectivity / SSH authentication…", "p", sshProfiles, sshTests)
	add(ViewSSH, listActionSSHDiagnose, "diagnose an SSH profile…", "", sshProfiles, sshTests)
	add(ViewSSH, listActionSSHRegister, "register an SSH profile in fleet / Herdr…", "", sshProfiles, sshRegistry)
	add(ViewSSH, listActionSSHSetupTarget, "set up this discovered target…", "", func(c ActionContext) bool {
		r, ok := c.model.currentSSH()
		return ok && len(r.LAN)+len(r.Tailscale) > 0
	}, sshOnboard)
	add(ViewSSH, listActionSSHInstallKey, "set up / install an SSH key…", "", sshProfiles, sshOnboard)
	add(ViewSSH, listActionSSHSetup, "set up / import connections…", "n", nil, func(c ActionContext) string {
		return requires(c.model.actions.SSH.PrepareOnboarding != nil || c.model.actions.SSH.Workflow != nil, "SSH setup")
	}).Scope = actionView
	add(ViewSSH, listActionSSHDiscover, "discover Tailscale / LAN hosts…", "c", nil, func(c ActionContext) string {
		return requires(c.model.actions.SSH.Discover != nil || c.model.actions.SSH.Workflow != nil, "SSH discovery")
	}).Scope = actionView
	add(ViewSSH, listActionSSHMappings, "adopt / link / unlink / merge machine mappings…", "e m", nil, sshRegistry).Scope = actionView
	add(ViewSSH, listActionSSHCopy, "copy machine data…", "y", nil, copyReady)
	add(ViewSSH, listActionSSHToggle, "expand connection profiles", "space", nil, nil).Menu = menuAdapter

	for _, view := range []View{ViewTasks, ViewRepos, ViewRemote} {
		keys := "n"
		if view == ViewRepos {
			keys = "a"
		}
		applies := func(c ActionContext) bool { _, ok := c.model.selectedNoteTarget(); return ok }
		add(view, listActionAddNote, "add note", keys, applies, func(c ActionContext) string { return requires(c.model.actions.Notes.Add != nil, "Repository notes") }).Topic = "notes"
		add(view, listActionBrowseNotes, "browse notes", "N", applies, func(c ActionContext) string { return requires(c.model.actions.Notes.List != nil, "Repository notes") }).Topic = "notes"
	}
	for _, view := range []View{ViewTasks, ViewRepos, ViewRemote, ViewTries} {
		s := add(view, listActionStats, "open activity heatmap", "H", func(c ActionContext) bool { return c.model.selectedRepoName() != "" }, func(c ActionContext) string {
			_, target := c.model.selectedNoteTarget()
			return requires((target && c.model.actions.Stats.Read != nil) || c.model.actions.LoadStats != nil, "Activity history")
		})
		s.Keywords, s.Topic = "stats heatmap activity", "journal"
		if view == ViewRemote || view == ViewTries {
			s.Menu = menuAdapter
		}
	}
	for _, view := range []View{ViewRepos, ViewTries} {
		s := add(view, listActionTriage, "triage / finish up this item…", "", nil, workflow)
		s.Title = func(c ActionContext) string {
			if c.try != nil && c.try.Item.Live.Presence == "missing" {
				return "forget missing Try entry…"
			}
			return "triage / finish up this item…"
		}
		add(view, listActionTriageFiltered, "organize current filtered results…", "", func(c ActionContext) bool { return c.model.count() > 0 }, workflow).Scope = actionFiltered
		add(view, listActionTriageAll, "organize all local work…", "", nil, workflow).Scope = actionView
	}
	add(ViewRepos, listActionSkillRepo, "manage repository skills…", "", parent, workflow)
	add(ViewRepos, listActionSkillFiltered, "manage skills across filtered repositories…", "", func(c ActionContext) bool { return len(c.model.visibleRepos()) > 0 }, workflow).Scope = actionFiltered
	hygieneMenu := add(ViewRepos, listActionHygieneMenu, "hygiene…", "", hygieneTargetApplicable, hygieneReady)
	hygieneMenu.Description = "Inspect hooks and policy, read this checkout's stored scan, scan an explicit scope, or review hygiene setup."
	hygieneMenu.Keywords = "hygiene scan status report setup secrets privacy"
	for _, entry := range []struct {
		id    listAction
		label string
	}{
		{listActionHygieneStatus, "inspect hygiene status"},
		{listActionHygieneReport, "read latest hygiene report"},
		{listActionHygieneScan, "scan secrets and privacy…"},
		{listActionHygieneRepo, "set up hygiene for this checkout…"},
		{listActionHygieneFiltered, "set up hygiene across filtered repositories…"},
	} {
		s := add(ViewRepos, entry.id, entry.label, "", hygieneTargetApplicable, hygieneReady)
		s.Menu, s.Topic, s.Keywords = menuHygiene, "hygiene", "hygiene status report scan setup secrets privacy"
		if entry.id == listActionHygieneFiltered {
			s.Scope = actionFiltered
			s.Applies = func(c ActionContext) bool { return len(c.model.visibleRepos()) > 0 }
			s.Ready = func(c ActionContext) string {
				for _, row := range c.model.visibleRepos() {
					if row.Pending != "" {
						return "Wait for repository refresh before managing hygiene"
					}
				}
				return workflow(c)
			}
		}
	}
	for _, entry := range []struct {
		id    listAction
		label string
	}{
		{listActionHygieneScanWorktree, "scan working files (worktree)"},
		{listActionHygieneScanStaged, "scan staged index"},
		{listActionHygieneScanHistory, "scan local Git history"},
	} {
		s := add(ViewRepos, entry.id, entry.label, "", hygieneTargetApplicable, hygieneReady)
		s.Menu, s.Topic = menuAdapter, "hygiene"
	}
	for _, view := range []View{ViewTasks, ViewRepos, ViewRemote, ViewTries} {
		add(view, listActionBrowse, "open repository in browser…", "", func(c ActionContext) bool {
			return !c.model.snippetsActive() && (c.try == nil || c.try.Item.Live.Repo != nil)
		}, workflow)
	}
	tryRecover := func(c ActionContext) bool {
		pending := c.try.Item.Entry != nil && c.try.Item.Entry.MoveIntent != nil && c.try.Item.Entry.MoveIntent.Operation == "remove-trash"
		return c.try.Item.Phase != catalog.PhaseGraduated && (c.try.LocationState() == catalog.LocationEvicted || pending)
	}
	add(ViewTries, listActionTryRecover, "reassociate a folder restored from Trash…", "", tryRecover, workflow)
	add(ViewTries, listActionTryDelete, "move to Trash…", "", func(c ActionContext) bool {
		return c.try.Item.Phase != catalog.PhaseGraduated && c.try.Item.Live.Present && !tryRecover(c)
	}, workflow)
	add(ViewTries, listActionTryDeletePermanent, "permanently delete…", "", func(c ActionContext) bool {
		return c.try.Item.ID != "" && c.try.Item.Phase != catalog.PhaseGraduated && c.try.Item.Live.Present && !tryRecover(c)
	}, workflow)

	for _, view := range Views {
		if view != ViewSSH && view != ViewSkills && view != ViewMCP {
			add(view, listActionTools, "tools…", "", func(c ActionContext) bool { return !c.model.snippetsActive() && len(c.model.actions.Tools) > 0 }, nil)
		}
		add(view, listActionIssues, "issues / suggested actions…", "", nil, nil).Scope = actionView
		add(view, listActionSortMenu, "sort columns…", "", func(c ActionContext) bool { return c.model.count() > 0 }, nil).Scope = actionView
		add(view, listActionLastTriage, "last triage results…", "", func(c ActionContext) bool { return c.model.lastTriageLedger != nil }, nil).Scope = actionView
		add(view, listActionStatusDetails, "full status / error…", "", func(c ActionContext) bool { return c.model.currentStatusText() != "" }, nil).Scope = actionView
		if view != ViewSSH && view != ViewSkills && view != ViewMCP {
			label := "settings / configuration…"
			if view == ViewFleet {
				label = "edit hosts (remotes.toml)…"
			}
			add(view, listActionSettings, label, "e", nil, func(c ActionContext) string {
				if c.model.view == ViewFleet {
					return requires(c.model.actions.EditFleetConfig != nil, "Fleet configuration editor")
				}
				return requires(c.model.actions.EditConfig != nil, "Configuration editor")
			}).Scope = actionView
		}
		s := add(view, listActionRefresh, "refresh this view", "r", nil, nil)
		s.Scope, s.Menu = actionView, menuAdapter
		s.Description = refreshActionDescription(view)
	}
	add(ViewTasks, listActionStateFilter, "filter task state…", "", nil, nil).Scope = actionView
	for _, view := range []View{ViewTasks, ViewTries} {
		s := add(view, listActionToggleHistory, "include or hide completed history", "a", nil, nil)
		s.Scope, s.Menu = actionView, menuAdapter
	}
	add(ViewTries, listActionTryCreate, "new Try…", "n", nil, tryApply).Scope = actionView
	for _, view := range []View{ViewRepos, ViewTries} {
		for _, entry := range []struct {
			id         listAction
			label, key string
		}{{listActionCycleSort, "cycle ordering", "O"}, {listActionReverseSort, "reverse ordering", "R"}} {
			s := add(view, entry.id, entry.label, entry.key, nil, nil)
			s.Scope, s.Menu = actionView, menuAdapter
		}
	}
	for _, entry := range []struct {
		id    listAction
		label string
	}{{listActionRegisterRepo, "add startup repository to repo_paths…"}, {listActionRegisterParent, "add startup repository's parent to scan_roots…"}} {
		add(ViewRepos, entry.id, entry.label, "", func(c ActionContext) bool { return c.model.startupOutside() }, func(c ActionContext) string {
			return requires(c.model.actions.Discovery.Plan != nil, "Discovery registration")
		}).Scope = actionView
	}

	// Parameterized submenus use the same finite dispatch registry. Their
	// providers retain the exact issue, profile, tool or column payload.
	adapters := []struct {
		views []View
		scope actionScope
		ids   []listAction
	}{
		{[]View{ViewTasks}, actionView, []listAction{listActionStateAll, listActionStateHot, listActionStateWarm, listActionStateCold, listActionStateDone}},
		{Views, actionView, []listAction{listActionSortColumn}},
		{[]View{ViewRepos}, actionView, []listAction{listActionRegistrationConfirm, listActionRegistrationCancel, listActionRegistrationEdit}},
		{[]View{ViewSSH}, actionSelection, []listAction{listActionSSHCopyID, listActionSSHCopyAliases, listActionSSHCopySummary}},
		{[]View{ViewFleet}, actionView, []listAction{listActionFleetHost, listActionFleetProfiles, listActionFleetProfile}},
		{Views, actionView, []listAction{listActionIssuesScope, listActionIssuesNext, listActionIssuesPrevious, listActionIssueDetails, listActionIssueFull, listActionIssueCopy, listActionIssueAction, listActionIssueConfirm}},
	}
	for _, family := range adapters {
		for _, view := range family.views {
			for _, id := range family.ids {
				s := add(view, id, "", "", nil, nil)
				s.Scope, s.Menu = family.scope, menuAdapter
			}
		}
	}

	// Dynamic issue execution must receive its finite typed payload.
	for i := range specs {
		if issueAction(specs[i].ID) {
			specs[i].Run = func(m Model, o actionOption) (tea.Model, tea.Cmd) { return m.runIssueAction(o.action, o) }
		}
	}
	return specs
}
