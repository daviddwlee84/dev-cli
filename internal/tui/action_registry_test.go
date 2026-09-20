package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func selectMenuAction(t *testing.T, m Model, id listAction) Model {
	t.Helper()
	for i := 0; i < m.overlay.optionCount; i++ {
		if m.overlay.options[i].action == id {
			m.overlay.optionIndex = i
			return m
		}
	}
	t.Fatalf("action %d absent from %s", id, m.overlay.title)
	return m
}

// Locate the actual displayed option, including scrolling and disabled entries.
func menuActionY(t *testing.T, m *Model, id listAction) int {
	t.Helper()
	*m = selectMenuAction(t, *m, id)
	visible, from, to := m.actionMenuWindow()
	for i := from; i < to; i++ {
		if visible[i] == m.overlay.optionIndex {
			return m.buildActionMenuLayout().firstOptionY + i - from
		}
	}
	t.Fatal("action is not visible")
	return 0
}

func TestRegistryKeysAndIdentitiesAreUnambiguous(t *testing.T) {
	for _, view := range Views {
		ids, keys := map[listAction]bool{}, map[string]listAction{}
		for _, spec := range dashboardActions {
			if !spec.inView(view) {
				continue
			}
			if ids[spec.ID] {
				t.Errorf("duplicate action %d in %s", spec.ID, view)
			}
			ids[spec.ID] = true
			for _, key := range strings.Fields(spec.Keys) {
				if _, exists := keys[key]; exists {
					t.Errorf("ambiguous key %s in %s", key, view)
				}
				keys[key] = spec.ID
				if len(key) == 1 && key != "u" && key != "A" { // existing context-only tool-key exceptions
					if _, reserved := config.ReservedKey(key); !reserved {
						t.Errorf("unreserved built-in key %s", key)
					}
				}
			}
		}
		if len(ids) == 0 {
			t.Errorf("no registry for %s", view)
		}
	}
}

func TestRegistryDisabledMenuAndKeyCannotExecute(t *testing.T) {
	m := New(Actions{}, nil, []RepoRow{{Repo: repo.Repo{Path: "/repo", Name: "repo"}}})
	m.view = ViewRepos
	m = selectMenuAction(t, m.openActionMenu(), listActionRepoCreate)
	if m.overlay.options[m.overlay.optionIndex].disabled == "" {
		t.Fatal("missing wizard must be explained")
	}
	next, cmd := m.runOverlayAction()
	m = next.(Model)
	if cmd != nil || m.overlay.kind != overlayActionMenu || !strings.Contains(m.overlay.body, "unavailable") {
		t.Fatal("disabled action did not retain its reason")
	}
	m.overlay = overlayState{}
	next, cmd = m.runListAction(listActionRepoCreate)
	if cmd != nil || !strings.Contains(next.(Model).status, "unavailable") {
		t.Fatal("key escaped the same availability gate")
	}
}

func TestRegistryRechecksCallbackAndTaskRevision(t *testing.T) {
	row := inventory.Row{Task: &task.Task{ID: "task", Name: "task", State: task.Hot}, CheckoutExists: true}
	a := Actions{Open: func(context.Context, *task.Task) (OpenResult, error) {
		t.Fatal("stale action executed")
		return OpenResult{}, nil
	}}
	for _, changed := range []string{"callback", "revision"} {
		t.Run(changed, func(t *testing.T) {
			m := selectMenuAction(t, New(a, []inventory.Row{row}, nil).openActionMenu(), listActionOpen)
			if changed == "callback" {
				m.actions.Open = nil
			} else {
				copy := *row.Task
				copy.Next = "changed"
				m.rows = []inventory.Row{{Task: &copy, CheckoutExists: true}}
			}
			next, cmd := m.runOverlayAction()
			got := next.(Model)
			if cmd != nil || (got.err == nil && got.status == "") {
				t.Fatal("changed authority was not rejected")
			}
		})
	}
}

func TestRegistryRejectsChangedSSHProfile(t *testing.T) {
	m := New(Actions{Copy: func(string) error { t.Fatal("changed profile was copied"); return nil }}, nil, nil)
	m.view = ViewSSH
	m.ssh = sshTestInventory("machine")
	m.toggleSSHEntry()
	m.sshCursor = 1
	m = selectMenuAction(t, m.openActionMenu(), listActionSSHCopy)
	m.ssh.Machines[0].Profiles[0].Fingerprint = "changed-source"
	next, cmd := m.runOverlayAction()
	if cmd != nil || next.(Model).err == nil || next.(Model).overlay.kind != overlayNone {
		t.Fatal("profile source change did not invalidate menu")
	}
}

func TestRegistryPreservesSSHWorkflowFallbacks(t *testing.T) {
	for _, test := range []struct {
		id        listAction
		operation string
	}{
		{listActionSSHProbe, "probe"}, {listActionSSHDiagnose, "diagnose"}, {listActionSSHDiscover, "discover"},
	} {
		calls := 0
		m := New(Actions{SSH: SSHActions{Workflow: func(_ context.Context, r SSHWorkflowRequest) (SSHWorkflow, error) {
			calls++
			if r.Action != test.operation {
				t.Fatalf("fallback %q, want %q", r.Action, test.operation)
			}
			return &testSSHWorkflow{}, nil
		}}}, nil, nil)
		m.view, m.ssh = ViewSSH, sshTestInventory("machine")
		m = selectMenuAction(t, m.openActionMenu(), test.id)
		if m.overlay.options[m.overlay.optionIndex].disabled != "" {
			t.Fatalf("working SSH fallback %s was disabled", test.operation)
		}
		_, cmd := m.runOverlayAction()
		if cmd == nil || calls != 1 {
			t.Fatalf("fallback %s did not run", test.operation)
		}
	}
}

func TestRegistryPreservesActivityKeysOnLocalRemoteAndGitTry(t *testing.T) {
	for _, view := range []View{ViewRemote, ViewTries} {
		selected := ""
		m := New(Actions{LoadStats: func(_ context.Context, name string) (StatsPanel, error) { selected = name; return StatsPanel{}, nil }}, nil, nil)
		m.view = view
		m.remotes = []RemoteRow{{LocalPath: "/repo", LocalName: "repo"}}
		m.tries = []TryRow{{Item: experiment.Item{ID: "try", Phase: catalog.PhaseActive, Live: experiment.LiveFacts{Present: true, CurrentPath: "/repo", Repo: &gitx.Repo{Name: "repo"}}}}}
		next, cmd, handled := m.actionKey("H")
		if !handled || cmd == nil || next.(Model).mode != modeStats {
			t.Fatalf("lost H binding on %s", view)
		}
		cmd()
		if selected != "repo" {
			t.Fatalf("activity target on %s = %q", view, selected)
		}
	}
}

func TestRegistryFilteredActionsWaitForEveryRepository(t *testing.T) {
	var requests []WorkflowRequest
	m := hygieneMenuModel(&requests)
	m.repos = append(m.repos, RepoRow{Repo: repo.Repo{Name: "pending", Path: "/pending", HasGit: true}, Pending: "loading"})
	for _, id := range []listAction{listActionHygieneFiltered, listActionSkillFiltered, listActionTriageFiltered} {
		next, cmd := m.runListAction(id)
		if cmd != nil || len(requests) != 0 || !strings.Contains(next.(Model).status, "fresh") {
			t.Fatalf("filtered action %d ignored a pending member", id)
		}
	}
}

type registryWorkflow struct{}

func (registryWorkflow) Run() error             { return nil }
func (registryWorkflow) SetStdin(io.Reader)     {}
func (registryWorkflow) SetStdout(io.Writer)    {}
func (registryWorkflow) SetStderr(io.Writer)    {}
func (registryWorkflow) Result() WorkflowResult { return WorkflowResult{} }

func hygieneMenuModel(requests *[]WorkflowRequest) Model {
	r := RepoRow{Repo: repo.Repo{Name: "demo", Path: "/repos/demo", CommonDir: "/repos/demo/.git", HasGit: true}, Worktrees: 1}
	r.Context.Checkouts = []inventory.RepoCheckout{
		{Worktree: gitx.Worktree{Path: "/repos/demo", Main: true}, Exists: true},
		{Worktree: gitx.Worktree{Path: "/worktrees/feature"}, Exists: true},
	}
	m := New(Actions{Workflow: func(_ context.Context, request WorkflowRequest) (Workflow, error) {
		*requests = append(*requests, request)
		return registryWorkflow{}, nil
	}}, nil, []RepoRow{r})
	m.view = ViewRepos
	m.toggleRepo(r)
	return m
}

func TestHygieneMenuPinsExactCheckoutAndScanScope(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, action := range []listAction{listActionHygieneStatus, listActionHygieneReport, listActionHygieneRepo, listActionHygieneScanWorktree, listActionHygieneScanStaged, listActionHygieneScanHistory} {
			var requests []WorkflowRequest
			m := hygieneMenuModel(&requests)
			want := "/repos/demo"
			if child {
				m.setAt(1)
				want = "/worktrees/feature"
			}
			m = selectMenuAction(t, m.openActionMenu(), listActionHygieneMenu)
			next, cmd := m.runOverlayAction()
			m = next.(Model)
			if cmd != nil || len(requests) != 0 || !strings.Contains(m.overlay.subject, want) {
				t.Fatal("opening menu executed or retargeted hygiene")
			}
			if action >= listActionHygieneScanWorktree && action <= listActionHygieneScanHistory {
				m = selectMenuAction(t, m, listActionHygieneScan)
				next, _ = m.runOverlayAction()
				m = next.(Model)
			}
			m = selectMenuAction(t, m, action)
			_, cmd = m.runOverlayAction()
			if cmd == nil || len(requests) != 1 {
				t.Fatalf("action %d did not invoke shared workflow", action)
			}
			request := requests[0]
			if action == listActionHygieneRepo {
				if len(request.RepoRefs) != 1 || request.RepoRefs[0] != want {
					t.Fatalf("setup target: %+v", request.RepoRefs)
				}
			} else {
				if request.Hygiene == nil || request.Hygiene.Checkout != want {
					t.Fatalf("inspection target: %+v", request.Hygiene)
				}
				scopes := map[listAction]string{listActionHygieneScanWorktree: "worktree", listActionHygieneScanStaged: "staged", listActionHygieneScanHistory: "history"}
				if request.Hygiene.Scope != scopes[action] {
					t.Fatalf("wrong scan scope: %+v", request.Hygiene)
				}
			}
		}
	}
}

func TestHygieneMissingWorktreeDoesNotFallBackToParent(t *testing.T) {
	var requests []WorkflowRequest
	m := hygieneMenuModel(&requests)
	m.setAt(1)
	m.repos[0].Context.Checkouts[1].Exists = false
	next, cmd := m.runListAction(listActionHygieneRepo)
	if cmd != nil || len(requests) != 0 || !strings.Contains(next.(Model).status, "missing") {
		t.Fatal("missing checkout was not blocked")
	}
}

func TestHygieneFilteredSetupUsesRepositoryPool(t *testing.T) {
	var requests []WorkflowRequest
	m := hygieneMenuModel(&requests)
	m.setAt(1)
	_, cmd := m.runListAction(listActionHygieneFiltered)
	if cmd == nil || len(requests) != 1 || len(requests[0].RepoRefs) != 1 || requests[0].RepoRefs[0] != "/repos/demo" {
		t.Fatalf("filtered pool included expanded children: %+v", requests)
	}
}
