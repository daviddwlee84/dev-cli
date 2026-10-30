package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func favoriteTestRows() []RepoRow {
	var rows []RepoRow
	for _, name := range []string{"a", "b", "c", "d"} {
		rows = append(rows, RepoRow{Repo: repo.Repo{Name: name, Path: "/" + name}, Asset: &catalog.Entry{
			ID: name, Kind: catalog.KindRepository, Name: name, Favorite: name == "b" || name == "d",
		}})
	}
	return rows
}

func TestRepoFavoritesStayAboveEverySortAndRespectFilter(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, column := range []string{"", "repo"} {
			m := New(Actions{RepoSort: "name", RepoReverse: reverse}, nil, favoriteTestRows())
			m.view = ViewRepos
			m.tableSorts[int(ViewRepos)] = tableSort{column: column, descending: reverse}
			var names []string
			for _, row := range m.visibleRepos() {
				names = append(names, row.Repo.Name)
			}
			want := []string{"b", "d", "a", "c"}
			if reverse {
				want = []string{"d", "b", "c", "a"}
			}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("reverse=%v column=%s: %v, want %v", reverse, column, names, want)
			}
			m.filter = "a"
			if rows := m.visibleRepos(); len(rows) != 1 || rows[0].Repo.Name != "a" {
				t.Fatalf("favorites escaped filtering: %+v", rows)
			}
		}
	}
}

func TestRepoFavoriteMutationKeepsFocusAndSurvivesOlderSnapshots(t *testing.T) {
	var calls int
	m := New(Actions{RepoSort: "name", Repos: RepoActions{SetFavorite: func(_ context.Context, row RepoRow, desired bool) (*catalog.Entry, error) {
		calls++
		asset := row.Asset.Clone()
		asset.Favorite, asset.Updated = desired, time.Unix(100, 0)
		return asset, nil
	}}}, nil, favoriteTestRows())
	m.view = ViewRepos
	m.setAt(1) // d, initially the second favorite
	selected := m.currentToken()
	next, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	m = next.(Model)
	if command == nil || !m.repoFavoriteBusy {
		t.Fatal("favorite setter not dispatched")
	}
	if _, duplicate := m.runListAction(listActionRepoFavorite); duplicate != nil {
		t.Fatal("second favorite mutation accepted while pending")
	}
	msg := command()
	next, reload := m.Update(msg)
	m = next.(Model)
	if reload != nil || m.repoFavoriteBusy || calls != 1 || m.currentToken() != selected {
		t.Fatalf("favorite mutation reloaded or moved focus: calls=%d reload=%v", calls, reload)
	}
	if row, _ := m.currentRepo(); row.Favorite() {
		t.Fatal("favorite was not removed")
	}

	generation := m.beginViewLoad(ViewRepos, loadRefresh)
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Valid: true, Repos: favoriteTestRows()})
	if row, _ := m.currentRepo(); row.Repo.Name != "d" || row.Favorite() {
		t.Fatal("older final snapshot overwrote favorite or focus")
	}
	fresh := favoriteTestRows()
	fresh[3].Asset.Updated = time.Unix(101, 0)
	m.repos = fresh
	if row := m.visibleRepos()[1]; row.Repo.Name != "d" || !row.Favorite() {
		t.Fatal("newer catalog preference did not supersede local overlay")
	}
}

func TestRepoFavoriteFailureAndPendingMetadata(t *testing.T) {
	failure := errors.New("catalog write failed")
	rows := favoriteTestRows()
	rows[0].Pending, rows[0].Unavailable = "unavailable", "directory is missing"
	m := New(Actions{Repos: RepoActions{SetFavorite: func(context.Context, RepoRow, bool) (*catalog.Entry, error) {
		return nil, failure
	}}}, nil, rows[:1])
	m.view = ViewRepos
	next, command := m.runListAction(listActionRepoFavorite)
	if command == nil {
		t.Fatal("known catalog preference could not be changed while unavailable")
	}
	m = next.(Model)
	next, reload := m.Update(command())
	m = next.(Model)
	if reload != nil || !errors.Is(m.err, failure) || m.visibleRepos()[0].Favorite() || m.repoFavoriteBusy {
		t.Fatal("failed favorite mutation changed intent or reloaded")
	}
	if _, command := m.runListAction(listActionOpen); command != nil {
		t.Fatal("unavailable repository opened")
	}
}

func TestPendingRepositoryOpenUsesTargetCallback(t *testing.T) {
	for _, pending := range []string{"cached", "loading", "refreshing", "runtime pending", "Git observation unavailable"} {
		for _, key := range []string{"enter", "o", "menu"} {
			t.Run(pending+"/"+key, func(t *testing.T) {
				calls := 0
				m := New(Actions{OpenRepo: func(_ context.Context, row RepoRow) (OpenResult, error) {
					calls++
					if row.Pending != pending || row.Repo.Path != "/selected" {
						t.Fatalf("opening wrong row or clearing pending: %+v", row)
					}
					return OpenResult{Directory: row.Repo.Path}, nil
				}}, nil, []RepoRow{{Repo: repo.Repo{Name: "selected", Path: "/selected"}, Pending: pending}})
				m.view = ViewRepos
				var next tea.Model
				var command tea.Cmd
				switch key {
				case "menu":
					next, command = m.runListAction(listActionOpen)
				case "enter":
					next, command = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				default:
					next, command = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
				}
				if command == nil {
					t.Fatalf("pending open blocked: %s", next.(Model).status)
				}
				msg := command()
				m = next.(Model)
				next, quit := m.Update(msg)
				if calls != 1 || quit == nil || next.(Model).chosen != "/selected" {
					t.Fatal("open did not complete independently of inventory")
				}
			})
		}
	}
}

func TestFavoriteProgressDeduplicatesAliasesAndKeepsFocus(t *testing.T) {
	row := RepoRow{Repo: repo.Repo{Name: "repo", Path: "/physical/repo", RealPath: "/physical/repo", CommonDir: "/physical/repo/.git"},
		Asset: &catalog.Entry{ID: "repo", Kind: catalog.KindRepository, Favorite: true}, Pending: "loading"}
	m := New(Actions{}, nil, []RepoRow{row})
	m.view = ViewRepos
	generation := m.beginViewLoad(ViewRepos, loadRefresh)
	selected := m.currentToken()
	alias := row
	alias.Repo.Path, alias.Repo.Symlink, alias.Asset = "/index/repo", true, nil
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "discovery", Valid: true, Repos: []RepoRow{alias}})
	if len(m.repos) != 1 || !m.visibleRepos()[0].Favorite() || m.currentToken() != selected {
		t.Fatal("alias duplicated favorite, lost intent or changed focus")
	}
	alias.Pending, alias.Asset = "", row.Asset
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Valid: true, Repos: []RepoRow{alias}})
	if len(m.repos) != 1 || m.repos[0].Repo.Path != "/index/repo" || !m.visibleRepos()[0].Favorite() {
		t.Fatal("final alias did not retain the single favorite row")
	}
}

func TestFavoriteFinalSnapshotDoesNotRestoreRemovedCatalogMetadata(t *testing.T) {
	row := favoriteTestRows()[1]
	fresh := row
	fresh.Asset = nil
	rows := completeRepoRows([]RepoRow{row}, []RepoRow{fresh})
	if len(rows) != 1 || rows[0].Asset != nil {
		t.Fatal("complete snapshot restored removed catalog metadata")
	}
}

func TestFavoriteProgressDoesNotLoseCacheOrDowngradeRows(t *testing.T) {
	m := New(Actions{RepoSort: "name"}, nil, nil)
	m.view = ViewRepos
	generation := m.beginViewLoad(ViewRepos, loadInitial)
	favorite := favoriteTestRows()[1]
	favorite.Pending = "loading"
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "favorites", Valid: true, Repos: []RepoRow{favorite}})
	cached := favoriteTestRows()
	for i := range cached {
		cached[i].Pending = "cached"
	}
	m, accepted := m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Valid: true, Repos: cached})
	if !accepted || len(m.repos) != 4 {
		t.Fatal("provisional favorites blocked the remaining cached rows")
	}
	favorite.Pending = ""
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "enrichment", Valid: true, Repos: []RepoRow{favorite}})
	favorite.Pending = "loading"
	m, _ = m.applyLocalResult(LocalResult{View: ViewRepos, Generation: generation, Phase: "favorites", Valid: true, Repos: []RepoRow{favorite}})
	if got := m.visibleRepos()[0]; got.Pending != "" || !strings.HasPrefix(favoriteRepoLabel(got), "★ ") {
		t.Fatal("late provisional favorite downgraded enriched data or lost its badge")
	}
}
