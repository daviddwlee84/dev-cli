package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
)

func TestGistRESTCommentsAndNodeIdentity(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitHub, nil)
	for _, value := range []string{"0", "null", `"bad"`} {
		body := strings.Replace(snippetGistJSON("abc"), `"id":"abc"`, `"id":"abc","node_id":"node-abc","comments":`+value, 1)
		item, err := p.parseGitHub([]byte(body))
		if err != nil || item.NodeID != "node-abc" {
			t.Fatalf("value=%s item=%+v err=%v", value, item, err)
		}
		switch value {
		case "0":
			assertMetric(t, item.Metrics.Comments, forgemetrics.StateKnown, metricValue(0))
		case "null":
			if item.Metrics.Comments != nil {
				t.Fatal("null became a comments measurement")
			}
		default:
			assertMetric(t, item.Metrics.Comments, forgemetrics.StateError, nil)
		}
	}
}

func TestGistMetricsPartialResponsePreservesIndependentCounts(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, command SnippetCommand) (SnippetCommandResult, error) {
		if command.Bin != "gh" || !strings.Contains(string(command.Stdin), "node(id:") || !strings.Contains(string(command.Stdin), "stargazerCount") || strings.Contains(string(command.Stdin), "nodes{") {
			t.Errorf("unexpected gist metrics query: %+v", command)
		}
		return snippetOK(`{"data":{"g0":{"id":"node-abc","name":"abc","url":"https://gist.github.example.com/alice/abc","owner":{"login":"alice"},"stargazerCount":0,"forks":{"totalCount":null},"comments":{"totalCount":0}}},"errors":[{"path":["g0","forks"],"message":"private raw diagnostic"}]}`), errors.New("exit 1")
	})
	item, err := p.parseGitHub([]byte(snippetGistJSON("abc")))
	if err != nil {
		t.Fatal(err)
	}
	item.NodeID = "node-abc"
	old := time.Now().UTC().Add(-time.Hour)
	item.Metrics.Forks = forgemetrics.Known(4, old)
	var got []snippet.Item
	if err := snippet.New(p).RefreshMetrics(t.Context(), []snippet.Item{item}, func(rows []snippet.Item) { got = append(got, rows...) }); err != nil || len(got) != 1 {
		t.Fatalf("rows=%+v err=%v", got, err)
	}
	assertMetric(t, got[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(0))
	assertMetric(t, got[0].Metrics.Comments, forgemetrics.StateKnown, metricValue(0))
	assertMetric(t, got[0].Metrics.Forks, forgemetrics.StateError, metricValue(4))
	if !got[0].Metrics.Forks.ObservedAt.Equal(old) {
		t.Fatal("failed fork query restamped cached data")
	}
}

func TestGistMetricsRejectsMismatchedNodeHostIDAndURL(t *testing.T) {
	for _, field := range []string{"id", "name", "url"} {
		t.Run(field, func(t *testing.T) {
			p := snippetFixtureProvider(snippet.GitHub, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
				resource := map[string]any{"id": "node-abc", "name": "abc", "url": "https://gist.github.example.com/alice/abc", "owner": map[string]string{"login": "alice"}, "stargazerCount": 99}
				resource[field] = "different"
				encoded, _ := json.Marshal(map[string]any{"data": map[string]any{"g0": resource}})
				return SnippetCommandResult{Stdout: encoded}, nil
			})
			item, _ := p.parseGitHub([]byte(snippetGistJSON("abc")))
			item.NodeID = "node-abc"
			var got []snippet.Item
			if err := p.RefreshMetrics(t.Context(), []snippet.Item{item}, func(rows []snippet.Item) { got = rows }); err != nil || len(got) != 1 {
				t.Fatalf("rows=%+v error=%v", got, err)
			}
			assertMetric(t, got[0].Metrics.Stars, forgemetrics.StateError, nil)
		})
	}
	for _, change := range []string{"missing-node", "different-host"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			p := snippetFixtureProvider(snippet.GitHub, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
				calls++
				return SnippetCommandResult{}, nil
			})
			item, _ := p.parseGitHub([]byte(snippetGistJSON("abc")))
			if change == "different-host" {
				item.NodeID, item.Host = "node-abc", "different.example.com"
			}
			if err := p.RefreshMetrics(t.Context(), []snippet.Item{item}, nil); err != nil || calls != 0 {
				t.Fatalf("unsafe identity made %d calls: %v", calls, err)
			}
		})
	}
}

func TestGistMetricsBoundedBatchesDoNotReadContentOrPaginate(t *testing.T) {
	calls := 0
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, command SnippetCommand) (SnippetCommandResult, error) {
		calls++
		if command.Args[1] != "graphql" || command.MaxOutputBytes != 2<<20 {
			t.Errorf("unexpected endpoint or missing output bound: %+v", command)
		}
		var body struct{ Query string }
		_ = json.Unmarshal(command.Stdin, &body)
		matches := regexp.MustCompile(`g\d+:node`).FindAllString(body.Query, -1)
		if len(matches) > MetricsBatchSize || len(matches) == 0 || strings.Contains(body.Query, "files") || strings.Contains(body.Query, "pageInfo") || strings.Contains(body.Query, "edges") {
			t.Errorf("unbounded/non-count query: %s", body.Query)
		}
		return snippetOK(`{"data":{}}`), nil
	})
	var items []snippet.Item
	for i := range 51 {
		item, _ := p.parseGitHub([]byte(snippetGistJSON(fmt.Sprintf("%x", i))))
		item.NodeID = fmt.Sprintf("node-%d", i)
		items = append(items, item)
	}
	got := 0
	if err := p.RefreshMetrics(t.Context(), items, func(rows []snippet.Item) { got += len(rows) }); err != nil || got != 51 || calls != 3 {
		t.Fatalf("rows=%d calls=%d err=%v", got, calls, err)
	}
}

func TestGitLabSnippetMetricsNeverRequestsComments(t *testing.T) {
	calls := 0
	p := snippetFixtureProvider(snippet.GitLab, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		calls++
		return SnippetCommandResult{}, nil
	})
	item, err := p.parseGitLab([]byte(snippetGitLabJSON(42, 0, "")))
	if err != nil {
		t.Fatal(err)
	}
	assertMetric(t, item.Metrics.Stars, forgemetrics.StateUnsupported, nil)
	assertMetric(t, item.Metrics.Comments, forgemetrics.StateUnknown, nil)
	if item.Metrics.Comments.Error != "not requested" {
		t.Fatal("comments scope is not explicit")
	}
	var got []snippet.Item
	if err := snippet.New(p).RefreshMetrics(t.Context(), []snippet.Item{item}, func(rows []snippet.Item) { got = rows }); err != nil || len(got) != 1 || calls != 0 {
		t.Fatalf("rows=%+v calls=%d err=%v", got, calls, err)
	}
	assertMetric(t, got[0].Metrics.Comments, forgemetrics.StateUnknown, nil)
}

func TestGistMetricsCancellationDiscardsLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	p := snippetFixtureProvider(snippet.GitHub, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		cancel()
		return snippetOK(`{"data":{}}`), nil
	})
	item, _ := p.parseGitHub([]byte(snippetGistJSON("abc")))
	item.NodeID = "node-abc"
	emits := 0
	if err := snippet.New(p).RefreshMetrics(ctx, []snippet.Item{item}, func([]snippet.Item) { emits++ }); !errors.Is(err, context.Canceled) || emits != 0 {
		t.Fatalf("late callbacks=%d err=%v", emits, err)
	}
}
