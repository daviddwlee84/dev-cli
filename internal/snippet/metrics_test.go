package snippet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
)

type observingProvider struct {
	*memoryProvider
	observe func(context.Context, []Item, func([]Item)) error
}

func (p *observingProvider) RefreshMetrics(ctx context.Context, rows []Item, emit func([]Item)) error {
	return p.observe(ctx, rows, emit)
}

func TestServiceMetricsBindsAllIdentityFieldsAndPreservesMetadata(t *testing.T) {
	item := sampleItem(GitHub, "abc")
	item.NodeID, item.Owner = "node-abc", "alice"
	provider := &observingProvider{memoryProvider: &memoryProvider{kind: GitHub, available: true}}
	provider.observe = func(_ context.Context, rows []Item, emit func([]Item)) error {
		var updates []Item
		for _, field := range []string{"node", "host", "url", "owner", "id"} {
			wrong := rows[0]
			wrong.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(99, time.Now().UTC())}
			switch field {
			case "node":
				wrong.NodeID = "other"
			case "host":
				wrong.Host = "other.example.com"
			case "url":
				wrong.URL = "https://other.example.com/abc"
			case "owner":
				wrong.Owner = "bob"
			case "id":
				wrong.ID = "def"
			}
			updates = append(updates, wrong)
		}
		right := rows[0]
		right.Title, right.FilesComplete = "unexpected metadata edit", false
		right.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(0, time.Now().UTC())}
		emit(append(updates, right))
		return nil
	}
	var got []Item
	if err := New(provider).RefreshMetrics(t.Context(), []Item{item}, func(rows []Item) { got = append(got, rows...) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != item.Title || got[0].FilesComplete != item.FilesComplete || *got[0].Metrics.Stars.Value != 0 {
		t.Fatalf("unbound callback or metadata replacement: %+v", got)
	}
	if item.Metrics != nil {
		t.Fatal("refresh mutated its caller's item")
	}
}

func TestServiceMetricsFailureRetainsMeasurementWithoutFailingInventory(t *testing.T) {
	item := sampleItem(GitHub, "abc")
	old := time.Now().UTC().Add(-time.Hour)
	item.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(6, old)}
	provider := &observingProvider{memoryProvider: &memoryProvider{kind: GitHub, available: true},
		observe: func(context.Context, []Item, func([]Item)) error { return errors.New("raw provider failure") }}
	var got []Item
	if err := New(provider).RefreshMetrics(t.Context(), []Item{item}, func(rows []Item) { got = rows }); err != nil || len(got) != 1 {
		t.Fatalf("metrics failed inventory: %+v %v", got, err)
	}
	count := got[0].Metrics.Stars
	if count.State != forgemetrics.StateError || *count.Value != 6 || !count.ObservedAt.Equal(old) {
		t.Fatalf("lost prior measurement: %+v", count)
	}
}
