package forgemetrics

import (
	"testing"
	"time"
)

func TestMissingAndRealZeroAreDistinct(t *testing.T) {
	now := time.Now().UTC()
	if FromValue(nil, now) != nil {
		t.Fatal("missing count became a measured value")
	}
	zero := int64(0)
	count := FromValue(&zero, now)
	if count.State != StateKnown || count.Value == nil || *count.Value != 0 || !Fresh(count, time.Hour, now) {
		t.Fatalf("measured zero = %+v", count)
	}
	if Fresh(Failed(now, "failed"), time.Hour, now) || Fresh(Unknown(now, "missing"), time.Hour, now) {
		t.Fatal("unavailable observation became fresh")
	}
}

func TestMergeRetainsValuesAndOriginalObservationTimes(t *testing.T) {
	old := time.Now().UTC().Add(-2 * time.Hour)
	previous := &Metrics{Stars: Known(7, old), Forks: Known(3, old)}
	failed := Merge(previous, &Metrics{Stars: Failed(old.Add(time.Hour), "refresh failed")})
	if failed.Stars.State != StateError || *failed.Stars.Value != 7 || !failed.Stars.ObservedAt.Equal(old) || *failed.Forks.Value != 3 {
		t.Fatalf("failed merge = %+v", failed)
	}
	if Fresh(failed.Stars, 3*time.Hour, time.Now()) {
		t.Fatal("failed observation is fresh")
	}
	late := Merge(failed, &Metrics{Stars: Known(8, old.Add(30*time.Minute))})
	if late.Stars.State != StateError || *late.Stars.Value != 7 {
		t.Fatal("late older success replaced a newer failed observation")
	}
	*failed.Stars.Value = 0
	if *previous.Stars.Value != 7 {
		t.Fatal("merge aliases the caller's observations")
	}
	newer := Merge(previous, &Metrics{Stars: Known(9, old.Add(time.Minute))})
	if got := Merge(newer, previous); *got.Stars.Value != 9 {
		t.Fatal("older batch replaced a newer observation")
	}
	retained := Merge(newer, Merge(previous, &Metrics{Stars: Failed(old.Add(time.Hour), "failed")}))
	if retained.Stars.State != StateError || *retained.Stars.Value != 9 || !retained.Stars.ObservedAt.Equal(old.Add(time.Minute)) {
		t.Fatal("failed batch replaced a newer retained measurement with its older cached value")
	}
}

func TestMetricCacheValidation(t *testing.T) {
	now := time.Now().UTC()
	negative := int64(-1)
	for _, invalid := range []*Count{{Value: &negative, State: StateKnown, ObservedAt: now}, Known(1, now.Add(time.Hour)), {State: StateKnown}, {State: StateError, Error: "bad\ntext"}} {
		if Valid(&Metrics{Stars: invalid}, now) {
			t.Fatalf("invalid observation accepted: %+v", invalid)
		}
	}
	if !Valid(&Metrics{Stars: Unsupported(), Forks: Unknown(time.Time{}, "not requested")}, now) || !Valid(nil, now) {
		t.Fatal("optional or capability observations rejected")
	}
}
