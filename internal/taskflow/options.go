package taskflow

import (
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

// DirtyPolicy is the explicitly selected treatment of checkout changes during
// completion planning.
type DirtyPolicy string

const (
	DirtyAuto    DirtyPolicy = "auto"
	DirtyFail    DirtyPolicy = "fail"
	DirtyCommit  DirtyPolicy = "commit"
	DirtyDiscard DirtyPolicy = "discard"
)

func normalizeDirty(policy DirtyPolicy) DirtyPolicy {
	if policy == "" {
		return DirtyAuto
	}
	return policy
}

func validateDirty(policy DirtyPolicy, message string) error {
	policy = normalizeDirty(policy)
	switch policy {
	case DirtyAuto, DirtyFail, DirtyCommit, DirtyDiscard:
	default:
		return fmt.Errorf("unknown dirty policy %q", policy)
	}
	if message != "" && policy != DirtyAuto && policy != DirtyCommit {
		return fmt.Errorf("commit message requires dirty policy auto or commit")
	}
	return nil
}

// ActionOptions is a closed set of operation-specific values. Implementations
// are value types; Request normalizes pointers to independent values at package
// boundaries.
type ActionOptions interface {
	Action() Action
	isActionOptions()
}

// ParkWarmOptions controls non-destructive parking while retaining checkout
// resources.
type ParkWarmOptions struct {
	Next            string
	Note            string
	CommitWIP       bool
	Push            bool
	KeepSession     bool
	CloseUnknown    bool
	AssumeNoRuntime bool
	Timeout         time.Duration
}

func (ParkWarmOptions) Action() Action   { return ParkWarm }
func (ParkWarmOptions) isActionOptions() {}

// ParkColdOptions controls reconstructible parking and checkout cleanup.
type ParkColdOptions struct {
	Next            string
	Note            string
	CommitWIP       bool
	Push            bool
	CloseUnknown    bool
	AssumeNoRuntime bool
	Timeout         time.Duration
}

func (ParkColdOptions) Action() Action   { return ParkCold }
func (ParkColdOptions) isActionOptions() {}

// ResumeOptions controls optional remote refresh, provisioning, and an explicit
// ownership transfer acknowledgement.
type ResumeOptions struct {
	FetchRefs     bool
	NoProvision   bool
	TakeOwnership bool
}

func (ResumeOptions) Action() Action   { return Resume }
func (ResumeOptions) isActionOptions() {}

// CompleteDirectOptions records completion in the canonical branch without an
// integration operation.
type CompleteDirectOptions struct {
	Dirty         DirtyPolicy
	CommitMessage string
	Push          bool
}

func (CompleteDirectOptions) Action() Action   { return CompleteDirect }
func (CompleteDirectOptions) isActionOptions() {}

// CompleteFFOptions controls local fast-forward integration.
type CompleteFFOptions struct {
	Dirty                    DirtyPolicy
	CommitMessage            string
	PushBase                 bool
	DiscardIntegrationTarget bool
}

func (CompleteFFOptions) Action() Action   { return CompleteFF }
func (CompleteFFOptions) isActionOptions() {}

// ReviewHandoffOptions controls publication and review creation while keeping
// the persisted lifecycle state unchanged.
type ReviewHandoffOptions struct {
	Dirty         DirtyPolicy
	CommitMessage string
	Draft         bool
	Title         string
	Body          string
}

func (ReviewHandoffOptions) Action() Action   { return ReviewHandoff }
func (ReviewHandoffOptions) isActionOptions() {}

// VerifyMergedOptions names the base and optional squash evidence used to prove
// an externally integrated branch.
type VerifyMergedOptions struct {
	Dirty         DirtyPolicy
	CommitMessage string
	BaseRef       string
	SquashCommit  string
	PushBase      bool
}

func (VerifyMergedOptions) Action() Action   { return VerifyMerged }
func (VerifyMergedOptions) isActionOptions() {}

// RetireOptions controls optional cleanup after DONE. Branch deletion remains
// separate from worktree removal and must be proven safe by the injected
// executor.
type RetireOptions struct {
	DeleteBranch    bool
	CloseUnknown    bool
	AssumeNoRuntime bool
	Timeout         time.Duration
}

func (RetireOptions) Action() Action   { return Retire }
func (RetireOptions) isActionOptions() {}

// AdoptOptions contains only metadata recorded for an existing checkout. Adopt
// never creates, moves, or removes the checkout.
type AdoptOptions struct {
	Mode  task.CheckoutMode
	State task.State
	Name  string
	Base  string
	Owner string
	Next  string
	Note  string
	Tags  StringList
}

func (AdoptOptions) Action() Action   { return Adopt }
func (AdoptOptions) isActionOptions() {}

// RemoveCheckoutOptions controls runtime safety acknowledgements. The normal
// action preserves the checkout's branch; contained branch deletion is a
// separate CLI-only compatibility intent that the flow TUI never supplies.
type RemoveCheckoutOptions struct {
	// DiscardDirty is the explicit compatibility intent behind `dev wt rm
	// --force`. The flow TUI never sets it and planners must require a typed
	// confirmation before allowing it.
	DiscardDirty          bool
	RequireContained      bool
	ContainmentBase       string
	DeleteContainedBranch bool
	CloseUnknown          bool
	AssumeNoRuntime       bool
	Timeout               time.Duration
}

func (RemoveCheckoutOptions) Action() Action   { return RemoveCheckout }
func (RemoveCheckoutOptions) isActionOptions() {}

// RefreshRemoteOptions independently selects Git ref fetching and review
// lookup. Either operation or both may be requested.
type RefreshRemoteOptions struct {
	FetchRefs   bool
	QueryReview bool
}

func (RefreshRemoteOptions) Action() Action   { return RefreshRemote }
func (RefreshRemoteOptions) isActionOptions() {}

// ReconcileOptions names one proven repair. Parameters are action-specific
// authority values, not an escape hatch for arbitrary state transitions.
type ReconcileOptions struct {
	Name       string
	Parameters Fields
}

func (ReconcileOptions) Action() Action   { return Reconcile }
func (ReconcileOptions) isActionOptions() {}

// Request binds one action and its typed options to an exact selected locator.
// DisplayedAt may be used by a caller but does not contribute to plan identity.
type Request struct {
	Locator     Locator
	Action      Action
	Options     ActionOptions
	DisplayedAt time.Time
}

// NewRequest infers the action from options and returns a normalized value.
func NewRequest(locator Locator, options ActionOptions) (Request, error) {
	if options == nil {
		return Request{}, fmt.Errorf("%w: action options are required", ErrInvalidRequest)
	}
	normalized, err := cloneActionOptions(options, "")
	if err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	request := Request{Locator: locator, Action: normalized.Action(), Options: normalized}
	return normalizeRequest(request)
}

// Clone returns a request whose option-owned collection values are independent.
func (r Request) Clone() Request {
	options, err := cloneActionOptions(r.Options, r.Action)
	if err == nil {
		r.Options = options
	}
	return r
}

// Validate checks option/action agreement and graph legality without performing
// observations or side effects.
func (r Request) Validate() error {
	_, err := normalizeRequest(r)
	return err
}

func normalizeRequest(request Request) (Request, error) {
	if !request.Action.Valid() {
		return Request{}, fmt.Errorf("%w: unknown action %q", ErrInvalidRequest, request.Action)
	}
	options, err := cloneActionOptions(request.Options, request.Action)
	if err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if options.Action() != request.Action {
		return Request{}, fmt.Errorf("%w: action %s cannot use %s options", ErrInvalidRequest, request.Action, options.Action())
	}
	if err := validateActionOptions(options); err != nil {
		return Request{}, fmt.Errorf("%w: %s: %v", ErrInvalidRequest, request.Action, err)
	}
	request.Options = options
	if isTransitionAction(request.Action) {
		if _, err := RequireTransition(request.Locator.Mode, request.Locator.State, request.Action); err != nil {
			return Request{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
	}
	return request, nil
}

func cloneActionOptions(options ActionOptions, action Action) (ActionOptions, error) {
	if options == nil {
		return defaultActionOptions(action)
	}
	switch value := options.(type) {
	case ParkWarmOptions:
		return value, nil
	case *ParkWarmOptions:
		if value == nil {
			return nil, fmt.Errorf("nil park-warm options")
		}
		return *value, nil
	case ParkColdOptions:
		return value, nil
	case *ParkColdOptions:
		if value == nil {
			return nil, fmt.Errorf("nil park-cold options")
		}
		return *value, nil
	case ResumeOptions:
		return value, nil
	case *ResumeOptions:
		if value == nil {
			return nil, fmt.Errorf("nil resume options")
		}
		return *value, nil
	case CompleteDirectOptions:
		value.Dirty = normalizeDirty(value.Dirty)
		return value, nil
	case *CompleteDirectOptions:
		if value == nil {
			return nil, fmt.Errorf("nil complete-direct options")
		}
		copy := *value
		copy.Dirty = normalizeDirty(copy.Dirty)
		return copy, nil
	case CompleteFFOptions:
		value.Dirty = normalizeDirty(value.Dirty)
		return value, nil
	case *CompleteFFOptions:
		if value == nil {
			return nil, fmt.Errorf("nil complete-ff options")
		}
		copy := *value
		copy.Dirty = normalizeDirty(copy.Dirty)
		return copy, nil
	case ReviewHandoffOptions:
		value.Dirty = normalizeDirty(value.Dirty)
		return value, nil
	case *ReviewHandoffOptions:
		if value == nil {
			return nil, fmt.Errorf("nil review-handoff options")
		}
		copy := *value
		copy.Dirty = normalizeDirty(copy.Dirty)
		return copy, nil
	case VerifyMergedOptions:
		value.Dirty = normalizeDirty(value.Dirty)
		return value, nil
	case *VerifyMergedOptions:
		if value == nil {
			return nil, fmt.Errorf("nil verify-merged options")
		}
		copy := *value
		copy.Dirty = normalizeDirty(copy.Dirty)
		return copy, nil
	case RetireOptions:
		return value, nil
	case *RetireOptions:
		if value == nil {
			return nil, fmt.Errorf("nil retire options")
		}
		return *value, nil
	case AdoptOptions:
		value.Tags = value.Tags.clone()
		return value, nil
	case *AdoptOptions:
		if value == nil {
			return nil, fmt.Errorf("nil adopt options")
		}
		copy := *value
		copy.Tags = copy.Tags.clone()
		return copy, nil
	case RemoveCheckoutOptions:
		return value, nil
	case *RemoveCheckoutOptions:
		if value == nil {
			return nil, fmt.Errorf("nil remove-checkout options")
		}
		return *value, nil
	case RefreshRemoteOptions:
		return value, nil
	case *RefreshRemoteOptions:
		if value == nil {
			return nil, fmt.Errorf("nil refresh-remote options")
		}
		return *value, nil
	case ReconcileOptions:
		value.Parameters = value.Parameters.clone()
		return value, nil
	case *ReconcileOptions:
		if value == nil {
			return nil, fmt.Errorf("nil reconcile options")
		}
		copy := *value
		copy.Parameters = copy.Parameters.clone()
		return copy, nil
	default:
		return nil, fmt.Errorf("unsupported options type %T", options)
	}
}

func defaultActionOptions(action Action) (ActionOptions, error) {
	switch action {
	case ParkWarm:
		return ParkWarmOptions{}, nil
	case ParkCold:
		return ParkColdOptions{}, nil
	case Resume:
		return ResumeOptions{}, nil
	case CompleteDirect:
		return CompleteDirectOptions{Dirty: DirtyAuto}, nil
	case CompleteFF:
		return CompleteFFOptions{Dirty: DirtyAuto}, nil
	case ReviewHandoff:
		return ReviewHandoffOptions{Dirty: DirtyAuto}, nil
	case VerifyMerged:
		return VerifyMergedOptions{Dirty: DirtyAuto}, nil
	case Retire:
		return RetireOptions{}, nil
	case Adopt:
		return AdoptOptions{}, nil
	case RemoveCheckout:
		return RemoveCheckoutOptions{}, nil
	case RefreshRemote:
		return RefreshRemoteOptions{}, nil
	case Reconcile:
		return ReconcileOptions{}, nil
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

func validateActionOptions(options ActionOptions) error {
	switch value := options.(type) {
	case ParkWarmOptions:
		return validateTimeout(value.Timeout)
	case ParkColdOptions:
		return validateTimeout(value.Timeout)
	case ResumeOptions:
		return nil
	case CompleteDirectOptions:
		return validateDirty(value.Dirty, value.CommitMessage)
	case CompleteFFOptions:
		return validateDirty(value.Dirty, value.CommitMessage)
	case ReviewHandoffOptions:
		return validateDirty(value.Dirty, value.CommitMessage)
	case VerifyMergedOptions:
		return validateDirty(value.Dirty, value.CommitMessage)
	case RetireOptions:
		return validateTimeout(value.Timeout)
	case AdoptOptions:
		if value.Mode != "" && value.Mode != task.ModeWorktree {
			return fmt.Errorf("adopted checkout mode must be empty or %q, got %q", task.ModeWorktree, value.Mode)
		}
		if value.State != "" && !validState(value.State) {
			return fmt.Errorf("unknown adopted task state %q", value.State)
		}
		if value.Base != strings.TrimSpace(value.Base) || strings.ContainsRune(value.Base, '\x00') {
			return fmt.Errorf("adopted base must be normalized")
		}
		return nil
	case RemoveCheckoutOptions:
		if err := validateTimeout(value.Timeout); err != nil {
			return err
		}
		if value.DeleteContainedBranch && !value.RequireContained {
			return fmt.Errorf("contained branch deletion requires containment proof")
		}
		if value.RequireContained {
			if value.ContainmentBase == "" || value.ContainmentBase != strings.TrimSpace(value.ContainmentBase) ||
				strings.ContainsRune(value.ContainmentBase, '\x00') {
				return fmt.Errorf("containment base must be a normalized nonempty branch")
			}
		} else if value.ContainmentBase != "" {
			return fmt.Errorf("containment base requires containment proof")
		}
		return nil
	case RefreshRemoteOptions:
		if !value.FetchRefs && !value.QueryReview {
			return fmt.Errorf("select fetch refs, query review, or both")
		}
		return nil
	case ReconcileOptions:
		if strings.TrimSpace(value.Name) == "" {
			return fmt.Errorf("a named reconciliation is required")
		}
		if value.Name != strings.TrimSpace(value.Name) {
			return fmt.Errorf("reconciliation name must be normalized")
		}
		return nil
	default:
		return fmt.Errorf("unsupported options type %T", options)
	}
}

func validateTimeout(timeout time.Duration) error {
	if timeout < 0 {
		return fmt.Errorf("timeout cannot be negative")
	}
	return nil
}

func appendOptionsIdentity(writer *identityWriter, options ActionOptions) {
	writer.addString("options.action", string(options.Action()))
	switch value := options.(type) {
	case ParkWarmOptions:
		writer.addString("next", value.Next)
		writer.addString("note", value.Note)
		writer.addBool("commit-wip", value.CommitWIP)
		writer.addBool("push", value.Push)
		writer.addBool("keep-session", value.KeepSession)
		appendRuntimeOptionsIdentity(writer, value.CloseUnknown, value.AssumeNoRuntime, value.Timeout)
	case ParkColdOptions:
		writer.addString("next", value.Next)
		writer.addString("note", value.Note)
		writer.addBool("commit-wip", value.CommitWIP)
		writer.addBool("push", value.Push)
		appendRuntimeOptionsIdentity(writer, value.CloseUnknown, value.AssumeNoRuntime, value.Timeout)
	case ResumeOptions:
		writer.addBool("fetch-refs", value.FetchRefs)
		writer.addBool("no-provision", value.NoProvision)
		writer.addBool("take-ownership", value.TakeOwnership)
	case CompleteDirectOptions:
		appendCompletionOptionsIdentity(writer, value.Dirty, value.CommitMessage, value.Push)
	case CompleteFFOptions:
		appendCompletionOptionsIdentity(writer, value.Dirty, value.CommitMessage, value.PushBase)
	case ReviewHandoffOptions:
		writer.addString("dirty", string(value.Dirty))
		writer.addString("commit-message", value.CommitMessage)
		writer.addBool("draft", value.Draft)
		writer.addString("title", value.Title)
		writer.addString("body", value.Body)
	case VerifyMergedOptions:
		writer.addString("dirty", string(value.Dirty))
		writer.addString("commit-message", value.CommitMessage)
		writer.addString("base-ref", value.BaseRef)
		writer.addString("squash-commit", value.SquashCommit)
		writer.addBool("push-base", value.PushBase)
	case RetireOptions:
		writer.addBool("delete-branch", value.DeleteBranch)
		appendRuntimeOptionsIdentity(writer, value.CloseUnknown, value.AssumeNoRuntime, value.Timeout)
	case AdoptOptions:
		writer.addString("mode", string(value.Mode))
		writer.addString("state", string(value.State))
		writer.addString("name", value.Name)
		writer.addString("base", value.Base)
		writer.addString("owner", value.Owner)
		writer.addString("next", value.Next)
		writer.addString("note", value.Note)
		writer.addStrings("tags", value.Tags.values)
	case RemoveCheckoutOptions:
		writer.addBool("discard-dirty", value.DiscardDirty)
		writer.addBool("require-contained", value.RequireContained)
		writer.addString("containment-base", value.ContainmentBase)
		writer.addBool("delete-contained-branch", value.DeleteContainedBranch)
		appendRuntimeOptionsIdentity(writer, value.CloseUnknown, value.AssumeNoRuntime, value.Timeout)
	case RefreshRemoteOptions:
		writer.addBool("fetch-refs", value.FetchRefs)
		writer.addBool("query-review", value.QueryReview)
	case ReconcileOptions:
		writer.addString("name", value.Name)
		writer.addFields("parameters", value.Parameters)
	}
}

func appendCompletionOptionsIdentity(writer *identityWriter, dirty DirtyPolicy, message string, push bool) {
	writer.addString("dirty", string(dirty))
	writer.addString("commit-message", message)
	writer.addBool("push", push)
}

func appendRuntimeOptionsIdentity(writer *identityWriter, closeUnknown, assumeNoRuntime bool, timeout time.Duration) {
	writer.addBool("close-unknown", closeUnknown)
	writer.addBool("assume-no-runtime", assumeNoRuntime)
	writer.addInt64("timeout", int64(timeout))
}
