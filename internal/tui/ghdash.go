package tui

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/forge"
)

type GHDashRequest struct {
	Path         string
	Repository   forge.RemoteRepo
	ResolveLocal bool
}

// GHDash is a native optional action. It is independent of user-defined tool
// bindings so an uncloned REMOTE repository can still be selected exactly.
type GHDashActions struct {
	Probe   func(context.Context) bool
	Prepare func(context.Context, GHDashRequest) (Workflow, error)
}

type ghDashProbeMsg struct {
	generation   uint64
	availability ToolAvailability
}
type ghDashFinishedMsg struct{ err error }

func (m Model) probeGHDash() tea.Cmd {
	if m.actions.GHDash.Probe == nil {
		return nil
	}
	probe, ctx, generation := m.actions.GHDash.Probe, m.baseContext(), m.configGeneration
	return func() tea.Msg {
		availability := ToolUnavailable
		if probe(ctx) {
			availability = ToolAvailable
		}
		if ctx.Err() != nil {
			availability = ToolUnknown
		}
		return ghDashProbeMsg{generation: generation, availability: availability}
	}
}

func (m Model) ghDashTarget() (GHDashRequest, bool) {
	if m.view == ViewRemote {
		item, ok := m.currentRemoteItem()
		if !ok || item.more || item.Repository.Repo.Forge != forge.GitHub || remotePRRepositoryKey(item.Repository.Repo) == "" {
			return GHDashRequest{}, false
		}
		return GHDashRequest{Path: item.Repository.LocalPath, Repository: item.Repository.Repo}, true
	}
	if m.view != ViewRepos {
		return GHDashRequest{}, false
	}
	item, ok := m.currentRepoItem()
	if !ok {
		return GHDashRequest{}, false
	}
	path, branch := item.Repo.Repo.Path, item.Repo.Status.Branch
	if checkout, child := item.checkout(); child {
		path, branch = checkout.Worktree.Path, checkout.Branch()
	}
	selected := ""
	for _, ref := range item.Repo.Topology.Branches {
		if ref.Branch == branch && ref.Remote != "." {
			selected = ref.Remote
		}
	}
	if selected == "" {
		for _, remote := range item.Repo.Topology.Remotes {
			if remote.Name == "origin" {
				selected = "origin"
			}
		}
	}
	var chosen *forge.RemoteRepo
	for _, remote := range item.Repo.Topology.Remotes {
		if selected != "" && remote.Name != selected {
			continue
		}
		for _, raw := range remote.FetchURLs {
			web, valid := forge.DeriveWebURL(forge.WebURLRequest{Remote: raw})
			if !valid || web.Provider != forge.GitHub {
				return GHDashRequest{}, false
			}
			parsed, err := url.Parse(web.URL)
			if err != nil {
				return GHDashRequest{}, false
			}
			fullName := strings.Trim(parsed.Path, "/")
			if chosen != nil && chosen.URL != web.URL {
				return GHDashRequest{Path: path, Repository: forge.RemoteRepo{Forge: forge.GitHub}, ResolveLocal: true}, true
			}
			repository := forge.RemoteRepo{Forge: forge.GitHub, FullName: fullName, URL: web.URL}
			chosen = &repository
		}
	}
	if chosen == nil {
		return GHDashRequest{}, false
	}
	return GHDashRequest{Path: path, Repository: *chosen, ResolveLocal: true}, true
}

func (m Model) ghDashReason() string {
	if reason := requires(m.actions.GHDash.Prepare != nil, "gh-dash integration"); reason != "" {
		return reason
	}
	if m.actions.GHDash.Probe != nil && m.ghDashAvailability != ToolAvailable {
		if m.ghDashAvailability == ToolUnknown {
			return "gh-dash availability is still being checked"
		}
		return "gh-dash is unavailable; install it with gh extension install dlvhdr/gh-dash"
	}
	if item, ok := m.currentRepoItem(); ok {
		if item.Repo.TopologyPending || item.Repo.TopologyErr != nil {
			return "Wait for repository remote observations"
		}
		if checkout, child := item.checkout(); child && (!checkout.Exists || checkout.Worktree.Prunable) {
			return "Worktree checkout is missing or prunable"
		}
	}
	return ""
}

func (m Model) runGHDash() (tea.Model, tea.Cmd) {
	target, ok := m.ghDashTarget()
	if !ok || m.actions.GHDash.Prepare == nil {
		return m, nil
	}
	workflow, err := m.actions.GHDash.Prepare(m.baseContext(), target)
	if err != nil {
		m.err = err
		return m, nil
	}
	if workflow == nil {
		m.err = fmt.Errorf("gh-dash launcher is unavailable")
		return m, nil
	}
	m.cancelRemotePRs(false)
	m.err, m.status = nil, "opening gh-dash…"
	return m, tea.Exec(workflow, func(err error) tea.Msg { return afterExec(ghDashFinishedMsg{err: err}) })
}
