package ephemeral

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type systemBackend struct {
	tasks           *task.Store
	artifacts       *artifact.Store
	runtimes        []runtime.Runtime
	runtimeDisabled bool
	cwd             func() (string, error)
}

func newSystemBackend(options ServiceOptions) *systemBackend {
	return &systemBackend{
		tasks: options.Tasks, artifacts: options.Artifacts,
		runtimes: append([]runtime.Runtime(nil), options.Runtimes...), runtimeDisabled: options.RuntimeDisabled, cwd: os.Getwd,
	}
}

func (b *systemBackend) discover(ctx context.Context, path string) (repositoryState, error) {
	repository, err := gitx.Discover(ctx, path)
	if err != nil {
		return repositoryState{}, err
	}
	root, err := pathx.Canonical(repository.Root)
	if err != nil {
		return repositoryState{}, err
	}
	commonDir, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		return repositoryState{}, err
	}
	return repositoryState{
		Identity: RepositoryIdentity{Root: root, CommonDir: commonDir, Name: repository.Name, Bare: repository.Bare},
		Linked:   repository.IsLinkedWorktree,
	}, nil
}

func (b *systemBackend) worktrees(ctx context.Context, repository repositoryState) ([]Target, error) {
	worktrees, err := gitx.Worktrees(ctx, repository.Identity.Root)
	if err != nil {
		return nil, err
	}
	targets := make([]Target, 0, len(worktrees))
	for _, worktree := range worktrees {
		path, err := pathx.Canonical(worktree.Path)
		if err != nil {
			return nil, err
		}
		targets = append(targets, Target{
			Path: path, Branch: worktree.Branch, RegistryHead: worktree.Head,
			Main: worktree.Main, Bare: worktree.Bare, Detached: worktree.Detached,
			Locked: worktree.Locked, LockedReason: worktree.LockedReason,
			Prunable: worktree.Prunable, PrunableReason: worktree.PrunableReason,
			Registered: true, RegistrationKnown: true, Hint: inventory.LooksEphemeralWorktree(path, worktree.Branch),
		})
	}
	return targets, nil
}

func (b *systemBackend) gitFacts(ctx context.Context, repository repositoryState, target Target) (GitFacts, error) {
	facts := structuralGitFacts(target, target.RegistrationKnown)
	facts.NonMain = KnownFact(!target.Main)
	if target.RegistrationKnown {
		facts.Registered = KnownFact(target.Registered)
		facts.LinkedWorktree = KnownFact(target.Registered && !target.Main && !target.Bare)
		facts.BranchNamed = KnownFact(target.Branch != "" && !target.Detached)
		facts.Unlocked = KnownFact(!target.Locked)
		facts.NotPrunable = KnownFact(!target.Prunable)
	}

	info, err := os.Lstat(target.Path)
	if errors.Is(err, fs.ErrNotExist) {
		facts.PathPresent = KnownFact(false)
		return facts, nil
	}
	if err != nil {
		return facts, err
	}
	facts.PathPresent = KnownFact(info.IsDir() && info.Mode()&os.ModeSymlink == 0)
	if !facts.PathPresent.Value {
		return facts, fmt.Errorf("checkout path is not a regular directory")
	}
	checkoutRepo, err := gitx.Discover(ctx, target.Path)
	if err != nil {
		return facts, err
	}
	checkoutCommon, err := pathx.Canonical(checkoutRepo.GitCommonDir)
	if err != nil {
		return facts, err
	}
	facts.CommonDir = checkoutCommon
	// Git exposes a reusable administrative pathname, not a non-replayable
	// registration generation. Leave RegistrationGeneration empty unless a future
	// live registry supplies an opaque generation that a provider also recorded.
	facts.CommonDirMatches = KnownFact(checkoutCommon == repository.Identity.CommonDir)
	liveHead, err := gitx.Run(ctx, target.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return facts, err
	}
	facts.LiveHead = liveHead
	facts.HeadMatches = KnownFact(target.RegistryHead != "" && liveHead == target.RegistryHead)
	inspection, err := gitx.InspectEphemeralCheckout(ctx, target.Path)
	if err != nil {
		return facts, err
	}
	facts.Staged, facts.Unstaged = inspection.Status.Staged, inspection.Status.Unstaged
	facts.Conflicted, facts.Untracked = inspection.Status.Conflicted, inspection.Status.Untracked
	facts.Ignored, facts.DirtySubmodules = inspection.Ignored, inspection.DirtySubmodules
	facts.BranchMatches = KnownFact(inspection.Status.Branch == target.Branch && !inspection.Status.Detached)
	facts.Clean = KnownFact(!inspection.Status.Dirty())
	facts.IgnoredEmpty = KnownFact(inspection.Ignored == 0)
	facts.SubmodulesClean = KnownFact(inspection.DirtySubmodules == 0)
	facts.StateFingerprint = inspection.Fingerprint
	operation, active, err := gitx.InProgress(target.Path)
	if err != nil {
		return facts, err
	}
	facts.NoGitOperation = KnownFact(!active)
	if active {
		facts.Operation = operation
	}
	return facts, nil
}

func (b *systemBackend) taskEvidence(_ context.Context, repository repositoryState, target Target) (taskEvidence, error) {
	if b.tasks == nil {
		return taskEvidence{}, fmt.Errorf("task store unavailable")
	}
	tasks, err := strictTaskList(b.tasks)
	if err != nil {
		return taskEvidence{}, err
	}
	type claim struct {
		ID, Repo, Worktree, Branch, State string
	}
	var claims []claim
	for _, current := range tasks {
		worktree := ""
		if current.WorktreePath != "" {
			worktree, _ = pathx.Canonical(current.WorktreePath)
		}
		repo := ""
		if current.RepoPath != "" {
			repo, _ = pathx.Canonical(current.RepoPath)
		}
		if worktree == target.Path || (repo == repository.Identity.Root && current.Branch != "" && current.Branch == target.Branch) {
			claims = append(claims, claim{current.ID, repo, worktree, current.Branch, string(current.State)})
		}
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].ID < claims[j].ID })
	return taskEvidence{Unclaimed: KnownFact(len(claims) == 0), Claims: len(claims), Fingerprint: digest(claims)}, nil
}

func strictTaskList(store *task.Store) ([]*task.Task, error) {
	entries, err := os.ReadDir(store.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []*task.Task
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".toml") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("task inventory contains an unsafe task entry")
		}
		id := strings.TrimSuffix(name, ".toml")
		current, err := store.Get(id)
		if err != nil {
			return nil, err
		}
		if err := current.Validate(); err != nil {
			return nil, err
		}
		tasks = append(tasks, current)
	}
	return tasks, nil
}

func (b *systemBackend) artifactEvidence(ctx context.Context, target Target) (artifactEvidence, error) {
	if b.artifacts == nil {
		return artifactEvidence{}, fmt.Errorf("artifact store unavailable")
	}
	inspections, err := artifact.InspectWorktrees(ctx, b.artifacts)
	if err != nil {
		return artifactEvidence{}, err
	}
	inspection, exists := inspections[target.Path]
	if !exists {
		return artifactEvidence{Known: KnownFact(true), Safe: KnownFact(true), Fingerprint: digest("no-artifacts")}, nil
	}
	return artifactEvidence{
		Known: KnownFact(true), Safe: KnownFact(inspection.Ready), Intents: inspection.IntentCount,
		Fingerprint: digest(inspection),
	}, nil
}

func (b *systemBackend) callerEvidence(target Target) (callerEvidence, error) {
	cwd, err := b.cwd()
	if err != nil {
		return callerEvidence{}, err
	}
	canonical, err := pathx.Canonical(cwd)
	if err != nil {
		return callerEvidence{}, err
	}
	inside, err := pathx.Contains(target.Path, canonical)
	if err != nil {
		return callerEvidence{}, err
	}
	return callerEvidence{Outside: KnownFact(!inside), Fingerprint: digest(canonical)}, nil
}

func (b *systemBackend) runtimeEvidence(ctx context.Context, target Target) (runtimeEvidence, error) {
	if b.runtimeDisabled {
		return runtimeEvidence{}, fmt.Errorf("runtime inventory disabled")
	}
	type coveringSession struct {
		Runtime string
		Handle  string
		Paths   []string
	}
	seenRuntimes := make(map[string]bool)
	var covering []coveringSession
	for _, rt := range b.runtimes {
		if rt == nil || rt.Name() == "none" || seenRuntimes[rt.Name()] {
			continue
		}
		seenRuntimes[rt.Name()] = true
		if !rt.Available() {
			continue
		}
		sessions, err := rt.List(ctx)
		if err != nil {
			return runtimeEvidence{}, err
		}
		for _, session := range sessions {
			paths := sessionPaths(session)
			if len(paths) == 0 {
				return runtimeEvidence{}, fmt.Errorf("live runtime session has no usable path inventory")
			}
			covers := false
			for _, path := range paths {
				if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
					return runtimeEvidence{}, fmt.Errorf("live runtime session has an unusable path inventory")
				}
				inside, err := pathx.Contains(target.Path, path)
				if err != nil {
					return runtimeEvidence{}, err
				}
				if inside {
					covers = true
				}
			}
			if covers {
				sort.Strings(paths)
				covering = append(covering, coveringSession{Runtime: rt.Name(), Handle: session.Handle, Paths: paths})
			}
		}
	}
	sort.Slice(covering, func(i, j int) bool {
		if covering[i].Runtime != covering[j].Runtime {
			return covering[i].Runtime < covering[j].Runtime
		}
		return covering[i].Handle < covering[j].Handle
	})
	return runtimeEvidence{
		Known: KnownFact(true), Clear: KnownFact(len(covering) == 0), Covering: len(covering), Fingerprint: digest(covering),
	}, nil
}

func sessionPaths(session runtime.Session) []string {
	set := make(map[string]bool)
	for _, path := range session.Dirs {
		if path != "" {
			set[path] = true
		}
	}
	for _, pane := range session.Panes {
		for _, path := range []string{pane.CWD, pane.ShellCWD} {
			if path != "" {
				set[path] = true
			}
		}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	return paths
}

func (b *systemBackend) branchState(ctx context.Context, repository repositoryState, base, branch string) (branchState, error) {
	baseHead, err := gitx.Run(ctx, repository.Identity.Root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return branchState{}, err
	}
	branchTip, err := gitx.Run(ctx, repository.Identity.Root, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return branchState{}, err
	}
	relation, err := gitx.CompareBranches(ctx, repository.Identity.Root, baseHead, branchTip)
	if err != nil {
		return branchState{}, err
	}
	return branchState{BaseHead: baseHead, BranchTip: branchTip, BaseOnly: relation.BaseOnly, BranchOnly: relation.BranchOnly}, nil
}

func (b *systemBackend) withCleanupLock(ctx context.Context, commonDir string, operation func() error) error {
	return lockx.WithDir(ctx, filepath.Join(commonDir, "dev-ephemeral-cleanup-v1"), "ephemeral worktree cleanup", operation)
}

func (b *systemBackend) removeWorktree(ctx context.Context, repository repositoryState, path string) error {
	return gitx.RemoveWorktree(ctx, repository.Identity.Root, path, false)
}

func (b *systemBackend) verifyRemoved(ctx context.Context, repository repositoryState, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("checkout path still exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	worktrees, err := gitx.Worktrees(ctx, repository.Identity.Root)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		candidate, err := pathx.Canonical(worktree.Path)
		if err != nil {
			return err
		}
		if candidate == path {
			return fmt.Errorf("worktree registration still exists")
		}
	}
	return nil
}

func (b *systemBackend) deleteBranch(ctx context.Context, repository repositoryState, branch string) error {
	_, err := gitx.Run(ctx, repository.Identity.Root, "branch", "-d", "--", branch)
	return err
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
