package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// glab drives the GitLab CLI. GitLab calls the same concept a merge request,
// and its flags differ enough from gh that the two need separate adapters
// rather than a shared one with conditionals.
type glab struct {
	once          sync.Once
	usernameValue string
	usernameErr   error
}

func (g *glab) Kind() Kind      { return GitLab }
func (g *glab) Bin() string     { return "glab" }
func (g *glab) Available() bool { return have("glab") }

// run is run() with GitLab authentication failures classified, so a signed-out
// or expired token reads as a remediation rather than as a failed argv.
func (g *glab) run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := run(ctx, "glab", dir, args...)
	return out, classifyAuth(GitLab, "glab", err)
}

// CreatePR opens a GitLab merge request.
func (g *glab) CreatePR(ctx context.Context, dir string, req PRRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	args := []string{"mr", "create", "--target-branch", req.Base, "--source-branch", req.Head}
	switch {
	case req.Title != "":
		args = append(args, "--title", req.Title, "--description", req.Body)
	default:
		args = append(args, "--fill")
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	if req.Web {
		args = append(args, "--web")
	}
	// Without this glab prompts interactively even when everything is supplied.
	args = append(args, "--yes")
	out, err := g.run(ctx, dir, args...)
	return lastURL(out), err
}

// QueryReview manually observes an exact GitLab merge request relationship.
func (g *glab) QueryReview(ctx context.Context, dir string, query ReviewQuery) (*Review, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	if err := validateReviewQuery(query); err != nil {
		return nil, err
	}

	const pageSize = 100
	var candidates []reviewCandidate
	for page := 1; ; page++ {
		out, err := reviewRunner(ctx, "glab", dir,
			"mr", "list", "--repo", query.Repository,
			"--source-branch", query.Head, "--target-branch", query.Base,
			"--all", "--output", "json", "--per-page", strconv.Itoa(pageSize),
			"--page", strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		pageCandidates, err := parseGitLabReviewCandidates(out)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, pageCandidates...)
		if len(pageCandidates) < pageSize {
			break
		}
	}
	return selectExactReview(GitLab, query, candidates, normalizeGitLabReviewState)
}

func parseGitLabReviewCandidates(out string) ([]reviewCandidate, error) {
	var raw []struct {
		ID              *int   `json:"id"`
		IID             *int   `json:"iid"`
		State           string `json:"state"`
		Draft           *bool  `json:"draft"`
		WorkInProgress  *bool  `json:"work_in_progress"`
		WebURL          string `json:"web_url"`
		SourceBranch    string `json:"source_branch"`
		TargetBranch    string `json:"target_branch"`
		SourceProjectID *int   `json:"source_project_id"`
		TargetProjectID *int   `json:"target_project_id"`
	}
	if err := decodeReviewJSON(GitLab, out, &raw); err != nil {
		return nil, err
	}
	candidates := make([]reviewCandidate, 0, len(raw))
	for _, item := range raw {
		id := ""
		if item.ID != nil {
			id = strconv.Itoa(*item.ID)
		}
		number := 0
		if item.IID != nil {
			number = *item.IID
		}
		draft := item.Draft
		if draft == nil {
			draft = item.WorkInProgress
		} else if item.WorkInProgress != nil && *draft != *item.WorkInProgress {
			draft = nil
		}
		var sourceMatches *bool
		if item.SourceProjectID != nil && item.TargetProjectID != nil && *item.SourceProjectID > 0 && *item.TargetProjectID > 0 {
			matches := *item.SourceProjectID == *item.TargetProjectID
			sourceMatches = &matches
		}
		candidates = append(candidates, reviewCandidate{
			ID: id, Number: number, State: item.State, Draft: draft,
			URL: item.WebURL, Head: item.SourceBranch, Base: item.TargetBranch,
			SourceMatchesRepository: sourceMatches,
		})
	}
	return candidates, nil
}

func normalizeGitLabReviewState(state string, draft bool) (ReviewState, error) {
	switch strings.ToLower(state) {
	case "opened", "open":
		if draft {
			return ReviewDraft, nil
		}
		return ReviewOpen, nil
	case "merged":
		return ReviewMerged, nil
	case "closed":
		return ReviewClosed, nil
	default:
		return "", errors.New("unrecognized GitLab review state")
	}
}

// CreateRepo creates a GitLab project from the local checkout.
func (g *glab) CreateRepo(ctx context.Context, dir string, req RepoRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	target, err := resolveRepoRequest(req)
	if err != nil {
		return "", err
	}
	args := []string{"repo", "create"}
	if req.FullName == "" && req.Namespace == "" && !strings.Contains(req.Name, "/") {
		// Preserve the original invocation for existing callers such as Try
		// graduation; glab uses the current checkout and adds the requested
		// remote as before.
		args = append(args, target.name)
	} else {
		args = append(args, "--name", target.name)
		if target.namespace != "" {
			args = append(args, "--group", target.namespace)
		}
	}
	args = append(args, "--remoteName", target.remoteName, visibilityFlag(target.visibility))
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	out, err := g.run(ctx, dir, args...)
	return lastURL(out), err
}

// PublishRepo creates an empty GitLab project while --skipGitInit prevents
// glab from initializing, cloning or changing the local checkout.
func (g *glab) PublishRepo(ctx context.Context, dir string, req RepoRequest) (CreateRepoResult, error) {
	if !g.Available() {
		return CreateRepoResult{}, &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	target, err := resolveRepoRequest(req)
	if err != nil {
		return CreateRepoResult{}, err
	}
	args := []string{"repo", "create", "--name", target.name, "--skipGitInit",
		visibilityFlag(target.visibility)}
	if target.namespace != "" {
		args = append(args, "--group", target.namespace)
	}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	out, err := publishRunner(ctx, "glab", dir, args...)
	if err != nil {
		return createRepoResult(GitLab, target, out, false), classifyAuth(GitLab, "glab", err)
	}
	result := createRepoResult(GitLab, target, out, true)
	if result.RemoteURL == "" {
		return result, fmt.Errorf("glab created %q but did not report a repository URL", target.fullName)
	}
	return result, nil
}

func gitLabHost() string {
	for _, name := range []string{"GITLAB_HOST", "GLAB_HOST"} {
		if host := strings.TrimSpace(os.Getenv(name)); host != "" {
			return host
		}
	}
	return "gitlab.com"
}

// CloneURL renders a clone target.
func (g *glab) CloneURL(ref string) string {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "git@") {
		return ref
	}
	return "https://" + gitLabHost() + "/" + strings.TrimSuffix(ref, ".git") + ".git"
}

// ListRepos lists every GitLab project of which the authenticated user is a
// member, most recently active first. glab owns authentication; dev passes an
// explicit GITLAB_HOST/GLAB_HOST (default gitlab.com) so inventory and cache
// identity never depend on the process's current Git repository.
func (g *glab) ListRepos(ctx context.Context) ([]RemoteRepo, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	const pageSize = 100
	var result []RemoteRepo
	for page := 1; ; page++ {
		out, err := g.run(ctx, "", "api", "--hostname", gitLabHost(), "projects", "--method", "GET",
			"-f", "membership=true", "-f", "simple=true", "-f", "per_page=100",
			"-f", fmt.Sprintf("page=%d", page), "-f", "order_by=last_activity_at", "-f", "sort=desc")
		if err != nil {
			return result, err
		}
		repos, err := parseGitLabRepos(out)
		if err != nil {
			return result, err
		}
		result = append(result, repos...)
		if len(repos) < pageSize {
			break
		}
	}
	return result, nil
}

func parseGitLabRepos(out string) ([]RemoteRepo, error) {
	var raw []struct {
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		Description       string `json:"description"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
		SSHURL            string `json:"ssh_url_to_repo"`
		HTTPURL           string `json:"http_url_to_repo"`
		WebURL            string `json:"web_url"`
		Archived          bool   `json:"archived"`
		ForkedFrom        any    `json:"forked_from_project"`
		LastActivityAt    string `json:"last_activity_at"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode glab api projects: %w", err)
	}
	result := make([]RemoteRepo, 0, len(raw))
	for _, r := range raw {
		updated, _ := time.Parse(time.RFC3339Nano, r.LastActivityAt)
		result = append(result, RemoteRepo{
			Forge: GitLab, Name: r.Name, FullName: r.PathWithNamespace,
			Description: r.Description, URL: r.WebURL,
			CloneURL: r.HTTPURL, SSHURL: r.SSHURL,
			Visibility: r.Visibility, DefaultBranch: r.DefaultBranch,
			Archived: r.Archived, Fork: r.ForkedFrom != nil, UpdatedAt: updated,
		})
	}
	return result, nil
}

// ListAccountPRs lists the authenticated user's open merge requests across
// every project they can see.
//
// Unlike GitHub's search surface, GitLab's REST list already carries the
// source branch and target branch, so these rows are full detail. The one
// thing they lack is pipeline status: head_pipeline is only returned by the
// single-MR endpoint, and a per-request fan-out is not worth it, so Checks is
// left empty for GitLab.
func (g *glab) ListAccountPRs(ctx context.Context, q PRQuery) ([]PullRequest, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	if state := q.EffectiveState(); state != PRStateOpen {
		return nil, &ErrUnsupported{Kind: GitLab,
			Operation: "account-wide search for " + string(state) + " requests"}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	byKey := map[string]PullRequest{}
	var order []string
	for _, role := range q.EffectiveRoles() {
		// GitLab has no "@me" for reviewers, so the reviewer query needs the
		// authenticated username; scope=created_by_me needs no such lookup.
		filter := []string{"-f", "scope=created_by_me"}
		if role == RoleReviewer {
			username, err := g.username(ctx)
			if err != nil {
				return collectPRs(byKey, order), err
			}
			filter = []string{"-f", "reviewer_username=" + username}
		}
		prs, err := g.listMergeRequests(ctx, "merge_requests", filter, role, q)
		for _, pr := range prs {
			key := pr.Key()
			if existing, ok := byKey[key]; ok {
				existing.Roles = unionRoles(existing.Roles, pr.Roles)
				byKey[key] = existing
				continue
			}
			byKey[key] = pr
			order = append(order, key)
		}
		if err != nil {
			return collectPRs(byKey, order), err
		}
	}
	return collectPRs(byKey, order), nil
}

// ListRepoPRs lists one project's merge requests.
func (g *glab) ListRepoPRs(ctx context.Context, repo string, q PRQuery) ([]PullRequest, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitLab, Bin: "glab"}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	path := "projects/" + url.PathEscape(strings.Trim(repo, "/")) + "/merge_requests"
	return g.listMergeRequests(ctx, path, nil, "", q)
}

// listMergeRequests runs one paginated GitLab list, reusing the same loop shape
// as ListRepos. Partial pages are returned alongside an error, so a failure on
// page three does not discard pages one and two.
func (g *glab) listMergeRequests(ctx context.Context, path string, filter []string, role PRRole, q PRQuery) ([]PullRequest, error) {
	const pageSize = 100
	var result []PullRequest
	for page := 1; ; page++ {
		args := []string{"api", "--hostname", gitLabHost(), path, "--method", "GET",
			"-f", "state=" + gitLabMRState(q.EffectiveState()),
			"-f", fmt.Sprintf("per_page=%d", pageSize),
			"-f", fmt.Sprintf("page=%d", page)}
		args = append(args, filter...)
		out, err := g.run(ctx, "", args...)
		if err != nil {
			return result, err
		}
		prs, err := parseGitLabMRs(out, role)
		if err != nil {
			return result, err
		}
		result = append(result, prs...)
		if len(prs) < pageSize || len(result) >= q.EffectiveLimit() {
			break
		}
	}
	if len(result) > q.EffectiveLimit() {
		result = result[:q.EffectiveLimit()]
	}
	return result, nil
}

// username resolves and memoizes the authenticated GitLab account, which the
// reviewer query needs and GitLab will not accept a placeholder for.
func (g *glab) username(ctx context.Context) (string, error) {
	g.once.Do(func() {
		out, err := g.run(ctx, "", "api", "--hostname", gitLabHost(), "user")
		if err != nil {
			g.usernameErr = err
			return
		}
		var raw struct {
			Username string `json:"username"`
		}
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			g.usernameErr = fmt.Errorf("decode glab api user: %w", err)
			return
		}
		if raw.Username == "" {
			g.usernameErr = fmt.Errorf("glab api user did not report a username")
			return
		}
		g.usernameValue = raw.Username
	})
	return g.usernameValue, g.usernameErr
}

func gitLabMRState(state PRState) string {
	switch state {
	case PRStateMerged:
		return "merged"
	case PRStateClosed:
		return "closed"
	case PRStateAll:
		return "all"
	default:
		return "opened"
	}
}

func parseGitLabMRs(out string, role PRRole) ([]PullRequest, error) {
	var raw []struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		WebURL       string `json:"web_url"`
		State        string `json:"state"`
		Draft        bool   `json:"draft"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		Author       struct {
			Username string `json:"username"`
		} `json:"author"`
		References struct {
			Full string `json:"full"`
		} `json:"references"`
		DetailedMergeStatus string `json:"detailed_merge_status"`
		CreatedAt           string `json:"created_at"`
		UpdatedAt           string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode glab api merge_requests: %w", err)
	}
	result := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		created, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
		updated, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
		var roles []PRRole
		if role != "" {
			roles = []PRRole{role}
		}
		result = append(result, PullRequest{
			Forge: GitLab, Repo: gitLabRepoFromReference(r.References.Full),
			Number: r.IID, Title: r.Title, URL: r.WebURL,
			State: gitLabPRState(r.State), Draft: r.Draft,
			Author: r.Author.Username, Roles: roles, Detail: PRDetailFull,
			HeadBranch: r.SourceBranch, BaseBranch: r.TargetBranch,
			Mergeable: strings.ToLower(r.DetailedMergeStatus),
			CreatedAt: created, UpdatedAt: updated,
		})
	}
	return result, nil
}

// gitLabRepoFromReference turns "group/project!123" into "group/project",
// which avoids a second call to resolve the project path by id.
func gitLabRepoFromReference(reference string) string {
	if index := strings.Index(reference, "!"); index >= 0 {
		return reference[:index]
	}
	return reference
}

func gitLabPRState(state string) PRState {
	switch strings.ToLower(state) {
	case "opened":
		return PRStateOpen
	case "merged":
		return PRStateMerged
	case "closed", "locked":
		return PRStateClosed
	}
	return PRState(strings.ToLower(state))
}
