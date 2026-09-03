package inventory_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestRepoContextClassifiesAndMatchesNestedCheckouts(t *testing.T) {
	r := gittest.New(t)
	r.Write(".gitignore", ".claude/worktrees/\n")
	r.Git("add", ".gitignore")
	r.Git("commit", "-m", "chore: ignore agent worktrees")

	devPath := filepath.Join(t.TempDir(), "dev-owned")
	if err := gitx.AddWorktree(context.Background(), r.Root, devPath, "feat/dev", "main"); err != nil {
		t.Fatal(err)
	}
	nestedPath := filepath.Join(r.Root, ".claude", "worktrees", "turn-1")
	if err := os.MkdirAll(filepath.Dir(nestedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitx.AddWorktree(context.Background(), r.Root, nestedPath, "worktree-turn-1", "main"); err != nil {
		t.Fatal(err)
	}

	tasks := []*task.Task{{
		ID: "repo__feat-dev", Repo: "repo", RepoPath: r.Root,
		Branch: "feat/dev", WorktreePath: devPath, State: task.Hot,
	}}
	sessions := []runtime.Session{
		{Handle: "main", Dirs: []string{r.Root}, AgentStatus: "idle"},
		{Handle: "dev", Dirs: []string{filepath.Join(devPath, "src")}, AgentStatus: "working",
			AgentSessions: []string{"claude:dev"}},
		{Handle: "nested", Dirs: []string{nestedPath}, AgentStatus: "working",
			AgentSessions: []string{"claude:turn"}},
	}
	ctx := inventory.CollectRepoContext(context.Background(), repo.Repo{
		Name: "repo", Path: r.Root, RealPath: r.Root,
		CommonDir: filepath.Join(r.Root, ".git"), HasGit: true,
	}, tasks, sessions, "herdr")

	if ctx.WorktreeCount != 2 || len(ctx.Checkouts) != 3 {
		t.Fatalf("context worktrees=%d checkouts=%d", ctx.WorktreeCount, len(ctx.Checkouts))
	}
	byBranch := map[string]inventory.RepoCheckout{}
	for _, checkout := range ctx.Checkouts {
		byBranch[checkout.Branch()] = checkout
	}
	if got := byBranch["feat/dev"]; got.Ownership != inventory.CheckoutDev ||
		len(got.Sessions) != 1 || got.Sessions[0].Handle != "dev" {
		t.Errorf("dev checkout = %+v", got)
	}
	if got := byBranch["worktree-turn-1"]; got.Ownership != inventory.CheckoutEphemeral ||
		len(got.Sessions) != 1 || got.Sessions[0].Handle != "nested" {
		t.Errorf("nested checkout = %+v", got)
	}
	main, _ := ctx.Main()
	if len(main.Sessions) != 1 || main.Sessions[0].Handle != "main" {
		t.Errorf("nested session must not leak into main: %+v", main.Sessions)
	}

	markdown := inventory.FormatRepoContext(ctx, -1)
	for _, want := range []string{"# dev repo context: repo", devPath, nestedPath, "claude:turn", "ephemeral"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("context missing %q:\n%s", want, markdown)
		}
	}
	devIndex := -1
	for i, checkout := range ctx.Checkouts {
		if checkout.Branch() == "feat/dev" {
			devIndex = i
			break
		}
	}
	child := inventory.FormatRepoContext(ctx, devIndex)
	if !strings.Contains(child, devPath) || strings.Contains(child, nestedPath) || strings.Contains(child, r.Root+"`") {
		t.Errorf("child context should contain only one checkout:\n%s", child)
	}
}

func TestRepoContextCopyPayloads(t *testing.T) {
	ctx := inventory.RepoContext{
		Runtime: "herdr",
		Checkouts: []inventory.RepoCheckout{
			{Worktree: gitx.Worktree{Path: "/repo", Branch: "main", Main: true}},
			{Worktree: gitx.Worktree{Path: "/wt/one", Branch: "feat/one"}, Sessions: []runtime.Session{{
				Handle: "w1", AgentStatus: "working", AgentSessions: []string{"codex:abc"},
			}}},
			{Worktree: gitx.Worktree{Path: "/wt/two", Branch: "feat/two"}},
		},
	}
	if got := inventory.LinkedWorktreePaths(ctx); got != "/wt/one\n/wt/two" {
		t.Errorf("LinkedWorktreePaths = %q", got)
	}
	if got := inventory.FormatSessions(ctx, 1); !strings.Contains(got, "herdr w1") || !strings.Contains(got, "codex:abc") {
		t.Errorf("FormatSessions = %q", got)
	}
	if got := inventory.FormatSessions(ctx, 2); got != "" {
		t.Errorf("closed checkout sessions = %q", got)
	}
}

func TestRepoContextUsesCanonicalIdentityAndAuthoritativeMain(t *testing.T) {
	repository := gittest.New(t)
	linkedPath := filepath.Join(t.TempDir(), "linked")
	if err := gitx.AddWorktree(context.Background(), repository.Root, linkedPath, "feat/linked", "main"); err != nil {
		t.Fatal(err)
	}
	mainRepo := discoveredRepo(t, repository.Root)
	linkedRepo := discoveredRepo(t, linkedPath)
	aliasRoot := t.TempDir()
	aliasPath := filepath.Join(aliasRoot, "repo-alias")
	if err := os.Symlink(repository.Root, aliasPath); err != nil {
		t.Fatal(err)
	}
	aliasRepo := mainRepo
	aliasRepo.Path = aliasPath
	aliasRepo.Symlink = true

	wantRepositoryID, err := pathx.Canonical(mainRepo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	wantMainID, err := pathx.Canonical(repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	wantLinkedID, err := pathx.Canonical(linkedPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		repository repo.Repo
	}{
		{name: "normal", repository: mainRepo},
		{name: "linked cwd", repository: linkedRepo},
		{name: "symlink alias", repository: aliasRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := inventory.CollectRepoContext(context.Background(), tc.repository, nil, nil, "none")
			if ctx.WorktreeErr != nil {
				t.Fatal(ctx.WorktreeErr)
			}
			if ctx.RepositoryID != wantRepositoryID {
				t.Errorf("RepositoryID = %q, want %q", ctx.RepositoryID, wantRepositoryID)
			}
			if len(ctx.Checkouts) != 2 || len(ctx.Rows) != 2 {
				t.Fatalf("checkouts=%d rows=%d, want two authoritative checkout rows", len(ctx.Checkouts), len(ctx.Rows))
			}
			main, ok := ctx.Main()
			if !ok || !main.Worktree.Main || main.Worktree.Path != wantMainID {
				t.Fatalf("Main = %+v, want Git's actual main record at %s", main.Worktree, wantMainID)
			}
			if ctx.Checkouts[0].Worktree.Path != wantMainID {
				t.Errorf("first checkout path = %q, selected path was incorrectly synthesized", ctx.Checkouts[0].Worktree.Path)
			}
			ids := map[string]bool{}
			for _, row := range ctx.Rows {
				if row.Kind != inventory.RepoContextRowCheckout || row.Checkout == nil {
					t.Fatalf("row = %+v, want checkout row", row)
				}
				ids[row.ID] = true
				if row.RepositoryID != wantRepositoryID {
					t.Errorf("row repository ID = %q", row.RepositoryID)
				}
			}
			if !ids[wantMainID] || !ids[wantLinkedID] {
				t.Errorf("row IDs = %v, want canonical registered paths", ids)
			}
		})
	}
}

func TestRepoContextIncludesBareHubWorktrees(t *testing.T) {
	source := gittest.New(t)
	root := t.TempDir()
	barePath := filepath.Join(root, "project.git")
	if _, err := gitx.Run(context.Background(), root, "clone", "--bare", source.Root, barePath); err != nil {
		t.Fatal(err)
	}
	linkedPath := filepath.Join(root, "feature")
	if err := gitx.AddWorktree(context.Background(), barePath, linkedPath, "feat/hub", "main"); err != nil {
		t.Fatal(err)
	}

	ctx := inventory.CollectRepoContext(context.Background(), repo.Repo{
		Name: "project", Path: barePath, Bare: true, HasGit: true,
	}, nil, nil, "none")
	if ctx.WorktreeErr != nil {
		t.Fatal(ctx.WorktreeErr)
	}
	if len(ctx.Checkouts) != 2 || ctx.WorktreeCount != 1 {
		t.Fatalf("checkouts=%d linked=%d, want bare record plus linked checkout", len(ctx.Checkouts), ctx.WorktreeCount)
	}
	main, ok := ctx.Main()
	if !ok || !main.Worktree.Main || !main.Worktree.Bare {
		t.Fatalf("bare hub main record = %+v", main.Worktree)
	}
	wantBare, _ := pathx.Canonical(barePath)
	if main.Worktree.Path != wantBare || main.ID != wantBare {
		t.Errorf("bare main path/id = %q/%q, want %q", main.Worktree.Path, main.ID, wantBare)
	}
	linked := checkoutForBranch(t, &ctx, "feat/hub")
	if linked.Worktree.Main || linked.Worktree.Bare || !linked.Exists {
		t.Errorf("linked checkout = %+v", linked)
	}
}

func TestRepoContextRowsKeepCheckoutsAndUnboundTasksExactlyOnce(t *testing.T) {
	repository := gittest.New(t)
	managedPath := filepath.Join(t.TempDir(), "managed")
	externalPath := filepath.Join(t.TempDir(), "external")
	if err := gitx.AddWorktree(context.Background(), repository.Root, managedPath, "feat/managed", "main"); err != nil {
		t.Fatal(err)
	}
	if err := gitx.AddWorktree(context.Background(), repository.Root, externalPath, "feat/external", "main"); err != nil {
		t.Fatal(err)
	}
	tasks := []*task.Task{
		{ID: "repo__main", Repo: "repo", RepoPath: repository.Root, Branch: "main", Mode: task.ModeBranch, State: task.Hot},
		{ID: "repo__managed", Repo: "repo", RepoPath: repository.Root, Branch: "feat/managed", WorktreePath: managedPath, Mode: task.ModeWorktree, State: task.Done},
		{ID: "repo__cold", Repo: "repo", RepoPath: repository.Root, Branch: "feat/cold", Mode: task.ModeWorktree, State: task.Cold},
		{ID: "repo__done", Repo: "repo", RepoPath: repository.Root, Branch: "feat/done", Mode: task.ModeWorktree, State: task.Done},
	}

	ctx := inventory.CollectRepoContext(context.Background(), discoveredRepo(t, repository.Root), tasks, nil, "none")
	if len(ctx.Checkouts) != 3 || len(ctx.OtherTasks) != 2 || len(ctx.Rows) != 5 {
		t.Fatalf("checkouts=%d other=%d rows=%d", len(ctx.Checkouts), len(ctx.OtherTasks), len(ctx.Rows))
	}
	managed := checkoutForBranch(t, &ctx, "feat/managed")
	if hasReason(managed.DriftReasons, inventory.ReasonUnexpectedCheckout) {
		t.Fatalf("normal retained DONE worktree was marked drifted: %+v", managed.DriftReasons)
	}
	wantRows := map[string]bool{"repo__cold": true, "repo__done": true}
	for _, checkout := range ctx.Checkouts {
		wantRows[checkout.ID] = true
	}
	seenRows := map[string]int{}
	for _, row := range ctx.Rows {
		seenRows[row.ID]++
		if row.TaskOnly() && row.ID != row.Task.ID {
			t.Errorf("task-only row ID = %q, task ID = %q", row.ID, row.Task.ID)
		}
	}
	for id := range wantRows {
		if seenRows[id] != 1 {
			t.Errorf("row %q appears %d times", id, seenRows[id])
		}
	}

	occurrences := map[string]int{}
	for _, checkout := range ctx.Checkouts {
		for _, binding := range checkout.TaskBindings {
			occurrences[binding.Task.ID]++
		}
	}
	for _, binding := range ctx.OtherTaskBindings {
		occurrences[binding.Task.ID]++
		if binding.Task.State == task.Cold || binding.Task.State == task.Done {
			if len(binding.DriftReasons) != 0 || len(binding.ConflictReasons) != 0 {
				t.Errorf("expected %s task %s has issues: %+v %+v", binding.Task.State, binding.Task.ID,
					binding.DriftReasons, binding.ConflictReasons)
			}
		}
	}
	for _, tracked := range tasks {
		if occurrences[tracked.ID] != 1 {
			t.Errorf("task %s appears in %d bindings", tracked.ID, occurrences[tracked.ID])
		}
	}
}

func TestRepoContextBindsMovedOrUnrecordedWorktreeByUniqueBranch(t *testing.T) {
	repository := gittest.New(t)
	actualPath := filepath.Join(t.TempDir(), "actual")
	if err := gitx.AddWorktree(context.Background(), repository.Root, actualPath, "feat/moved", "main"); err != nil {
		t.Fatal(err)
	}
	r := discoveredRepo(t, repository.Root)

	for _, tc := range []struct {
		name         string
		recordedPath string
	}{
		{name: "stale path", recordedPath: filepath.Join(t.TempDir(), "old")},
		{name: "absent path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracked := &task.Task{
				ID: "repo__moved", Repo: "repo", RepoPath: repository.Root,
				Branch: "feat/moved", WorktreePath: tc.recordedPath,
				Mode: task.ModeWorktree, State: task.Warm,
			}
			ctx := inventory.CollectRepoContext(context.Background(), r, []*task.Task{tracked}, nil, "none")
			checkout := checkoutForBranch(t, &ctx, "feat/moved")
			if len(checkout.TaskBindings) != 1 || checkout.TaskBindings[0].Kind != inventory.TaskBindingMovedPath {
				t.Fatalf("bindings = %+v", checkout.TaskBindings)
			}
			if !hasReason(checkout.TaskBindings[0].DriftReasons, inventory.ReasonWorktreePathMoved) {
				t.Errorf("moved binding lacks drift reason: %+v", checkout.TaskBindings[0])
			}
			if len(ctx.OtherTasks) != 0 {
				t.Errorf("moved task also remained unbound: %+v", ctx.OtherTasks)
			}
		})
	}
}

func TestRepoContextExactPathWinsAndWrongBranchConflicts(t *testing.T) {
	repository := gittest.New(t)
	actualPath := filepath.Join(t.TempDir(), "actual")
	expectedPath := filepath.Join(t.TempDir(), "expected")
	if err := gitx.AddWorktree(context.Background(), repository.Root, actualPath, "feat/actual", "main"); err != nil {
		t.Fatal(err)
	}
	if err := gitx.AddWorktree(context.Background(), repository.Root, expectedPath, "feat/expected", "main"); err != nil {
		t.Fatal(err)
	}
	r := discoveredRepo(t, repository.Root)
	tracked := &task.Task{
		ID: "repo__wrong", Repo: "repo", RepoPath: repository.Root,
		Branch: "feat/expected", WorktreePath: actualPath,
		Mode: task.ModeWorktree, State: task.Hot,
	}

	ctx := inventory.CollectRepoContext(context.Background(), r, []*task.Task{tracked}, nil, "none")
	actual := checkoutForBranch(t, &ctx, "feat/actual")
	expected := checkoutForBranch(t, &ctx, "feat/expected")
	if len(actual.TaskBindings) != 1 || actual.TaskBindings[0].Kind != inventory.TaskBindingExactPath {
		t.Fatalf("exact-path checkout bindings = %+v", actual.TaskBindings)
	}
	if !hasReason(actual.TaskBindings[0].ConflictReasons, inventory.ReasonBranchMismatch) {
		t.Errorf("wrong branch lacks conflict: %+v", actual.TaskBindings[0])
	}
	if len(expected.TaskBindings) != 0 || len(ctx.OtherTasks) != 0 {
		t.Errorf("task was rebound or duplicated: expected=%+v other=%+v", expected.TaskBindings, ctx.OtherTasks)
	}

	branchTask := &task.Task{
		ID: "repo__branch", Repo: "repo", RepoPath: repository.Root,
		Branch: "feat/expected", WorktreePath: expectedPath,
		Mode: task.ModeBranch, State: task.Hot,
	}
	ctx = inventory.CollectRepoContext(context.Background(), r, []*task.Task{branchTask}, nil, "none")
	if len(ctx.OtherTaskBindings) != 1 || ctx.OtherTaskBindings[0].Bound() {
		t.Fatalf("branch task outside main should remain unbound: %+v", ctx.OtherTaskBindings)
	}
	if !hasReason(ctx.OtherTaskBindings[0].ConflictReasons, inventory.ReasonTaskModeMismatch) {
		t.Errorf("branch task outside main lacks conflict: %+v", ctx.OtherTaskBindings[0])
	}
}

func TestRepoContextMultipleTaskClaimsRemainConflict(t *testing.T) {
	repository := gittest.New(t)
	worktreePath := filepath.Join(t.TempDir(), "shared")
	if err := gitx.AddWorktree(context.Background(), repository.Root, worktreePath, "feat/shared", "main"); err != nil {
		t.Fatal(err)
	}
	tasks := []*task.Task{
		{ID: "repo__a", Repo: "repo", RepoPath: repository.Root, Branch: "feat/shared", WorktreePath: worktreePath, Mode: task.ModeWorktree, State: task.Hot},
		{ID: "repo__b", Repo: "repo", RepoPath: repository.Root, Branch: "feat/shared", WorktreePath: worktreePath, Mode: task.ModeWorktree, State: task.Warm},
	}

	ctx := inventory.CollectRepoContext(context.Background(), discoveredRepo(t, repository.Root), tasks, nil, "none")
	checkout := checkoutForBranch(t, &ctx, "feat/shared")
	if len(checkout.TaskBindings) != 2 || len(ctx.OtherTasks) != 0 {
		t.Fatalf("bindings=%d other=%d", len(checkout.TaskBindings), len(ctx.OtherTasks))
	}
	if !hasReason(checkout.ConflictReasons, inventory.ReasonMultipleTaskClaims) {
		t.Errorf("checkout conflicts = %+v", checkout.ConflictReasons)
	}
	for _, binding := range checkout.TaskBindings {
		if !hasReason(binding.ConflictReasons, inventory.ReasonMultipleTaskClaims) {
			t.Errorf("binding %s conflicts = %+v", binding.Task.ID, binding.ConflictReasons)
		}
	}
}

func TestRepoContextStrictClaudeHarnessEvidenceAndTaskConflict(t *testing.T) {
	repository := gittest.New(t)
	harnessPath := filepath.Join(repository.Root, ".claude", "worktrees", "turn-1")
	if err := os.MkdirAll(filepath.Dir(harnessPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := gitx.AddWorktree(context.Background(), repository.Root, harnessPath, "worktree-turn-1", "main"); err != nil {
		t.Fatal(err)
	}
	externalPath := filepath.Join(t.TempDir(), "prefix-only")
	if err := gitx.AddWorktree(context.Background(), repository.Root, externalPath, "worktree-prefix-only", "main"); err != nil {
		t.Fatal(err)
	}
	tracked := &task.Task{
		ID: "repo__harness", Repo: "repo", RepoPath: repository.Root,
		Branch: "worktree-turn-1", WorktreePath: harnessPath,
		Mode: task.ModeWorktree, State: task.Hot, Owner: "host-a",
	}

	ctx := inventory.CollectRepoContext(context.Background(), discoveredRepo(t, repository.Root), []*task.Task{tracked}, nil, "none")
	harness := checkoutForBranch(t, &ctx, "worktree-turn-1")
	if harness.HarnessEvidence == nil || !hasEvidence(harness.OwnershipEvidence, inventory.OwnershipEvidenceClaudeHarness) ||
		!hasEvidence(harness.OwnershipEvidence, inventory.OwnershipEvidenceManagedTask) {
		t.Fatalf("harness ownership evidence = %+v", harness.OwnershipEvidence)
	}
	managedEvidence := evidenceOfKind(harness.OwnershipEvidence, inventory.OwnershipEvidenceManagedTask)
	if managedEvidence.TaskID != tracked.ID || managedEvidence.Owner != "host-a" {
		t.Errorf("managed ownership evidence = %+v", managedEvidence)
	}
	if !hasReason(harness.ConflictReasons, inventory.ReasonHarnessTaskConflict) {
		t.Errorf("harness/task overlap conflicts = %+v", harness.ConflictReasons)
	}
	if harness.Ownership != inventory.CheckoutEphemeral {
		t.Errorf("harness safety evidence must win the legacy display label, got %q", harness.Ownership)
	}
	formatted := inventory.FormatRepoContext(ctx, -1)
	if !strings.Contains(formatted, "Conflict: `harness-task-conflict`") {
		t.Errorf("harness/task conflict is hidden from formatted context:\n%s", formatted)
	}
	external := checkoutForBranch(t, &ctx, "worktree-prefix-only")
	if external.HarnessEvidence != nil || external.Ownership != inventory.CheckoutExternal {
		t.Errorf("branch prefix became harness proof: %+v", external)
	}
	if !inventory.IsEphemeralWorktree("", "worktree-prefix-only") {
		t.Error("legacy IsEphemeralWorktree branch-prefix compatibility changed")
	}

	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repository.Root, alias); err != nil {
		t.Fatal(err)
	}
	if evidence, ok := inventory.DetectClaudeHarnessWorktree(alias, harnessPath); !ok || evidence.CheckoutPath != harness.ID {
		t.Errorf("symlink-aware harness evidence = %+v, %v", evidence, ok)
	}
	outsideHarness := filepath.Join(t.TempDir(), ".claude", "worktrees", "turn")
	if err := os.MkdirAll(outsideHarness, 0o755); err != nil {
		t.Fatal(err)
	}
	if inventory.IsClaudeHarnessWorktree(repository.Root, outsideHarness) {
		t.Error("a lexical .claude/worktrees path outside the selected repository was accepted")
	}
	escapeTarget := t.TempDir()
	escapePath := filepath.Join(repository.Root, ".claude", "worktrees", "escape")
	if err := os.Symlink(escapeTarget, escapePath); err != nil {
		t.Fatal(err)
	}
	if inventory.IsClaudeHarnessWorktree(repository.Root, escapePath) {
		t.Error("a symlink escaping the harness root was accepted")
	}
}

func TestRepoContextRetainsMissingRegisteredCheckoutAsDrift(t *testing.T) {
	repository := gittest.New(t)
	worktreePath := filepath.Join(t.TempDir(), "missing")
	if err := gitx.AddWorktree(context.Background(), repository.Root, worktreePath, "feat/missing", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatal(err)
	}
	tracked := &task.Task{
		ID: "repo__missing", Repo: "repo", RepoPath: repository.Root,
		Branch: "feat/missing", WorktreePath: worktreePath,
		Mode: task.ModeWorktree, State: task.Warm,
	}

	ctx := inventory.CollectRepoContext(context.Background(), discoveredRepo(t, repository.Root), []*task.Task{tracked}, nil, "none")
	checkout := checkoutForBranch(t, &ctx, "feat/missing")
	if checkout.Exists || len(checkout.TaskBindings) != 1 ||
		checkout.TaskBindings[0].Kind != inventory.TaskBindingExactPath {
		t.Fatalf("missing registered checkout = %+v", checkout)
	}
	if !hasReason(checkout.TaskBindings[0].DriftReasons, inventory.ReasonCheckoutUnavailable) {
		t.Errorf("missing registered checkout lacks drift: %+v", checkout.TaskBindings[0])
	}
}

func TestRepoContextWorktreeQueryFailureIsTypedAndNotActionable(t *testing.T) {
	root := t.TempDir()
	tracked := &task.Task{
		ID: "repo__cold", Repo: "repo", RepoPath: root,
		Branch: "feat/cold", Mode: task.ModeWorktree, State: task.Cold,
	}
	r := repo.Repo{
		Name: "repo", Path: root, RealPath: root,
		CommonDir: filepath.Join(root, ".git"), HasGit: true,
	}

	ctx := inventory.CollectRepoContextWithOptions(context.Background(), r, []*task.Task{tracked}, inventory.RepoContextOptions{})
	var listErr *inventory.WorktreeListError
	if !errors.As(ctx.WorktreeErr, &listErr) || !ctx.WorktreeObservation.Failed() {
		t.Fatalf("worktree error/observation = %T %+v", ctx.WorktreeErr, ctx.WorktreeObservation)
	}
	if len(ctx.Checkouts) != 0 || len(ctx.Rows) != 1 || !ctx.Rows[0].TaskOnly() || ctx.Rows[0].ID != tracked.ID {
		t.Fatalf("failure projection: checkouts=%d rows=%+v", len(ctx.Checkouts), ctx.Rows)
	}
	if !hasReason(ctx.Rows[0].ConflictReasons, inventory.ReasonWorktreeInventoryUnavailable) ||
		!ctx.Rows[0].WorktreeObservation.Failed() {
		t.Errorf("task-only failure row lacks source state/conflict: %+v", ctx.Rows[0])
	}
	formatted := inventory.FormatRepoContext(ctx, -1)
	if !strings.Contains(formatted, "Worktree inventory error:") {
		t.Errorf("typed failure is not visible:\n%s", formatted)
	}
	if strings.Contains(formatted, "### ") || strings.Contains(formatted, "- Git: clean") || strings.Contains(formatted, "- Runtime: closed") {
		t.Errorf("failure synthesized an actionable checkout:\n%s", formatted)
	}
}

func TestRepoContextRuntimeObservationStates(t *testing.T) {
	repository := gittest.New(t)
	r := discoveredRepo(t, repository.Root)

	legacyEmpty := inventory.CollectRepoContext(context.Background(), r, nil, nil, "herdr")
	if legacyEmpty.RuntimeObservation.State != inventory.ObservationUnobserved {
		t.Errorf("legacy empty session input falsely proved a closed runtime: %+v", legacyEmpty.RuntimeObservation)
	}

	unobserved := inventory.CollectRepoContextWithOptions(context.Background(), r, nil, inventory.RepoContextOptions{
		Runtime: "herdr",
	})
	if !unobserved.WorktreeObservation.Available() || len(unobserved.Checkouts) != 1 ||
		unobserved.Checkouts[0].Worktree.Head == "" {
		t.Fatalf("normal repo did not retain Git's authoritative worktree record: %+v", unobserved)
	}
	if unobserved.RuntimeObservation.State != inventory.ObservationUnobserved ||
		unobserved.Checkouts[0].RuntimeObservation.State != inventory.ObservationUnobserved ||
		unobserved.Rows[0].RuntimeObservation.State != inventory.ObservationUnobserved {
		t.Errorf("unobserved runtime = %+v / %+v", unobserved.RuntimeObservation, unobserved.Checkouts[0].RuntimeObservation)
	}

	observed := inventory.CollectRepoContextWithOptions(context.Background(), r, nil, inventory.RepoContextOptions{
		Runtime: "herdr", RuntimeObserved: true,
	})
	if !observed.RuntimeObservation.Available() || len(observed.Checkouts[0].Sessions) != 0 {
		t.Errorf("successful empty runtime observation = %+v sessions=%+v", observed.RuntimeObservation, observed.Checkouts[0].Sessions)
	}

	sentinel := errors.New("runtime unavailable")
	failed := inventory.CollectRepoContextWithOptions(context.Background(), r, nil, inventory.RepoContextOptions{
		Runtime: "herdr", RuntimeObserved: true, RuntimeErr: sentinel,
		Sessions: []runtime.Session{{Handle: "must-not-bind", Dirs: []string{repository.Root}}},
	})
	var runtimeErr *inventory.RuntimeListError
	if !failed.RuntimeObservation.Failed() || !errors.Is(failed.RuntimeObservation.Err, sentinel) ||
		!errors.As(failed.RuntimeObservation.Err, &runtimeErr) {
		t.Fatalf("failed runtime observation = %+v", failed.RuntimeObservation)
	}
	if len(failed.Checkouts[0].Sessions) != 0 {
		t.Errorf("failed observation bound sessions: %+v", failed.Checkouts[0].Sessions)
	}

	live := inventory.CollectRepoContextWithOptions(context.Background(), r, nil, inventory.RepoContextOptions{
		Runtime: "herdr", RuntimeObserved: true,
		Sessions: []runtime.Session{{Handle: "live", Dirs: []string{filepath.Join(repository.Root, "src")}}},
	})
	if len(live.Checkouts[0].Sessions) != 1 || live.Checkouts[0].Sessions[0].Handle != "live" {
		t.Errorf("observed covering session = %+v", live.Checkouts[0].Sessions)
	}
}

func discoveredRepo(t *testing.T, selectedPath string) repo.Repo {
	t.Helper()
	discovered, err := gitx.Discover(context.Background(), selectedPath)
	if err != nil {
		t.Fatal(err)
	}
	realPath := discovered.Root
	if realPath == "" {
		realPath = discovered.MainRoot
	}
	return repo.Repo{
		Name: discovered.Name, Path: selectedPath, RealPath: realPath,
		GitDir: discovered.GitDir, CommonDir: discovered.GitCommonDir,
		MainRoot: discovered.MainRoot, Bare: discovered.Bare, HasGit: true,
	}
}

func checkoutForBranch(t *testing.T, ctx *inventory.RepoContext, branch string) *inventory.RepoCheckout {
	t.Helper()
	for i := range ctx.Checkouts {
		if ctx.Checkouts[i].Branch() == branch {
			return &ctx.Checkouts[i]
		}
	}
	t.Fatalf("no checkout for branch %q in %+v", branch, ctx.Checkouts)
	return nil
}

func hasReason(reasons []inventory.TopologyReason, code inventory.TopologyReasonCode) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func hasEvidence(evidence []inventory.OwnershipEvidence, kind inventory.OwnershipEvidenceKind) bool {
	return evidenceOfKind(evidence, kind).Kind == kind
}

func evidenceOfKind(evidence []inventory.OwnershipEvidence, kind inventory.OwnershipEvidenceKind) inventory.OwnershipEvidence {
	for _, item := range evidence {
		if item.Kind == kind {
			return item
		}
	}
	return inventory.OwnershipEvidence{}
}
