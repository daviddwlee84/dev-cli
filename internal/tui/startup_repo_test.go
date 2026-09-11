package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func startupSend(m Model, msg tea.Msg) Model { next, _ := m.Update(msg); return next.(Model) }
func startupKey(key string) tea.KeyMsg {
	switch key {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}
func startupRows() []RepoRow {
	var rows []RepoRow
	for _, name := range []string{"alpha", "beta", "gamma"} {
		rows = append(rows, RepoRow{Repo: repo.Repo{Name: name, Path: "/repos/" + name, CommonDir: "/repos/" + name + "/.git", HasGit: true}})
	}
	return rows
}
func startupObservation() StartupRepository {
	return StartupRepository{Path: "/repos/beta", CommonDir: "/repos/beta/.git", Covered: true}
}

func TestStartupRepoFocusKeepsOrderingAndUserChoice(t *testing.T) {
	m := New(Actions{RepoSort: "name"}, nil, startupRows())
	m = startupSend(m, startupRepoMsg{repository: startupObservation()})
	if m.view != ViewTasks {
		t.Fatal("startup changed the default view")
	}
	before := m.visibleRepos()
	m = startupSend(m, startupKey("2"))
	if m.repoCursor != 1 {
		t.Fatalf("cursor=%d", m.repoCursor)
	}
	if !reflect.DeepEqual(before, m.visibleRepos()) {
		t.Fatal("startup changed sort order")
	}
	m = startupSend(m, startupKey("down"))
	m = startupSend(m, startupKey("1"))
	rows := append([]RepoRow{{Repo: repo.Repo{Name: "aardvark", Path: "/a", CommonDir: "/a/.git"}}}, startupRows()...)
	m = startupSend(m, reposMsg{rows: rows, valid: true})
	m = startupSend(m, startupKey("2"))
	row, _ := m.currentRepo()
	if row.Repo.Name != "gamma" {
		t.Fatalf("background update stole selection: %s", row.Repo.Name)
	}
}

func TestStartupRepoFocusHandlesEitherArrivalOrder(t *testing.T) {
	for _, identityFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "identity-first", false: "rows-first"}[identityFirst], func(t *testing.T) {
			m := New(Actions{RepoSort: "name"}, nil, nil)
			m.view = ViewRepos
			generation := m.beginViewLoad(ViewRepos, loadInitial)
			identity := startupRepoMsg{repository: startupObservation()}
			if identityFirst {
				m = startupSend(m, identity)
			}
			m = startupSend(m, localMsg{result: LocalResult{View: ViewRepos, Generation: generation, Phase: "cache", Repos: startupRows()[:1], Valid: true}})
			if m.startupRepo.focusDone {
				t.Fatal("consumed pending focus before target arrived")
			}
			m = startupSend(m, localMsg{result: LocalResult{View: ViewRepos, Generation: generation, Phase: "discovery", Repos: startupRows()[1:], Valid: true}})
			if !identityFirst {
				m = startupSend(m, identity)
			}
			row, _ := m.currentRepo()
			if row.Repo.Name != "beta" {
				t.Fatal(row.Repo.Name)
			}
			m = startupSend(m, localMsg{result: LocalResult{View: ViewRepos, Generation: generation, Repos: startupRows(), Valid: true}})
			row, _ = m.currentRepo()
			if row.Repo.Name != "beta" {
				t.Fatal("completion lost identity")
			}
		})
	}
}

func TestStartupRepoDoesNotOverrideEarlyInput(t *testing.T) {
	for _, input := range []tea.Msg{startupKey("down"), startupKey("/"), mouseMessage(3, 3, tea.MouseButtonLeft, tea.MouseActionPress)} {
		m := New(Actions{RepoSort: "name"}, nil, startupRows())
		m.view = ViewRepos
		m = startupSend(m, input)
		selected := m.repoCursor
		m = startupSend(m, startupRepoMsg{repository: StartupRepository{Path: "/repos/gamma", CommonDir: "/repos/gamma/.git"}})
		if m.repoCursor != selected || !m.startupRepo.focusDone {
			t.Fatal("late lookup overrode input")
		}
	}
}

func outsideStartupModel() Model {
	m := New(Actions{RepoSort: "name"}, nil, startupRows())
	m.view = ViewRepos
	m.startupRepo = startupRepoState{loaded: true, repository: StartupRepository{Path: "/outside/current", CommonDir: "/outside/current/.git"}}
	return m
}

func TestStartupBannerWaitsForReliableInventory(t *testing.T) {
	m := outsideStartupModel()
	if !m.startupOutside() {
		t.Fatal("missing outside observation")
	}
	for _, change := range []func(*Model){
		func(m *Model) { m.beginViewLoad(ViewRepos, loadRefresh) },
		func(m *Model) { m.viewErrors[int(ViewRepos)] = errors.New("scan failed") },
		func(m *Model) { m.startupRepo.repository.CoverageErr = errors.New("unknown coverage") },
		func(m *Model) { m.startupRepo.repository.Covered = true },
		func(m *Model) { m.startupRepo.repository.CommonDir = "" },
	} {
		candidate := m
		change(&candidate)
		if candidate.startupOutside() {
			t.Fatal("offered registration with incomplete/covered observation")
		}
	}
}

func TestStartupBannerTouchPreviewCancelAndConfirm(t *testing.T) {
	for _, width := range []int{38, 100} {
		m := outsideStartupModel()
		m.width, m.height = width, 35
		applied := 0
		m.actions.Discovery = DiscoveryActions{
			Plan: func(_ context.Context, r StartupRepository, scope repo.DiscoveryScope) (repo.DiscoveryRegistrationPlan, error) {
				if r.CommonDir != "/outside/current/.git" || scope != repo.DiscoveryExact {
					t.Fatal(r, scope)
				}
				return repo.DiscoveryRegistrationPlan{File: "/config/custom.toml", Field: "repo_paths", RepoPath: r.Path, Before: "repo_paths = []", After: "repo_paths = [\"/outside/current\"]"}, nil
			},
			Apply: func(context.Context, repo.DiscoveryRegistrationPlan) (string, error) {
				applied++
				return "Configuration saved", nil
			},
		}
		banner := m.discoveryBanner()
		if len(banner.buttons) != 2 || m.listPreambleLines() != 4 {
			t.Fatal("banner geometry")
		}
		if row, ok := m.mouseRow(3, 2+m.listPreambleLines()); !ok || row != 0 {
			t.Fatal("banner offset data row")
		}
		button := banner.buttons[0]
		next, command := m.Update(mouseMessage(button.from, 2+button.line, tea.MouseButtonLeft, tea.MouseActionPress))
		m = next.(Model)
		if command == nil || applied != 0 {
			t.Fatal("tap did not prepare preview")
		}
		prepared := command()
		canceled := startupSend(m, startupKey("esc"))
		canceled = startupSend(canceled, prepared)
		if canceled.overlay.kind != overlayNone || applied != 0 {
			t.Fatal("late preview resurrected canceled modal")
		}
		m = startupSend(m, prepared)
		if !strings.Contains(m.View(), "custom.toml") || applied != 0 {
			t.Fatal("missing read-only preview")
		}
		if lineCount(m.View()) > m.height {
			t.Fatal("preview exceeds frame")
		}
		m = startupSend(m, popupMouse(m, 3, m.buildActionMenuLayout().firstOptionY, tea.MouseButtonLeft, tea.MouseActionRelease))
		if applied != 0 {
			t.Fatal("release applied preview")
		}
		next, command = m.Update(popupMouse(m, 3, m.buildActionMenuLayout().firstOptionY, tea.MouseButtonLeft, tea.MouseActionPress))
		m = next.(Model)
		if command == nil {
			t.Fatal("confirm did not apply")
		}
		m = startupSend(m, command())
		if applied != 1 || !m.startupRepo.saved || m.overlay.kind != overlayNone {
			t.Fatal("save did not reload", applied)
		}
		m = startupSend(m, configMsg{generation: m.configGeneration, err: errors.New("reload failed")})
		if !strings.Contains(m.status, "Configuration saved; reload failed") {
			t.Fatal(m.status)
		}
	}
}

func TestStartupPreviewLongPathsScrollAndEmptyMenuEntry(t *testing.T) {
	m := outsideStartupModel()
	m.repos = nil
	m.actions.Discovery.Plan = func(context.Context, StartupRepository, repo.DiscoveryScope) (repo.DiscoveryRegistrationPlan, error) {
		return repo.DiscoveryRegistrationPlan{}, nil
	}
	m = m.openActionMenu()
	found := false
	for _, option := range m.overlay.options[:m.overlay.optionCount] {
		found = found || option.action == listActionRegisterRepo
	}
	if !found {
		t.Fatal("empty REPOS has no registration action")
	}
	m.width, m.height = 32, 16
	m.overlay = overlayState{kind: overlayActionMenu, subject: "Review", body: strings.Repeat("very/long/path/", 80)}
	m.overlay.addOption(listActionRegistrationConfirm, "confirm and save")
	m.overlay.addOption(listActionRegistrationCancel, "cancel")
	if !m.mouseFrameFits() {
		t.Fatal(m.View())
	}
	m = startupSend(m, popupMouse(m, 3, 1, tea.MouseButtonWheelDown, tea.MouseActionPress))
	if m.overlay.scroll == 0 || !m.mouseFrameFits() {
		t.Fatal("body did not scroll safely")
	}
}

func TestStartupRegistrationRefreshFocusIsOneTime(t *testing.T) {
	m := outsideStartupModel()
	m = startupSend(m, registrationAppliedMsg{status: "Configuration saved"})
	m = startupSend(m, configMsg{generation: m.configGeneration})
	rows := append(startupRows(), RepoRow{Repo: repo.Repo{Name: "current", Path: "/outside/current", CommonDir: "/outside/current/.git"}})
	m = startupSend(m, reposMsg{generation: m.viewLoad(ViewRepos).generation, rows: rows, valid: true})
	selected, _ := m.currentRepo()
	if selected.Repo.Name != "current" || m.startupRepo.saved {
		t.Fatal("registration did not finish and select target", selected.Repo.Name)
	}
	m = startupSend(m, startupKey("down"))
	selected, _ = m.currentRepo()
	want := selected.Repo.CommonDir
	m.beginConfigLoad()
	m = startupSend(m, configMsg{generation: m.configGeneration})
	m = startupSend(m, reposMsg{generation: m.viewLoad(ViewRepos).generation, rows: rows, valid: true})
	selected, _ = m.currentRepo()
	if selected.Repo.CommonDir != want {
		t.Fatal("subsequent reload restored the startup selection")
	}
}
