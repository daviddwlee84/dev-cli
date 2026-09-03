package flowtui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/muesli/termenv"
)

var (
	testRepo = RepositoryRow{
		RepoKey: "/repos/example/.git", Name: "example", Path: "/repos/example", Available: true,
	}
	testNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
)

func testLocator(rowKey, taskID string, mode task.CheckoutMode, state task.State) taskflow.Locator {
	return taskflow.Locator{
		RepoKey: testRepo.RepoKey, RowKey: rowKey, RowKind: "managed",
		RepositoryID: testRepo.RepoKey, GitCommonDir: testRepo.RepoKey,
		TaskID: taskID, TaskRevision: "revision-1",
		RepoPath: testRepo.Path, CheckoutPath: "/worktrees/" + strings.TrimPrefix(rowKey, "row-"),
		Branch: "feature/example", Base: "main", Upstream: "origin/feature/example", Remote: "origin",
		HeadOID: strings.Repeat("1", 40), BaseOID: strings.Repeat("2", 40), UpstreamOID: strings.Repeat("3", 40),
		Mode: mode, State: state,
	}
}

func managedSurface(rowKey, taskID string, mode task.CheckoutMode, state task.State, choices ...ActionChoice) SurfaceRow {
	locator := testLocator(rowKey, taskID, mode, state)
	return SurfaceRow{
		RowKey: rowKey, Kind: SurfaceManaged, Label: taskID, Path: locator.CheckoutPath,
		Branch: locator.Branch, Base: locator.Base, Mode: mode, State: state,
		Evidence: NewLines("checkout registered", "runtime observation known"),
		Locator:  locator, Actions: NewActionList(choices...),
	}
}

func parkChoice(id string) ActionChoice {
	return NewActionChoice(id, "Park warm", "keep the checkout and close the runtime", taskflow.ParkWarmOptions{})
}

func remoteChoice(id string, fetch, review bool) ActionChoice {
	return NewActionChoice(id, id, "explicit remote observation", taskflow.RefreshRemoteOptions{
		FetchRefs: fetch, QueryReview: review,
	})
}

func freshSnapshot(rows ...SurfaceRow) Snapshot {
	return Snapshot{
		Repository: testRepo, Surfaces: NewSurfaceList(rows...),
		ObservedAt: testNow, Freshness: FreshnessFresh,
	}
}

func buildPlan(t *testing.T, locator taskflow.Locator, options taskflow.ActionOptions, verdict taskflow.Verdict, confirmation taskflow.Confirmation) taskflow.Plan {
	t.Helper()
	request, err := taskflow.NewRequest(locator, options)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	plan, err := taskflow.BuildPlan(request, taskflow.PlanSpec{
		Authority: map[string]string{"snapshot": "exact"},
		Conditions: []taskflow.Condition{
			{Code: "checkout-current", Verdict: verdict, Requirement: taskflow.RequirementRequired, Evidence: "exact checkout evidence", Remediation: "reload and repair the checkout"},
			{Code: "runtime-advisory", Verdict: taskflow.VerdictUnknown, Requirement: taskflow.RequirementAdvisory, Evidence: "runtime probe not requested", Remediation: "inspect runtime if needed"},
		},
		Effects: []taskflow.Effect{
			taskflow.NewEffect("close-runtime", "close exact runtime", "runtime-1", false, false, map[string]string{"backend": "test"}),
			taskflow.NewEffect("write-task", "persist exact state", locator.TaskID, false, false, map[string]string{"state": "warm"}),
		},
		RetainedResources: []string{"branch:" + locator.Branch, "checkout:" + locator.CheckoutPath},
		Confirmation:      confirmation, FallbackCommand: "dev park " + locator.TaskID,
		Summary: "park exact task warm", DisplayedAt: testNow,
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return plan
}

func readyPlan(t *testing.T, locator taskflow.Locator, options taskflow.ActionOptions) taskflow.Plan {
	t.Helper()
	return buildPlan(t, locator, options, taskflow.VerdictMet, taskflow.Confirmation{
		Kind: taskflow.ConfirmationApproval, Prompt: "Apply this exact plan?",
	})
}

func updateWith(t *testing.T, model Model, message tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, command := model.Update(message)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want flowtui.Model", updated)
	}
	return result, command
}

func execute(t *testing.T, model Model, command tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if command == nil {
		t.Fatal("expected non-nil command")
	}
	return updateWith(t, model, command())
}

func keyMessage(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func press(t *testing.T, model Model, key string) (Model, tea.Cmd) {
	t.Helper()
	return updateWith(t, model, keyMessage(key))
}

func loadedModel(t *testing.T, actions Actions, snapshot Snapshot) Model {
	t.Helper()
	if actions.LoadRepository == nil {
		actions.LoadRepository = func(context.Context, string) (Snapshot, error) { return snapshot.Clone(), nil }
	}
	model := NewRepository(actions, snapshot.Repository)
	loaded, _ := execute(t, model, model.Init())
	return loaded
}

func openPlan(t *testing.T, model Model) Model {
	t.Helper()
	planning, command := press(t, model, "enter")
	if planning.overlay != overlayPlanLoading || command == nil {
		t.Fatalf("Enter did not start Plan: overlay=%v command=%v", planning.overlay, command)
	}
	planned, _ := execute(t, planning, command)
	if planned.overlay != overlayPlan {
		t.Fatalf("plan completion overlay = %v", planned.overlay)
	}
	return planned
}

func TestPublicInputValuesDefensivelyCopyOptionsAndCollections(t *testing.T) {
	park := &taskflow.ParkWarmOptions{Next: "original", Push: true}
	choice := NewActionChoice("park-push", "Park + push", "variant", park)
	park.Next = "mutated"
	got := choice.Options().(taskflow.ParkWarmOptions)
	if got.Next != "original" || !got.Push {
		t.Fatalf("choice options changed through input pointer: %+v", got)
	}

	params := map[string]string{"path": "/old"}
	reconcile := &taskflow.ReconcileOptions{Name: "repair-path", Parameters: taskflow.NewFields(params)}
	repair := NewActionChoice("repair", "Repair", "typed repair", reconcile)
	params["path"] = "/mutated"
	reconcile.Name = "mutated"
	reconcile.Parameters = taskflow.NewFields(map[string]string{"path": "/new"})
	gotRepair := repair.Options().(taskflow.ReconcileOptions)
	if gotRepair.Name != "repair-path" || gotRepair.Parameters.Map()["path"] != "/old" {
		t.Fatalf("reconcile options were not copied: %+v", gotRepair)
	}
	returned := gotRepair.Parameters.Map()
	returned["path"] = "/caller-change"
	if repair.Options().(taskflow.ReconcileOptions).Parameters.Map()["path"] != "/old" {
		t.Fatal("options accessor exposed internal parameter storage")
	}

	row := managedSurface("row-one", "task-one", task.ModeWorktree, task.Hot, choice)
	row.Drift = NewLines("old path")
	snapshot := freshSnapshot(row)
	rows := snapshot.Surfaces.Values()
	rows[0].RowKey = "changed"
	actions := rows[0].Actions.Values()
	actions[0] = NewActionChoice("changed", "Changed", "", taskflow.ParkWarmOptions{})
	again := snapshot.Surfaces.Values()[0]
	if again.RowKey != "row-one" || again.Actions.Values()[0].ID != "park-push" {
		t.Fatalf("snapshot collections share mutable storage: %+v", again)
	}
}

func TestInitialViewReturnsBeforeCallbacksRun(t *testing.T) {
	t.Run("picker", func(t *testing.T) {
		calls := 0
		model := NewPicker(Actions{ListRepositories: func(context.Context) ([]RepositoryRow, error) {
			calls++
			return nil, nil
		}})
		view := model.View()
		if calls != 0 || !strings.Contains(view, "LOADING") || !strings.Contains(view, "Loading local repositories asynchronously") {
			t.Fatalf("initial picker frame=%q calls=%d", view, calls)
		}
		command := model.Init()
		if calls != 0 || command == nil {
			t.Fatalf("Init ran callback synchronously: calls=%d command=%v", calls, command)
		}
	})

	t.Run("preselected", func(t *testing.T) {
		calls := 0
		model := NewRepository(Actions{LoadRepository: func(context.Context, string) (Snapshot, error) {
			calls++
			return freshSnapshot(), nil
		}}, testRepo)
		view := model.View()
		if calls != 0 || !strings.Contains(view, "Loading repository topology") || !strings.Contains(view, "NON-AUTHORITATIVE") {
			t.Fatalf("initial repository frame=%q calls=%d", view, calls)
		}
		if command := model.Init(); calls != 0 || command == nil {
			t.Fatalf("Init ran preselected callback synchronously: calls=%d command=%v", calls, command)
		}
	})
}

func TestPickerFiltersAndSelectsExactRepository(t *testing.T) {
	var loadedKey string
	beta := RepositoryRow{RepoKey: "/repos/beta/.git", Name: "beta service", Path: "/repos/beta", Available: true}
	actions := Actions{
		ListRepositories: func(context.Context) ([]RepositoryRow, error) {
			return []RepositoryRow{testRepo, beta}, nil
		},
		LoadRepository: func(_ context.Context, key string) (Snapshot, error) {
			loadedKey = key
			return Snapshot{Repository: beta, Freshness: FreshnessFresh, ObservedAt: testNow}, nil
		},
	}
	model := NewPicker(actions)
	model, _ = execute(t, model, model.Init())
	model, _ = press(t, model, "/")
	model, _ = press(t, model, "beta")
	view := model.View()
	if strings.Contains(view, "example") || !strings.Contains(view, "beta service") {
		t.Fatalf("picker filter did not narrow live: %s", view)
	}
	model, _ = press(t, model, "enter") // keep the filter
	model, command := press(t, model, "enter")
	if model.screen != screenRepository || command == nil || loadedKey != "" {
		t.Fatalf("selection did not start asynchronous load: screen=%v key=%q command=%v", model.screen, loadedKey, command)
	}
	model, _ = execute(t, model, command)
	if loadedKey != beta.RepoKey || model.RepositoryKey() != beta.RepoKey {
		t.Fatalf("LoadRepository key=%q model key=%q", loadedKey, model.RepositoryKey())
	}
}

func TestPreselectedRepositoryLoadsExactKeyAndFocus(t *testing.T) {
	choice := parkChoice("park")
	first := managedSurface("row-first", "task-first", task.ModeWorktree, task.Hot, choice)
	focused := managedSurface("row-focus", "task-focus", task.ModeWorktree, task.Hot, choice)
	repo := testRepo
	repo.FocusTarget = "task-focus"
	var gotKey string
	model := NewRepository(Actions{LoadRepository: func(_ context.Context, key string) (Snapshot, error) {
		gotKey = key
		return Snapshot{
			Repository: repo, Surfaces: NewSurfaceList(first, focused),
			Freshness: FreshnessFresh, ObservedAt: testNow,
		}, nil
	}}, repo)
	model, _ = execute(t, model, model.Init())
	if gotKey != repo.RepoKey || model.SelectedRowKey() != "row-focus" {
		t.Fatalf("preselected load key=%q selected=%q", gotKey, model.SelectedRowKey())
	}
}

func TestStaleRepositoryGenerationIsRejected(t *testing.T) {
	calls := 0
	actions := Actions{LoadRepository: func(context.Context, string) (Snapshot, error) {
		calls++
		row := managedSurface(fmt.Sprintf("row-%d", calls), fmt.Sprintf("task-%d", calls), task.ModeWorktree, task.Hot, parkChoice("park"))
		return freshSnapshot(row), nil
	}}
	model := NewRepository(actions, testRepo)
	model, _ = execute(t, model, model.Init())
	if model.SelectedRowKey() != "row-1" {
		t.Fatalf("initial row = %q", model.SelectedRowKey())
	}
	model, staleCommand := press(t, model, "r")
	model, currentCommand := press(t, model, "r")
	staleMessage := staleCommand()
	model, _ = updateWith(t, model, staleMessage)
	if model.SelectedRowKey() != "row-1" || !model.loadRequest.loading {
		t.Fatalf("stale generation changed snapshot: row=%q loading=%v", model.SelectedRowKey(), model.loadRequest.loading)
	}
	model, _ = execute(t, model, currentCommand)
	if model.SelectedRowKey() != "row-3" || model.loadRequest.loading {
		t.Fatalf("current generation was not accepted: row=%q loading=%v", model.SelectedRowKey(), model.loadRequest.loading)
	}
}

func TestLeavingRepositoryRejectsItsInFlightLoad(t *testing.T) {
	row := managedSurface("row-late", "task-late", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{
		ListRepositories: func(context.Context) ([]RepositoryRow, error) { return []RepositoryRow{testRepo}, nil },
		LoadRepository:   func(context.Context, string) (Snapshot, error) { return freshSnapshot(row), nil },
	}
	model := NewRepository(actions, testRepo)
	lateCommand := model.Init()
	model, pickerCommand := press(t, model, "esc")
	if model.screen != screenPicker || pickerCommand == nil {
		t.Fatalf("Esc did not back out to asynchronous picker: screen=%v command=%v", model.screen, pickerCommand)
	}
	model, _ = updateWith(t, model, lateCommand())
	if model.hasSnapshot || model.screen != screenPicker {
		t.Fatalf("late repository load was accepted after backing out: snapshot=%v screen=%v", model.hasSnapshot, model.screen)
	}
}

func TestValidEmptyReplacesRowsAndFailedRefreshRetainsStaleRows(t *testing.T) {
	t.Run("valid empty", func(t *testing.T) {
		loads := 0
		actions := Actions{LoadRepository: func(context.Context, string) (Snapshot, error) {
			loads++
			if loads == 1 {
				return freshSnapshot(managedSurface("row-one", "task-one", task.ModeWorktree, task.Hot, parkChoice("park"))), nil
			}
			return freshSnapshot(), nil
		}}
		model := NewRepository(actions, testRepo)
		model, _ = execute(t, model, model.Init())
		model, command := press(t, model, "r")
		model, _ = execute(t, model, command)
		if model.snapshot.Surfaces.Len() != 0 || model.loadRequest.err != nil || !strings.Contains(model.View(), "valid empty snapshot") {
			t.Fatalf("valid empty did not replace rows: %s", model.View())
		}
	})

	t.Run("failed refresh", func(t *testing.T) {
		loads := 0
		actions := Actions{LoadRepository: func(context.Context, string) (Snapshot, error) {
			loads++
			if loads == 1 {
				return freshSnapshot(managedSurface("row-kept", "task-kept", task.ModeWorktree, task.Hot, parkChoice("park"))), nil
			}
			return Snapshot{}, errors.New("git status failed")
		}}
		model := NewRepository(actions, testRepo)
		model, _ = execute(t, model, model.Init())
		model, command := press(t, model, "r")
		model, _ = execute(t, model, command)
		view := model.View()
		if model.SelectedRowKey() != "row-kept" || model.snapshot.Freshness != FreshnessStale ||
			!strings.Contains(view, "STALE") || !strings.Contains(view, "NON-AUTHORITATIVE") || !strings.Contains(view, "git status failed") {
			t.Fatalf("failed refresh did not retain stale snapshot: %s", view)
		}
	})
}

func TestSelectionFollowsTaskOnlyRowToCheckoutByTaskID(t *testing.T) {
	loads := 0
	canonical := SurfaceRow{RowKey: "/repos/example", Kind: SurfaceCanonical, Label: "canonical", Path: testRepo.Path}
	taskOnlyLocator := testLocator("task-only-row", "task-stable", task.ModeWorktree, task.Cold)
	taskOnlyLocator.CheckoutPath = ""
	taskOnly := SurfaceRow{
		RowKey: "task-only-row", Kind: SurfaceTaskOnly, Label: "cold task",
		Mode: task.ModeWorktree, State: task.Cold, Locator: taskOnlyLocator,
		Actions: NewActionList(NewActionChoice("resume", "Resume", "rebuild checkout", taskflow.ResumeOptions{})),
	}
	checkout := managedSurface("/worktrees/rebuilt", "task-stable", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{LoadRepository: func(context.Context, string) (Snapshot, error) {
		loads++
		if loads == 1 {
			return freshSnapshot(canonical, taskOnly), nil
		}
		return freshSnapshot(canonical, checkout), nil
	}}
	model := NewRepository(actions, testRepo)
	model, _ = execute(t, model, model.Init())
	model, _ = press(t, model, "down")
	if model.SelectedRowKey() != "task-only-row" {
		t.Fatalf("selected row before reload = %q", model.SelectedRowKey())
	}
	model, command := press(t, model, "r")
	model, _ = execute(t, model, command)
	if model.SelectedRowKey() != "/worktrees/rebuilt" {
		t.Fatalf("selection did not follow stable TaskID: %q", model.SelectedRowKey())
	}
}

func TestRowActionAndFocusNavigation(t *testing.T) {
	rowOne := managedSurface("row-one", "task-one", task.ModeWorktree, task.Hot,
		parkChoice("park"), NewActionChoice("cold", "Park cold", "remove checkout", taskflow.ParkColdOptions{}))
	rowTwo := managedSurface("row-two", "task-two", task.ModeWorktree, task.Hot, parkChoice("park-two"))
	model := loadedModel(t, Actions{}, freshSnapshot(rowOne, rowTwo))
	model, _ = press(t, model, "right")
	if model.SelectedActionID() != "cold" || model.focus != FocusActions {
		t.Fatalf("right navigation action=%q focus=%v", model.SelectedActionID(), model.focus)
	}
	model, _ = press(t, model, "down")
	if model.SelectedRowKey() != "row-two" || model.SelectedActionID() != "park-two" {
		t.Fatalf("row navigation row=%q action=%q", model.SelectedRowKey(), model.SelectedActionID())
	}
	model, _ = press(t, model, "tab")
	if model.focus != FocusSurfaces { // actions -> surfaces
		t.Fatalf("Tab focus = %v", model.focus)
	}
	model, _ = press(t, model, "shift+tab")
	if model.focus != FocusActions {
		t.Fatalf("Shift+Tab focus = %v", model.focus)
	}
}

func TestBlockedPlanIsFullyInspectableAndCannotApply(t *testing.T) {
	applies := 0
	row := managedSurface("row-blocked", "task-blocked", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			return buildPlan(t, locator, options, taskflow.VerdictBlocked, taskflow.Confirmation{}), nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			applies++
			return taskflow.Result{}, nil
		},
	}
	model := loadedModel(t, actions, freshSnapshot(row))
	model = openPlan(t, model)
	view := model.View()
	for _, text := range []string{"availability: BLOCKED", "CONDITIONS (ordered)", "exact checkout evidence", "reload and repair", "EFFECTS (execution order)", "RETAINED RESOURCES", "fallback command", "NO APPLY PATH"} {
		if !strings.Contains(view, text) {
			t.Errorf("blocked plan view missing %q:\n%s", text, view)
		}
	}
	model, command := press(t, model, "y")
	if command != nil || applies != 0 || model.applyRunning {
		t.Fatalf("blocked plan found Apply path: command=%v applies=%d running=%v", command, applies, model.applyRunning)
	}
}

func TestPlanRendersNetworkAndDestructiveEffectMarkers(t *testing.T) {
	row := managedSurface("row-markers", "task-markers", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
		request, err := taskflow.NewRequest(locator, options)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		return taskflow.BuildPlan(request, taskflow.PlanSpec{
			Conditions: []taskflow.Condition{{Code: "safe", Verdict: taskflow.VerdictMet, Requirement: taskflow.RequirementRequired, Evidence: "checked"}},
			Effects: []taskflow.Effect{
				taskflow.NewEffect("fetch-refs", "fetch refs", "origin", false, true, nil),
				taskflow.NewEffect("remove-checkout", "remove checkout", locator.CheckoutPath, true, false, nil),
			},
			Confirmation: taskflow.Confirmation{Kind: taskflow.ConfirmationApproval},
		})
	}}
	model := openPlan(t, loadedModel(t, actions, freshSnapshot(row)))
	view := model.View()
	if !strings.Contains(view, "[NETWORK]") || !strings.Contains(view, "[DESTRUCTIVE]") {
		t.Fatalf("effect markers missing from plan:\n%s", view)
	}
}

func TestReadyPlanRequiresExplicitYAndEnterNeverApplies(t *testing.T) {
	plans, applies := 0, 0
	row := managedSurface("row-ready", "task-ready", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			plans++
			return readyPlan(t, locator, options), nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			applies++
			return taskflow.NewResult(taskflow.ResultSpec{}), nil
		},
	}
	model := loadedModel(t, actions, freshSnapshot(row))
	model = openPlan(t, model)
	if plans != 1 || applies != 0 || !strings.Contains(model.View(), "Press y to approve") {
		t.Fatalf("plan counts plans=%d applies=%d view=%s", plans, applies, model.View())
	}
	model, command := press(t, model, "enter")
	if command != nil || applies != 0 {
		t.Fatalf("Enter applied a ready plan: command=%v applies=%d", command, applies)
	}
	model, command = press(t, model, "y")
	if command == nil || !model.applyRunning || applies != 0 {
		t.Fatalf("y did not begin asynchronous Apply: command=%v running=%v applies=%d", command, model.applyRunning, applies)
	}
	_, _ = execute(t, model, command)
	if applies != 1 {
		t.Fatalf("Apply calls = %d", applies)
	}
}

func TestTypedApprovalRejectsWrongTokenAndAcceptsExactToken(t *testing.T) {
	applies := 0
	row := managedSurface("row-typed", "task-typed", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			return buildPlan(t, locator, options, taskflow.VerdictMet, taskflow.Confirmation{
				Kind: taskflow.ConfirmationTyped, Prompt: "Confirm destructive variant", Token: "DELETE task-typed",
			}), nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			applies++
			return taskflow.NewResult(taskflow.ResultSpec{}), nil
		},
	}
	model := openPlan(t, loadedModel(t, actions, freshSnapshot(row)))
	model, _ = press(t, model, "WRONG")
	model, command := press(t, model, "enter")
	if command != nil || applies != 0 || !strings.Contains(model.View(), "does not match exactly") {
		t.Fatalf("wrong token was not rejected: command=%v applies=%d view=%s", command, applies, model.View())
	}
	for range len("WRONG") {
		model, _ = press(t, model, "backspace")
	}
	model, _ = press(t, model, "DELETE task-typed")
	model, command = press(t, model, "enter")
	if command == nil || !model.applyRunning {
		t.Fatalf("exact token did not begin Apply: command=%v running=%v", command, model.applyRunning)
	}
	_, _ = execute(t, model, command)
	if applies != 1 {
		t.Fatalf("typed Apply calls = %d", applies)
	}
}

func TestPlanWithDifferentConcreteOptionsIsInspectableButCannotApply(t *testing.T) {
	row := managedSurface("row-options", "task-options", task.ModeWorktree, task.Hot,
		NewActionChoice("park-no-push", "Park", "do not push", taskflow.ParkWarmOptions{Push: false}))
	applies := 0
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, _ taskflow.ActionOptions) (taskflow.Plan, error) {
			return readyPlan(t, locator, taskflow.ParkWarmOptions{Push: true}), nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			applies++
			return taskflow.Result{}, nil
		},
	}
	model := openPlan(t, loadedModel(t, actions, freshSnapshot(row)))
	if model.planErr == nil || !strings.Contains(model.View(), "different concrete action options") {
		t.Fatalf("mismatched plan options were accepted: %s", model.View())
	}
	model, command := press(t, model, "y")
	if command != nil || applies != 0 {
		t.Fatalf("mismatched options reached Apply: command=%v applies=%d", command, applies)
	}
}

func TestStalePlanGenerationIsRejected(t *testing.T) {
	row := managedSurface("row-plan", "task-plan", task.ModeWorktree, task.Hot, parkChoice("park"))
	plans := 0
	actions := Actions{Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
		plans++
		return readyPlan(t, locator, options), nil
	}}
	model := loadedModel(t, actions, freshSnapshot(row))
	model, staleCommand := press(t, model, "enter")
	model, _ = press(t, model, "esc")
	model, currentCommand := press(t, model, "enter")
	model, _ = updateWith(t, model, staleCommand())
	if model.overlay != overlayPlanLoading || !model.planRequest.loading || model.hasPlan {
		t.Fatalf("stale plan message changed current request: overlay=%v loading=%v hasPlan=%v", model.overlay, model.planRequest.loading, model.hasPlan)
	}
	model, _ = execute(t, model, currentCommand)
	if model.overlay != overlayPlan || !model.hasPlan || plans != 2 {
		t.Fatalf("current plan not accepted: overlay=%v hasPlan=%v plans=%d", model.overlay, model.hasPlan, plans)
	}
}

func TestApplyErrorRetainsPartialLedgerWithoutOptimisticState(t *testing.T) {
	row := managedSurface("row-partial", "task-partial", task.ModeWorktree, task.Hot, parkChoice("park"))
	applyErr := errors.New("task write failed")
	var planned taskflow.Plan
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			planned = readyPlan(t, locator, options)
			return planned, nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			effects := planned.Effects()
			return taskflow.NewResult(taskflow.ResultSpec{
				Steps: []taskflow.StepResult{
					{Effect: effects[0], Status: taskflow.StepCompleted, Detail: "runtime closed"},
					{Effect: effects[1], Status: taskflow.StepFailed, Failure: applyErr.Error()},
				},
				Warnings: []string{"runtime was already absent"},
				Recovery: []string{"reload the task revision before retrying"},
			}), applyErr
		},
	}
	model := openPlan(t, loadedModel(t, actions, freshSnapshot(row)))
	model, applyCommand := press(t, model, "y")
	model, reloadCommand := execute(t, model, applyCommand)
	if reloadCommand == nil || model.overlay != overlayResult || !model.loadRequest.loading {
		t.Fatalf("Apply completion did not start fresh local generation: overlay=%v loading=%v command=%v", model.overlay, model.loadRequest.loading, reloadCommand)
	}
	result, err, ok := model.LastResult()
	if !ok || !errors.Is(err, applyErr) || !result.PartialSuccess || len(result.AttemptedSteps()) != 2 {
		t.Fatalf("retained result ok=%v err=%v partial=%v steps=%d", ok, err, result.PartialSuccess, len(result.AttemptedSteps()))
	}
	current, _ := model.CurrentSnapshot()
	if got := current.Surfaces.Values()[0].State; got != task.Hot {
		t.Fatalf("model optimistically changed state to %s", got)
	}
	view := model.View()
	for _, text := range []string{"APPLY ERROR", "PARTIAL SUCCESS", "[COMPLETED]", "[FAILED]", "runtime was already absent", "reload the task revision", "state was not changed optimistically"} {
		if !strings.Contains(view, text) {
			t.Errorf("result view missing %q:\n%s", text, view)
		}
	}
	if _, ok := model.Handoff(); ok {
		t.Fatal("failed Apply exposed a final handoff")
	}
}

func TestApplySuccessRetainsHandoffOnlyAfterResult(t *testing.T) {
	row := managedSurface("row-handoff", "task-handoff", task.ModeWorktree, task.Hot, parkChoice("park"))
	handoff := taskflow.Handoff{Kind: taskflow.HandoffDirectory, Path: "/worktrees/next", Label: "next checkout"}
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			return readyPlan(t, locator, options), nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			return taskflow.NewResult(taskflow.ResultSpec{Handoff: &handoff, Milestone: taskflow.MilestoneReviewReady}), nil
		},
	}
	model := openPlan(t, loadedModel(t, actions, freshSnapshot(row)))
	if _, ok := model.Handoff(); ok {
		t.Fatal("plan exposed handoff before Apply")
	}
	model, command := press(t, model, "y")
	if _, ok := model.Handoff(); ok {
		t.Fatal("in-flight Apply exposed handoff")
	}
	model, _ = execute(t, model, command)
	got, ok := model.Handoff()
	if !ok || got != handoff || !strings.Contains(model.View(), "result milestone: REVIEW") || !strings.Contains(model.View(), "HANDOFF") {
		t.Fatalf("successful handoff ok=%v got=%+v view=%s", ok, got, model.View())
	}
	model, _ = press(t, model, "?")
	if model.overlay != overlayHelp {
		t.Fatalf("result help overlay = %v", model.overlay)
	}
	model, _ = press(t, model, "esc")
	if model.overlay != overlayResult || !strings.Contains(model.View(), "STEP LEDGER") {
		t.Fatalf("closing help did not restore retained result: overlay=%v view=%s", model.overlay, model.View())
	}
}

func TestApplySuccessfulPartialLedgerStillRetainsHandoff(t *testing.T) {
	row := managedSurface("row-partial-handoff", "task-partial-handoff", task.ModeWorktree, task.Warm, parkChoice("resume"))
	handoff := taskflow.Handoff{Kind: taskflow.HandoffRuntime, Path: "/worktrees/next", Runtime: "herdr", RuntimeHandle: "w-next"}
	var planned taskflow.Plan
	actions := Actions{
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			planned = readyPlan(t, locator, options)
			return planned, nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			effects := planned.Effects()
			return taskflow.NewResult(taskflow.ResultSpec{
				Steps: []taskflow.StepResult{
					{Effect: effects[0], Status: taskflow.StepFailed, Failure: "optional fetch failed"},
					{Effect: effects[1], Status: taskflow.StepCompleted, Detail: "recorded HOT"},
				},
				Warnings: []string{"fetch failed"}, Handoff: &handoff,
			}), nil
		},
	}
	model := openPlan(t, loadedModel(t, actions, freshSnapshot(row)))
	model, command := press(t, model, "y")
	model, _ = execute(t, model, command)
	result, err, ok := model.LastResult()
	got, hasHandoff := model.Handoff()
	if !ok || err != nil || !result.PartialSuccess || !hasHandoff || got != handoff {
		t.Fatalf("partial successful handoff result=%+v err=%v ok=%v handoff=%+v present=%v", result, err, ok, got, hasHandoff)
	}
}

func TestApplyQueuesRefreshAndQuitWithoutCancelingMutation(t *testing.T) {
	newActions := func(applyCalls *int) Actions {
		return Actions{
			Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
				return readyPlan(t, locator, options), nil
			},
			Apply: func(ctx context.Context, _ string, _ string, _ string, _ taskflow.Locator, _ taskflow.ActionOptions, _ taskflow.Plan, _ taskflow.Approval) (taskflow.Result, error) {
				*applyCalls++
				if ctx.Err() != nil {
					t.Fatalf("Apply context was canceled: %v", ctx.Err())
				}
				return taskflow.NewResult(taskflow.ResultSpec{}), nil
			},
		}
	}

	t.Run("refresh", func(t *testing.T) {
		calls := 0
		row := managedSurface("row-queue", "task-queue", task.ModeWorktree, task.Hot, parkChoice("park"))
		model := openPlan(t, loadedModel(t, newActions(&calls), freshSnapshot(row)))
		model, applyCommand := press(t, model, "y")
		generation := model.loadRequest.generation
		model, refreshCommand := press(t, model, "r")
		if refreshCommand != nil || !model.queuedRefresh || !model.applyRunning {
			t.Fatalf("refresh was not queued: command=%v queued=%v running=%v", refreshCommand, model.queuedRefresh, model.applyRunning)
		}
		model, reloadCommand := execute(t, model, applyCommand)
		if calls != 1 || reloadCommand == nil || model.loadRequest.generation <= generation || model.applyRunning {
			t.Fatalf("queued refresh result calls=%d command=%v generation=%d->%d running=%v", calls, reloadCommand, generation, model.loadRequest.generation, model.applyRunning)
		}
	})

	t.Run("quit", func(t *testing.T) {
		calls := 0
		row := managedSurface("row-quit", "task-quit", task.ModeWorktree, task.Hot, parkChoice("park"))
		model := openPlan(t, loadedModel(t, newActions(&calls), freshSnapshot(row)))
		model, applyCommand := press(t, model, "y")
		model, quitCommand := press(t, model, "q")
		if quitCommand != nil || !model.queuedQuit || !model.applyRunning {
			t.Fatalf("quit was not queued: command=%v queued=%v running=%v", quitCommand, model.queuedQuit, model.applyRunning)
		}
		model, finalCommand := execute(t, model, applyCommand)
		if calls != 1 || finalCommand == nil || !model.quitting || model.applyRunning {
			t.Fatalf("queued quit result calls=%d command=%v quitting=%v running=%v", calls, finalCommand, model.quitting, model.applyRunning)
		}
		if _, ok := finalCommand().(tea.QuitMsg); !ok {
			t.Fatalf("queued quit command emitted %T", finalCommand())
		}
		if _, _, ok := model.LastResult(); !ok || model.loadRequest.generation == 0 {
			t.Fatalf("queued quit discarded result or fresh generation: result=%v generation=%d", ok, model.loadRequest.generation)
		}
	})
}

func TestRemoteMenuUsesProvidedVariantAndRetainsRunLocalResult(t *testing.T) {
	loads := 0
	row := managedSurface("row-remote", "task-remote", task.ModeWorktree, task.Hot,
		parkChoice("park"), remoteChoice("Fetch refs", true, false), remoteChoice("Both", true, true))
	remote := taskflow.RemoteObservation{
		RemoteName: "origin", Repository: "owner/example", Head: "feature/example", Base: "main",
		BeforeRefs: taskflow.RemoteRefsObservation{LocalHead: taskflow.NamedRefObservation{Ref: "refs/heads/feature/example", Exists: true, OID: strings.Repeat("1", 40)}},
		HasReview:  true,
		Review:     taskflow.RemoteReviewObservation{State: taskflow.ObservationKnown, Exists: false, ObservedAt: testNow},
	}
	var selected taskflow.RefreshRemoteOptions
	actions := Actions{
		LoadRepository: func(context.Context, string) (Snapshot, error) {
			loads++
			return freshSnapshot(row), nil
		},
		Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
			selected = options.(taskflow.RefreshRemoteOptions)
			return readyPlan(t, locator, options), nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			return taskflow.NewResult(taskflow.ResultSpec{Remote: &remote}), nil
		},
	}
	model := NewRepository(actions, testRepo)
	model, _ = execute(t, model, model.Init())
	model, _ = press(t, model, "R")
	if model.overlay != overlayRemoteMenu || model.remoteChoices.Len() != 2 || !strings.Contains(model.View(), "REMOTE ACTIONS") {
		t.Fatalf("R did not open provided remote menu: %s", model.View())
	}
	model, _ = press(t, model, "down")
	model, planCommand := press(t, model, "enter")
	model, _ = execute(t, model, planCommand)
	if !selected.FetchRefs || !selected.QueryReview {
		t.Fatalf("remote menu selected options %+v", selected)
	}
	model, applyCommand := press(t, model, "y")
	model, reloadCommand := execute(t, model, applyCommand)
	if _, ok := model.RemoteObservation(); !ok || !strings.Contains(model.View(), "REMOTE REVIEW: NONE (known)") {
		t.Fatalf("remote result not retained before reload: %s", model.View())
	}
	model, _ = execute(t, model, reloadCommand)
	got, ok := model.RemoteObservation()
	if !ok || got.RemoteName != "origin" || loads != 2 {
		t.Fatalf("remote result lost after local reload: ok=%v remote=%+v loads=%d", ok, got, loads)
	}
}

func TestWideNarrowAndMonochromeViewsRemainTextReadable(t *testing.T) {
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.ColorProfile()) })
	SetColorEnabled(false)
	row := managedSurface("row-layout", "task-layout", task.ModeWorktree, task.Hot, parkChoice("park"))
	row.Drift = NewLines("recorded path moved")
	row.Conflicts = NewLines("duplicate task claim")
	model := loadedModel(t, Actions{}, freshSnapshot(row))
	model, _ = updateWith(t, model, tea.WindowSizeMsg{Width: 130, Height: 35})
	wide := model.View()
	if !strings.Contains(wide, "SURFACES [FOCUS]") || !strings.Contains(wide, " | LIFECYCLE / EVIDENCE") ||
		!strings.Contains(wide, " | ACTIONS / CONDITION") || !strings.Contains(wide, "DRIFT:") || !strings.Contains(wide, "CONFLICT:") {
		t.Fatalf("wide layout missing text labels:\n%s", wide)
	}
	model, _ = updateWith(t, model, tea.WindowSizeMsg{Width: 70, Height: 35})
	narrow := model.View()
	if !strings.Contains(narrow, "SURFACES [FOCUS]\n") || !strings.Contains(narrow, "\nLIFECYCLE / EVIDENCE\n") ||
		!strings.Contains(narrow, "\nACTIONS / CONDITION\n") || strings.Contains(narrow, "\x1b[") {
		t.Fatalf("narrow/monochrome layout unreadable:\n%s", narrow)
	}
	for _, label := range []string{"HOT", "WARM", "COLD", "DONE", "DRIFT", "CONFLICT", "MANAGED"} {
		if !strings.Contains(narrow, label) {
			t.Errorf("monochrome view missing label %q", label)
		}
	}
}

func TestLifecycleRailsAreModeAware(t *testing.T) {
	directLocator := testLocator("row-direct", "task-direct", task.ModeDirect, task.Warm)
	directLocator.CheckoutPath = testRepo.Path
	direct := SurfaceRow{
		RowKey: "row-direct", Kind: SurfaceManaged, Label: "direct task", Path: testRepo.Path,
		Branch: directLocator.Branch, Base: directLocator.Base, Mode: task.ModeDirect, State: task.Warm,
		Locator: directLocator, Actions: NewActionList(parkChoice("park-direct")),
	}
	worktree := managedSurface("row-worktree", "task-worktree", task.ModeWorktree, task.Cold,
		NewActionChoice("resume", "Resume", "rebuild", taskflow.ResumeOptions{}))
	model := loadedModel(t, Actions{}, freshSnapshot(direct, worktree))
	model, _ = updateWith(t, model, tea.WindowSizeMsg{Width: 80, Height: 40})
	directView := model.View()
	if !strings.Contains(directView, "rail: HOT -> [WARM CURRENT] -> DONE") || strings.Contains(directView, "rail: HOT -> [WARM CURRENT] -> COLD") {
		t.Fatalf("direct rail is not mode-aware:\n%s", directView)
	}
	model, _ = press(t, model, "down")
	worktreeView := model.View()
	if !strings.Contains(worktreeView, "rail: HOT -> WARM -> [COLD CURRENT] -> DONE") {
		t.Fatalf("worktree rail missing COLD:\n%s", worktreeView)
	}
}

func TestPlanAndResultMilestonesAreNotPersistedRailStates(t *testing.T) {
	row := managedSurface("row-milestone", "task-milestone", task.ModeWorktree, task.Hot, parkChoice("park"))
	actions := Actions{Plan: func(_ context.Context, _, _, _ string, locator taskflow.Locator, options taskflow.ActionOptions) (taskflow.Plan, error) {
		return readyPlan(t, locator, options), nil
	}}
	model := loadedModel(t, actions, freshSnapshot(row))
	mainView := model.View()
	for _, forbidden := range []string{"[READY", "[REVIEW", "[MERGED", "[RETIRED"} {
		if strings.Contains(mainView, forbidden) {
			t.Errorf("persisted rail contains milestone %q:\n%s", forbidden, mainView)
		}
	}
	model = openPlan(t, model)
	if !strings.Contains(model.View(), "availability: READY") {
		t.Fatalf("READY not rendered as plan availability:\n%s", model.View())
	}
}

func TestQDoesNotInvokeAnyCallback(t *testing.T) {
	calls := 0
	actions := Actions{
		ListRepositories: func(context.Context) ([]RepositoryRow, error) { calls++; return nil, nil },
		LoadRepository:   func(context.Context, string) (Snapshot, error) { calls++; return Snapshot{}, nil },
		Plan: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions) (taskflow.Plan, error) {
			calls++
			return taskflow.Plan{}, nil
		},
		Apply: func(context.Context, string, string, string, taskflow.Locator, taskflow.ActionOptions, taskflow.Plan, taskflow.Approval) (taskflow.Result, error) {
			calls++
			return taskflow.Result{}, nil
		},
	}
	for name, model := range map[string]Model{
		"picker":      NewPicker(actions),
		"preselected": NewRepository(actions, testRepo),
	} {
		t.Run(name, func(t *testing.T) {
			updated, command := press(t, model, "q")
			if calls != 0 || command == nil || !updated.quitting {
				t.Fatalf("q calls=%d command=%v quitting=%v", calls, command, updated.quitting)
			}
			message := command()
			if _, ok := message.(tea.QuitMsg); !ok {
				t.Fatalf("q command emitted %T", message)
			}
		})
	}
}
