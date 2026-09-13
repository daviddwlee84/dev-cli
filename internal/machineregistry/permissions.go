package machineregistry

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// PathError describes metadata only. It preserves ErrUnsafePath for existing
// callers while giving interactive adapters an exact, actionable diagnosis.
type PathError struct {
	Path          string      `json:"path"`
	Reason        string      `json:"reason"`
	Owner         string      `json:"owner"`
	ExpectedOwner string      `json:"expected_owner"`
	Mode          fs.FileMode `json:"mode"`
	ExpectedMode  fs.FileMode `json:"expected_mode,omitempty"`
	Repairable    bool        `json:"repairable"`
}

func (e *PathError) Error() string {
	expected := e.ExpectedOwner
	if e.ExpectedMode != 0 {
		expected += fmt.Sprintf("; mode %04o", e.ExpectedMode.Perm())
	}
	return fmt.Sprintf("registry %s: %s (owner %s; mode %04o; expected %s): %v", e.Path, e.Reason, e.Owner, e.Mode.Perm(), expected, ErrUnsafePath)
}
func (e *PathError) Unwrap() error { return ErrUnsafePath }

type PermissionChange struct {
	Path       string      `json:"path"`
	BeforeMode fs.FileMode `json:"before_mode"`
	AfterMode  fs.FileMode `json:"after_mode"`
}
type PermissionPlan struct {
	Changes     []PermissionChange `json:"changes"`
	Diagnostics []*PathError       `json:"diagnostics,omitempty"`
	state       *permissionState
}

func (p PermissionPlan) Ready() bool {
	return p.state != nil && !p.state.blocked && len(p.Diagnostics) == 0 && p.matchesPreview()
}
func (p PermissionPlan) NeedsRepair() bool { return p.Ready() && len(p.state.changes) > 0 }
func (p PermissionPlan) matchesPreview() bool {
	if p.state == nil || !slices.Equal(p.Changes, p.state.changes) || len(p.Diagnostics) != len(p.state.diagnostics) {
		return false
	}
	for index, diagnostic := range p.Diagnostics {
		if diagnostic == nil || *diagnostic != p.state.diagnostics[index] {
			return false
		}
	}
	return true
}

type PermissionOutcome struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}
type PermissionResult struct {
	Status   string              `json:"status"`
	Outcomes []PermissionOutcome `json:"outcomes"`
}
type permissionEntry struct {
	path  string
	info  fs.FileInfo
	after fs.FileMode
}
type permissionState struct {
	path        string
	entries     []permissionEntry
	changes     []PermissionChange
	blocked     bool
	diagnostics []PathError
}

// PlanPermissions never creates a missing registry, opens its database contents,
// changes ownership, or follows user-controlled symlinks.
func (s *Store) PlanPermissions(ctx context.Context) (PermissionPlan, error) {
	if err := s.validatePermissionInput(ctx); err != nil {
		return PermissionPlan{}, err
	}
	plan, err := planPermissions(s.Path)
	if err != nil {
		return PermissionPlan{}, err
	}
	if plan.state != nil {
		plan.state.diagnostics = make([]PathError, len(plan.Diagnostics))
		for index, diagnostic := range plan.Diagnostics {
			if diagnostic == nil {
				return PermissionPlan{}, ErrInvalidPlan
			}
			plan.state.diagnostics[index] = *diagnostic
		}
	}
	return plan, nil
}
func (s *Store) ApplyPermissions(ctx context.Context, plan PermissionPlan) (PermissionResult, error) {
	if err := s.validatePermissionInput(ctx); err != nil {
		return PermissionResult{}, err
	}
	if !plan.Ready() || plan.state.path != s.Path {
		return PermissionResult{}, ErrInvalidPlan
	}
	return applyPermissions(ctx, plan.state)
}

func (s *Store) validatePermissionInput(ctx context.Context) error {
	if s == nil || ctx == nil || !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path || strings.IndexByte(s.Path, 0) >= 0 {
		return ErrInvalidPlan
	}
	return ctx.Err()
}
