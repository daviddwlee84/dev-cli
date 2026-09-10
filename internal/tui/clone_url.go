package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

type cloneSourcesMsg struct {
	selection  selectionToken
	generation uint64
	sources    []gitx.CloneSource
	err        error
}

func (m Model) copyCloneURL() (tea.Model, tea.Cmd) {
	m.mode, m.err, m.status = modeList, nil, ""
	if m.actions.Copy == nil {
		m.err = fmt.Errorf("clipboard integration is unavailable; use dev repo remote --cached --json")
		return m, nil
	}
	if m.view == ViewRemote {
		row, ok := m.currentRemote()
		if !ok {
			return m, nil
		}
		u := row.Repo.CloneURL
		if u == "" {
			u = row.Repo.SSHURL
		}
		if err := gitx.NetworkCloneURL(u); err != nil {
			m.err = fmt.Errorf("no safe clone URL available; refresh the remote inventory explicitly")
			return m, nil
		}
		return m.copyText(u, "clone URL", false)
	}
	item, ok := m.currentRepoItem()
	if !ok || m.actions.CloneSources == nil {
		m.err = fmt.Errorf("repository clone URL lookup is unavailable")
		return m, nil
	}
	path := item.Repo.Repo.Path
	if checkout, child := item.checkout(); child {
		path = checkout.Worktree.Path
	}
	token, ok := m.currentSelectionToken()
	if !ok {
		return m, nil
	}
	m.cloneURLSelection, m.cloneURLSources, m.cloneURLCursor = token, nil, 0
	m.mode, m.cloneURLLoading = modeCloneURL, true
	m.cloneURLGeneration++
	generation := m.cloneURLGeneration
	ctx := m.baseContext()
	return m, func() tea.Msg {
		sources, err := m.actions.CloneSources(ctx, path)
		return cloneSourcesMsg{selection: token, generation: generation, sources: sources, err: err}
	}
}

func (m Model) acceptCloneSources(msg cloneSourcesMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeCloneURL || !m.cloneURLLoading || msg.selection != m.cloneURLSelection || msg.generation != m.cloneURLGeneration {
		return m, nil
	}
	m.cloneURLLoading = false
	if !m.selectToken(msg.selection) {
		m.mode, m.err = modeList, fmt.Errorf("selected repository changed before URL lookup completed")
		return m, nil
	}
	if msg.err != nil {
		m.mode, m.err = modeList, fmt.Errorf("cannot read selected repository fetch URLs")
		return m, nil
	}
	var safe []gitx.CloneSource
	for _, s := range msg.sources {
		if gitx.NetworkCloneURL(s.URL) == nil {
			safe = append(safe, s)
		}
	}
	m.cloneURLSources = gitx.PreferredCloneSources(safe)
	switch len(m.cloneURLSources) {
	case 0:
		m.mode, m.err = modeList, fmt.Errorf("no portable network fetch URL available; no clipboard changes made")
		return m, nil
	case 1:
		return m.copyText(m.cloneURLSources[0].URL, "clone URL", true)
	default:
		return m, nil
	}
}

func (m Model) updateCloneURL(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode, m.cloneURLLoading = modeList, false
		m.cloneURLSelection = selectionToken{}
		return m, nil
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit
	}
	if m.cloneURLLoading {
		return m, nil
	}
	if !m.selectToken(m.cloneURLSelection) {
		m.mode, m.err = modeList, fmt.Errorf("selected repository changed before copy")
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if m.cloneURLCursor > 0 {
			m.cloneURLCursor--
		}
	case "down", "j":
		if m.cloneURLCursor+1 < len(m.cloneURLSources) {
			m.cloneURLCursor++
		}
	case "enter":
		if m.cloneURLCursor < len(m.cloneURLSources) {
			return m.copyText(m.cloneURLSources[m.cloneURLCursor].URL, "clone URL", true)
		}
	}
	return m, nil
}

func (m Model) renderCloneURLs() string {
	if m.cloneURLLoading {
		return "  Reading local fetch URLs… · esc cancel"
	}
	var b strings.Builder
	b.WriteString("  Choose a fetch URL to copy · ↑/↓ select · enter copy · esc cancel\n")
	for i, s := range m.cloneURLSources {
		prefix := "    "
		if i == m.cloneURLCursor {
			prefix = "  > "
		}
		fmt.Fprintf(&b, "%s%s  %s\n", prefix, s.Remote, s.URL)
	}
	return b.String()
}
