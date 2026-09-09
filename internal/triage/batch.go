package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
	"github.com/google/uuid"
)

type Preview struct {
	ItemID  string           `json:"item_id"`
	Path    string           `json:"path"`
	Action  string           `json:"action"`
	PlanID  string           `json:"plan_id,omitempty"`
	Ready   bool             `json:"ready"`
	Reasons []string         `json:"reasons"`
	Effects []string         `json:"effects"`
	Discard []gitx.LocalPath `json:"discard,omitempty"`
}
type batchEntry struct {
	tryApply func(context.Context) (experiment.RemovalResult, error)
	preview  Preview
	item     Item
	plan     taskflow.Plan
	service  *taskflow.Service
}
type Batch struct {
	ID      string
	Token   string
	entries []batchEntry
	seal    string
}

func (b Batch) Previews() []Preview {
	out := []Preview{}
	for _, e := range b.entries {
		p := e.preview
		p.Reasons = append([]string{}, p.Reasons...)
		p.Effects = append([]string{}, p.Effects...)
		p.Discard = append([]gitx.LocalPath{}, p.Discard...)
		out = append(out, p)
	}
	return out
}
func (b Batch) ReadyCount() int {
	n := 0
	for _, e := range b.entries {
		if e.preview.Ready {
			n++
		}
	}
	return n
}
func (b Batch) fingerprint() string { return digest([]any{b.ID, b.Token, b.Previews()}) }

func (s *Service) Prepare(ctx context.Context, items []Item, action string) (Batch, error) {
	b := Batch{ID: uuid.NewString()}
	seen := map[string]bool{}
	destructive := 0
	for _, item := range items {
		matched := false
		for _, a := range item.Actions {
			if a.Name != action {
				continue
			}
			matched = true
			key := item.RepositoryID + ":" + item.ID + ":" + a.Name + ":" + a.Remote
			if a.Name == "fetch" {
				key = item.RepositoryID + ":" + a.Remote
			}
			if (a.Name == "push" || a.Name == "fast-forward") && item.Branch != nil {
				key = item.RepositoryID + ":" + item.Branch.Ref + ":" + a.Name + ":" + a.Remote
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			e := batchEntry{item: item, preview: Preview{ItemID: item.ID, Path: item.Path, Action: a.Name, Reasons: []string{}, Effects: []string{}}}
			if a.Availability == "blocked" {
				e.preview.Reasons = append(e.preview.Reasons, a.Reason)
				b.entries = append(b.entries, e)
				continue
			}
			if a.Name == "trash-try" || a.Name == "forget-try" {
				e = s.prepareTry(ctx, item, a.Name)
				if e.preview.Ready {
					destructive++
				}
				b.entries = append(b.entries, e)
				continue
			}
			var req taskflow.Request
			var err error
			if a.Name == "fetch" || a.Name == "push" || a.Name == "fast-forward" {
				e.service, err = taskflow.NewSyncService(taskflow.SyncConfig{Tasks: s.cfg.Tasks, Host: s.cfg.Host, Runtimes: func() []runtime.Runtime { return append([]runtime.Runtime{}, s.cfg.Runtimes...) }})
				l := taskflow.Locator{RepoKey: item.RepositoryID, RepositoryID: item.RepositoryID, GitCommonDir: item.RepositoryID, RepoPath: item.RepositoryPath, CheckoutPath: item.Path, RowKey: item.ID, RowKind: item.Scope, Remote: a.Remote}
				op := taskflow.FetchRepository
				if item.Branch != nil {
					l.Branch = strings.TrimPrefix(item.Branch.Ref, "refs/heads/")
					l.HeadOID = item.Branch.OID
					l.Upstream = item.Branch.Upstream
				}
				if a.Name == "push" {
					op = taskflow.PushBranch
				}
				if a.Name == "fast-forward" {
					op = taskflow.FastForwardBranch
				}
				if err == nil {
					req, err = taskflow.NewRequest(l, taskflow.SyncOptions{Operation: op})
				}
			} else {
				var guard func(context.Context) error
				guard, err = s.cleanupGuard(ctx, item, a.Name)
				if err == nil {
					e.service, err = taskflow.NewProtectedLifecycleService(s.cfg.Lifecycle, guard, func(ctx context.Context, path string) error {
						if path != item.Path {
							return errors.New("cleanup target changed")
						}
						return guard(ctx)
					})
				}
				if err == nil {
					var l taskflow.Locator
					var options taskflow.ActionOptions
					switch a.Name {
					case "park-warm":
						options = taskflow.ParkWarmOptions{}
					case "park-cold":
						options = taskflow.ParkColdOptions{}
					case "retire":
						options = taskflow.RetireOptions{}
					case "remove-checkout":
						options = taskflow.RemoveCheckoutOptions{}
					default:
						err = errors.New("unsupported action")
					}
					if len(item.Tasks) == 1 {
						l, err = e.service.LocateTask(ctx, item.Tasks[0].ID)
						if err == nil && l.TaskRevision != item.Tasks[0].Revision() {
							err = errors.New("task changed after inventory")
						}
					} else if item.Worktree != nil {
						l = taskflow.Locator{RepoKey: item.RepositoryID, RowKey: item.Path, RowKind: "unmanaged", RepositoryID: item.RepositoryID, GitCommonDir: item.RepositoryID, RepoPath: item.RepositoryPath, CheckoutPath: item.Path, Branch: item.Worktree.Branch, HeadOID: item.Worktree.Head}
					}
					if err == nil {
						req, err = taskflow.NewRequest(l, options)
					}
				}
			}
			if err == nil {
				e.plan, err = e.service.Plan(ctx, req)
			}
			if err != nil {
				e.preview.Reasons = append(e.preview.Reasons, SafeText(err.Error()))
			} else {
				e.preview.PlanID = e.plan.PlanID
				e.preview.Ready = e.plan.Availability == taskflow.AvailabilityReady
				for _, c := range e.plan.Conditions() {
					if c.Verdict != taskflow.VerdictMet {
						e.preview.Reasons = append(e.preview.Reasons, c.Evidence+"; "+c.Remediation)
					}
				}
				for _, effect := range e.plan.Effects() {
					e.preview.Effects = append(e.preview.Effects, effect.Description+" · "+effect.Target)
					if effect.Code == taskflow.EffectRemoveWorktree {
						destructive++
						e.preview.Discard = append([]gitx.LocalPath{}, item.Ignored...)
					}
				}
			}
			b.entries = append(b.entries, e)
		}
		if !matched {
			b.entries = append(b.entries, batchEntry{item: item, preview: Preview{ItemID: item.ID, Path: item.Path, Action: action, Reasons: []string{"action does not apply to this item; choose an action from its available actions"}, Effects: []string{}}})
		}
	}
	if len(b.entries) == 0 {
		return b, errors.New("selected items have no matching action")
	}
	if destructive > 0 {
		b.Token = fmt.Sprintf("CLEAN %d", destructive)
		if action == "trash-try" {
			b.Token = fmt.Sprintf("TRASH %d", destructive)
		}
		if action == "forget-try" {
			b.Token = fmt.Sprintf("FORGET %d", destructive)
		}
	}
	b.seal = b.fingerprint()
	return b, nil
}

func (s *Service) cleanupGuard(ctx context.Context, item Item, action string) (func(context.Context) error, error) {
	if !item.Complete {
		return nil, errors.New("incomplete inventory cannot authorize cleanup")
	}
	if item.Kind == "try" {
		return nil, errors.New("use the individual Try lifecycle")
	}
	removes := action == "park-cold" || action == "remove-checkout" || action == "retire"
	identity, err := Identity(item.RepositoryID)
	if err != nil {
		return nil, err
	}
	pathIdentity := ""
	if item.Scope == "checkout" {
		pathIdentity, err = Identity(item.Path)
		if err != nil {
			return nil, err
		}
	}
	catalogState, err := s.cleanupCatalog(item)
	if err != nil {
		return nil, err
	}
	guard := func(ctx context.Context) error {
		currentCatalog, e := s.cleanupCatalog(item)
		if e != nil {
			return e
		}
		if currentCatalog != catalogState {
			return fmt.Errorf("%w: catalog identity changed", taskflow.ErrStalePlan)
		}

		actual, e := Identity(item.RepositoryID)
		if e != nil || actual != identity {
			return errors.New("repository directory identity changed")
		}
		prefs, e := s.Store.Read(item.RepositoryID)
		if e != nil {
			return e
		}
		if digest(prefs) != item.PreferenceFingerprint {
			return errors.New("triage preferences changed; preview again")
		}
		if item.Scope != "checkout" {
			return nil
		}
		actual, e = Identity(item.Path)
		if e != nil || actual != pathIdentity {
			return errors.New("checkout directory identity changed")
		}
		if removes {
			contents, e := gitx.InspectTriageContents(ctx, item.Path, prefs.DisposableDirs)
			if e != nil {
				return e
			}
			if contents.Fingerprint != item.ContentsFingerprint {
				return errors.New("checkout content changed; preview again")
			}
			if contents.Status.Dirty() || len(contents.Nested) > 0 {
				return errors.New("checkout has unpreserved work or nested repositories")
			}
			for _, p := range contents.Ignored {
				if !p.Disposable {
					return fmt.Errorf("ignored path requires preservation: %s", SafeText(p.Path))
				}
			}
			gitlinks, e := gitx.Run(ctx, item.Path, "ls-files", "--stage")
			if e != nil {
				return e
			}
			for _, line := range strings.Split(gitlinks, "\n") {
				if strings.HasPrefix(line, "160000 ") {
					return errors.New("submodules require individual cleanup")
				}
			}
		}
		if len(s.cfg.Runtimes) == 0 {
			return errors.New("runtime coverage unavailable")
		}
		for _, rt := range s.cfg.Runtimes {
			occ, e := runtime.InspectOccupancy(ctx, rt, item.Path, runtime.OccupancyOptions{InspectProcesses: true})
			if e != nil || !occ.SessionList.Observed() || occ.SessionCoverageErr != nil || occ.AgentActivityList.Err != nil {
				return errors.New("runtime coverage changed or is unavailable")
			}
			if len(occ.Agents) > 0 {
				return errors.New("recognized agent occupies checkout")
			}
			// A lifecycle can close only its own backend. Other covering sessions
			// must be handled separately instead of disappearing from authority.
			chosen := s.cfg.Lifecycle.DefaultRuntime()
			if len(item.Tasks) == 1 && item.Tasks[0].RuntimeName != "" {
				chosen = s.cfg.Lifecycle.NamedRuntime(item.Tasks[0].RuntimeName)
			}
			if chosen == nil || (rt.Name() != chosen.Name() && len(occ.Sessions) > 0) {
				return errors.New("another runtime covers checkout; handle it individually")
			}
		}
		return nil
	}
	return guard, guard(ctx)
}

type Outcome struct {
	CatalogID       string                `json:"catalog_id,omitempty"`
	OperationRecord string                `json:"operation_record,omitempty"`
	ItemID          string                `json:"item_id"`
	Path            string                `json:"path"`
	Action          string                `json:"action"`
	Status          string                `json:"status"`
	Error           string                `json:"error,omitempty"`
	Steps           []taskflow.StepResult `json:"steps,omitempty"`
}
type Ledger struct {
	SchemaVersion int       `json:"schema_version"`
	BatchID       string    `json:"batch_id"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished,omitempty"`
	Outcomes      []Outcome `json:"outcomes"`
	Path          string    `json:"-"`
}

func (s *Service) Apply(ctx context.Context, b Batch, approval, token string, notify func(Outcome)) (Ledger, error) {
	l := Ledger{SchemaVersion: 1, BatchID: b.ID, Started: time.Now().UTC(), Outcomes: []Outcome{}}
	if approval != b.ID || token != b.Token || b.seal != b.fingerprint() {
		return l, errors.New("batch approval does not match reviewed plan")
	}
	dir := filepath.Join(s.Store.Dir, "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return l, err
	}
	l.Path = filepath.Join(dir, b.ID+".json")
	save := func() error {
		data, e := json.MarshalIndent(l, "", "  ")
		if e != nil {
			return e
		}
		f, e := os.CreateTemp(dir, ".ledger-*")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		if _, e = f.Write(data); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		return os.Rename(f.Name(), l.Path)
	}
	if err := save(); err != nil {
		return l, err
	}
	for _, e := range b.entries {
		o := Outcome{ItemID: e.item.ID, Path: e.item.Path, Action: e.preview.Action, Status: "skipped"}
		if !e.preview.Ready {
			o.Error = strings.Join(e.preview.Reasons, "; ")
		} else if ctx.Err() != nil {
			o.Status = "canceled"
		} else {
			o.Status = "running"
			l.Outcomes = append(l.Outcomes, o)
			if err := save(); err != nil {
				return l, err
			}
			l.Outcomes = l.Outcomes[:len(l.Outcomes)-1]
			if e.tryApply != nil {
				result, err := e.tryApply(context.WithoutCancel(ctx))
				o.CatalogID, o.OperationRecord = result.ID, result.Journal
				o.Status = "completed"
				if err != nil {
					o.Status, o.Error = "failed", SafeText(err.Error())
					if result.Outcome != "" && result.Outcome != "not-applied" {
						o.Status = "partial"
					}
				}
			} else {
				approval := taskflow.Approve(e.plan.PlanID)
				if e.plan.Confirmation.Kind == taskflow.ConfirmationTyped {
					approval = taskflow.ApproveWithToken(e.plan.PlanID, e.plan.Confirmation.Token)
				}
				result, err := e.service.Apply(context.WithoutCancel(ctx), e.plan, approval)
				o.Steps = result.AttemptedSteps()
				o.Status = "completed"
				if err != nil {
					o.Status = "failed"
					o.Error = SafeText(err.Error())
					if errors.Is(err, taskflow.ErrStalePlan) {
						o.Status = "stale"
					}
					if len(result.CompletedSteps()) > 0 {
						o.Status = "partial"
					}
				}
			}
		}
		l.Outcomes = append(l.Outcomes, o)
		if err := save(); err != nil {
			return l, err
		}
		if notify != nil {
			notify(o)
		}
	}
	l.Finished = time.Now().UTC()
	return l, save()
}

func (s *Service) cleanupCatalog(item Item) (string, error) {
	entries, diagnostics, err := s.cfg.Catalog.ListWithDiagnostics()
	if err != nil || len(diagnostics) > 0 {
		return "", errors.New("catalog inventory is incomplete")
	}
	related := []*catalog.Entry{}
	for _, entry := range entries {
		location, ok := entry.LocationFor(s.cfg.Host)
		if !ok {
			continue
		}
		p, _ := pathx.Canonical(location.CurrentPath)
		common, _ := pathx.Canonical(location.GitCommonDir)
		if p != item.Path && p != item.RepositoryPath && common != item.RepositoryID {
			continue
		}
		related = append(related, entry)
		if entry.MoveIntent != nil {
			return "", errors.New("catalog move is pending")
		}
		if p == item.Path && entry.Kind == catalog.KindTry && (entry.Experiment == nil || entry.Experiment.Phase != catalog.PhaseGraduated) {
			return "", errors.New("Try checkout requires its individual lifecycle")
		}
	}
	return digest(related), nil
}
