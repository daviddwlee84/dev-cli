package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
)

const MetricsBatchSize = 25

var errMetricsRateLimited = errors.New("statistics rate limited; refresh to retry")

func restMetricCount(raw json.RawMessage, observedAt time.Time) *forgemetrics.Count {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return forgemetrics.Failed(observedAt, "provider returned an invalid count")
	}
	return forgemetrics.Known(value, observedAt)
}

// RepoMetricsKey binds observations to the provider, web host and exact
// repository path. Invalid or inconsistent inventory identities cannot carry
// observations into another row.
func RepoMetricsKey(repo RemoteRepo) string {
	u, err := url.Parse(repo.URL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	web, ok := webURLFromExact(repo)
	if !ok {
		return ""
	}
	u, _ = url.Parse(web.URL)
	return string(repo.Forge) + "\x00" + strings.ToLower(u.Host) + "\x00" + repo.FullName
}

// RefreshRepoMetrics enriches already-selected inventory without changing its
// membership or coverage. Provider failures become per-count observations;
// only cancellation ends the operation with an error. Emissions are serialized.
func RefreshRepoMetrics(ctx context.Context, repos []RemoteRepo, emit func([]RemoteRepo)) error {
	return RefreshRepoMetricsWithRunner(ctx, repos, emit, runSnippetCommand)
}

func RefreshRepoMetricsWithRunner(ctx context.Context, repos []RemoteRepo, emit func([]RemoteRepo), runner SnippetRunner) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	groups := map[string][]RemoteRepo{}
	var immediate []RemoteRepo
	for _, repo := range repos {
		repo.Metrics = forgemetrics.Clone(repo.Metrics)
		key := RepoMetricsKey(repo)
		if repo.Forge != GitHub && repo.Forge != GitLab {
			repo.Metrics = unsupportedRepoMetrics()
			immediate = append(immediate, repo)
		} else if key == "" {
			repo.Metrics = forgemetrics.Merge(repo.Metrics, failedRepoMetrics("repository identity is unavailable"))
			immediate = append(immediate, repo)
		} else {
			parts := strings.Split(key, "\x00")
			group := parts[0] + "\x00" + parts[1]
			groups[group] = append(groups[group], repo)
		}
	}
	var emitMu sync.Mutex
	publish := func(rows []RemoteRepo) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if ctx.Err() == nil && emit != nil && len(rows) > 0 {
			emit(rows)
		}
	}
	publish(immediate)
	var wg sync.WaitGroup
	var limitedMu sync.Mutex
	var limited bool
	for key, rows := range groups {
		wg.Go(func() {
			host := strings.Split(key, "\x00")[1]
			for start := 0; start < len(rows) && ctx.Err() == nil; start += MetricsBatchSize {
				end := min(start+MetricsBatchSize, len(rows))
				batch := append([]RemoteRepo(nil), rows[start:end]...)
				query := repoMetricsQuery(batch)
				response, err := requestMetricsGraphQL(ctx, runner, batch[0].Forge, host, query)
				for i := range batch {
					alias := fmt.Sprintf("r%d", i)
					observed := parseRepoMetrics(batch[i], response, alias, err)
					batch[i].Metrics = forgemetrics.Merge(batch[i].Metrics, observed)
				}
				publish(batch)
				if errors.Is(err, errMetricsRateLimited) {
					limitedMu.Lock()
					limited = true
					limitedMu.Unlock()
					for rest := end; rest < len(rows); rest += MetricsBatchSize {
						patch := append([]RemoteRepo(nil), rows[rest:min(rest+MetricsBatchSize, len(rows))]...)
						for i := range patch {
							patch[i].Metrics = forgemetrics.Merge(patch[i].Metrics, failedRepoMetrics("statistics rate limited"))
						}
						publish(patch)
					}
					break
				}
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if limited {
		return errMetricsRateLimited
	}
	return nil
}

func unsupportedRepoMetrics() *forgemetrics.Metrics {
	return &forgemetrics.Metrics{Stars: forgemetrics.Unsupported(), Forks: forgemetrics.Unsupported(),
		OpenIssues: forgemetrics.Unsupported(), OpenPRs: forgemetrics.Unsupported(), Comments: forgemetrics.Unsupported()}
}

func failedRepoMetrics(reason string) *forgemetrics.Metrics {
	now := time.Now().UTC()
	return &forgemetrics.Metrics{Stars: forgemetrics.Failed(now, reason), Forks: forgemetrics.Failed(now, reason),
		OpenIssues: forgemetrics.Failed(now, reason), OpenPRs: forgemetrics.Failed(now, reason), Comments: forgemetrics.Unsupported()}
}

func repoMetricsQuery(rows []RemoteRepo) string {
	var query strings.Builder
	query.WriteString("query {")
	for i, repo := range rows {
		if repo.Forge == GitHub {
			owner, name, _ := strings.Cut(repo.FullName, "/")
			fmt.Fprintf(&query, " r%d:repository(owner:%s,name:%s){nameWithOwner url stargazerCount forkCount issues(first:1,states:OPEN){totalCount} pullRequests(first:1,states:OPEN){totalCount}}", i, graphQLString(owner), graphQLString(name))
		} else {
			fmt.Fprintf(&query, " r%d:project(fullPath:%s){fullPath webUrl starCount forksCount openIssuesCount openMergeRequestsCount}", i, graphQLString(repo.FullName))
		}
	}
	query.WriteString(" }")
	return query.String()
}

func graphQLString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type metricsGraphQLError struct {
	Path       []any  `json:"path"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type metricsGraphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []metricsGraphQLError      `json:"errors"`
}

func requestMetricsGraphQL(ctx context.Context, runner SnippetRunner, kind Kind, host, query string) (metricsGraphQLResponse, error) {
	var response metricsGraphQLResponse
	if err := snippet.ValidateHost(host); err != nil {
		return response, errors.New("invalid metrics endpoint")
	}
	if runner == nil {
		return response, errors.New("metrics transport is unavailable")
	}
	body, _ := json.Marshal(map[string]string{"query": query})
	bin := "gh"
	if kind == GitLab {
		bin = "glab"
	}
	result, runErr := runner(ctx, SnippetCommand{Bin: bin,
		Args:  []string{"api", "graphql", "--hostname", host, "--method", "POST", "--input", "-", "--header", "Content-Type: application/json"},
		Stdin: body, MaxOutputBytes: 2 << 20})
	if result.Overflow || len(result.Stdout) > 2<<20 {
		return response, errors.New("metrics response exceeds the read limit")
	}
	decodeErr := json.Unmarshal(result.Stdout, &response)
	limited := strings.ToLower(string(result.Stderr))
	for _, issue := range response.Errors {
		limited += " " + strings.ToLower(issue.Type+" "+issue.Message+" "+issue.Extensions.Code)
	}
	if strings.Contains(limited, "rate limit") || strings.Contains(limited, "rate_limit") || strings.Contains(limited, "too many requests") || strings.Contains(limited, "http 429") {
		return response, errMetricsRateLimited
	}
	if decodeErr != nil {
		return metricsGraphQLResponse{}, errors.New("metrics response is unavailable")
	}
	// gh/glab can exit nonzero while returning useful partial GraphQL data.
	if runErr != nil {
		return response, errors.New("metrics request failed")
	}
	return response, nil
}

func parseRepoMetrics(repo RemoteRepo, response metricsGraphQLResponse, alias string, requestErr error) *forgemetrics.Metrics {
	raw := response.Data[alias]
	var identity struct {
		NameWithOwner string `json:"nameWithOwner"`
		URL           string `json:"url"`
		FullPath      string `json:"fullPath"`
		WebURL        string `json:"webUrl"`
	}
	if json.Unmarshal(raw, &identity) != nil || len(raw) == 0 || string(raw) == "null" {
		return failedRepoMetrics("repository metrics are unavailable")
	}
	observed := repo
	observed.FullName, observed.URL = identity.NameWithOwner, identity.URL
	if repo.Forge == GitLab {
		observed.FullName, observed.URL = identity.FullPath, identity.WebURL
	}
	if RepoMetricsKey(repo) == "" || RepoMetricsKey(observed) != RepoMetricsKey(repo) ||
		graphQLPathFailed(response, alias, "nameWithOwner") || graphQLPathFailed(response, alias, "url") ||
		graphQLPathFailed(response, alias, "fullPath") || graphQLPathFailed(response, alias, "webUrl") {
		return failedRepoMetrics("repository metrics identity did not match")
	}
	count := func(path ...string) *forgemetrics.Count {
		return graphQLCount(response, alias, raw, requestErr, path...)
	}
	if repo.Forge == GitLab {
		return &forgemetrics.Metrics{Stars: count("starCount"), Forks: count("forksCount"), OpenIssues: count("openIssuesCount"), OpenPRs: count("openMergeRequestsCount"), Comments: forgemetrics.Unsupported()}
	}
	return &forgemetrics.Metrics{Stars: count("stargazerCount"), Forks: count("forkCount"), OpenIssues: count("issues", "totalCount"), OpenPRs: count("pullRequests", "totalCount"), Comments: forgemetrics.Unsupported()}
}

func graphQLPathFailed(response metricsGraphQLResponse, alias string, path ...string) bool {
	wanted := append([]string{alias}, path...)
	for _, issue := range response.Errors {
		if len(issue.Path) == 0 {
			continue
		}
		matches := true
		for i := 0; i < min(len(issue.Path), len(wanted)); i++ {
			part, ok := issue.Path[i].(string)
			if !ok || part != wanted[i] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func graphQLCount(response metricsGraphQLResponse, alias string, raw json.RawMessage, requestErr error, path ...string) *forgemetrics.Count {
	now := time.Now().UTC()
	if graphQLPathFailed(response, alias, path...) {
		return forgemetrics.Failed(now, "provider could not return this count")
	}
	for _, field := range path {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return forgemetrics.Failed(now, "provider returned an invalid count")
		}
		raw = object[field]
		if len(raw) == 0 || string(raw) == "null" {
			if requestErr != nil || len(response.Errors) > 0 {
				return forgemetrics.Failed(now, "provider could not return this count")
			}
			return forgemetrics.Unknown(now, "provider did not return this count")
		}
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil {
		return forgemetrics.Failed(now, "provider returned an invalid count")
	}
	return forgemetrics.Known(value, now)
}
