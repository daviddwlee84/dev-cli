package snippet

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
)

// MetricsProvider is optional: metrics never affect snippet inventory support,
// publication capability or completeness.
type MetricsProvider interface {
	RefreshMetrics(context.Context, []Item, func([]Item)) error
}

// MetricsKey also binds the resource URL, project path, owner and opaque node
// ID, so a refreshed inventory cannot inherit another resource's observations.
func MetricsKey(item Item) string {
	if ValidateIdentity(item.Identity) != nil || item.URL == "" || strings.ContainsRune(item.URL+item.NodeID+item.Owner, '\x00') {
		return ""
	}
	return item.Key() + "\x00" + item.Project + "\x00" + item.URL + "\x00" + item.NodeID + "\x00" + item.Owner
}

// RefreshMetrics observes only supplied items. Provider/host groups run in
// parallel and each provider bounds its own requests. Callbacks are serialized,
// identity checked, and suppressed after cancellation. Failures remain count
// observations and do not turn a usable inventory into a failed list.
func (s *Service) RefreshMetrics(ctx context.Context, items []Item, emit func([]Item)) error {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	groups := map[string][]Item{}
	for _, item := range items {
		key := string(item.Forge) + "\x00" + strings.ToLower(item.Host)
		item.Metrics = forgemetrics.Clone(item.Metrics)
		groups[key] = append(groups[key], item)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, rows := range groups {
		wg.Go(func() {
			expected := map[string]Item{}
			for _, item := range rows {
				if key := MetricsKey(item); key != "" {
					expected[key] = item
				}
			}
			publish := func(updates []Item) {
				mu.Lock()
				defer mu.Unlock()
				if ctx.Err() != nil || emit == nil {
					return
				}
				var result []Item
				for _, update := range updates {
					previous, ok := expected[MetricsKey(update)]
					if !ok || !forgemetrics.Valid(update.Metrics, time.Now().UTC()) {
						continue
					}
					previous.Metrics = forgemetrics.Merge(previous.Metrics, update.Metrics)
					expected[MetricsKey(update)] = previous
					result = append(result, previous)
				}
				if len(result) > 0 {
					emit(result)
				}
			}
			for _, provider := range s.providers {
				if provider.Kind() != rows[0].Forge || !strings.EqualFold(provider.Host(), rows[0].Host) {
					continue
				}
				if observer, ok := provider.(MetricsProvider); ok {
					if err := observer.RefreshMetrics(ctx, rows, publish); err == nil || ctx.Err() != nil {
						return
					}
				}
				break
			}
			now := time.Now().UTC()
			for i := range rows {
				if rows[i].Forge == GitLab {
					rows[i].Metrics = GitLabMetrics()
				} else {
					rows[i].Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Failed(now, "metrics provider is unavailable"), Forks: forgemetrics.Failed(now, "metrics provider is unavailable"), Comments: forgemetrics.Failed(now, "metrics provider is unavailable"), OpenIssues: forgemetrics.Unsupported(), OpenPRs: forgemetrics.Unsupported()}
				}
			}
			publish(rows)
		})
	}
	wg.Wait()
	return ctx.Err()
}

// GitLabMetrics records the v1 scope explicitly. Comments are supported by the
// provider but are deliberately not requested by the metrics enrichment path.
func GitLabMetrics() *forgemetrics.Metrics {
	return &forgemetrics.Metrics{Stars: forgemetrics.Unsupported(), Forks: forgemetrics.Unsupported(),
		OpenIssues: forgemetrics.Unsupported(), OpenPRs: forgemetrics.Unsupported(),
		Comments: forgemetrics.Unknown(time.Time{}, "not requested")}
}
