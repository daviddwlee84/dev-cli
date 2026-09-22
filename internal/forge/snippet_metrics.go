package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
)

func (p *snippetProvider) RefreshMetrics(ctx context.Context, items []snippet.Item, emit func([]snippet.Item)) error {
	ctx, cancel := context.WithTimeout(ctx, snippet.OperationTimeout)
	defer cancel()
	publish := func(rows []snippet.Item) {
		if ctx.Err() == nil && emit != nil && len(rows) > 0 {
			emit(rows)
		}
	}
	var pending, immediate []snippet.Item
	for _, item := range items {
		item.Metrics = forgemetrics.Clone(item.Metrics)
		switch {
		case item.Forge != p.kind || !strings.EqualFold(item.Host, p.host) || p.validateItem(item) != nil:
			item.Metrics = forgemetrics.Merge(item.Metrics, failedGistMetrics("snippet identity did not match"))
			immediate = append(immediate, item)
		case p.kind == snippet.GitLab:
			item.Metrics = snippet.GitLabMetrics()
			immediate = append(immediate, item)
		case item.NodeID == "" || len(item.NodeID) > 2048 || containsControl(item.NodeID):
			item.Metrics = forgemetrics.Merge(item.Metrics, failedGistMetrics("gist node identity is unavailable"))
			immediate = append(immediate, item)
		case !p.Available():
			item.Metrics = forgemetrics.Merge(item.Metrics, failedGistMetrics("metrics provider is unavailable"))
			immediate = append(immediate, item)
		default:
			pending = append(pending, item)
		}
	}
	publish(immediate)
	for start := 0; start < len(pending) && ctx.Err() == nil; start += MetricsBatchSize {
		batch := append([]snippet.Item(nil), pending[start:min(start+MetricsBatchSize, len(pending))]...)
		var query strings.Builder
		query.WriteString("query {")
		for i, item := range batch {
			fmt.Fprintf(&query, " g%d:node(id:%s){... on Gist{id name url owner{login} stargazerCount forks(first:1){totalCount} comments(first:1){totalCount}}}", i, graphQLString(item.NodeID))
		}
		query.WriteString(" }")
		response, err := requestMetricsGraphQL(ctx, p.runner, GitHub, p.host, query.String())
		for i := range batch {
			metrics := parseGistMetrics(batch[i], response, fmt.Sprintf("g%d", i), err)
			batch[i].Metrics = forgemetrics.Merge(batch[i].Metrics, metrics)
		}
		publish(batch)
		if errors.Is(err, errMetricsRateLimited) {
			for rest := start + len(batch); rest < len(pending); rest += MetricsBatchSize {
				patch := append([]snippet.Item(nil), pending[rest:min(rest+MetricsBatchSize, len(pending))]...)
				for i := range patch {
					patch[i].Metrics = forgemetrics.Merge(patch[i].Metrics, failedGistMetrics("statistics rate limited"))
				}
				publish(patch)
			}
			return err
		}
	}
	return ctx.Err()
}

func failedGistMetrics(reason string) *forgemetrics.Metrics {
	now := time.Now().UTC()
	return &forgemetrics.Metrics{Stars: forgemetrics.Failed(now, reason), Forks: forgemetrics.Failed(now, reason), Comments: forgemetrics.Failed(now, reason), OpenIssues: forgemetrics.Unsupported(), OpenPRs: forgemetrics.Unsupported()}
}

func parseGistMetrics(item snippet.Item, response metricsGraphQLResponse, alias string, requestErr error) *forgemetrics.Metrics {
	raw := response.Data[alias]
	var identity struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		URL   string `json:"url"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if json.Unmarshal(raw, &identity) != nil || len(raw) == 0 || string(raw) == "null" {
		return failedGistMetrics("gist metrics are unavailable")
	}
	if identity.ID != item.NodeID || identity.Name != item.ID || identity.URL != item.URL ||
		(item.Owner != "" && identity.Owner.Login != item.Owner) ||
		graphQLPathFailed(response, alias, "id") || graphQLPathFailed(response, alias, "name") ||
		graphQLPathFailed(response, alias, "url") || graphQLPathFailed(response, alias, "owner") {
		return failedGistMetrics("gist metrics identity did not match")
	}
	count := func(path ...string) *forgemetrics.Count {
		return graphQLCount(response, alias, raw, requestErr, path...)
	}
	return &forgemetrics.Metrics{Stars: count("stargazerCount"), Forks: count("forks", "totalCount"), Comments: count("comments", "totalCount"), OpenIssues: forgemetrics.Unsupported(), OpenPRs: forgemetrics.Unsupported()}
}
