package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

type repoFavoriteOverride struct {
	repository repo.Repo
	asset      *catalog.Entry
}

type repoFavoriteMsg struct {
	row   RepoRow
	asset *catalog.Entry
	err   error
}

func (m Model) toggleRepoFavorite() (tea.Model, tea.Cmd) {
	row, ok := m.currentRepo()
	if !ok || m.actions.Repos.SetFavorite == nil || m.repoFavoriteBusy {
		return m, nil
	}
	m.repoFavoriteBusy = true
	m.err, m.status = nil, "Saving repository favorite…"
	return m, func() tea.Msg {
		asset, err := m.actions.Repos.SetFavorite(context.Background(), row, !row.Favorite())
		return repoFavoriteMsg{row: row, asset: asset, err: err}
	}
}

func (m Model) applyRepoFavorite(msg repoFavoriteMsg) (tea.Model, tea.Cmd) {
	m.repoFavoriteBusy = false
	m.statusSeverity = ""
	if msg.err != nil {
		m.err, m.status = msg.err, ""
		return m, nil
	}
	if msg.asset == nil || msg.asset.ID == "" || (msg.row.Asset != nil && msg.row.Asset.ID != "" && msg.row.Asset.ID != msg.asset.ID) {
		m.err, m.status = fmt.Errorf("repository favorite returned a different catalog identity"), ""
		return m, nil
	}
	token, path := m.currentToken(), m.repoFocusPath()
	if m.repoFavorites == nil {
		m.repoFavorites = make(map[string]repoFavoriteOverride)
	}
	m.repoFavorites[msg.asset.ID] = repoFavoriteOverride{repository: msg.row.Repo, asset: msg.asset.Clone()}
	m.err, m.status = nil, "Removed favorite: "+msg.asset.Title()
	if msg.asset.Favorite {
		m.status = "Added favorite: " + msg.asset.Title()
	}
	m.restoreRepoFocus(token, path)
	return m, nil
}

// A successful intent write survives older cache/enrichment messages and scan
// generations. A newer catalog revision remains authoritative for all metadata.
func (m Model) withRepoFavorite(row RepoRow) RepoRow {
	for id, saved := range m.repoFavorites {
		if row.Asset != nil && row.Asset.ID != "" {
			if row.Asset.ID != id {
				continue
			}
			if row.Asset.Updated.After(saved.asset.Updated) {
				return row
			}
			row.Asset = row.Asset.Clone()
			row.Asset.Favorite = saved.asset.Favorite
			return row
		}
		if !sameFavoriteRepository(row.Repo, saved.repository) {
			continue
		}
		row.Asset = saved.asset.Clone()
		return row
	}
	return row
}

func sameFavoriteRepository(a, b repo.Repo) bool {
	if a.CommonDir != "" && b.CommonDir != "" {
		return a.CommonDir == b.CommonDir
	}
	if a.RealPath != "" && b.RealPath != "" {
		return a.RealPath == b.RealPath
	}
	return a.Path != "" && a.Path == b.Path
}

func favoriteRepoLabel(row RepoRow) string {
	label := row.Repo.Display()
	if row.Favorite() {
		label = "★ " + label
	}
	if row.Unavailable != "" {
		label += " (unavailable)"
	}
	return label
}
