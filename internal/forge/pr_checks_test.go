package forge

import (
	"context"
	"strings"
	"testing"
)

func TestPRGitHubAggregateChecksRequireCompleteCoverage(t *testing.T) {
	for _, tc := range []struct{ name, checks, ready string }{
		{"passing", ChecksPassing, "ready"},
		{"partial-passing", "", "unknown"},
		{"partial-empty", "", "unknown"},
		{"partial-failing", ChecksFailing, "unknown"},
		{"partial-pending", ChecksPending, "unknown"},
		{"unknown-conclusion", "", "unknown"},
		{"wrong-head", "", "unknown"},
		{"missing-rollup", "", "unknown"},
		{"explicit-null", ChecksNone, "ready"},
		{"policy-unknown", ChecksPassing, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := githubPolicyFixture("CLEAN", nil)
			repository := graph["data"].(map[string]any)["repository"].(map[string]any)
			pr := repository["pullRequest"].(map[string]any)
			commit := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["commit"].(map[string]any)
			check := map[string]any{"name": "tests", "status": "COMPLETED", "conclusion": "SUCCESS"}
			nodes := []any{check}
			next := strings.HasPrefix(tc.name, "partial-")
			switch tc.name {
			case "partial-empty":
				nodes = []any{}
			case "partial-failing":
				check["conclusion"] = "FAILURE"
			case "partial-pending":
				check["status"] = "IN_PROGRESS"
				delete(check, "conclusion")
			case "unknown-conclusion":
				nodes = append(nodes, map[string]any{"name": "unknown", "status": "COMPLETED", "conclusion": "FUTURE_OUTCOME"})
			case "wrong-head":
				commit["oid"] = testPRBase
			case "policy-unknown":
				delete(repository, "squashMergeAllowed")
			}
			commit["statusCheckRollup"] = map[string]any{"contexts": map[string]any{"pageInfo": map[string]any{"hasNextPage": next}, "nodes": nodes}}
			if tc.name == "missing-rollup" {
				delete(commit, "statusCheckRollup")
			}
			if tc.name == "explicit-null" {
				commit["statusCheckRollup"] = nil
			}
			withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
				if strings.Contains(strings.Join(args, " "), " graphql ") {
					return prReply(graph), nil
				}
				if args[len(args)-1] == "user" {
					return prReply(map[string]any{"id": 1, "login": "me"}), nil
				}
				return prReply(githubDetailFixture()), nil
			})
			d, err := NewPRProvider(GitHub).Detail(t.Context(), prTestRef(GitHub))
			if err != nil {
				t.Fatal(err)
			}
			if d.Checks != tc.checks || d.Readiness != tc.ready {
				t.Fatalf("checks=%q readiness=%q, want %q/%q", d.Checks, d.Readiness, tc.checks, tc.ready)
			}
		})
	}
}

func TestPRGitLabPipelineOmissionAndHeadBinding(t *testing.T) {
	for _, tc := range []struct{ name, checks, ready string }{
		{"passing", ChecksPassing, "ready"},
		{"missing", "", "unknown"},
		{"explicit-null", ChecksNone, "ready"},
		{"wrong-head", "", "unknown"},
		{"policy-unknown", ChecksPassing, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := map[string]any{"id": 12, "iid": 1, "project_id": 7, "target_project_id": 7, "source_project_id": 7, "state": "opened", "sha": testPRHead, "source_branch": "feature", "target_branch": "main", "diff_refs": map[string]any{"head_sha": testPRHead, "start_sha": testPRBase}, "detailed_merge_status": "mergeable", "user": map[string]any{"can_merge": true}}
			project := map[string]any{"id": 7, "path_with_namespace": "acme/demo", "squash_option": "default_off", "merge_trains_enabled": false}
			pipeline := map[string]any{"sha": testPRHead, "status": "success"}
			mr["head_pipeline"] = pipeline
			switch tc.name {
			case "missing":
				delete(mr, "head_pipeline")
			case "explicit-null":
				mr["head_pipeline"] = nil
			case "wrong-head":
				pipeline["sha"] = testPRBase
			case "policy-unknown":
				delete(project, "merge_trains_enabled")
			}
			withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
				switch args[len(args)-1] {
				case "user":
					return prReply(map[string]any{"id": 1, "username": "me"}), nil
				case "projects/acme%2Fdemo":
					return prReply(project), nil
				case "projects/7/repository/branches/main":
					return prReply(map[string]any{"name": "main", "commit": map[string]any{"id": testPRBase}}), nil
				default:
					return prReply(mr), nil
				}
			})
			d, err := NewPRProvider(GitLab).Detail(t.Context(), prTestRef(GitLab))
			if err != nil {
				t.Fatal(err)
			}
			if d.Checks != tc.checks || d.Readiness != tc.ready {
				t.Fatalf("checks=%q readiness=%q, want %q/%q", d.Checks, d.Readiness, tc.checks, tc.ready)
			}
		})
	}
}
