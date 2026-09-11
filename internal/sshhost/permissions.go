package sshhost

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// PermissionRequest selects the bounded local metadata inspected before setup.
// KeyPath is optional and stays within ~/.ssh. Its existing public/private
// companion and required parent directories are inspected, never their bytes.
type PermissionRequest struct {
	KeyPath string `json:"key_path,omitempty"`
}

type PermissionChange struct {
	Path       string      `json:"path"`
	Kind       string      `json:"kind"`
	BeforeMode fs.FileMode `json:"before_mode"`
	AfterMode  fs.FileMode `json:"after_mode"`
}

// PermissionPlan describes only exact permission tightening. Its opaque state
// binds the complete observation to one Service and cannot be reconstructed
// from JSON. Blocked plans never authorize partial repairs.
type PermissionPlan struct {
	SchemaVersion int                `json:"schema_version"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`
	Changes       []PermissionChange `json:"changes"`
	Diagnostics   []Diagnostic       `json:"diagnostics,omitempty"`
	state         *permissionPlanState
}

func (p PermissionPlan) Ready() bool {
	return p.state != nil && (p.Status == "ready" || p.Status == "repairable")
}

func (p PermissionPlan) NeedsRepair() bool { return p.Ready() && len(p.Changes) > 0 }

type PermissionOutcome struct {
	Path       string      `json:"path"`
	Status     string      `json:"status"`
	BeforeMode fs.FileMode `json:"before_mode"`
	AfterMode  fs.FileMode `json:"after_mode"`
}

// PermissionResult retains every completed tightening. Cancellation and later
// setup failures never restore a formerly broader mode. Unknown outcomes mean
// chmod may have occurred but its final named identity could not be verified.
type PermissionResult struct {
	SchemaVersion int                 `json:"schema_version"`
	Kind          string              `json:"kind"`
	Status        string              `json:"status"`
	Outcomes      []PermissionOutcome `json:"outcomes"`
}

type permissionTarget struct {
	path      string
	kind      string
	directory bool
	public    bool
	optional  bool
}

type permissionAnchor struct {
	path string
	info fs.FileInfo
}

type permissionSnapshot struct {
	target      permissionTarget
	info        fs.FileInfo
	metadata    permissionMetadata
	anchors     []permissionAnchor
	missingPath string
}

type permissionPlanState struct {
	serviceID uint64
	public    PermissionPlan
	snapshots []permissionSnapshot
	// Test-only seams exercise changes after the initial all-target check and
	// after chmod; production plans leave them nil.
	beforeChange func(int)
	afterChange  func(int)
}

// PlanPermissions is metadata-only. It never invokes a subprocess, reads key
// contents, enumerates other identities, creates directories or takes a lock.
// Existing canonical setup paths are inspected even when ~/.ssh is too broad
// for the ordinary private-tree validators.
func (s *Service) PlanPermissions(ctx context.Context, request PermissionRequest) (PermissionPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	plan := PermissionPlan{SchemaVersion: 1, Kind: "ssh_permission_plan", Status: "ready", Changes: []PermissionChange{}}
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	targets, err := s.permissionTargets(request)
	if err != nil {
		return plan, err
	}
	state := &permissionPlanState{serviceID: s.id}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		snapshot, file, err := s.observePermissionTarget(target)
		if file != nil {
			_ = file.Close()
		}
		if err != nil {
			plan.Status = "blocked"
			plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Code: "permission_manual_remediation", Path: target.path, Message: err.Error(), BlocksMutation: true})
			continue
		}
		state.snapshots = append(state.snapshots, snapshot)
		if snapshot.info == nil {
			continue
		}
		desired, err := permissionDesiredMode(snapshot)
		if err != nil {
			plan.Status = "blocked"
			plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Code: "permission_manual_remediation", Path: target.path, Message: err.Error(), BlocksMutation: true})
			continue
		}
		if desired != snapshot.info.Mode().Perm() {
			plan.Changes = append(plan.Changes, PermissionChange{Path: target.path, Kind: target.kind, BeforeMode: snapshot.info.Mode().Perm(), AfterMode: desired})
		}
	}
	if plan.Status != "blocked" && len(plan.Changes) > 0 {
		plan.Status = "repairable"
	}
	state.public = clonePermissionPlan(plan)
	plan.state = state
	return plan, nil
}

func clonePermissionPlan(plan PermissionPlan) PermissionPlan {
	plan.state = nil
	plan.Changes = append([]PermissionChange{}, plan.Changes...)
	plan.Diagnostics = append([]Diagnostic(nil), plan.Diagnostics...)
	return plan
}

func (s *Service) permissionTargets(request PermissionRequest) ([]permissionTarget, error) {
	targets := []permissionTarget{
		{path: s.paths.SSHDir, kind: "ssh_directory", directory: true, optional: true},
		{path: s.paths.RootConfig, kind: "root_config", optional: true},
		{path: s.paths.ManagedDir, kind: "managed_directory", directory: true, optional: true},
	}
	if request.KeyPath != "" {
		if !validUTF8NoControl(request.KeyPath) {
			return nil, fmt.Errorf("selected key path contains unsupported characters: %w", ErrUnsafePath)
		}
		path, err := s.resolveSSHKeyPath(request.KeyPath)
		if err != nil {
			return nil, err
		}
		for parent, count := filepath.Dir(path), 0; parent != s.paths.SSHDir; parent, count = filepath.Dir(parent), count+1 {
			if count >= maxCatalogDepth || !s.pathWithinSSH(parent) {
				return nil, fmt.Errorf("key parent exceeds the supported SSH tree: %w", ErrUnsafePath)
			}
			targets = append(targets, permissionTarget{path: parent, kind: "key_directory", directory: true})
		}
		if strings.HasSuffix(strings.ToLower(path), ".pub") {
			targets = append(targets, permissionTarget{path: path, kind: "public_key", public: true, optional: true}, permissionTarget{path: path[:len(path)-4], kind: "private_key", optional: true})
		} else {
			targets = append(targets, permissionTarget{path: path, kind: "private_key", optional: true}, permissionTarget{path: path + ".pub", kind: "public_key", public: true, optional: true})
		}
	}
	byPath := map[string]permissionTarget{}
	for _, target := range targets {
		if previous, exists := byPath[target.path]; exists {
			if previous.directory != target.directory || previous.public != target.public {
				return nil, fmt.Errorf("selected key overlaps a canonical setup path: %w", ErrUnsafePath)
			}
			continue
		}
		byPath[target.path] = target
	}
	targets = targets[:0]
	for _, target := range byPath {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		left, right := strings.Count(targets[i].path, string(filepath.Separator)), strings.Count(targets[j].path, string(filepath.Separator))
		if left != right {
			return left < right
		}
		return targets[i].path < targets[j].path
	})
	return targets, nil
}

func (s *Service) observePermissionTarget(target permissionTarget) (permissionSnapshot, *os.File, error) {
	snapshot := permissionSnapshot{target: target}
	if err := validateHomeDirectory(s.paths.Home); err != nil {
		return snapshot, nil, err
	}
	relative, err := filepath.Rel(s.paths.Home, target.path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return snapshot, nil, ErrUnsafePath
	}
	root, held, err := safefile.OpenRoot(s.paths.Home)
	if err != nil {
		return snapshot, nil, err
	}
	defer func() { _ = root.Close() }()
	snapshot.anchors = append(snapshot.anchors, permissionAnchor{path: s.paths.Home, info: held})
	parts := strings.Split(relative, string(filepath.Separator))
	currentPath := s.paths.Home
	for index, name := range parts {
		currentPath = filepath.Join(currentPath, name)
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) && target.optional {
			snapshot.missingPath = currentPath
			if err := verifyPermissionAnchors(snapshot.anchors); err != nil {
				return snapshot, nil, err
			}
			return snapshot, nil, nil
		}
		if err != nil {
			return snapshot, nil, err
		}
		final := index == len(parts)-1
		if info.Mode()&os.ModeSymlink != 0 || !final && !info.IsDir() || final && target.directory != info.IsDir() || final && !target.directory && !info.Mode().IsRegular() {
			return snapshot, nil, fmt.Errorf("permission target or parent is not a direct expected file type: %w", ErrUnsafePath)
		}
		if !final {
			if err := permissionValidateAncestor(currentPath, info); err != nil {
				return snapshot, nil, err
			}
			child, childInfo, err := safefile.OpenChildRoot(root, name)
			if err != nil {
				return snapshot, nil, err
			}
			_ = root.Close()
			root = child
			snapshot.anchors = append(snapshot.anchors, permissionAnchor{path: currentPath, info: childInfo})
			continue
		}
		file, err := permissionOpenFile(root, name, target.directory)
		if err != nil {
			return snapshot, nil, err
		}
		fail := func(err error) (permissionSnapshot, *os.File, error) { _ = file.Close(); return snapshot, nil, err }
		opened, err := file.Stat()
		if err != nil {
			return fail(err)
		}
		if !stableFileInfo(info, opened) {
			return fail(ErrSourceChanged)
		}
		metadata, err := permissionReadMetadata(target.path, file, opened, target)
		if err != nil {
			return fail(err)
		}
		after, err := file.Stat()
		if err != nil {
			return fail(err)
		}
		current, err := root.Lstat(name)
		if err != nil {
			return fail(err)
		}
		if !stableFileInfo(opened, after) || !stableFileInfo(after, current) || !permissionSameChangeTime(opened, after) {
			return fail(ErrSourceChanged)
		}
		if err := verifyPermissionAnchors(snapshot.anchors); err != nil {
			return fail(err)
		}
		snapshot.info, snapshot.metadata = after, metadata
		return snapshot, file, nil
	}
	return snapshot, nil, ErrUnsafePath
}

func verifyPermissionAnchors(anchors []permissionAnchor) error {
	for _, anchor := range anchors {
		if err := safefile.VerifyRoot(anchor.path, anchor.info); err != nil {
			return ErrSourceChanged
		}
		current, err := os.Lstat(anchor.path)
		if err != nil {
			return err
		}
		if !permissionSameOwner(anchor.info, current) {
			return ErrSourceChanged
		}
	}
	return nil
}

func samePermissionSnapshot(expected, current permissionSnapshot) bool {
	if expected.missingPath != current.missingPath || (expected.info == nil) != (current.info == nil) {
		return false
	}
	if expected.info == nil {
		return true
	}
	return stableFileInfo(expected.info, current.info) && permissionSameChangeTime(expected.info, current.info) && reflect.DeepEqual(expected.metadata, current.metadata)
}

func (s *Service) checkPermissionSnapshots(expected []permissionSnapshot) error {
	for _, prior := range expected {
		if err := verifyPermissionAnchors(prior.anchors); err != nil {
			return err
		}
		current, file, err := s.observePermissionTarget(prior.target)
		if file != nil {
			_ = file.Close()
		}
		if err != nil || !samePermissionSnapshot(prior, current) {
			return fmt.Errorf("permission source changed at %s: %w", prior.target.path, errors.Join(ErrSourceChanged, err))
		}
	}
	return nil
}

// ApplyPermissions applies only the reviewed exact mode changes. It owns the
// existing HOME-scoped SSH operation lock; callers must not hold it recursively.
// All sources are revalidated before the first chmod and each following step.
func (s *Service) ApplyPermissions(ctx context.Context, plan PermissionPlan) (PermissionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := PermissionResult{SchemaVersion: 1, Kind: "ssh_permission_result", Status: "not_run", Outcomes: []PermissionOutcome{}}
	for _, change := range plan.Changes {
		result.Outcomes = append(result.Outcomes, PermissionOutcome{Path: change.Path, Status: "not_run", BeforeMode: change.BeforeMode, AfterMode: change.AfterMode})
	}
	if !plan.Ready() || plan.state.serviceID != s.id || !reflect.DeepEqual(clonePermissionPlan(plan), plan.state.public) {
		return result, ErrBlocked
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(plan.Changes) == 0 {
		if err := s.checkPermissionSnapshots(plan.state.snapshots); err != nil {
			return result, err
		}
		result.Status = "ready"
		return result, nil
	}
	expected := append([]permissionSnapshot(nil), plan.state.snapshots...)
	err := WithOperationLock(ctx, s.paths, func() error {
		if err := s.checkPermissionSnapshots(expected); err != nil {
			return err
		}
		for index, change := range plan.Changes {
			if plan.state.beforeChange != nil {
				plan.state.beforeChange(index)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.checkPermissionSnapshots(expected); err != nil {
				return err
			}
			var snapshotIndex int
			for i, snapshot := range expected {
				if snapshot.target.path == change.Path {
					snapshotIndex = i
					break
				}
			}
			prior := expected[snapshotIndex]
			current, file, err := s.observePermissionTarget(prior.target)
			if err != nil {
				return err
			}
			if file == nil || !samePermissionSnapshot(prior, current) {
				if file != nil {
					_ = file.Close()
				}
				return ErrSourceChanged
			}
			result.Status = "partial"
			result.Outcomes[index].Status = "unknown"
			err = permissionChmod(file, change.AfterMode)
			if err == nil && plan.state.afterChange != nil {
				plan.state.afterChange(index)
			}
			if err == nil {
				err = permissionSync(file)
			}
			held, statErr := file.Stat()
			closeErr := file.Close()
			if err = errors.Join(err, statErr, closeErr); err != nil {
				return err
			}
			after, verifyFile, err := s.observePermissionTarget(prior.target)
			if verifyFile != nil {
				_ = verifyFile.Close()
			}
			if err != nil || after.info == nil || !os.SameFile(prior.info, after.info) || !stableFileInfo(held, after.info) || after.info.Mode().Perm() != change.AfterMode || !permissionSameNonModeMetadata(prior.metadata, after.metadata) {
				return fmt.Errorf("permission tightening could not be verified at %s: %w", change.Path, errors.Join(ErrSourceChanged, err))
			}
			expected[snapshotIndex] = after
			result.Outcomes[index].Status = "applied"
		}
		if err := s.checkPermissionSnapshots(expected); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result.Status = "applied"
		return nil
	})
	return result, err
}
