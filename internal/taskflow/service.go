package taskflow

import "context"

// PlanHook performs action-specific observation and returns only construction
// input. BuildPlan remains the single graph, availability, and identity
// authority.
type PlanHook func(context.Context, Request) (PlanSpec, error)

// ApplyHook executes one already validated plan and may return a partial Result
// together with an error.
type ApplyHook func(context.Context, Plan) (Result, error)

// Handler contains the narrow hooks for one exact action. Either hook may be
// omitted; no production or unsafe fallback is installed.
type Handler struct {
	Plan  PlanHook
	Apply ApplyHook
}

// Handlers maps stable action codes to injected behavior.
type Handlers map[Action]Handler

// LocateTaskHook builds one exact managed-task locator from fresh durable and
// Git evidence. It is intentionally not accepted by NewService: only the
// guarded lifecycle constructor may install it.
type LocateTaskHook func(context.Context, string) (Locator, error)

// Service is the UI-agnostic locate/plan/apply dispatcher.
type Service struct {
	handlers   map[Action]Handler
	locateTask LocateTaskHook
}

// NewService copies handlers so later mutation of the caller's map cannot
// redirect a planned action.
func NewService(handlers Handlers) *Service {
	copied := make(map[Action]Handler, len(handlers))
	for action, handler := range handlers {
		copied[action] = handler
	}
	return &Service{handlers: copied}
}

// LocateTask returns an exact locator only when this service came from
// NewLifecycleService. Generic dispatch services deliberately have no fallback
// locator because guessing task, repository, or checkout identity is unsafe.
func (s *Service) LocateTask(ctx context.Context, taskID string) (Locator, error) {
	if s == nil || s.locateTask == nil {
		return Locator{}, ErrLocatorUnavailable
	}
	return s.locateTask(ctx, taskID)
}

// Plan validates and freezes a request, delegates action-specific observations,
// then centrally builds the immutable plan.
func (s *Service) Plan(ctx context.Context, request Request) (Plan, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Plan{}, err
	}
	handler, ok := s.handlers[normalized.Action]
	if !ok || handler.Plan == nil {
		return Plan{}, &HandlerUnavailableError{Action: normalized.Action, Stage: "plan"}
	}
	spec, err := handler.Plan(ctx, normalized.Clone())
	if err != nil {
		return Plan{}, err
	}
	return BuildPlan(normalized, spec)
}

// Apply validates identity and approval, rejects every status except READY, and
// delegates only to the handler registered for the plan's exact action. A
// handler's partial Result is returned unchanged in meaning alongside errors.
func (s *Service) Apply(ctx context.Context, plan Plan, approval Approval) (Result, error) {
	frozen := plan.Clone()
	if err := frozen.Validate(); err != nil {
		return Result{}, err
	}
	if err := frozen.ValidateApproval(approval); err != nil {
		return Result{}, err
	}
	if frozen.Availability != AvailabilityReady {
		notReady := &PlanNotReadyError{
			PlanID: frozen.PlanID, Availability: frozen.Availability,
			conditions: frozen.Conditions(),
		}
		return Result{}, &InvalidPlanError{
			PlanID: frozen.PlanID, Reason: "required conditions are not ready", Cause: notReady,
		}
	}
	handler, ok := s.handlers[frozen.Action]
	if !ok || handler.Apply == nil {
		return Result{}, &HandlerUnavailableError{Action: frozen.Action, Stage: "apply"}
	}
	result, err := handler.Apply(ctx, frozen.Clone())
	result = result.Clone()
	if err != nil && len(result.CompletedSteps()) > 0 {
		result.PartialSuccess = true
	}
	return result, err
}
