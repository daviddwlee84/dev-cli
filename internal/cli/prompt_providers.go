package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	artifactdomain "github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/closeout"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/promptkit"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/retire"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
)

func prCapabilities(statuses []prProviderStatus) []promptkit.Capability {
	out := make([]promptkit.Capability, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, promptkit.Capability{
			Name: "forge-" + string(status.Forge), Available: status.Status == string(forge.ReadinessReady),
			Detail: status.Status,
		})
	}
	return out
}

func prWarnings(statuses []prProviderStatus, collectErr error) []promptkit.Warning {
	var warnings []promptkit.Warning
	for _, status := range statuses {
		if status.Status == string(forge.ReadinessReady) {
			continue
		}
		warnings = append(warnings, promptkit.Warning{
			Source: string(status.Forge), Code: status.Status,
			Message: firstNonEmpty(status.Detail, prProviderPhrase(status.Status)), Action: status.Action,
		})
	}
	if collectErr != nil {
		warnings = append(warnings, promptkit.Warning{
			Source: "pull-request-inventory", Code: "partial", Message: boundedError(collectErr),
		})
	}
	return warnings
}

type sessionCloseContext struct {
	Runtime             string             `json:"runtime"`
	Sessions            []sessionCloseItem `json:"sessions"`
	UnmatchedActivities []promptActivity   `json:"unmatched_activities"`
}

type sessionCloseItem struct {
	Closeout closeout.SessionCloseReport `json:"closeout"`
	Tasks    []sessionTaskContext        `json:"tasks"`
}

type sessionTaskContext struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	Checkout        string `json:"checkout"`
	CheckoutExists  bool   `json:"checkout_exists"`
	StatusAvailable bool   `json:"status_available"`
	Dirty           *bool  `json:"dirty,omitempty"`
	Ahead           *int   `json:"ahead,omitempty"`
	Behind          *int   `json:"behind,omitempty"`
	Next            string `json:"next,omitempty"`
	ArtifactStatus  string `json:"artifact_status,omitempty"`
	ArtifactReady   *bool  `json:"artifact_ready,omitempty"`
}

type promptActivity struct {
	PaneID      string `json:"pane_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	CWD         string `json:"cwd,omitempty"`
}

func collectSessionClosePrompt(ctx context.Context, app *App) (promptkit.Snapshot, error) {
	rt := app.Runtime()
	snapshot := promptkit.Snapshot{
		Scope: "machine", WorkingDirectory: currentDirectory(), ContextVersion: 1,
		Capabilities: []promptkit.Capability{{Name: "runtime", Available: rt != nil && rt.Name() != "none"}},
	}
	if rt == nil || rt.Name() == "none" {
		snapshot.Context = sessionCloseContext{Runtime: "none", Sessions: []sessionCloseItem{}, UnmatchedActivities: []promptActivity{}}
		snapshot.Capabilities = append(snapshot.Capabilities,
			promptkit.Capability{Name: "runtime-agent-activity", Available: false, Detail: "selected runtime has no session inventory"})
		return snapshot, nil
	}

	sessions, err := rt.List(ctx)
	if err != nil {
		snapshot.Capabilities[0].Detail = rt.Name()
		snapshot.Capabilities[0].Available = false
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{
			Source: rt.Name(), Code: "runtime-list-failed", Message: boundedError(err),
		})
		snapshot.Context = sessionCloseContext{Runtime: rt.Name(), Sessions: []sessionCloseItem{}, UnmatchedActivities: []promptActivity{}}
		return snapshot, nil
	}
	snapshot.Capabilities[0].Detail = rt.Name()

	var activities []runtime.AgentActivity
	activityAvailable := false
	if lister, ok := rt.(runtime.AgentActivityLister); ok {
		activities, err = lister.AgentActivities(ctx)
		if err != nil {
			snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{
				Source: rt.Name(), Code: "agent-activity-failed", Message: boundedError(err),
			})
			activities = nil
		} else {
			activityAvailable = true
			if activities == nil {
				activities = []runtime.AgentActivity{}
			}
		}
	}
	snapshot.Capabilities = append(snapshot.Capabilities, promptkit.Capability{
		Name: "runtime-agent-activity", Available: activityAvailable, Detail: rt.Name(),
	})

	callerPane, callerErr := callerPaneID(ctx, rt)
	if callerErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{
			Source: rt.Name(), Code: "caller-pane-unknown", Message: boundedError(callerErr),
		})
		callerPane = os.Getenv("HERDR_PANE_ID")
	}

	tasks, taskErr := app.Tasks.List()
	if taskErr != nil {
		return promptkit.Snapshot{}, taskErr
	}
	rows := inventory.Collect(ctx, tasks, rt, inventory.Options{
		Sessions: sessions, SessionsSet: true, SessionsTracked: true,
	})
	rowsByRoot := map[string][]inventory.Row{}
	for _, row := range rows {
		if root, ok := canonicalWorktreeRoot(ctx, row.Checkout); ok {
			rowsByRoot[root] = append(rowsByRoot[root], row)
		}
	}
	artifactInspections, artifactErr := artifactdomain.InspectWorktrees(ctx, artifactStore(app))
	if artifactErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{
			Source: "artifact", Code: "inventory-failed", Message: boundedError(artifactErr),
		})
	}

	contextValue := sessionCloseContext{Runtime: rt.Name(), Sessions: []sessionCloseItem{}, UnmatchedActivities: []promptActivity{}}
	matchedActivities := map[string]bool{}
	for _, session := range sessions {
		mappings := closeoutMappings(ctx, session, callerPane)
		if len(mappings) == 0 {
			mappings = []closeout.TargetMapping{{CoverageKnown: false}}
		}
		for _, mapping := range mappings {
			report := closeout.BuildSession(session, activities, mapping)
			item := sessionCloseItem{Closeout: report, Tasks: sessionTasks(rowsByRoot[mapping.CheckoutPath], artifactInspections, artifactErr == nil)}
			contextValue.Sessions = append(contextValue.Sessions, item)
		}
		for _, activity := range activities {
			if activity.WorkspaceID == session.Handle || paneInSession(activity.PaneID, session) {
				matchedActivities[activity.PaneID+"\x00"+activity.WorkspaceID] = true
			}
		}
	}
	for _, activity := range activities {
		if matchedActivities[activity.PaneID+"\x00"+activity.WorkspaceID] {
			continue
		}
		contextValue.UnmatchedActivities = append(contextValue.UnmatchedActivities, promptActivity{
			PaneID: activity.PaneID, WorkspaceID: activity.WorkspaceID, Agent: activity.Agent,
			Name: activity.Name, Status: activity.Status, CWD: activity.CWD,
		})
	}
	snapshot.Context = contextValue
	return snapshot, nil
}

func closeoutMappings(ctx context.Context, session runtime.Session, callerPane string) []closeout.TargetMapping {
	rootSet := map[string]bool{}
	rootCache := map[string]string{}
	rootFor := func(path string) (string, bool) {
		if path == "" {
			return "", false
		}
		if root, seen := rootCache[path]; seen {
			return root, root != ""
		}
		root, ok := canonicalWorktreeRoot(ctx, path)
		if !ok {
			root = ""
		}
		rootCache[path] = root
		return root, ok
	}
	for _, pane := range session.Panes {
		for _, path := range []string{pane.CWD, pane.ShellCWD} {
			if root, ok := rootFor(path); ok {
				rootSet[root] = true
			}
		}
	}
	for _, dir := range session.Dirs {
		if root, ok := rootFor(dir); ok {
			rootSet[root] = true
		}
	}
	roots := make([]string, 0, len(rootSet))
	for root := range rootSet {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	mappings := make([]closeout.TargetMapping, 0, len(roots))
	for _, target := range roots {
		inspection, err := retire.InspectSessions(target, []runtime.Session{session}, retire.Options{
			CWD: currentDirectory(), CallerWorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"), CallerPaneID: callerPane,
		})
		if err != nil {
			mappings = append(mappings, closeout.TargetMapping{CheckoutPath: target, CoverageKnown: false})
			continue
		}
		mapping := closeout.TargetMapping{
			CheckoutPath: target, CoverageKnown: true,
			CoversTarget:    len(inspection.Sessions) > 0,
			CallerContained: inspection.CallerContained,
		}
		if len(inspection.Sessions) > 0 {
			for _, pane := range inspection.Sessions[0].Panes {
				mapping.CoveringPaneIDs = append(mapping.CoveringPaneIDs, pane.ID)
			}
			for _, pane := range inspection.Sessions[0].Mixed {
				mapping.MixedPaneIDs = append(mapping.MixedPaneIDs, pane.ID)
			}
		}
		mapping.MixedPurpose = len(mapping.MixedPaneIDs) > 0
		mappings = append(mappings, mapping)
	}
	return mappings
}

func paneInSession(paneID string, session runtime.Session) bool {
	if paneID == "" {
		return false
	}
	for _, pane := range session.Panes {
		if pane.ID == paneID {
			return true
		}
	}
	return false
}

func sessionTasks(rows []inventory.Row, artifacts map[string]artifactdomain.WorktreeInspection, artifactKnown bool) []sessionTaskContext {
	out := make([]sessionTaskContext, 0, len(rows))
	for _, row := range rows {
		if row.Task == nil {
			continue
		}
		item := sessionTaskContext{
			ID: row.Task.ID, State: string(row.Task.State), Checkout: row.Checkout,
			CheckoutExists: row.CheckoutExists, StatusAvailable: row.CheckoutExists && row.StatusErr == nil,
			Next: row.Task.Next,
		}
		if item.StatusAvailable {
			dirty, ahead, behind := row.Status.Dirty(), row.Status.Ahead, row.Status.Behind
			item.Dirty, item.Ahead, item.Behind = &dirty, &ahead, &behind
		}
		if artifactKnown {
			inspection, exists := artifactInspectionForPath(artifacts, row.Checkout)
			ready := !exists || inspection.Ready
			item.ArtifactReady = &ready
			if exists {
				item.ArtifactStatus = string(inspection.Status)
			}
		}
		out = append(out, item)
	}
	return out
}

type workspacePromptContext struct {
	Workspace    closeout.WorkspaceReport `json:"workspace"`
	PullRequests []prJSONRow              `json:"pull_requests"`
}

func collectWorkspaceCloseoutPrompt(ctx context.Context, app *App, ref, requestedBase string) (promptkit.Snapshot, error) {
	repository, workdir, err := resolvePromptWorkspace(ctx, app, ref)
	if err != nil {
		return promptkit.Snapshot{}, err
	}
	tasks, err := app.Tasks.List()
	if err != nil {
		return promptkit.Snapshot{}, err
	}
	tasks = tasksForRepo(tasks, repository)
	rt := app.Runtime()
	sessions, runtimeErr := rt.List(ctx)
	if rt == nil || rt.Name() == "none" {
		sessions, runtimeErr = []runtime.Session{}, nil
	}
	contextValue := inventory.CollectRepoContext(ctx, repository, tasks, sessions, rt.Name())

	callerPane, paneErr := callerPaneID(ctx, rt)
	runtimeAuditErr := runtimeErr
	if paneErr != nil {
		runtimeAuditErr = errors.Join(runtimeAuditErr, paneErr)
	}
	artifactFactsByPath, artifactErr := artifactdomain.InspectWorktrees(ctx, artifactStore(app))
	audits := map[string]retire.AuditResult{}
	for _, checkout := range contextValue.Checkouts {
		audits[checkout.Worktree.Path] = auditWorkspaceCheckout(ctx, repository, checkout,
			requestedBase, sessions, runtimeAuditErr, callerPane, artifactFactsByPath, artifactErr)
	}
	workspace := closeout.BuildWorkspace(contextValue, audits)

	snapshot := promptkit.Snapshot{
		Scope: "repository", WorkingDirectory: workdir, ContextVersion: 1,
		Target: &promptkit.Target{
			Kind: "repository", Name: repository.Display(), Path: repository.Path,
			WorkingDirectory: workdir,
		},
		Capabilities: []promptkit.Capability{
			{Name: "git-worktree-inventory", Available: contextValue.WorktreeErr == nil},
			{Name: "runtime", Available: runtimeErr == nil, Detail: rt.Name()},
			{Name: "artifact-inventory", Available: artifactErr == nil},
		},
	}
	if runtimeErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{Source: rt.Name(), Code: "runtime-list-failed", Message: boundedError(runtimeErr)})
	}
	if paneErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{Source: rt.Name(), Code: "caller-pane-unknown", Message: boundedError(paneErr)})
	}
	if artifactErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{Source: "artifact", Code: "inventory-failed", Message: boundedError(artifactErr)})
	}
	if contextValue.WorktreeErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, promptkit.Warning{Source: "git", Code: "worktree-list-failed", Message: boundedError(contextValue.WorktreeErr)})
	}

	var prs []prJSONRow
	remoteIdentity := forge.ParseRemoteIdentity(gitx.RemoteFromConfig(repository.CommonDir, "origin"))
	if remoteIdentity.Kind == forge.Unknown || remoteIdentity.Name == "" {
		snapshot.Capabilities = append(snapshot.Capabilities, promptkit.Capability{Name: "pull-request-inventory", Available: false, Detail: "repository has no supported origin"})
	} else {
		collection := collectPullRequests(ctx, app, prCollectOptions{
			Scope: scopeLocal, Query: forge.PRQuery{AnyRole: true, State: forge.PRStateAll},
			Repos: []prRepoSelector{{Kind: remoteIdentity.Kind, Host: remoteIdentity.Host, Name: remoteIdentity.Name}},
		})
		for _, row := range collection.Rows {
			prs = append(prs, makePRJSONRow(row))
		}
		snapshot.Capabilities = append(snapshot.Capabilities, prCapabilities(collection.Providers)...)
		snapshot.Warnings = append(snapshot.Warnings, prWarnings(collection.Providers, collection.Err)...)
	}
	snapshot.Context = workspacePromptContext{Workspace: workspace, PullRequests: prs}
	return snapshot, nil
}

func resolvePromptWorkspace(ctx context.Context, app *App, ref string) (repo.Repo, string, error) {
	if strings.TrimSpace(ref) == "" {
		cwd := currentDirectory()
		discovered, err := gitx.Discover(ctx, cwd)
		if err != nil {
			return repo.Repo{}, "", fmt.Errorf("%s is not a git repository — pass a repo or checkout", cwd)
		}
		return repo.Repo{
			Name: discovered.Name, Path: discovered.MainRoot, RealPath: discovered.MainRoot,
			CommonDir: discovered.GitCommonDir, HasGit: true, Bare: discovered.Bare,
		}, discovered.Root, nil
	}
	if info, err := os.Stat(ref); err == nil && info.IsDir() {
		discovered, err := gitx.Discover(ctx, ref)
		if err != nil {
			return repo.Repo{}, "", fmt.Errorf("%s is not a git repository", ref)
		}
		return repo.Repo{
			Name: discovered.Name, Path: discovered.MainRoot, RealPath: discovered.MainRoot,
			CommonDir: discovered.GitCommonDir, HasGit: true, Bare: discovered.Bare,
		}, discovered.Root, nil
	}
	resolved, _, err := resolveRepoRef(app, ref)
	if err != nil {
		return repo.Repo{}, "", err
	}
	return resolved, resolved.Path, nil
}

func auditWorkspaceCheckout(ctx context.Context, repository repo.Repo, checkout inventory.RepoCheckout,
	requestedBase string, sessions []runtime.Session, runtimeErr error, callerPane string,
	artifacts map[string]artifactdomain.WorktreeInspection, artifactErr error) retire.AuditResult {

	targetKind := retire.AuditTargetKind(checkout.Ownership)
	if checkout.Worktree.Main {
		targetKind = retire.AuditTargetCanonical
	}
	if checkout.Ownership == inventory.CheckoutEphemeral {
		targetKind = retire.AuditTargetEphemeral
	}
	if targetKind == retire.AuditTargetCanonical || targetKind == retire.AuditTargetEphemeral {
		return retire.Audit(retire.AuditInput{TargetKind: targetKind})
	}

	branch := checkout.Branch()
	input := retire.AuditInput{
		TargetKind:  targetKind,
		Registered:  retire.KnownFact(!checkout.Worktree.Prunable),
		Unlocked:    retire.KnownFact(!checkout.Worktree.Locked),
		BranchNamed: retire.KnownFact(branch != "" && !checkout.Worktree.Detached),
		PathExists:  retire.KnownFact(checkout.Exists),
	}
	statusAvailable := checkout.Exists && checkout.StatusErr == nil && !checkout.Worktree.Prunable
	if checkout.StatusErr != nil {
		input.StatusError = boundedError(checkout.StatusErr)
	}
	if statusAvailable {
		input.Dirty = retire.KnownFact(checkout.Status.Dirty())
		_, inProgress, err := gitx.InProgress(checkout.Worktree.Path)
		if err == nil {
			input.InProgress = retire.KnownFact(inProgress)
		}
	}

	base := ""
	var taskState string
	if len(checkout.Tasks) > 0 {
		input.TaskPresent = retire.KnownFact(true)
		allDone := true
		identityMatches := true
		for _, item := range checkout.Tasks {
			if base == "" && item.Base != "" {
				base = item.Base
			}
			if item.Branch != branch {
				identityMatches = false
			}
			if item.State != "done" {
				allDone = false
				taskState = string(item.State)
			}
		}
		input.IdentityMatches = retire.KnownFact(identityMatches)
		if allDone {
			taskState = "done"
		}
	} else {
		input.TaskPresent = retire.KnownFact(false)
		input.IdentityMatches = retire.KnownFact(true)
		base = requestedBase
	}
	input.TaskState = taskState
	if base == "" {
		base = gitx.DefaultBranch(ctx, repository.Path)
	}
	input.BaseKnown = base != "" && branch != "" && gitx.RefExists(ctx, repository.Path, base) && gitx.RefExists(ctx, repository.Path, branch)
	if input.BaseKnown {
		if relation, err := gitx.CompareBranches(ctx, repository.Path, base, branch); err == nil {
			input.Contained = retire.KnownFact(relation.Contained())
		}
	}

	if artifactErr == nil {
		input.ArtifactKnown = true
		fact, exists := artifactInspectionForPath(artifacts, checkout.Worktree.Path)
		input.Finalized = !exists || fact.Ready
	}
	if runtimeErr == nil {
		input.RuntimeKnown = true
		inspection, err := retire.InspectSessions(checkout.Worktree.Path, sessions, retire.Options{
			CWD: currentDirectory(), CallerWorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"), CallerPaneID: callerPane,
		})
		if err == nil {
			input.RuntimeReady = inspection.Ready() && !inspection.RuntimeUnknown
		} else {
			input.RuntimeKnown = false
		}
	}
	return retire.Audit(input)
}

func artifactInspectionForPath(facts map[string]artifactdomain.WorktreeInspection, path string) (artifactdomain.WorktreeInspection, bool) {
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return artifactdomain.WorktreeInspection{}, false
	}
	fact, ok := facts[canonical]
	return fact, ok
}
