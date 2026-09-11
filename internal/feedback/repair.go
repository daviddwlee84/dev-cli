package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

type RepairSource struct {
	Path       string `json:"path"`
	CommonDir  string `json:"common_dir"`
	Remote     string `json:"verified_remote"`
	Repository string `json:"repository"`
	Fork       bool   `json:"fork"`
}
type RepairRequest struct{ ReportID, Repository, Base string }
type RepairSnapshot struct {
	Source         RepairSource `json:"source"`
	RootIdentity   string       `json:"root_identity"`
	CommonIdentity string       `json:"common_identity"`
	BaseRef        string       `json:"base_ref"`
	BaseOID        string       `json:"base_oid"`
	Branch         string       `json:"branch"`
	Checkout       string       `json:"checkout"`
	TaskID         string       `json:"task_id"`
	ConfigRevision string       `json:"config_revision"`
}
type RepairBinding struct {
	PlanID           string         `json:"plan_id"`
	Snapshot         RepairSnapshot `json:"snapshot"`
	CheckoutIdentity string         `json:"checkout_identity,omitempty"`
	TaskSaved        bool           `json:"task_saved"`
	Partial          bool           `json:"partial"`
}
type RepairPlan struct {
	SchemaVersion  int             `json:"schema_version"`
	Kind           string          `json:"kind"`
	ID             string          `json:"plan_id,omitempty"`
	ReportID       string          `json:"report_id"`
	ReportRevision string          `json:"report_revision,omitempty"`
	Status         string          `json:"status"`
	Snapshot       *RepairSnapshot `json:"snapshot,omitempty"`
	Candidates     []RepairSource  `json:"candidates,omitempty"`
	Effects        []string        `json:"effects"`
	Next           []string        `json:"next,omitempty"`
}
type RepairResult struct {
	SchemaVersion int            `json:"schema_version"`
	Kind          string         `json:"kind"`
	Status        string         `json:"status"`
	ReportID      string         `json:"report_id"`
	Binding       *RepairBinding `json:"binding,omitempty"`
	Next          []string       `json:"next,omitempty"`
}

// RepairBackend adapts the existing start service. CreateLocked is invoked only
// after fresh observations match the plan under the repository lifecycle lock.
type RepairBackend interface {
	Sources(context.Context, string) ([]RepairSource, error)
	Observe(context.Context, RepairSource, string, string) (RepairSnapshot, error)
	CreateLocked(context.Context, RepairSnapshot) (RepairBinding, error)
	ValidateBinding(context.Context, RepairBinding) error
}

func (s Store) reportRevision(ctx context.Context, id string) (string, error) {
	report, err := s.read(ctx, id, "report.json")
	if err != nil {
		return "", err
	}
	body, err := s.read(ctx, id, "public.md")
	if err != nil {
		return "", err
	}
	private, err := s.read(ctx, id, "context.json")
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal([][]byte{report, body, private})
	return digest(data), nil
}
func repairPlanID(p RepairPlan) string { p.ID = ""; data, _ := json.Marshal(p); return digest(data) }
func (s Store) PlanRepair(ctx context.Context, r RepairRequest, backend RepairBackend) (RepairPlan, error) {
	plan := RepairPlan{SchemaVersion: 1, Kind: "feedback_repair_plan", ReportID: r.ReportID, Status: "needs_source", Effects: []string{}}
	report, err := s.Load(ctx, r.ReportID)
	if err != nil {
		return plan, err
	}
	if report.Repair != nil {
		plan.Status = "prepared"
		plan.Snapshot = &report.Repair.Snapshot
		if err = s.validateRepairBinding(ctx, r.ReportID, *report.Repair, backend); err != nil {
			plan.Status = "inspect_retained_workspace"
			return plan, err
		}
		plan.Next = []string{"dev prompt render feedback-fix " + r.ReportID}
		return plan, nil
	}
	sources, err := backend.Sources(ctx, r.Repository)
	if err != nil {
		return plan, err
	}
	plan.Candidates = sources
	if len(sources) == 0 {
		plan.Next = []string{"After approving acquisition, use: dev try dev-cli-feedback --clone https://github.com/daviddwlee84/dev-cli.git", "Then preview repair again with --repo <retained-checkout> --base <ref>."}
		return plan, nil
	}
	if len(sources) > 1 {
		plan.Status = "choose_source"
		plan.Next = []string{"Repeat with --repo <exact-path> and --base <ref>."}
		return plan, nil
	}
	if r.Base == "" {
		plan.Status = "choose_base"
		plan.Next = []string{"Pass an explicit --base <ref>; the current branch is never inferred."}
		return plan, nil
	}
	snapshot, err := backend.Observe(ctx, sources[0], r.Base, "fix/feedback-"+r.ReportID)
	if err != nil {
		return plan, err
	}
	plan.Snapshot = &snapshot
	plan.Status = "planned"
	plan.ReportRevision, err = s.reportRevision(ctx, r.ReportID)
	if err != nil {
		return plan, err
	}
	plan.Effects = []string{"Create an isolated branch/worktree from the displayed commit", "Record a HOT worktree task with the feedback report as its next action", "Keep the source checkout's branch and dirty files; skip runtime, provisioning, submodule downloads and agent launch"}
	plan.ID = repairPlanID(plan)
	err = s.withLock(ctx, r.ReportID, func() error {
		current, e := s.reportRevision(ctx, r.ReportID)
		if e != nil {
			return e
		}
		if current != plan.ReportRevision {
			return ErrStale
		}
		existing, e := s.read(ctx, r.ReportID, "plan-"+plan.ID+".json")
		if e == nil {
			var old RepairPlan
			if json.Unmarshal(existing, &old) == nil && repairPlanID(old) == plan.ID {
				return nil
			}
			return ErrStale
		}
		return s.writeJSON(ctx, r.ReportID, "plan-"+plan.ID+".json", plan, false)
	})
	return plan, err
}
func (s Store) ApplyRepair(ctx context.Context, id, planID string, backend RepairBackend) (RepairResult, error) {
	result := RepairResult{SchemaVersion: 1, Kind: "feedback_repair_result", ReportID: id, Status: "not_applied"}
	if len(planID) != 64 || !safeID.MatchString(planID) {
		return result, errors.New("provide the exact plan ID from a repair preview")
	}
	err := s.withLock(ctx, id, func() error {
		raw, err := s.read(ctx, id, "plan-"+planID+".json")
		if err != nil {
			return err
		}
		var plan RepairPlan
		if json.Unmarshal(raw, &plan) != nil || plan.SchemaVersion != 1 || plan.Kind != "feedback_repair_plan" || plan.ID != planID || plan.ReportID != id || plan.Snapshot == nil || plan.Status != "planned" || repairPlanID(plan) != planID {
			return ErrStale
		}
		report, err := s.Load(ctx, id)
		if err != nil {
			return err
		}
		if report.Repair != nil {
			if report.Repair.PlanID == plan.ID && reflect.DeepEqual(report.Repair.Snapshot, *plan.Snapshot) && s.validateRepairBinding(ctx, id, *report.Repair, backend) == nil {
				result.Status = "prepared"
				result.Binding = report.Repair
				return nil
			}
			return ErrStale
		}
		revision, err := s.reportRevision(ctx, id)
		if err != nil {
			return err
		}
		if revision != plan.ReportRevision {
			return ErrStale
		}
		return gitx.WithLifecycleLock(ctx, plan.Snapshot.Source.CommonDir, func() error {
			fresh, err := backend.Observe(ctx, plan.Snapshot.Source, plan.Snapshot.BaseRef, plan.Snapshot.Branch)
			if err != nil {
				return fmt.Errorf("repair authority is no longer available: %w", ErrStale)
			}
			if !reflect.DeepEqual(fresh, *plan.Snapshot) {
				return ErrStale
			}
			currentRevision, err := s.reportRevision(ctx, id)
			if err != nil {
				return err
			}
			if currentRevision != plan.ReportRevision {
				return ErrStale
			}
			binding, createErr := backend.CreateLocked(ctx, fresh)
			binding.PlanID = plan.ID
			if binding.Snapshot.Checkout != "" {
				result.Binding = &binding
				result.Status = "partial"
				result.Next = []string{"Inspect the retained checkout and task record before retrying."}
				report.Repair = &binding
				if createErr == nil {
					result.Status = "prepared"
					result.Next = []string{"dev prompt render feedback-fix " + id}
				}
				saveErr := s.writeJSON(context.WithoutCancel(ctx), id, "report.json", report, true)
				return errors.Join(createErr, saveErr)
			}
			return createErr
		})
	})
	if errors.Is(err, ErrStale) {
		result.Status = "stale_plan"
	}
	return result, err
}
func (s Store) RepairContext(ctx context.Context, id string, backend RepairBackend) (Report, string, error) {
	report, err := s.Load(ctx, id)
	if err != nil {
		return report, "", err
	}
	if report.Repair == nil {
		return report, "", errors.New("prepare a repair workspace before rendering the fix prompt")
	}
	if err = s.validateRepairBinding(ctx, id, *report.Repair, backend); err != nil {
		return report, "", err
	}
	body, err := s.read(ctx, id, "public.md")
	return report, string(body), err
}

// SavedRepairPlan exposes only an exact stored plan for the confirmation UI.
func (s Store) SavedRepairPlan(ctx context.Context, id, planID string) (RepairPlan, error) {
	var plan RepairPlan
	if len(planID) != 64 || !safeID.MatchString(planID) {
		return plan, errors.New("invalid repair plan ID")
	}
	data, err := s.read(ctx, id, "plan-"+planID+".json")
	if err != nil {
		return plan, err
	}
	if json.Unmarshal(data, &plan) != nil || plan.SchemaVersion != 1 || plan.Kind != "feedback_repair_plan" || plan.ReportID != id || plan.ID != planID || repairPlanID(plan) != planID || plan.Snapshot == nil {
		return plan, ErrStale
	}
	return plan, nil
}

func (s Store) validateRepairBinding(ctx context.Context, id string, binding RepairBinding, backend RepairBackend) error {
	plan, err := s.SavedRepairPlan(ctx, id, binding.PlanID)
	if err != nil {
		return err
	}
	if binding.Snapshot.Branch != "fix/feedback-"+id || !reflect.DeepEqual(binding.Snapshot, *plan.Snapshot) {
		return ErrStale
	}
	return backend.ValidateBinding(ctx, binding)
}
