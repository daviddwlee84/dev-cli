package assessment

import "fmt"

// ReduceOutcomes combines independent requirements using the fail-closed
// precedence blocked > indeterminate > eligible > not-applicable. No evidence
// is itself indeterminate.
func ReduceOutcomes(outcomes ...Outcome) Outcome {
	if len(outcomes) == 0 {
		return OutcomeIndeterminate
	}
	result := OutcomeNotApplicable
	for _, outcome := range outcomes {
		switch outcome {
		case OutcomeBlocked:
			return OutcomeBlocked
		case OutcomeIndeterminate:
			result = OutcomeIndeterminate
		case OutcomeEligible:
			if result == OutcomeNotApplicable {
				result = OutcomeEligible
			}
		case OutcomeNotApplicable:
		default:
			return OutcomeIndeterminate
		}
	}
	return result
}

// Reduce applies source-quality policy after reducing requirement outcomes.
// Cheap reports retain the observed result for display. Deep reports downgrade
// any conclusive result to indeterminate unless every relied-on source is fresh,
// complete, and backed by a live authority.
func Reduce(profile Profile, sources []Source, outcomes ...Outcome) (Outcome, error) {
	if err := profile.Validate(); err != nil {
		return OutcomeIndeterminate, err
	}
	for _, outcome := range outcomes {
		if err := outcome.Validate(); err != nil {
			return OutcomeIndeterminate, err
		}
	}
	for _, source := range sources {
		if err := source.Validate(); err != nil {
			return OutcomeIndeterminate, fmt.Errorf("reduce assessment source: %w", err)
		}
	}
	result := ReduceOutcomes(outcomes...)
	if result == OutcomeNotApplicable || result == OutcomeIndeterminate {
		return result, nil
	}
	if len(sources) == 0 {
		return OutcomeIndeterminate, nil
	}
	if profile == ProfileDeep {
		for _, source := range sources {
			if !source.conclusive() {
				return OutcomeIndeterminate, nil
			}
		}
	}
	return result, nil
}
