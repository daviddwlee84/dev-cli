package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

func cloneURLKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestRemoteYankCloneURLAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, clone, ssh string
		ok               bool
	}{
		{"clone", "https://host/team/repo.git", "git@host:team/repo.git", true},
		{"ssh", "", "git@host:team/repo.git", true},
		{"missing", "", "", false},
		{"unsafe", "https://example:password@host/repo", "git@host:repo", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copied := ""
			m := New(Actions{Copy: func(s string) error { copied = s; return nil }}, nil, nil)
			m.view = ViewRemote
			m.remotes = []RemoteRow{{Repo: forge.RemoteRepo{Forge: forge.GitHub, FullName: "team/repo", CloneURL: tc.clone, SSHURL: tc.ssh, URL: "https://browser/never-copy"}}}
			m.setAt(0)
			next, _ := m.runListAction(listActionCopy)
			m = next.(Model)
			next, cmd := m.updateCopy(cloneURLKey("u"))
			m = next.(Model)
			if tc.ok {
				if cmd == nil {
					t.Fatalf("missing copy command: %v", m.err)
				}
				_ = cmd()
				want := tc.clone
				if want == "" {
					want = tc.ssh
				}
				if copied != want {
					t.Fatalf("copied %q", copied)
				}
			} else if cmd != nil || m.err == nil || copied != "" {
				t.Fatalf("unsafe copy: %q %v", copied, m.err)
			}
		})
	}
}

func TestRepoCloneURLMultipleRemotesAndCancel(t *testing.T) {
	copied := ""
	m := New(Actions{Copy: func(s string) error { copied = s; return nil }, CloneSources: func(_ context.Context, path string) ([]gitx.CloneSource, error) {
		if path != "/repo" {
			t.Fatal(path)
		}
		return []gitx.CloneSource{{Remote: "fork", URL: "git@host:fork/repo"}, {Remote: "upstream", URL: "https://host/team/repo"}}, nil
	}}, nil, []RepoRow{{Repo: repo.Repo{Name: "repo", Path: "/repo"}}})
	m.view = ViewRepos
	m.setAt(0)
	next, load := m.copyCloneURL()
	m = next.(Model)
	if load == nil || m.mode != modeCloneURL {
		t.Fatal("missing lookup")
	}
	next, cmd := m.Update(load())
	m = next.(Model)
	if cmd != nil || len(m.cloneURLSources) != 2 || copied != "" {
		t.Fatal("did not require remote selection")
	}
	cancelled, _ := m.updateCloneURL(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled.(Model).mode != modeList || copied != "" {
		t.Fatal("cancel copied")
	}
	next, _ = m.updateCloneURL(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	_, cmd = m.updateCloneURL(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("missing selected copy")
	}
	_ = cmd()
	if copied != "https://host/team/repo" {
		t.Fatal(copied)
	}
}

func TestRepoCloneURLOriginAndStaleLookup(t *testing.T) {
	for _, scenario := range []string{"origin", "stale", "cancel", "error", "empty", "clipboard"} {
		t.Run(scenario, func(t *testing.T) {
			copied := ""
			m := New(Actions{Copy: func(s string) error {
				if scenario == "clipboard" {
					return errors.New("clipboard failed")
				}
				copied = s
				return nil
			}, CloneSources: func(context.Context, string) ([]gitx.CloneSource, error) {
				if scenario == "error" {
					return nil, errors.New("source read failed")
				}
				if scenario == "empty" {
					return nil, nil
				}
				return []gitx.CloneSource{{Remote: "other", URL: "https://host/other"}, {Remote: "origin", URL: "https://host/origin"}}, nil
			}}, nil, []RepoRow{{Repo: repo.Repo{Name: "repo", Path: "/repo"}}})
			m.view = ViewRepos
			m.setAt(0)
			next, load := m.copyCloneURL()
			m = next.(Model)
			if scenario == "stale" {
				m.repos = nil
			}
			if scenario == "cancel" {
				next, _ = m.updateCloneURL(tea.KeyMsg{Type: tea.KeyEsc})
				m = next.(Model)
			}
			next, copy := m.Update(load())
			m = next.(Model)
			if copy != nil {
				next, _ = m.Update(copy())
				m = next.(Model)
			}
			if scenario == "origin" {
				if copied != "https://host/origin" {
					t.Fatal(copied)
				}
			} else if copied != "" {
				t.Fatal("copied on failure", copied)
			}
			if scenario != "origin" && scenario != "cancel" && m.err == nil {
				t.Fatal("missing failure")
			}
		})
	}
}

func TestRepoCloneURLIgnoresCanceledGenerationAfterReopen(t *testing.T) {
	copied := ""
	m := New(Actions{Copy: func(s string) error { copied = s; return nil }, CloneSources: func(context.Context, string) ([]gitx.CloneSource, error) {
		return []gitx.CloneSource{{Remote: "origin", URL: "https://host/repo"}}, nil
	}}, nil, []RepoRow{{Repo: repo.Repo{Name: "repo", Path: "/repo"}}})
	m.view = ViewRepos
	m.setAt(0)
	next, old := m.copyCloneURL()
	m = next.(Model)
	next, _ = m.updateCloneURL(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	next, current := m.copyCloneURL()
	m = next.(Model)
	next, cmd := m.Update(old())
	m = next.(Model)
	if cmd != nil || copied != "" || !m.cloneURLLoading {
		t.Fatal("late canceled result accepted")
	}
	_, cmd = m.Update(current())
	if cmd == nil {
		t.Fatal("current result lost")
	}
	_ = cmd()
	if copied != "https://host/repo" {
		t.Fatal(copied)
	}
}
