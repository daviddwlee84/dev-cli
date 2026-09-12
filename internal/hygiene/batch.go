package hygiene

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

type SetupEntry struct {
	Root   string `json:"root"`
	RepoID string `json:"repo_id,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Plan   *Plan  `json:"plan,omitempty"`
}
type setupOperation struct {
	options Options
	service *Service
	planID  string
	index   int
}
type SetupBatch struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	Entries       []SetupEntry `json:"entries"`
	ops           []setupOperation
	fingerprint   string
}
type BatchOutcome struct {
	Root   string `json:"root"`
	PlanID string `json:"plan_id,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Result *Plan  `json:"result,omitempty"`
}
type BatchReceipt struct {
	SchemaVersion int            `json:"schema_version"`
	Kind          string         `json:"kind"`
	ID            string         `json:"id"`
	At            time.Time      `json:"at"`
	Outcomes      []BatchOutcome `json:"outcomes"`
	ReceiptPath   string         `json:"receipt_path,omitempty"`
}

// PreviewSetupBatch retains exact per-checkout plans and processes shared Git
// repositories once. A duplicate row never authorizes a second hook mutation.
func PreviewSetupBatch(ctx context.Context, repos []Options, setup SetupOptions) (SetupBatch, error) {
	b := SetupBatch{SchemaVersion: 1, Kind: "hygiene_setup_batch", Entries: []SetupEntry{}}
	if len(repos) == 0 || len(repos) > 256 {
		return b, errors.New("select between 1 and 256 repositories")
	}
	seen := map[string]string{}
	for _, o := range repos {
		if err := ctx.Err(); err != nil {
			return b, err
		}
		e := SetupEntry{Root: o.Root, Status: "blocked"}
		s, err := Open(ctx, o)
		if err == nil {
			e.Root, e.RepoID = s.Root, s.RepoID
			if first, duplicate := seen[s.RepoID]; duplicate {
				e.Status, e.Detail = "duplicate", "shared repository already selected at "+first
				b.Entries = append(b.Entries, e)
				continue
			}
			seen[s.RepoID] = s.Root
			var p Plan
			p, err = s.PreviewSetupOptions(ctx, setup)
			if err == nil {
				e.Status, e.Plan = "prepared", &p
				b.ops = append(b.ops, setupOperation{o, s, p.ID, len(b.Entries)})
			}
		}
		if err != nil {
			e.Detail = err.Error()
		}
		b.Entries = append(b.Entries, e)
	}
	data, _ := json.Marshal(b.Entries)
	b.fingerprint = digest(data)
	return b, nil
}

// ApplySetupBatch executes only explicitly selected immutable child plans.
// Failures are independent; canceled work stops and retains the partial ledger.
func ApplySetupBatch(ctx context.Context, batch SetupBatch, selected []string, options ApplyOptions) (BatchReceipt, error) {
	r := BatchReceipt{SchemaVersion: 1, Kind: "hygiene_batch_result", ID: newID(), At: time.Now().UTC(), Outcomes: []BatchOutcome{}}
	data, _ := json.Marshal(batch.Entries)
	if batch.fingerprint == "" || digest(data) != batch.fingerprint {
		return r, ErrStale
	}
	chosen := map[string]bool{}
	for _, id := range selected {
		if chosen[id] {
			return r, errors.New("duplicate selected setup plan")
		}
		chosen[id] = true
	}
	if len(chosen) == 0 {
		return r, errors.New("select setup plans explicitly")
	}
	for id := range chosen {
		found := false
		for _, op := range batch.ops {
			found = found || id == op.planID
		}
		if !found {
			return r, errors.New("selected setup plan was not previewed")
		}
	}
	owner := batch.ops[0].service
	r.ReceiptPath = filepath.Join(owner.Dir, r.ID+".json")
	for _, entry := range batch.Entries {
		out := BatchOutcome{Root: entry.Root, Status: entry.Status, Detail: entry.Detail}
		if entry.Plan != nil {
			out.PlanID = entry.Plan.ID
			out.Status = "skipped"
			if chosen[out.PlanID] {
				out.Status = "pending"
			}
		}
		r.Outcomes = append(r.Outcomes, out)
	}
	if err := owner.save(ctx, r.ID, r); err != nil {
		return r, err
	}
	var failures []error
	for _, op := range batch.ops {
		if !chosen[op.planID] {
			continue
		}
		out := &r.Outcomes[op.index]
		if ctx.Err() != nil {
			out.Status, out.Detail = "canceled", "batch canceled before this repository"
			failures = append(failures, ctx.Err())
			continue
		}
		s, err := Open(ctx, op.options)
		if err == nil {
			var result Plan
			result, err = s.Apply(ctx, op.planID, options)
			out.Result = &result
		}
		if err != nil {
			out.Status, out.Detail = "failed", err.Error()
			if errors.Is(err, ErrStale) {
				out.Status = "stale"
			}
			if out.Result != nil && (out.Result.Status == "partial" || out.Result.Status == "applying") {
				out.Status = "partial"
			}
			failures = append(failures, errors.New("repository setup requires attention"))
		} else {
			out.Status = "completed"
			status, e := s.Status(ctx)
			if e != nil || !status.Configured || status.Hook != "recognized" {
				out.Status, out.Detail = "unverified", "setup changed files but effective hook verification failed"
				failures = append(failures, errors.New(out.Detail))
			}
		}
		if err := owner.save(context.WithoutCancel(ctx), r.ID, r); err != nil {
			return r, errors.Join(append(failures, err)...)
		}
	}
	if err := owner.save(context.WithoutCancel(ctx), r.ID, r); err != nil {
		failures = append(failures, err)
	}
	return r, errors.Join(failures...)
}
