// Package forgemetrics describes optional, dated forge observations. Metrics
// are display data; their availability never establishes inventory completeness
// or grants repository lifecycle authority.
package forgemetrics

import (
	"strings"
	"time"
	"unicode"
)

type State string

const (
	StateKnown       State = "known"
	StateUnknown     State = "unknown"
	StateUnsupported State = "unsupported"
	StateError       State = "error"
)

// Count distinguishes a measured zero from an unavailable observation. An
// error may retain a previous Value and its original ObservedAt; Error describes
// the latest failed attempt without making that older value newly observed.
type Count struct {
	Value      *int64    `json:"value,omitempty"`
	State      State     `json:"state"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	// AttemptedAt is separate only when a failed refresh retains an older
	// measurement. It orders responses without restamping that measurement.
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	Error       string    `json:"error,omitempty"`
}

type Metrics struct {
	Stars      *Count `json:"stars,omitempty"`
	Forks      *Count `json:"forks,omitempty"`
	OpenIssues *Count `json:"open_issues,omitempty"`
	OpenPRs    *Count `json:"open_prs,omitempty"`
	Comments   *Count `json:"comments,omitempty"`
}

func Known(value int64, observedAt time.Time) *Count {
	if value < 0 {
		return Failed(observedAt, "provider returned an invalid count")
	}
	return &Count{Value: &value, State: StateKnown, ObservedAt: observedAt.UTC()}
}

// FromValue preserves missing/null provider fields as unknown rather than zero.
func FromValue(value *int64, observedAt time.Time) *Count {
	if value == nil {
		return nil
	}
	return Known(*value, observedAt)
}

func Unknown(observedAt time.Time, reason string) *Count {
	return &Count{State: StateUnknown, ObservedAt: observedAt.UTC(), Error: reason}
}

func Unsupported() *Count { return &Count{State: StateUnsupported} }

func Failed(observedAt time.Time, reason string) *Count {
	return &Count{State: StateError, ObservedAt: observedAt.UTC(), Error: reason}
}

// Fresh uses only successful observations. Unsupported is a capability fact,
// while failed/unknown attempts must not make an old retained value fresh.
func Fresh(count *Count, ttl time.Duration, now time.Time) bool {
	if count == nil {
		return false
	}
	if count.State == StateUnsupported {
		return true
	}
	return count.State == StateKnown && count.Value != nil && *count.Value >= 0 &&
		!count.ObservedAt.IsZero() && !count.ObservedAt.After(now.Add(5*time.Minute)) &&
		(ttl <= 0 || now.Sub(count.ObservedAt) <= ttl)
}

// Clone returns independent observations, including the numeric pointers.
func Clone(metrics *Metrics) *Metrics {
	if metrics == nil {
		return nil
	}
	return &Metrics{Stars: cloneCount(metrics.Stars), Forks: cloneCount(metrics.Forks),
		OpenIssues: cloneCount(metrics.OpenIssues), OpenPRs: cloneCount(metrics.OpenPRs), Comments: cloneCount(metrics.Comments)}
}

func cloneCount(count *Count) *Count {
	if count == nil {
		return nil
	}
	out := *count
	if count.Value != nil {
		value := *count.Value
		out.Value = &value
	}
	return &out
}

// Merge combines only observations of the same already-verified resource.
// Callers own provider/host/resource matching. An older response cannot replace
// a newer value, and failed refreshes retain the value's original timestamp.
func Merge(previous, next *Metrics) *Metrics {
	if previous == nil {
		return Clone(next)
	}
	if next == nil {
		return Clone(previous)
	}
	return &Metrics{Stars: mergeCount(previous.Stars, next.Stars), Forks: mergeCount(previous.Forks, next.Forks),
		OpenIssues: mergeCount(previous.OpenIssues, next.OpenIssues), OpenPRs: mergeCount(previous.OpenPRs, next.OpenPRs),
		Comments: mergeCount(previous.Comments, next.Comments)}
}

func mergeCount(previous, next *Count) *Count {
	if next == nil {
		return cloneCount(previous)
	}
	if previous == nil {
		return cloneCount(next)
	}
	if !attemptTime(next).IsZero() && attemptTime(next).Before(attemptTime(previous)) {
		return cloneCount(previous)
	}
	if (next.State == StateError || next.State == StateUnknown) && previous.Value != nil &&
		(next.Value == nil || previous.ObservedAt.After(next.ObservedAt)) {
		out := cloneCount(next)
		out.Value = cloneCount(previous).Value
		out.AttemptedAt = attemptTime(next)
		out.ObservedAt = previous.ObservedAt
		return out
	}
	return cloneCount(next)
}

func attemptTime(count *Count) time.Time {
	if count.AttemptedAt.After(count.ObservedAt) {
		return count.AttemptedAt
	}
	return count.ObservedAt
}

// Valid bounds cache observations independently from inventory coverage.
func Valid(metrics *Metrics, now time.Time) bool {
	if metrics == nil {
		return true
	}
	for _, count := range []*Count{metrics.Stars, metrics.Forks, metrics.OpenIssues, metrics.OpenPRs, metrics.Comments} {
		if count == nil {
			continue
		}
		if count.Value != nil && *count.Value < 0 || count.ObservedAt.After(now.Add(5*time.Minute)) || count.AttemptedAt.After(now.Add(5*time.Minute)) ||
			(!count.AttemptedAt.IsZero() && count.AttemptedAt.Before(count.ObservedAt)) ||
			len(count.Error) > 1024 || strings.IndexFunc(count.Error, unicode.IsControl) >= 0 {
			return false
		}
		switch count.State {
		case StateKnown:
			if count.Value == nil || count.ObservedAt.IsZero() || count.Error != "" {
				return false
			}
		case StateUnknown, StateError:
			if count.Value != nil && count.ObservedAt.IsZero() {
				return false
			}
		case StateUnsupported:
			if count.Value != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}
