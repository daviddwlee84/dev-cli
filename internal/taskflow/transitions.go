package taskflow

import "github.com/daviddwlee84/dev-cli/internal/task"

// TransitionRule is one cell in the exhaustive persisted lifecycle table. An
// empty Target on an allowed rule means the task record is removed rather than
// assigned another persisted state.
type TransitionRule struct {
	Mode            task.CheckoutMode
	Source          task.State
	Action          Action
	Target          task.State
	Allowed         bool
	StatePreserving bool
	RemovesTask     bool
	Milestone       Milestone
	Reason          string
}

// HasTarget reports whether an allowed edge writes a persisted target state.
func (r TransitionRule) HasTarget() bool { return r.Allowed && r.Target != "" }

type transitionKey struct {
	mode   task.CheckoutMode
	state  task.State
	action Action
}

var (
	modeOrder = []task.CheckoutMode{
		task.ModeWorktree,
		task.ModeBranch,
		task.ModeDirect,
	}
	stateOrder = []task.State{
		task.Hot,
		task.Warm,
		task.Cold,
		task.Done,
	}
	transitionActionOrder = []Action{
		ParkWarm,
		ParkCold,
		Resume,
		CompleteDirect,
		CompleteFF,
		ReviewHandoff,
		VerifyMerged,
		Retire,
	}
	transitionTable = buildTransitionTable()
)

// TransitionActions returns the actions represented in every mode/state row.
// Adopt, RemoveCheckout, RefreshRemote, and Reconcile are deliberately absent
// because they operate on row ownership, repository context, or drift rather
// than persisted-state edges.
func TransitionActions() []Action {
	return append([]Action(nil), transitionActionOrder...)
}

// PersistedLifecycleActions is the stable action dimension of the lifecycle
// table, including state-preserving review handoff but excluding repository
// context actions such as remote refresh.
func PersistedLifecycleActions() []Action { return TransitionActions() }

// Transitions returns all table cells in mode, state, and action order. Denied
// cells are retained so additions cannot silently fall through a switch.
func Transitions() []TransitionRule {
	out := make([]TransitionRule, 0, len(modeOrder)*len(stateOrder)*len(transitionActionOrder))
	for _, mode := range modeOrder {
		for _, state := range stateOrder {
			for _, action := range transitionActionOrder {
				out = append(out, transitionTable[transitionKey{mode: mode, state: state, action: action}])
			}
		}
	}
	return out
}

// LookupTransition returns the exhaustive table cell for known dimensions.
func LookupTransition(mode task.CheckoutMode, source task.State, action Action) (TransitionRule, bool) {
	if !validMode(mode) || !validState(source) || !isTransitionAction(action) {
		return TransitionRule{Mode: mode, Source: source, Action: action}, false
	}
	rule, ok := transitionTable[transitionKey{mode: mode, state: source, action: action}]
	return rule, ok
}

// RequireTransition returns the allowed table cell or a typed graph error.
func RequireTransition(mode task.CheckoutMode, source task.State, action Action) (TransitionRule, error) {
	rule, found := LookupTransition(mode, source, action)
	if !found {
		rule.Reason = "unknown mode, state, or non-transition action"
		return rule, &InvalidTransitionError{Rule: rule}
	}
	if !rule.Allowed {
		return rule, &InvalidTransitionError{Rule: rule}
	}
	return rule, nil
}

func isTransitionAction(action Action) bool {
	for _, candidate := range transitionActionOrder {
		if action == candidate {
			return true
		}
	}
	return false
}

func validMode(mode task.CheckoutMode) bool {
	for _, candidate := range modeOrder {
		if mode == candidate {
			return true
		}
	}
	return false
}

func validState(state task.State) bool {
	for _, candidate := range stateOrder {
		if state == candidate {
			return true
		}
	}
	return false
}

func buildTransitionTable() map[transitionKey]TransitionRule {
	table := make(map[transitionKey]TransitionRule, len(modeOrder)*len(stateOrder)*len(transitionActionOrder))
	for _, mode := range modeOrder {
		for _, source := range stateOrder {
			for _, action := range transitionActionOrder {
				rule := TransitionRule{
					Mode: mode, Source: source, Action: action,
					Reason: deniedTransitionReason(mode, source, action),
				}
				table[transitionKey{mode: mode, state: source, action: action}] = rule
			}
		}
	}

	allow := func(mode task.CheckoutMode, source task.State, action Action, target task.State, preserving, removes bool, milestone Milestone) {
		key := transitionKey{mode: mode, state: source, action: action}
		table[key] = TransitionRule{
			Mode: mode, Source: source, Action: action, Target: target,
			Allowed: true, StatePreserving: preserving, RemovesTask: removes,
			Milestone: milestone,
		}
	}

	for _, mode := range []task.CheckoutMode{task.ModeWorktree, task.ModeBranch} {
		allow(mode, task.Hot, ParkWarm, task.Warm, false, false, MilestoneNone)
		// Existing park accepts WARM tasks so it can update parking metadata and
		// close a residual runtime without manufacturing a backward transition.
		allow(mode, task.Warm, ParkWarm, task.Warm, true, false, MilestoneNone)
		allow(mode, task.Hot, ParkCold, task.Cold, false, false, MilestoneNone)
		allow(mode, task.Warm, ParkCold, task.Cold, false, false, MilestoneNone)
		allow(mode, task.Warm, Resume, task.Hot, false, false, MilestoneNone)
		allow(mode, task.Cold, Resume, task.Hot, false, false, MilestoneNone)
		allow(mode, task.Hot, CompleteFF, task.Done, false, false, MilestoneMerged)
		allow(mode, task.Warm, CompleteFF, task.Done, false, false, MilestoneMerged)
		allow(mode, task.Hot, VerifyMerged, task.Done, false, false, MilestoneMerged)
		allow(mode, task.Warm, VerifyMerged, task.Done, false, false, MilestoneMerged)
		allow(mode, task.Hot, ReviewHandoff, task.Hot, true, false, MilestoneReviewReady)
		allow(mode, task.Warm, ReviewHandoff, task.Warm, true, false, MilestoneReviewReady)
		allow(mode, task.Done, Retire, "", false, true, MilestoneRetired)
	}

	allow(task.ModeDirect, task.Hot, ParkWarm, task.Warm, false, false, MilestoneNone)
	allow(task.ModeDirect, task.Warm, ParkWarm, task.Warm, true, false, MilestoneNone)
	allow(task.ModeDirect, task.Warm, Resume, task.Hot, false, false, MilestoneNone)
	allow(task.ModeDirect, task.Hot, CompleteDirect, task.Done, false, false, MilestoneMerged)
	allow(task.ModeDirect, task.Warm, CompleteDirect, task.Done, false, false, MilestoneMerged)
	allow(task.ModeDirect, task.Done, Retire, "", false, true, MilestoneRetired)

	return table
}

func deniedTransitionReason(mode task.CheckoutMode, source task.State, action Action) string {
	if mode == task.ModeDirect && source == task.Cold {
		return "direct tasks cannot be COLD"
	}
	if action == ParkCold && mode == task.ModeDirect {
		return "direct tasks use the canonical checkout and cannot be parked COLD"
	}
	if source == task.Done {
		return "DONE tasks can only be retired through the persisted lifecycle graph"
	}
	if source == task.Cold {
		return "COLD tasks must resume before any completion or review action"
	}
	if action == CompleteDirect && mode != task.ModeDirect {
		return "direct completion is only valid for direct tasks"
	}
	if (action == CompleteFF || action == ReviewHandoff || action == VerifyMerged) && mode == task.ModeDirect {
		return "direct tasks have no separate branch to integrate or hand off"
	}
	return "the edge is not in the approved lifecycle graph"
}
