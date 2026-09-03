package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type reviewFixtureProvider struct {
	name           string
	kind           Kind
	adapter        func() Forge
	query          ReviewQuery
	item           func(state ReviewState, number int, head, base string) map[string]any
	expectedID     func(number int) string
	expectedURL    func(number int) string
	criticalFields []struct {
		name string
		keys []string
	}
	stateKey string
}

func reviewFixtureProviders() []reviewFixtureProvider {
	return []reviewFixtureProvider{
		{
			name: "github", kind: GitHub,
			adapter: func() Forge { adapter, _ := For(GitHub); return adapter },
			query:   ReviewQuery{Repository: "acme/widget", Head: "feat/review", Base: "main"},
			item: func(state ReviewState, number int, head, base string) map[string]any {
				rawState := map[ReviewState]string{
					ReviewOpen: "OPEN", ReviewDraft: "OPEN", ReviewMerged: "MERGED", ReviewClosed: "CLOSED",
				}[state]
				return map[string]any{
					"id": fmt.Sprintf("PR_node_%d", number), "number": number,
					"state": rawState, "isDraft": state == ReviewDraft,
					"url":         fmt.Sprintf("https://github.example/acme/widget/pull/%d", number),
					"headRefName": head, "baseRefName": base,
					"headRepository": map[string]any{"nameWithOwner": "acme/widget"},
				}
			},
			expectedID:  func(number int) string { return fmt.Sprintf("PR_node_%d", number) },
			expectedURL: func(number int) string { return fmt.Sprintf("https://github.example/acme/widget/pull/%d", number) },
			criticalFields: []struct {
				name string
				keys []string
			}{
				{name: "identifier", keys: []string{"id"}},
				{name: "number", keys: []string{"number"}},
				{name: "state", keys: []string{"state"}},
				{name: "draft", keys: []string{"isDraft"}},
				{name: "URL", keys: []string{"url"}},
				{name: "head", keys: []string{"headRefName"}},
				{name: "base", keys: []string{"baseRefName"}},
				{name: "source repository", keys: []string{"headRepository"}},
			},
			stateKey: "state",
		},
		{
			name: "gitlab", kind: GitLab,
			adapter: func() Forge { adapter, _ := For(GitLab); return adapter },
			query:   ReviewQuery{Repository: "acme/widget", Head: "feat/review", Base: "main"},
			item: func(state ReviewState, number int, head, base string) map[string]any {
				rawState := map[ReviewState]string{
					ReviewOpen: "opened", ReviewDraft: "opened", ReviewMerged: "merged", ReviewClosed: "closed",
				}[state]
				draft := state == ReviewDraft
				return map[string]any{
					"id": 9000 + number, "iid": number, "state": rawState,
					"draft": draft, "work_in_progress": draft,
					"web_url":       fmt.Sprintf("https://gitlab.example/acme/widget/-/merge_requests/%d", number),
					"source_branch": head, "target_branch": base,
					"source_project_id": 100, "target_project_id": 100,
				}
			},
			expectedID: func(number int) string { return fmt.Sprint(9000 + number) },
			expectedURL: func(number int) string {
				return fmt.Sprintf("https://gitlab.example/acme/widget/-/merge_requests/%d", number)
			},
			criticalFields: []struct {
				name string
				keys []string
			}{
				{name: "identifier", keys: []string{"id"}},
				{name: "number", keys: []string{"iid"}},
				{name: "state", keys: []string{"state"}},
				{name: "draft", keys: []string{"draft", "work_in_progress"}},
				{name: "URL", keys: []string{"web_url"}},
				{name: "head", keys: []string{"source_branch"}},
				{name: "base", keys: []string{"target_branch"}},
				{name: "source repository", keys: []string{"source_project_id", "target_project_id"}},
			},
			stateKey: "state",
		},
		{
			name: "azure-devops", kind: AzureDevOps,
			adapter: func() Forge {
				return NewAzureDevOps([]AzureDevOpsTarget{{
					Organization: "https://dev.azure.com/acme/", Project: "Platform",
				}})
			},
			query: ReviewQuery{Repository: "acme/Platform/widget", Head: "feat/review", Base: "main"},
			item: func(state ReviewState, number int, head, base string) map[string]any {
				rawState := map[ReviewState]string{
					ReviewOpen: "active", ReviewDraft: "active", ReviewMerged: "completed", ReviewClosed: "abandoned",
				}[state]
				return map[string]any{
					"pullRequestId": number, "status": rawState, "isDraft": state == ReviewDraft,
					"remoteUrl":           fmt.Sprintf("https://dev.azure.com/acme/Platform/_git/widget/pullrequest/%d", number),
					"repositoryRemoteUrl": "https://dev.azure.com/acme/Platform/_git/widget",
					"sourceRefName":       "refs/heads/" + head, "targetRefName": "refs/heads/" + base,
					"forkSource": nil,
				}
			},
			expectedID: func(number int) string { return fmt.Sprint(number) },
			expectedURL: func(number int) string {
				return fmt.Sprintf("https://dev.azure.com/acme/Platform/_git/widget/pullrequest/%d", number)
			},
			criticalFields: []struct {
				name string
				keys []string
			}{
				{name: "identifier", keys: []string{"pullRequestId"}},
				{name: "state", keys: []string{"status"}},
				{name: "draft", keys: []string{"isDraft"}},
				{name: "URL", keys: []string{"remoteUrl", "repositoryRemoteUrl"}},
				{name: "head", keys: []string{"sourceRefName"}},
				{name: "base", keys: []string{"targetRefName"}},
				{name: "source repository", keys: []string{"forkSource"}},
			},
			stateKey: "status",
		},
	}
}

type reviewInvocation struct {
	bin  string
	dir  string
	args []string
}

func installReviewTestRunner(t *testing.T, output string) *[]reviewInvocation {
	t.Helper()
	calls := []reviewInvocation{}
	installReviewTestSeams(t, func(_ context.Context, bin, dir string, args ...string) (string, error) {
		calls = append(calls, reviewInvocation{bin: bin, dir: dir, args: append([]string(nil), args...)})
		if bin == "az" && len(args) > 0 && args[0] == "extension" {
			return "azure-devops", nil
		}
		return output, nil
	})
	return &calls
}

func installReviewTestSeams(t *testing.T, runner cliRunner) {
	t.Helper()
	originalLookup := lookupPath
	originalReview := reviewRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		reviewRunner = originalReview
	})
	lookupPath = func(bin string) (string, error) { return "/fake/" + bin, nil }
	reviewRunner = runner
}

func marshalReviewFixture(t *testing.T, items ...map[string]any) string {
	t.Helper()
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReviewAdaptersNormalizeProviderFixtures(t *testing.T) {
	states := []ReviewState{ReviewOpen, ReviewDraft, ReviewMerged, ReviewClosed}
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		for _, state := range states {
			state := state
			t.Run(provider.name+"/"+string(state), func(t *testing.T) {
				item := provider.item(state, 42, provider.query.Head, provider.query.Base)
				installReviewTestRunner(t, marshalReviewFixture(t, item))
				got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
				if err != nil {
					t.Fatal(err)
				}
				if got == nil {
					t.Fatal("exact provider fixture should produce review evidence")
				}
				if got.Provider != provider.kind || got.Repository != provider.query.Repository ||
					got.ID != provider.expectedID(42) || got.Number != 42 || got.State != state ||
					got.Draft != (state == ReviewDraft) || got.URL != provider.expectedURL(42) ||
					got.Head != provider.query.Head || got.Base != provider.query.Base || got.ObservedAt.IsZero() {
					t.Fatalf("normalized review = %+v", got)
				}
			})
		}
	}
}

func TestReviewAdaptersReturnKnownAbsence(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			installReviewTestRunner(t, "[]")
			got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			if err != nil || got != nil {
				t.Fatalf("zero matches = %+v, %v; want nil, nil", got, err)
			}
		})
	}
}

func TestReviewAdaptersFilterObservedBranchesLocally(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			wrongHead := provider.item(ReviewOpen, 1, "feat/other", provider.query.Base)
			wrongBase := provider.item(ReviewClosed, 2, provider.query.Head, "release")
			// Nonmatching provider extras do not become evidence and need not have
			// unrelated portable fields. Their observed branches remain mandatory.
			for _, field := range provider.criticalFields {
				if field.name == "identifier" {
					for _, key := range field.keys {
						delete(wrongHead, key)
					}
				}
				if field.name == "URL" {
					for _, key := range field.keys {
						delete(wrongBase, key)
					}
				}
			}
			exact := provider.item(ReviewMerged, 3, provider.query.Head, provider.query.Base)
			installReviewTestRunner(t, marshalReviewFixture(t, wrongHead, wrongBase, exact))

			got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Number != 3 || got.State != ReviewMerged {
				t.Fatalf("filtered review = %+v", got)
			}
		})
	}
}

func TestReviewAdaptersRejectForkSourceIdentity(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			fork := provider.item(ReviewMerged, 42, provider.query.Head, provider.query.Base)
			switch provider.kind {
			case GitHub:
				fork["headRepository"] = map[string]any{"nameWithOwner": "contributor/widget"}
			case GitLab:
				fork["source_project_id"] = 200
			case AzureDevOps:
				fork["forkSource"] = map[string]any{"repository": map[string]any{"id": "fork-repository"}}
			}
			installReviewTestRunner(t, marshalReviewFixture(t, fork))
			got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			if err != nil || got != nil {
				t.Fatalf("fork review became local evidence: got=%+v err=%v", got, err)
			}
		})
	}
}

func TestReviewAdaptersRejectMultipleExactMatches(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			first := provider.item(ReviewOpen, 41, provider.query.Head, provider.query.Base)
			second := provider.item(ReviewClosed, 42, provider.query.Head, provider.query.Base)
			installReviewTestRunner(t, marshalReviewFixture(t, first, second))

			got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			if got != nil {
				t.Fatalf("ambiguous query returned evidence: %+v", got)
			}
			var ambiguous *AmbiguousReviewError
			if !errors.Is(err, ErrAmbiguousReview) || !errors.As(err, &ambiguous) || len(ambiguous.Matches) != 2 {
				t.Fatalf("ambiguity = %T %v", err, err)
			}
			if strings.Contains(err.Error(), provider.query.Head) || strings.Contains(err.Error(), provider.expectedURL(41)) {
				t.Fatalf("ambiguity text leaked provider values: %q", err)
			}
		})
	}
}

func TestReviewAdaptersRejectMissingCriticalFields(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		for _, field := range provider.criticalFields {
			field := field
			t.Run(provider.name+"/"+field.name, func(t *testing.T) {
				item := provider.item(ReviewOpen, 42, provider.query.Head, provider.query.Base)
				for _, key := range field.keys {
					delete(item, key)
				}
				installReviewTestRunner(t, marshalReviewFixture(t, item))

				got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
				var malformed *MalformedReviewResponseError
				if got != nil || !errors.Is(err, ErrMalformedReviewResponse) || !errors.As(err, &malformed) {
					t.Fatalf("missing %s = %+v, %T %v", field.name, got, err, err)
				}
			})
		}
	}
}

func TestReviewAdaptersRejectMalformedJSONWithoutLeakingBody(t *testing.T) {
	const privateBody = `[{"title":"PRIVATE_RESPONSE_BODY"}`
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			installReviewTestRunner(t, privateBody)
			_, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			var malformed *MalformedReviewResponseError
			var syntax *json.SyntaxError
			if !errors.Is(err, ErrMalformedReviewResponse) || !errors.As(err, &malformed) || !errors.As(err, &syntax) {
				t.Fatalf("malformed JSON error = %T %v", err, err)
			}
			if strings.Contains(err.Error(), "PRIVATE_RESPONSE_BODY") {
				t.Fatalf("error leaked private response body: %q", err)
			}
		})
	}
}

func TestReviewAdaptersRejectUnknownStateWithoutLeakingValue(t *testing.T) {
	const privateState = "PRIVATE_RESPONSE_STATE"
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			item := provider.item(ReviewOpen, 42, provider.query.Head, provider.query.Base)
			item[provider.stateKey] = privateState
			installReviewTestRunner(t, marshalReviewFixture(t, item))
			_, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			if !errors.Is(err, ErrMalformedReviewResponse) {
				t.Fatalf("unknown state error = %T %v", err, err)
			}
			if strings.Contains(err.Error(), privateState) {
				t.Fatalf("error leaked provider state value: %q", err)
			}
		})
	}
}

func TestReviewQueryCommandArguments(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			calls := installReviewTestRunner(t, "[]")
			if _, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query); err != nil {
				t.Fatal(err)
			}

			var want []reviewInvocation
			switch provider.kind {
			case GitHub:
				want = []reviewInvocation{{
					bin: "gh", dir: "/checkout",
					args: []string{"pr", "list", "--repo", "acme/widget", "--head", "feat/review", "--base", "main", "--state", "all", "--limit", "1000", "--json", "id,number,state,isDraft,url,headRefName,baseRefName,headRepository"},
				}}
			case GitLab:
				want = []reviewInvocation{{
					bin: "glab", dir: "/checkout",
					args: []string{"mr", "list", "--repo", "acme/widget", "--source-branch", "feat/review", "--target-branch", "main", "--all", "--output", "json", "--per-page", "100", "--page", "1"},
				}}
			case AzureDevOps:
				want = []reviewInvocation{
					{bin: "az", args: []string{"extension", "show", "--name", "azure-devops", "--query", "name", "--output", "tsv", "--only-show-errors"}},
					{
						bin: "az", dir: "/checkout",
						args: []string{"repos", "pr", "list", "--detect", "false", "--organization", "https://dev.azure.com/acme/", "--project", "Platform", "--repository", "widget", "--source-branch", "feat/review", "--target-branch", "main", "--status", "all", "--top", "100", "--skip", "0", "--query", azureReviewJSONQuery, "--output", "json", "--only-show-errors"},
					},
				}
			}
			if !reflect.DeepEqual(*calls, want) {
				t.Fatalf("invocations = %#v, want %#v", *calls, want)
			}
		})
	}
}

func TestAzureReviewQueryScopeFallbacks(t *testing.T) {
	t.Run("full identity is explicit without configuration", func(t *testing.T) {
		calls := installReviewTestRunner(t, "[]")
		adapter := NewAzureDevOps(nil)
		query := ReviewQuery{Repository: "other/Tools/api", Head: "topic", Base: "trunk"}
		if _, err := QueryReview(t.Context(), adapter, "/checkout", query); err != nil {
			t.Fatal(err)
		}
		got := (*calls)[1].args
		wantScope := []string{"--detect", "false", "--organization", "https://dev.azure.com/other", "--project", "Tools", "--repository", "api"}
		if !reflect.DeepEqual(got[3:11], wantScope) {
			t.Fatalf("explicit Azure scope = %q, want %q", got[3:11], wantScope)
		}
	})

	t.Run("bare repository uses checkout detection", func(t *testing.T) {
		calls := installReviewTestRunner(t, "[]")
		adapter := NewAzureDevOps(nil)
		query := ReviewQuery{Repository: "api", Head: "topic", Base: "trunk"}
		if _, err := QueryReview(t.Context(), adapter, "/checkout", query); err != nil {
			t.Fatal(err)
		}
		got := (*calls)[1].args
		wantScope := []string{"--detect", "true", "--repository", "api"}
		if !reflect.DeepEqual(got[3:7], wantScope) {
			t.Fatalf("detected Azure scope = %q, want %q", got[3:7], wantScope)
		}
	})
}

func TestReviewQueryPreservesRunnerErrorsWithoutResponseBody(t *testing.T) {
	sentinel := errors.New("provider authentication or version error")
	for _, provider := range reviewFixtureProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			installReviewTestSeams(t, func(_ context.Context, bin, _ string, args ...string) (string, error) {
				if bin == "az" && len(args) > 0 && args[0] == "extension" {
					return "azure-devops", nil
				}
				return "PRIVATE_RESPONSE_BODY", sentinel
			})
			got, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
			if got != nil || !errors.Is(err, sentinel) {
				t.Fatalf("runner error = %+v, %T %v", got, err, err)
			}
			if strings.Contains(err.Error(), "PRIVATE_RESPONSE_BODY") {
				t.Fatalf("runner error leaked stdout: %q", err)
			}
		})
	}
}

func TestReviewQueryCapabilityAndUnavailableCLI(t *testing.T) {
	for _, provider := range reviewFixtureProviders() {
		if _, ok := provider.adapter().(ReviewQuerier); !ok {
			t.Errorf("%s adapter does not implement ReviewQuerier", provider.name)
		}
	}

	query := ReviewQuery{Repository: "acme/widget", Head: "topic", Base: "main"}
	if _, err := QueryReview(t.Context(), noReviewForge{}, "/checkout", query); err == nil {
		t.Fatal("forge without ReviewQuerier should be unsupported")
	} else {
		var unsupported *ErrUnsupported
		if !errors.As(err, &unsupported) {
			t.Fatalf("unavailable capability error = %T %v", err, err)
		}
	}

	originalLookup := lookupPath
	originalReview := reviewRunner
	t.Cleanup(func() {
		lookupPath = originalLookup
		reviewRunner = originalReview
	})
	lookupPath = func(string) (string, error) { return "", errors.New("not found") }
	reviewRunner = func(context.Context, string, string, ...string) (string, error) {
		t.Fatal("missing CLI must not invoke review runner")
		return "", nil
	}
	for _, provider := range reviewFixtureProviders() {
		_, err := QueryReview(t.Context(), provider.adapter(), "/checkout", provider.query)
		var missing *ErrNoCLI
		if !errors.As(err, &missing) {
			t.Errorf("%s missing CLI error = %T %v", provider.name, err, err)
		}
	}
}

func TestReviewQueryValidationDoesNotInvokeProvider(t *testing.T) {
	installReviewTestSeams(t, func(context.Context, string, string, ...string) (string, error) {
		t.Fatal("invalid query must not invoke provider")
		return "", nil
	})
	adapter, _ := For(GitHub)
	for _, query := range []ReviewQuery{
		{Head: "topic", Base: "main"},
		{Repository: "acme/widget", Base: "main"},
		{Repository: "acme/widget", Head: "topic"},
	} {
		if _, err := QueryReview(t.Context(), adapter, "/checkout", query); err == nil {
			t.Errorf("query %+v should fail validation", query)
		}
	}
}

type noReviewForge struct{}

func (noReviewForge) Kind() Kind      { return GitHub }
func (noReviewForge) Bin() string     { return "none" }
func (noReviewForge) Available() bool { return true }
func (noReviewForge) CreatePR(context.Context, string, PRRequest) (string, error) {
	return "", nil
}
func (noReviewForge) CreateRepo(context.Context, string, RepoRequest) (string, error) {
	return "", nil
}
func (noReviewForge) CloneURL(ref string) string { return ref }
func (noReviewForge) ListRepos(context.Context) ([]RemoteRepo, error) {
	return nil, nil
}
