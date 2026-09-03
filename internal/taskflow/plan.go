package taskflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/task"
)

// PlanSpec is action-handler construction input. Authority holds additional
// exact identities such as status, runtime, artifact, or remote fingerprints.
// BuildPlan copies every map and slice.
type PlanSpec struct {
	Authority         map[string]string
	Conditions        []Condition
	Effects           []Effect
	RetainedResources []string
	Confirmation      Confirmation
	FallbackCommand   string
	Summary           string
	DisplayedAt       time.Time
}

// Plan is an immutable-by-interface proposal bound to one request and one
// authority fingerprint. Collection accessors always return copies.
type Plan struct {
	PlanID                 string
	AuthorityFingerprint   string
	Request                Request
	Locator                Locator
	Action                 Action
	Source                 task.State
	Target                 task.State
	HasPersistedTransition bool
	StatePreserving        bool
	RemovesTask            bool
	Availability           Availability
	Confirmation           Confirmation
	ExpectedMilestone      Milestone
	FallbackCommand        string
	Summary                string
	DisplayedAt            time.Time

	authority  Fields
	conditions []Condition
	effects    []Effect
	retained   StringList
}

// BuildPlan validates graph legality, derives availability, and seals a plan's
// deterministic authority fingerprint and PlanID.
func BuildPlan(request Request, spec PlanSpec) (Plan, error) {
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Plan{}, &InvalidPlanError{Reason: "invalid request", Cause: err}
	}
	if err := spec.Confirmation.Validate(); err != nil {
		return Plan{}, &InvalidPlanError{Reason: "invalid confirmation", Cause: err}
	}
	conditions := append([]Condition(nil), spec.Conditions...)
	if err := validateConditions(conditions); err != nil {
		return Plan{}, &InvalidPlanError{Reason: "invalid conditions", Cause: err}
	}
	effects := cloneEffects(spec.Effects)
	if err := validateEffects(effects); err != nil {
		return Plan{}, &InvalidPlanError{Reason: "invalid effects", Cause: err}
	}
	authority := NewFields(spec.Authority)
	if err := validateAuthority(authority); err != nil {
		return Plan{}, &InvalidPlanError{Reason: "invalid authority", Cause: err}
	}

	plan := Plan{
		Request: normalized.Clone(), Locator: normalized.Locator, Action: normalized.Action,
		Source:       normalized.Locator.State,
		Availability: AvailabilityFor(conditions), Confirmation: spec.Confirmation,
		FallbackCommand: spec.FallbackCommand, Summary: spec.Summary, DisplayedAt: spec.DisplayedAt,
		authority: authority, conditions: conditions, effects: effects,
		retained: NewStringList(spec.RetainedResources...),
	}
	if isTransitionAction(plan.Action) {
		rule, transitionErr := RequireTransition(plan.Locator.Mode, plan.Locator.State, plan.Action)
		if transitionErr != nil {
			return Plan{}, &InvalidPlanError{Reason: "invalid transition", Cause: transitionErr}
		}
		plan.HasPersistedTransition = true
		plan.Target = rule.Target
		plan.StatePreserving = rule.StatePreserving
		plan.RemovesTask = rule.RemovesTask
		plan.ExpectedMilestone = rule.Milestone
	} else {
		switch plan.Action {
		case Adopt:
			plan.ExpectedMilestone = MilestoneAdopted
		case Reconcile:
			plan.ExpectedMilestone = MilestoneReconciled
		}
	}
	plan.AuthorityFingerprint, plan.PlanID = computePlanIdentity(plan)
	return plan.Clone(), nil
}

// Clone returns a plan with independent request, map, and slice storage.
func (p Plan) Clone() Plan {
	p.Request = p.Request.Clone()
	p.authority = p.authority.clone()
	p.conditions = append([]Condition(nil), p.conditions...)
	p.effects = cloneEffects(p.effects)
	p.retained = p.retained.clone()
	return p
}

// AuthorityFields returns a mutable copy of the extra authority identities.
func (p Plan) AuthorityFields() map[string]string { return p.authority.Map() }

// Conditions returns the ordered conditions as a mutable copy.
func (p Plan) Conditions() []Condition { return append([]Condition(nil), p.conditions...) }

// Effects returns independent copies of the ordered effects.
func (p Plan) Effects() []Effect { return cloneEffects(p.effects) }

// RetainedResources returns the resources intentionally left after Apply.
func (p Plan) RetainedResources() []string { return p.retained.Values() }

// InputConditions returns required operator input without conflating it with a
// safety override.
func (p Plan) InputConditions() []Condition {
	return filterConditions(p.conditions, func(condition Condition) bool {
		return condition.Requirement == RequirementRequired && condition.Verdict == VerdictNeedsInput
	})
}

// BlockingConditions returns every required, non-input condition that prevents
// READY. Advisory conditions are intentionally absent.
func (p Plan) BlockingConditions() []Condition {
	return filterConditions(p.conditions, func(condition Condition) bool {
		return condition.Requirement == RequirementRequired &&
			condition.Verdict != VerdictMet && condition.Verdict != VerdictNeedsInput
	})
}

// AdvisoryConditions returns non-authorizing evidence for display.
func (p Plan) AdvisoryConditions() []Condition {
	return filterConditions(p.conditions, func(condition Condition) bool {
		return condition.Requirement == RequirementAdvisory
	})
}

// Validate proves that a plan is structurally consistent and still carries its
// original deterministic identity. It performs no live observations.
func (p Plan) Validate() error {
	if p.PlanID == "" || p.AuthorityFingerprint == "" {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "plan identity is missing"}
	}
	if !p.Action.Valid() {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: fmt.Sprintf("unknown action %q", p.Action)}
	}
	if p.Request.Action != p.Action {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "request action and plan action differ"}
	}
	if p.Request.Locator != p.Locator {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "request locator and plan locator differ"}
	}
	if p.Request.Options == nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "normalized request options are missing"}
	}
	normalized, err := normalizeRequest(p.Request)
	if err != nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "request is no longer valid", Cause: err}
	}
	if requestIdentity(normalized) != requestIdentity(p.Request) {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "request options are not normalized"}
	}
	if err := p.Confirmation.Validate(); err != nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "invalid confirmation", Cause: err}
	}
	if err := validateConditions(p.conditions); err != nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "invalid conditions", Cause: err}
	}
	if err := validateEffects(p.effects); err != nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "invalid effects", Cause: err}
	}
	if err := validateAuthority(p.authority); err != nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "invalid authority", Cause: err}
	}
	if err := validatePlanTransition(p); err != nil {
		return &InvalidPlanError{PlanID: p.PlanID, Reason: "transition metadata is inconsistent", Cause: err}
	}

	authorityFingerprint, planID := computePlanIdentity(p)
	if authorityFingerprint != p.AuthorityFingerprint {
		return &StalePlanError{
			ExpectedPlanID: p.PlanID, ActualPlanID: planID,
			ExpectedAuthorityFingerprint: p.AuthorityFingerprint,
			ActualAuthorityFingerprint:   authorityFingerprint,
			Reason:                       "authority fields, effects, or conditions changed",
		}
	}
	if planID != p.PlanID {
		return &StalePlanError{
			ExpectedPlanID: p.PlanID, ActualPlanID: planID,
			ExpectedAuthorityFingerprint: p.AuthorityFingerprint,
			ActualAuthorityFingerprint:   authorityFingerprint,
			Reason:                       "PlanID does not match its authority fingerprint",
		}
	}
	if availability := AvailabilityFor(p.conditions); availability != p.Availability {
		return &InvalidPlanError{
			PlanID: p.PlanID,
			Reason: fmt.Sprintf("availability is %s, want derived %s", p.Availability, availability),
		}
	}
	return nil
}

// ValidateApproval binds affirmative approval and any typed token to this exact
// plan. It never changes plan readiness or condition verdicts.
func (p Plan) ValidateApproval(approval Approval) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if !approval.Granted {
		return &ApprovalError{PlanID: p.PlanID, Reason: "affirmative approval was not granted", Cause: ErrApprovalRequired}
	}
	if approval.PlanID != p.PlanID {
		return &StalePlanError{
			ExpectedPlanID: p.PlanID, ActualPlanID: approval.PlanID,
			ExpectedAuthorityFingerprint: p.AuthorityFingerprint,
			Reason:                       "approval names a different plan",
		}
	}
	if p.Confirmation.Kind == ConfirmationTyped {
		if approval.Token != p.Confirmation.Token {
			return &ApprovalError{PlanID: p.PlanID, Reason: "typed confirmation token does not match", Cause: ErrInvalidApproval}
		}
	} else if approval.Token != "" {
		return &ApprovalError{PlanID: p.PlanID, Reason: "plan does not accept a typed confirmation token", Cause: ErrInvalidApproval}
	}
	return nil
}

func validatePlanTransition(plan Plan) error {
	if isTransitionAction(plan.Action) {
		rule, err := RequireTransition(plan.Locator.Mode, plan.Locator.State, plan.Action)
		if err != nil {
			return err
		}
		if !plan.HasPersistedTransition || plan.Source != rule.Source || plan.Target != rule.Target ||
			plan.StatePreserving != rule.StatePreserving || plan.RemovesTask != rule.RemovesTask ||
			plan.ExpectedMilestone != rule.Milestone {
			return fmt.Errorf("plan does not match the transition table")
		}
		return nil
	}
	expectedMilestone := MilestoneNone
	switch plan.Action {
	case Adopt:
		expectedMilestone = MilestoneAdopted
	case Reconcile:
		expectedMilestone = MilestoneReconciled
	}
	if plan.HasPersistedTransition || plan.Target != "" || plan.StatePreserving || plan.RemovesTask ||
		plan.Source != plan.Locator.State || plan.ExpectedMilestone != expectedMilestone {
		return fmt.Errorf("domain action is represented as a persisted-state edge")
	}
	return nil
}

func validateConditions(conditions []Condition) error {
	seen := make(map[ConditionCode]bool, len(conditions))
	for index, condition := range conditions {
		if err := condition.Validate(); err != nil {
			return fmt.Errorf("condition %d: %w", index, err)
		}
		if seen[condition.Code] {
			return fmt.Errorf("condition code %q is duplicated", condition.Code)
		}
		seen[condition.Code] = true
	}
	return nil
}

func validateEffects(effects []Effect) error {
	for index, effect := range effects {
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("effect %d: %w", index, err)
		}
	}
	return nil
}

func validateAuthority(authority Fields) error {
	for _, field := range authority.entries {
		if field.Key == "" {
			return fmt.Errorf("authority field key is empty")
		}
	}
	return nil
}

func cloneEffects(effects []Effect) []Effect {
	out := make([]Effect, len(effects))
	for index, effect := range effects {
		out[index] = effect.Clone()
	}
	return out
}

func filterConditions(conditions []Condition, keep func(Condition) bool) []Condition {
	out := make([]Condition, 0, len(conditions))
	for _, condition := range conditions {
		if keep(condition) {
			out = append(out, condition)
		}
	}
	return out
}

func requestIdentity(request Request) string {
	writer := newIdentityWriter("taskflow-request-v1")
	appendRequestIdentity(writer, request)
	return writer.sumHex()
}

func computePlanIdentity(plan Plan) (string, string) {
	writer := newIdentityWriter("taskflow-authority-v1")
	appendRequestIdentity(writer, plan.Request)
	writer.addString("plan.action", string(plan.Action))
	writer.addString("plan.source", string(plan.Source))
	writer.addString("plan.target", string(plan.Target))
	writer.addBool("plan.has-persisted-transition", plan.HasPersistedTransition)
	writer.addBool("plan.state-preserving", plan.StatePreserving)
	writer.addBool("plan.removes-task", plan.RemovesTask)
	writer.addString("plan.expected-milestone", string(plan.ExpectedMilestone))
	writer.addFields("plan.authority", plan.authority)
	writer.addInt64("plan.condition-count", int64(len(plan.conditions)))
	for index, condition := range plan.conditions {
		prefix := "plan.condition." + strconv.Itoa(index) + "."
		writer.addString(prefix+"code", string(condition.Code))
		writer.addString(prefix+"verdict", string(condition.Verdict))
		writer.addString(prefix+"requirement", string(condition.Requirement))
		writer.addString(prefix+"evidence", condition.Evidence)
		writer.addString(prefix+"remediation", condition.Remediation)
	}
	writer.addInt64("plan.effect-count", int64(len(plan.effects)))
	for index, effect := range plan.effects {
		prefix := "plan.effect." + strconv.Itoa(index) + "."
		writer.addString(prefix+"code", string(effect.Code))
		writer.addString(prefix+"description", effect.Description)
		writer.addString(prefix+"target", effect.Target)
		writer.addBool(prefix+"destructive", effect.Destructive)
		writer.addBool(prefix+"network", effect.Network)
		writer.addFields(prefix+"details", effect.Details)
	}
	writer.addStrings("plan.retained", plan.retained.values)
	writer.addString("plan.confirmation-kind", string(plan.Confirmation.Kind))
	writer.addString("plan.confirmation-token", plan.Confirmation.Token)
	authorityFingerprint := "sha256:" + writer.sumHex()

	planWriter := newIdentityWriter("taskflow-plan-v1")
	planWriter.addString("authority-fingerprint", authorityFingerprint)
	return authorityFingerprint, "plan:" + planWriter.sumHex()
}

func appendRequestIdentity(writer *identityWriter, request Request) {
	locator := request.Locator
	writer.addString("request.action", string(request.Action))
	writer.addString("locator.repo-key", locator.RepoKey)
	writer.addString("locator.row-key", locator.RowKey)
	writer.addString("locator.row-kind", locator.RowKind)
	writer.addString("locator.repository-id", locator.RepositoryID)
	writer.addString("locator.git-common-dir", locator.GitCommonDir)
	writer.addString("locator.task-id", locator.TaskID)
	writer.addString("locator.task-revision", locator.TaskRevision)
	writer.addString("locator.repo-path", locator.RepoPath)
	writer.addString("locator.checkout-path", locator.CheckoutPath)
	writer.addString("locator.branch", locator.Branch)
	writer.addString("locator.base", locator.Base)
	writer.addString("locator.upstream", locator.Upstream)
	writer.addString("locator.remote", locator.Remote)
	writer.addString("locator.head-oid", locator.HeadOID)
	writer.addString("locator.base-oid", locator.BaseOID)
	writer.addString("locator.upstream-oid", locator.UpstreamOID)
	writer.addString("locator.mode", string(locator.Mode))
	writer.addString("locator.state", string(locator.State))
	appendOptionsIdentity(writer, request.Options)
}

type identityWriter struct {
	buffer bytes.Buffer
}

func newIdentityWriter(domain string) *identityWriter {
	writer := &identityWriter{}
	writer.addString("domain", domain)
	return writer
}

func (w *identityWriter) addString(label, value string) {
	w.addBytes([]byte(label))
	w.addBytes([]byte(value))
}

func (w *identityWriter) addBool(label string, value bool) {
	w.addString(label, strconv.FormatBool(value))
}

func (w *identityWriter) addInt64(label string, value int64) {
	w.addString(label, strconv.FormatInt(value, 10))
}

func (w *identityWriter) addStrings(label string, values []string) {
	w.addInt64(label+".count", int64(len(values)))
	for index, value := range values {
		w.addString(label+"."+strconv.Itoa(index), value)
	}
}

func (w *identityWriter) addFields(label string, fields Fields) {
	w.addInt64(label+".count", int64(len(fields.entries)))
	for index, field := range fields.entries {
		prefix := label + "." + strconv.Itoa(index)
		w.addString(prefix+".key", field.Key)
		w.addString(prefix+".value", field.Value)
	}
}

func (w *identityWriter) addBytes(value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = w.buffer.Write(length[:])
	_, _ = w.buffer.Write(value)
}

func (w *identityWriter) sumHex() string {
	sum := sha256.Sum256(w.buffer.Bytes())
	return hex.EncodeToString(sum[:])
}
