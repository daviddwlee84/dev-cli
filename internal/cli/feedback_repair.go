package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/feedback"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type feedbackStartBackend struct{ app *App }

func (b feedbackStartBackend) Sources(ctx context.Context, explicit string) ([]feedback.RepairSource, error) {
	return feedback.Sources(ctx, b.app.Cfg, explicit, b.app.Cfg.Feedback.SourceRepo)
}
func (b feedbackStartBackend) freshApp() (*App, error) {
	cfg, err := config.Load(b.app.configPath)
	if err != nil {
		return nil, errors.New("repair configuration is unavailable")
	}
	app := *b.app
	app.Cfg = cfg
	app.Tasks = task.NewStore(cfg.TasksDir())
	return &app, nil
}
func feedbackConfigRevision(cfg config.Config) string {
	data, _ := json.Marshal(struct {
		Paths         config.Paths
		Worktree      config.Worktree
		TaskDirectory string
	}{cfg.Paths, cfg.Worktree, cfg.TasksDir()})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (b feedbackStartBackend) Observe(ctx context.Context, source feedback.RepairSource, base, branch string) (feedback.RepairSnapshot, error) {
	snapshot := feedback.RepairSnapshot{}
	if base == "" || strings.HasPrefix(base, "-") || strings.ContainsFunc(base, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return snapshot, errors.New("provide an explicit valid base ref")
	}
	verified, err := feedback.VerifySource(ctx, source.Path)
	if err != nil {
		return snapshot, err
	}
	if verified != source {
		return snapshot, feedback.ErrStale
	}
	if gitx.BranchExists(ctx, source.Path, branch) {
		return snapshot, errors.New("repair branch already exists; inspect the retained work before continuing")
	}
	oid, err := gitx.Run(ctx, source.Path, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return snapshot, errors.New("repair base ref is unavailable")
	}
	freshApp, err := b.freshApp()
	if err != nil {
		return snapshot, err
	}
	r, err := resolveStartRepository(ctx, freshApp, source.Path)
	if err != nil {
		return snapshot, err
	}
	exists, err := freshApp.Tasks.RecordExists(task.MakeID(r.Name, branch))
	if err != nil || exists {
		return snapshot, errors.New("repair task identity is already present or unavailable")
	}
	spec, err := buildStartSpecForRepository(ctx, freshApp, r, startRequest{Name: "Repair dev feedback " + strings.TrimPrefix(branch, "fix/feedback-"), Branch: branch, Base: base, Mode: task.ModeWorktree, NoProvision: true, NoRuntime: true, Submodules: "none"})
	if err != nil {
		return snapshot, err
	}
	spec.WorktreePath, err = pathx.Canonical(spec.WorktreePath)
	if err != nil {
		return snapshot, err
	}
	if _, err = os.Lstat(spec.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		return snapshot, errors.New("repair checkout path is already present or cannot be inspected")
	}
	rootIdentity, err := gitx.DirectoryIdentity(source.Path)
	if err != nil {
		return snapshot, err
	}
	commonIdentity, err := gitx.DirectoryIdentity(source.CommonDir)
	if err != nil {
		return snapshot, err
	}
	snapshot = feedback.RepairSnapshot{Source: source, RootIdentity: rootIdentity, CommonIdentity: commonIdentity, BaseRef: base, BaseOID: strings.TrimSpace(oid), Branch: branch, Checkout: spec.WorktreePath, TaskID: task.MakeID(r.Name, branch), ConfigRevision: feedbackConfigRevision(freshApp.Cfg)}
	return snapshot, nil
}
func (b feedbackStartBackend) CreateLocked(ctx context.Context, snapshot feedback.RepairSnapshot) (feedback.RepairBinding, error) {
	binding := feedback.RepairBinding{}
	app, err := b.freshApp()
	if err != nil {
		return binding, err
	}
	if feedbackConfigRevision(app.Cfg) != snapshot.ConfigRevision {
		return binding, feedback.ErrStale
	}
	r, err := resolveStartRepository(ctx, app, snapshot.Source.Path)
	if err != nil {
		return binding, err
	}
	reportID := strings.TrimPrefix(snapshot.Branch, "fix/feedback-")
	spec, err := buildStartSpecForRepository(ctx, app, r, startRequest{Name: "Repair dev feedback " + reportID, Branch: snapshot.Branch, Base: snapshot.BaseRef, Mode: task.ModeWorktree, NoProvision: true, NoRuntime: true, Submodules: "none", Next: "Review feedback " + reportID + "; dev prompt render feedback-fix " + reportID})
	if err != nil {
		return binding, err
	}
	spec.WorktreePath, err = pathx.Canonical(spec.WorktreePath)
	if err != nil {
		return binding, err
	}
	if spec.WorktreePath != snapshot.Checkout {
		return binding, feedback.ErrStale
	}
	spec.BaseOID = snapshot.BaseOID
	spec.RequireNewTask = true
	result, createErr := executeStartSpecLocked(ctx, app, spec, io.Discard)
	if result == nil || result.Worktree == nil || result.Worktree.Path == "" {
		return binding, createErr
	}
	identity, identityErr := gitx.DirectoryIdentity(result.Worktree.Path)
	binding = feedback.RepairBinding{Snapshot: snapshot, CheckoutIdentity: identity, TaskSaved: result.TaskSaved, Partial: createErr != nil || identityErr != nil}
	return binding, errors.Join(createErr, identityErr)
}
func (b feedbackStartBackend) ValidateBinding(ctx context.Context, binding feedback.RepairBinding) error {
	if binding.Partial || !binding.TaskSaved || binding.CheckoutIdentity == "" {
		return errors.New("repair is partial; inspect the retained worktree and task record")
	}
	snapshot := binding.Snapshot
	source, err := feedback.VerifySource(ctx, snapshot.Source.Path)
	if err != nil || source != snapshot.Source {
		return feedback.ErrStale
	}
	identity, err := gitx.DirectoryIdentity(source.CommonDir)
	if err != nil || identity != snapshot.CommonIdentity {
		return feedback.ErrStale
	}
	identity, err = gitx.DirectoryIdentity(source.Path)
	if err != nil || identity != snapshot.RootIdentity {
		return feedback.ErrStale
	}
	identity, err = gitx.DirectoryIdentity(snapshot.Checkout)
	if err != nil || identity != binding.CheckoutIdentity {
		return feedback.ErrStale
	}
	registered, err := gitx.ResolveRegisteredWorktree(ctx, source.Path, snapshot.Checkout)
	if err != nil {
		return feedback.ErrStale
	}
	if !registered.IsLinkedWorktree() || registered.Worktree.Locked || registered.Worktree.Prunable || registered.Worktree.Branch != snapshot.Branch {
		return feedback.ErrStale
	}
	r, err := gitx.Discover(ctx, snapshot.Checkout)
	if err != nil || r.Root != snapshot.Checkout || r.GitCommonDir != source.CommonDir {
		return feedback.ErrStale
	}
	branch, err := gitx.Run(ctx, snapshot.Checkout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != snapshot.Branch {
		return feedback.ErrStale
	}
	app, err := b.freshApp()
	if err != nil {
		return err
	}
	record, err := app.Tasks.Get(snapshot.TaskID)
	if err != nil || record.Mode != task.ModeWorktree || record.State != task.Hot || record.Owner != config.Hostname() || record.RepoPath != snapshot.Source.Path || record.WorktreePath != snapshot.Checkout || record.Branch != snapshot.Branch {
		return errors.New("repair task binding changed; inspect or resume the task explicitly")
	}
	return nil
}
