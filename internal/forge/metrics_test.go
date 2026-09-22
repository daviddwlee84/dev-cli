package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
)

func metricRepo(kind Kind, host, name string) RemoteRepo {
	return RemoteRepo{Forge: kind, Name: name, FullName: "owner/" + name, URL: "https://" + host + "/owner/" + name}
}

func assertMetric(t *testing.T, count *forgemetrics.Count, state forgemetrics.State, value *int64) {
	t.Helper()
	if count == nil || count.State != state || !reflect.DeepEqual(count.Value, value) {
		t.Fatalf("count=%+v; want %s value=%v", count, state, value)
	}
}

func metricValue(value int64) *int64 { return &value }

func TestRepoMetricsRateLimitStopsRemainingRequests(t *testing.T) {
	var rows []RemoteRepo
	for i := 0; i < 51; i++ {
		rows = append(rows, metricRepo(GitHub, "github.example.com", fmt.Sprintf("r%d", i)))
	}
	calls, observed := 0, 0
	err := RefreshRepoMetricsWithRunner(t.Context(), rows, func(patch []RemoteRepo) {
		observed += len(patch)
		for _, row := range patch {
			if row.Metrics.OpenPRs == nil || row.Metrics.OpenPRs.Value != nil {
				t.Fatal("rate limited count became a number")
			}
		}
	}, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		calls++
		return snippetOK(`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`), errors.New("exit 1")
	})
	if !errors.Is(err, errMetricsRateLimited) || calls != 1 || observed != 51 {
		t.Fatalf("calls=%d observed=%d err=%v", calls, observed, err)
	}
}

func TestRepoRESTMetricsPreserveMissingAndZero(t *testing.T) {
	github, err := parseGitHubRepos(`[{"name":"a","full_name":"owner/a","stargazers_count":0,"forks_count":3,"open_issues_count":99},{"name":"b","full_name":"owner/b","forks_count":null},{"name":"c","full_name":"owner/c","stargazers_count":"invalid"}]`)
	if err != nil {
		t.Fatal(err)
	}
	assertMetric(t, github[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(0))
	assertMetric(t, github[0].Metrics.Forks, forgemetrics.StateKnown, metricValue(3))
	if github[0].Metrics.OpenIssues != nil || github[0].Metrics.OpenPRs != nil || github[1].Metrics.Stars != nil || github[1].Metrics.Forks != nil {
		t.Fatal("REST issue+PR aggregate or missing native counts were adopted")
	}
	assertMetric(t, github[2].Metrics.Stars, forgemetrics.StateError, nil)
	gitlab, err := parseGitLabRepos(`[{"name":"a","path_with_namespace":"owner/a","star_count":0,"forks_count":4}]`)
	if err != nil {
		t.Fatal(err)
	}
	assertMetric(t, gitlab[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(0))
	assertMetric(t, gitlab[0].Metrics.Forks, forgemetrics.StateKnown, metricValue(4))
}

func TestRepoMetricsPartialGraphQLDataAndFailedField(t *testing.T) {
	repo := metricRepo(GitHub, "github.example.com", "a")
	old := time.Now().UTC().Add(-time.Hour)
	repo.Metrics = &forgemetrics.Metrics{OpenIssues: forgemetrics.Known(11, old)}
	var got []RemoteRepo
	err := RefreshRepoMetricsWithRunner(t.Context(), []RemoteRepo{repo}, func(rows []RemoteRepo) { got = append(got, rows...) },
		func(ctx context.Context, command SnippetCommand) (SnippetCommandResult, error) {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute || command.Bin != "gh" || !slices.Contains(command.Args, "github.example.com") || slices.Contains(command.Args, "--paginate") {
				t.Errorf("unbounded or unbound command: %+v", command)
			}
			var body struct{ Query string }
			if json.Unmarshal(command.Stdin, &body) != nil || !strings.Contains(body.Query, "issues(first:1,states:OPEN){totalCount}") || !strings.Contains(body.Query, "pullRequests(first:1,states:OPEN){totalCount}") {
				t.Errorf("wrong query: %s", command.Stdin)
			}
			return snippetOK(`{"data":{"r0":{"nameWithOwner":"owner/a","url":"https://github.example.com/owner/a","stargazerCount":0,"forkCount":null,"issues":{"totalCount":0},"pullRequests":{"totalCount":2}}},"errors":[{"path":["r0","issues","totalCount"],"message":"private detail must not be retained"}]}`), errors.New("exit 1")
		})
	if err != nil || len(got) != 1 {
		t.Fatalf("rows=%+v error=%v", got, err)
	}
	assertMetric(t, got[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(0))
	assertMetric(t, got[0].Metrics.OpenPRs, forgemetrics.StateKnown, metricValue(2))
	assertMetric(t, got[0].Metrics.OpenIssues, forgemetrics.StateError, metricValue(11))
	assertMetric(t, got[0].Metrics.Forks, forgemetrics.StateError, nil)
	if !got[0].Metrics.OpenIssues.ObservedAt.Equal(old) || strings.Contains(got[0].Metrics.OpenIssues.Error, "private") {
		t.Fatal("failed refresh restamped old data or retained raw provider diagnostics")
	}
}

func TestRepoMetricsGitLabScalarCountsAndIdentity(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			repo := metricRepo(GitLab, "gitlab.example.com", "a")
			var got []RemoteRepo
			err := RefreshRepoMetricsWithRunner(t.Context(), []RemoteRepo{repo}, func(rows []RemoteRepo) { got = rows }, func(_ context.Context, command SnippetCommand) (SnippetCommandResult, error) {
				if command.Bin != "glab" || !strings.Contains(string(command.Stdin), "openMergeRequestsCount") || strings.Contains(string(command.Stdin), "mergeRequests{") {
					t.Errorf("wrong GitLab scalar query: %+v", command)
				}
				host := "gitlab.example.com"
				if mismatch {
					host = "different.example.com"
				}
				return snippetOK(fmt.Sprintf(`{"data":{"r0":{"fullPath":"owner/a","webUrl":"https://%s/owner/a","starCount":0,"forksCount":1,"openIssuesCount":2,"openMergeRequestsCount":3}}}`, host)), nil
			})
			if err != nil || len(got) != 1 {
				t.Fatalf("rows=%+v err=%v", got, err)
			}
			if mismatch {
				assertMetric(t, got[0].Metrics.Stars, forgemetrics.StateError, nil)
			} else {
				assertMetric(t, got[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(0))
				assertMetric(t, got[0].Metrics.OpenIssues, forgemetrics.StateKnown, metricValue(2))
				assertMetric(t, got[0].Metrics.OpenPRs, forgemetrics.StateKnown, metricValue(3))
			}
		})
	}
}

func TestRepoMetricsBoundedBatchesParallelHostsAndNoUnsupportedRequests(t *testing.T) {
	var repos []RemoteRepo
	for _, host := range []string{"first.example.com", "second.example.com"} {
		for i := range 26 {
			repos = append(repos, metricRepo(GitHub, host, fmt.Sprint(i)))
		}
	}
	repos = append(repos, RemoteRepo{Forge: AzureDevOps})
	var mu sync.Mutex
	calls := map[string]int{}
	active := map[string]int{}
	var all []RemoteRepo
	started := make(chan struct{}, 2)
	var release sync.Once
	gate := make(chan struct{})
	err := RefreshRepoMetricsWithRunner(t.Context(), repos, func(rows []RemoteRepo) { all = append(all, rows...) }, func(ctx context.Context, command SnippetCommand) (SnippetCommandResult, error) {
		host := command.Args[slices.Index(command.Args, "--hostname")+1]
		mu.Lock()
		calls[host]++
		active[host]++
		if active[host] != 1 {
			t.Error("one host had concurrent metrics batches")
		}
		first := calls[host] == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
			if len(started) == 2 {
				release.Do(func() { close(gate) })
			}
			select {
			case <-gate:
			case <-ctx.Done():
				return SnippetCommandResult{}, ctx.Err()
			}
		}
		var body struct{ Query string }
		_ = json.Unmarshal(command.Stdin, &body)
		matches := regexp.MustCompile(`r\d+:repository`).FindAllString(body.Query, -1)
		if len(matches) < 1 || len(matches) > MetricsBatchSize || strings.Contains(body.Query, "nodes{") || strings.Contains(body.Query, "pageInfo") {
			t.Errorf("unbounded query: %s", body.Query)
		}
		mu.Lock()
		active[host]--
		mu.Unlock()
		return snippetOK(`{"data":{}}`), nil
	})
	if err != nil || len(all) != 53 || calls["first.example.com"] != 2 || calls["second.example.com"] != 2 {
		t.Fatalf("rows=%d calls=%v error=%v", len(all), calls, err)
	}
}

func TestRepoMetricsCancellationSuppressesLateEmissions(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	count := 0
	err := RefreshRepoMetricsWithRunner(ctx, []RemoteRepo{metricRepo(GitHub, "github.com", "a")}, func([]RemoteRepo) { count++ }, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		cancel()
		return snippetOK(`{"data":{"r0":{"nameWithOwner":"owner/a","url":"https://github.com/owner/a","stargazerCount":7}}}`), nil
	})
	if !errors.Is(err, context.Canceled) || count != 0 {
		t.Fatalf("late emissions=%d err=%v", count, err)
	}
}

func TestMetricMissingNullAndMalformedNeverBecomeZero(t *testing.T) {
	for _, value := range []string{`{}`, `{"x":null}`, `{"x":-1}`, `{"x":"0"}`, `{"x":0.5}`} {
		count := graphQLCount(metricsGraphQLResponse{}, "r0", json.RawMessage(value), nil, "x")
		if count.Value != nil || count.State == forgemetrics.StateKnown {
			t.Fatalf("%s => %+v", value, count)
		}
	}
}

func TestMergeCachedRepoMetricsPreservesInventoryAndSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	now := time.Now().UTC()
	repo := metricRepo(GitHub, "github.com", "a")
	repo.Description = "new inventory description"
	cache := Cache{Version: CacheVersion, SourceID: "current", FetchedAt: now.Add(-time.Hour), Complete: false,
		Providers: map[Kind]ProviderStatus{GitHub: {FetchedAt: now.Add(-time.Hour), Complete: false, Error: "partial inventory"}}, Repos: []RemoteRepo{repo}}
	if err := SaveCacheState(path, cache); err != nil {
		t.Fatal(err)
	}
	old := repo
	old.Description = "old description"
	old.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(8, now)}
	foreign := metricRepo(GitHub, "different.example.com", "a")
	foreign.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(99, now)}
	removed := metricRepo(GitHub, "github.com", "removed")
	removed.Metrics = old.Metrics
	if err := MergeCachedRepoMetrics(t.Context(), path, "current", []RemoteRepo{old, foreign, removed}); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadCacheAny(path)
	if !ok || len(got.Repos) != 1 || got.Repos[0].Description != repo.Description || got.Complete || !got.FetchedAt.Equal(cache.FetchedAt) || !reflect.DeepEqual(got.Providers, cache.Providers) {
		t.Fatalf("inventory was overwritten: %+v", got)
	}
	assertMetric(t, got.Repos[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(8))
	before, _ := os.ReadFile(path)
	if err := MergeCachedRepoMetrics(t.Context(), path, "old-source", []RemoteRepo{foreign}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := MergeCachedRepoMetrics(ctx, path, "current", []RemoteRepo{old}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled merge: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source-mismatched or canceled merge rewrote cache")
	}
}

func TestMergeCachedRepoMetricsReloadsUnderPublicationLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	repo := metricRepo(GitHub, "github.com", "a")
	newer := repo
	newer.Description = "new generation"
	cache := Cache{Version: CacheVersion, SourceID: "source", FetchedAt: time.Now().UTC(), Complete: true, Repos: []RemoteRepo{newer}}
	lock := cacheWriteLock(path)
	lock.Lock()
	complete := make(chan error, 1)
	repo.Metrics = &forgemetrics.Metrics{Stars: forgemetrics.Known(3, time.Now().UTC())}
	go func() { complete <- MergeCachedRepoMetrics(t.Context(), path, "source", []RemoteRepo{repo}) }()
	if err := saveCacheStateLocked(t.Context(), path, cache); err != nil {
		lock.Unlock()
		t.Fatal(err)
	}
	lock.Unlock()
	if err := <-complete; err != nil {
		t.Fatal(err)
	}
	got, _ := LoadCacheAny(path)
	if got.Repos[0].Description != "new generation" {
		t.Fatal("late metrics batch restored a stale inventory")
	}
	assertMetric(t, got.Repos[0].Metrics.Stars, forgemetrics.StateKnown, metricValue(3))
}
