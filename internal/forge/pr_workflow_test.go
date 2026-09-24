package forge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

const testPRHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testPRBase = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const testPRMerged = "cccccccccccccccccccccccccccccccccccccccc"

func TestPRReferenceStrictHostAndShape(t *testing.T) {
	t.Setenv("GH_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	t.Setenv("GLAB_HOST", "")
	for _, raw := range []string{"https://github.com/acme/demo/pull/12", "https://gitlab.com/group/sub/demo/-/merge_requests/42"} {
		r, err := ParsePRReference(raw)
		if err != nil || r.URL() != raw {
			t.Fatalf("%s: %#v %v", raw, r, err)
		}
	}
	for _, raw := range []string{"https://github.com.evil/a/b/pull/1", "https://github.com/a/b", "http://github.com/a/b/pull/1", "https://user@github.com/a/b/pull/1", "https://github.com/a/b/pull/1?token=x", "https://github.com/a/b/pull/%31", "https://github.com/a/../pull/1", "https://github.com/a/b/pull/0", "https://gitlab.com/a/b/merge_requests/1"} {
		if _, err := ParsePRReference(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	t.Setenv("GH_HOST", "git.example.test")
	if _, err := ParsePRReference("https://git.example.test/acme/demo/pull/1"); err != nil {
		t.Fatal(err)
	}
}

func withPRRunner(t *testing.T, run prCommandRunner) {
	t.Helper()
	old := prCommand
	prCommand = run
	t.Cleanup(func() { prCommand = old })
	t.Setenv("GH_HOST", "")
	t.Setenv("GITLAB_HOST", "")
	t.Setenv("GLAB_HOST", "")
}
func prTestRef(k Kind) PRReference {
	return PRReference{Forge: k, Host: ConfiguredHost(k), Repo: "acme/demo", Number: 1}
}
func prReply(v any) prRunResult { b, _ := json.Marshal(v); return prRunResult{Body: b, Status: 200} }

func TestPRRelatedQueriesProviderAndBindsCursor(t *testing.T) {
	var searches []string
	withPRRunner(t, func(_ context.Context, bin string, args []string, _ []byte) (prRunResult, error) {
		path := args[len(args)-1]
		if path == "user" {
			return prReply(map[string]any{"id": 1, "login": "me"}), nil
		}
		if !strings.HasPrefix(path, "search/issues?") {
			t.Fatalf("not provider search: %v", args)
		}
		u, _ := url.Parse(path)
		q := u.Query().Get("q")
		searches = append(searches, q)
		return prReply(map[string]any{"total_count": 40, "incomplete_results": false, "items": []any{map[string]any{"number": 9, "title": "old related request", "html_url": "https://github.com/acme/demo/pull/9", "state": "open", "user": map[string]any{"login": "me"}, "pull_request": map[string]any{}}}}), nil
	})
	p := NewPRProvider(GitHub)
	ref := prTestRef(GitHub)
	ref.Number = 0
	page, err := p.ListPage(t.Context(), PRPageQuery{Reference: ref, Relationship: "related", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PullRequests) != 1 || len(page.PullRequests[0].Roles) != 2 || page.NextCursor == "" || page.Complete {
		t.Fatalf("bad page: %#v", page)
	}
	if len(searches) != 2 || !strings.Contains(searches[0], "author:me") || !strings.Contains(searches[1], "review-requested:me") {
		t.Fatalf("queries: %v", searches)
	}
	_, err = p.ListPage(t.Context(), PRPageQuery{Reference: ref, Relationship: "author", Limit: 50, Cursor: page.NextCursor})
	if err == nil {
		t.Fatal("cursor crossed scope")
	}
}

func TestPRGitLabRelatedUsesServerFilters(t *testing.T) {
	var filters []string
	withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
		path := args[len(args)-1]
		if path == "user" {
			return prReply(map[string]any{"id": 1, "username": "me"}), nil
		}
		u, _ := url.Parse(path)
		if u.Query().Get("author_username") == "me" {
			filters = append(filters, "author")
		} else if u.Query().Get("reviewer_username") == "me" {
			filters = append(filters, "reviewer")
		} else {
			t.Fatalf("unfiltered request: %s", path)
		}
		return prReply([]any{map[string]any{"iid": 4, "title": "old MR", "state": "opened", "author": map[string]any{"username": "other"}}}), nil
	})
	ref := prTestRef(GitLab)
	ref.Number = 0
	page, err := NewPRProvider(GitLab).ListPage(t.Context(), PRPageQuery{Reference: ref, Relationship: "related", Limit: 50})
	if err != nil || len(filters) != 2 || len(page.PullRequests) != 1 || len(page.PullRequests[0].Roles) != 2 {
		t.Fatalf("%#v %v filters=%v", page, err, filters)
	}
}

func githubDetailFixture() map[string]any {
	return map[string]any{"id": 12, "number": 1, "title": "Demo", "state": "open", "html_url": "https://github.com/acme/demo/pull/1", "mergeable": true, "head": map[string]any{"sha": testPRHead, "ref": "feature", "repo": map[string]any{"id": 8, "full_name": "other/demo"}}, "base": map[string]any{"sha": testPRBase, "ref": "main", "repo": map[string]any{"id": 7, "full_name": "acme/demo"}}, "additions": 3, "deletions": 1, "changed_files": 2}
}
func githubPolicyFixture(status string, queue any) map[string]any {
	return map[string]any{"data": map[string]any{"repository": map[string]any{"squashMergeAllowed": true, "viewerPermission": "WRITE", "deleteBranchOnMerge": false, "pullRequest": map[string]any{"headRefOid": testPRHead, "baseRefOid": testPRBase, "mergeStateStatus": status, "reviewDecision": nil, "mergeQueue": queue, "mergeQueueEntry": nil, "autoMergeRequest": nil, "commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"oid": testPRHead, "statusCheckRollup": nil}}}}}}}}
}

func TestPRGitHubDetailReadinessAndMergeGuard(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		queue        any
		ready        bool
	}{{"clean", "CLEAN", nil, true}, {"unstable", "UNSTABLE", nil, false}, {"queued", "CLEAN", map[string]any{"__typename": "MergeQueue"}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			withPRRunner(t, func(_ context.Context, _ string, args []string, input []byte) (prRunResult, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--method PUT") {
					writes++
					var body map[string]any
					_ = json.Unmarshal(input, &body)
					if body["sha"] != testPRHead || body["merge_method"] != "squash" || len(body) != 2 {
						t.Fatalf("unsafe merge body %s", input)
					}
					return prReply(map[string]any{"merged": true, "sha": testPRMerged}), nil
				}
				if strings.Contains(joined, " graphql ") {
					if !strings.Contains(string(input), "commits(last:1)") {
						t.Fatal("checks not sourced from head commit")
					}
					return prReply(githubPolicyFixture(tc.status, tc.queue)), nil
				}
				if args[len(args)-1] == "user" {
					return prReply(map[string]any{"id": 1, "login": "me"}), nil
				}
				return prReply(githubDetailFixture()), nil
			})
			p := NewPRProvider(GitHub)
			d, err := p.Detail(t.Context(), prTestRef(GitHub))
			if err != nil {
				t.Fatal(err)
			}
			if (d.Readiness == "ready") != tc.ready {
				t.Fatalf("detail %#v", d)
			}
			if d.BaseURL != "https://github.com/acme/demo.git" || d.HeadURL != "https://github.com/other/demo.git" {
				t.Fatalf("URLs %#v", d)
			}
			out, err := p.Merge(t.Context(), d)
			if tc.ready {
				if err != nil || out.Status != "merged" || writes != 1 {
					t.Fatalf("%#v %v writes=%d", out, err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("blocked merge ran: %v", err)
			}
		})
	}
}

func TestPRNativeUnknownWriteAndTerminalDiff(t *testing.T) {
	withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
		if strings.Contains(strings.Join(args, " "), "--method PUT") {
			return prRunResult{}, errors.New("connection lost")
		}
		return prRunResult{Body: []byte("diff --git a/x b/x\n+\x1b]52;c;secret\a\u202e\n")}, nil
	})
	p := NewPRProvider(GitHub)
	d := PRDetailResult{Reference: prTestRef(GitHub), HeadOID: testPRHead, BaseOID: testPRBase, Readiness: "ready"}
	out, err := p.Merge(t.Context(), d)
	if err == nil || out.Status != "unknown" {
		t.Fatalf("%#v %v", out, err)
	}
	diff, err := p.Diff(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff.Text, "\x1b") || strings.ContainsAny(diff.TerminalText, "\x1b\a\u202e") {
		t.Fatalf("unsafe preview %#v", diff)
	}
}

func TestPRGitLabDetailAndImmediateSquash(t *testing.T) {
	for _, train := range []bool{false, true} {
		t.Run(strconv.FormatBool(train), func(t *testing.T) {
			writes := 0
			targetOID := testPRMerged
			withPRRunner(t, func(_ context.Context, bin string, args []string, input []byte) (prRunResult, error) {
				if bin != "glab" {
					t.Fatalf("wrong provider %s", bin)
				}
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "--method PUT") {
					writes++
					var body map[string]any
					_ = json.Unmarshal(input, &body)
					if body["sha"] != testPRHead || body["squash"] != true || body["auto_merge"] != false || body["should_remove_source_branch"] != false || len(body) != 4 {
						t.Fatalf("unsafe MR merge %s", input)
					}
					return prReply(map[string]any{"iid": 1, "state": "merged", "merge_commit_sha": testPRMerged}), nil
				}
				path := args[len(args)-1]
				switch path {
				case "user":
					return prReply(map[string]any{"id": 1, "username": "me"}), nil
				case "projects/acme%2Fdemo":
					return prReply(map[string]any{"id": 7, "path_with_namespace": "acme/demo", "squash_option": "default_off", "merge_trains_enabled": train}), nil
				case "projects/8":
					return prReply(map[string]any{"id": 8, "path_with_namespace": "other/demo"}), nil
				case "projects/7/repository/branches/main":
					return prReply(map[string]any{"name": "main", "commit": map[string]any{"id": targetOID}}), nil
				default:
					return prReply(map[string]any{"id": 12, "iid": 1, "project_id": 7, "target_project_id": 7, "source_project_id": 8, "title": "MR", "state": "opened", "sha": testPRHead, "source_branch": "feature", "target_branch": "main", "diff_refs": map[string]any{"head_sha": testPRHead, "base_sha": testPRBase, "start_sha": testPRBase}, "detailed_merge_status": "mergeable", "changes_count": "2", "user": map[string]any{"can_merge": true}, "head_pipeline": map[string]any{"sha": testPRHead, "status": "success", "web_url": "https://gitlab.com/acme/demo/-/pipelines/1"}}), nil
				}
			})
			p := NewPRProvider(GitLab)
			d, err := p.Detail(t.Context(), prTestRef(GitLab))
			if err != nil {
				t.Fatal(err)
			}
			if d.HeadURL != "https://gitlab.com/other/demo.git" || d.BaseURL != "https://gitlab.com/acme/demo.git" || d.Checks != ChecksPassing || (d.Readiness == "ready") == train {
				t.Fatalf("bad detail %#v", d)
			}
			if d.BaseOID != targetOID || d.DiffStartOID != testPRBase {
				t.Fatalf("historical diff start became live base: %#v", d)
			}
			targetOID = strings.Repeat("d", 40)
			changed, e := p.Detail(t.Context(), prTestRef(GitLab))
			if e != nil || changed.BaseOID != targetOID || changed.DiffStartOID != d.DiffStartOID {
				t.Fatalf("target-only drift not observed: %#v %v", changed, e)
			}
			out, err := p.Merge(t.Context(), d)
			if train {
				if err == nil || writes != 0 {
					t.Fatal("train bypassed")
				}
			} else if err != nil || writes != 1 || out.Status != "merged" {
				t.Fatalf("%#v %v calls=%d", out, err, writes)
			}
		})
	}
}

func TestPRAllPageAnnotatesRolesWithoutDetailFanout(t *testing.T) {
	calls := 0
	withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
		calls++
		if args[len(args)-1] == "user" {
			return prReply(map[string]any{"id": 1, "login": "me"}), nil
		}
		row := githubDetailFixture()
		row["user"] = map[string]any{"login": "me"}
		row["requested_reviewers"] = []any{map[string]any{"login": "me"}}
		return prReply([]any{row}), nil
	})
	ref := prTestRef(GitHub)
	ref.Number = 0
	page, err := NewPRProvider(GitHub).ListPage(t.Context(), PRPageQuery{Reference: ref, Relationship: "all", Limit: 50})
	if err != nil || len(page.PullRequests) != 1 || len(page.PullRequests[0].Roles) != 2 || calls != 3 {
		t.Fatalf("%#v %v calls=%d", page, err, calls)
	}
}

func TestPRBoundedBufferDoesNotRetainOverflow(t *testing.T) {
	b := &boundedPRBuffer{limit: 4}
	n, err := b.Write([]byte("12345678"))
	if err != nil || n != 8 || b.String() != "1234" || !b.exceeded {
		t.Fatalf("buffer %#v n=%d err=%v", b, n, err)
	}
	b = &boundedPRBuffer{limit: 4}
	copied, err := io.Copy(b, struct{ io.Reader }{strings.NewReader("12345678")})
	if err != nil || copied != 8 || b.String() != "1234" || !b.exceeded {
		t.Fatalf("io.Copy bypassed bound: copied=%d size=%d exceeded=%v err=%v", copied, b.Len(), b.exceeded, err)
	}
}

func TestPRCountAndCursorAccountBinding(t *testing.T) {
	account := 1
	withPRRunner(t, func(_ context.Context, _ string, args []string, input []byte) (prRunResult, error) {
		if args[len(args)-1] == "user" {
			return prReply(map[string]any{"id": account, "login": "me"}), nil
		}
		if len(input) > 0 {
			return prReply(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"totalCount": 123}}}}), nil
		}
		out := prReply([]any{githubDetailFixture()})
		out.Headers = map[string]string{"link": `<https://api.github.com/next>; rel="next"`}
		return out, nil
	})
	ref := prTestRef(GitHub)
	ref.Number = 0
	p := NewPRProvider(GitHub)
	page, err := p.ListPage(t.Context(), PRPageQuery{Reference: ref, Limit: 50})
	if err != nil || page.Total == nil || *page.Total != 123 || page.TotalLowerBound || page.NextCursor == "" {
		t.Fatalf("%#v %v", page, err)
	}
	account = 2
	_, err = p.ListPage(t.Context(), PRPageQuery{Reference: ref, Limit: 50, Cursor: page.NextCursor})
	if err == nil {
		t.Fatal("cursor crossed authenticated accounts")
	}
}

func TestPRGitLabDiffReportsProviderOmission(t *testing.T) {
	for _, state := range []string{"overflow", "collected"} {
		t.Run(state, func(t *testing.T) {
			withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
				path := args[len(args)-1]
				if strings.Contains(path, "versions?") {
					return prReply([]any{map[string]any{"id": 3, "head_commit_sha": testPRHead, "start_commit_sha": testPRBase, "state": state}}), nil
				}
				if strings.HasSuffix(path, "versions/3") {
					return prReply(map[string]any{"head_commit_sha": testPRHead, "start_commit_sha": testPRBase, "state": "collected", "diffs": []any{map[string]any{"too_large": true}}}), nil
				}
				return prRunResult{Body: []byte("diff --git a/x b/x\n")}, nil
			})
			files := 1
			d := PRDetailResult{Reference: prTestRef(GitLab), HeadOID: testPRHead, BaseOID: testPRBase, DiffStartOID: testPRBase, Size: PRSize{Files: &files}}
			diff, err := NewPRProvider(GitLab).Diff(t.Context(), d)
			if err != nil || diff.Complete || diff.Warning == "" || diff.Text == "" {
				t.Fatalf("%#v %v", diff, err)
			}
		})
	}
}

func TestPRRelatedSingleRowPagesVisitBothRoles(t *testing.T) {
	var roles []string
	withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
		path := args[len(args)-1]
		if path == "user" {
			return prReply(map[string]any{"id": 1, "login": "me"}), nil
		}
		u, _ := url.Parse(path)
		role := "author"
		number := 1
		if strings.Contains(u.Query().Get("q"), "review-requested:") {
			role = "reviewer"
			number = 2
		}
		roles = append(roles, role)
		return prReply(map[string]any{"total_count": 1, "items": []any{map[string]any{"number": number, "html_url": "https://github.com/acme/demo/pull/" + strconv.Itoa(number), "state": "open", "pull_request": map[string]any{}}}}), nil
	})
	ref := prTestRef(GitHub)
	ref.Number = 0
	p := NewPRProvider(GitHub)
	first, err := p.ListPage(t.Context(), PRPageQuery{Reference: ref, Relationship: "related", Limit: 1})
	if err != nil || len(first.PullRequests) != 1 || first.NextCursor == "" {
		t.Fatalf("%#v %v", first, err)
	}
	second, err := p.ListPage(t.Context(), PRPageQuery{Reference: ref, Relationship: "related", Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.PullRequests) != 1 || !second.Complete || strings.Join(roles, ",") != "author,reviewer" {
		t.Fatalf("%#v %v roles=%v", second, err, roles)
	}
}

func TestPRGitLabCountLowerBoundWithoutTotalHeader(t *testing.T) {
	withPRRunner(t, func(_ context.Context, _ string, args []string, _ []byte) (prRunResult, error) {
		if args[len(args)-1] == "user" {
			return prReply(map[string]any{"id": 1, "username": "me"}), nil
		}
		rows := make([]any, 50)
		for i := range rows {
			rows[i] = map[string]any{"iid": i + 1, "state": "opened"}
		}
		out := prReply(rows)
		out.Headers = map[string]string{"x-next-page": "2"}
		return out, nil
	})
	ref := prTestRef(GitLab)
	ref.Number = 0
	page, err := NewPRProvider(GitLab).ListPage(t.Context(), PRPageQuery{Reference: ref, Limit: 50})
	if err != nil || page.Total == nil || *page.Total != 51 || !page.TotalLowerBound || page.NextCursor == "" {
		t.Fatalf("%#v %v", page, err)
	}
}
