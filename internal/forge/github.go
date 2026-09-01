package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// gh drives the GitHub CLI.
type gh struct{}

func (g *gh) Kind() Kind      { return GitHub }
func (g *gh) Bin() string     { return "gh" }
func (g *gh) Available() bool { return have("gh") }

// run is run() with GitHub authentication failures classified, so a signed-out
// or expired token reads as a remediation rather than as a failed argv.
func (g *gh) run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := run(ctx, "gh", dir, args...)
	return out, classifyAuth(GitHub, "gh", err)
}

// CreatePR opens a GitHub pull request.
func (g *gh) CreatePR(ctx context.Context, dir string, req PRRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	args := []string{"pr", "create", "--base", req.Base, "--head", req.Head}
	switch {
	case req.Title != "":
		args = append(args, "--title", req.Title, "--body", req.Body)
	default:
		// --fill derives title and body from the branch's commits, which is
		// the right default for a solo workflow where the commits already say
		// what happened.
		args = append(args, "--fill")
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	if req.Web {
		args = append(args, "--web")
	}
	out, err := g.run(ctx, dir, args...)
	return lastURL(out), err
}

// QueryReview manually observes an exact GitHub pull request relationship.
func (g *gh) QueryReview(ctx context.Context, dir string, query ReviewQuery) (*Review, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	if err := validateReviewQuery(query); err != nil {
		return nil, err
	}
	out, err := reviewRunner(ctx, "gh", dir,
		"pr", "list", "--repo", query.Repository,
		"--head", query.Head, "--base", query.Base, "--state", "all",
		"--limit", "1000",
		"--json", "id,number,state,isDraft,url,headRefName,baseRefName,headRepository")
	if err != nil {
		return nil, err
	}
	return parseGitHubReviews(out, query)
}

func parseGitHubReviews(out string, query ReviewQuery) (*Review, error) {
	var raw []struct {
		ID             string `json:"id"`
		Number         *int   `json:"number"`
		State          string `json:"state"`
		IsDraft        *bool  `json:"isDraft"`
		URL            string `json:"url"`
		HeadRefName    string `json:"headRefName"`
		BaseRefName    string `json:"baseRefName"`
		HeadRepository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"headRepository"`
	}
	if err := decodeReviewJSON(GitHub, out, &raw); err != nil {
		return nil, err
	}
	candidates := make([]reviewCandidate, 0, len(raw))
	for _, item := range raw {
		number := 0
		if item.Number != nil {
			number = *item.Number
		}
		var sourceMatches *bool
		if source := strings.TrimSpace(item.HeadRepository.NameWithOwner); source != "" {
			matches := strings.EqualFold(source, query.Repository)
			sourceMatches = &matches
		}
		candidates = append(candidates, reviewCandidate{
			ID: item.ID, Number: number, State: item.State, Draft: item.IsDraft,
			URL: item.URL, Head: item.HeadRefName, Base: item.BaseRefName,
			SourceMatchesRepository: sourceMatches,
		})
	}
	return selectExactReview(GitHub, query, candidates, normalizeGitHubReviewState)
}

func normalizeGitHubReviewState(state string, draft bool) (ReviewState, error) {
	switch strings.ToUpper(state) {
	case "OPEN":
		if draft {
			return ReviewDraft, nil
		}
		return ReviewOpen, nil
	case "MERGED":
		return ReviewMerged, nil
	case "CLOSED":
		return ReviewClosed, nil
	default:
		return "", errors.New("unrecognized GitHub review state")
	}
}

// CreateRepo creates a GitHub repository from the local checkout.
func (g *gh) CreateRepo(ctx context.Context, dir string, req RepoRequest) (string, error) {
	if !g.Available() {
		return "", &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	target, err := resolveRepoRequest(req)
	if err != nil {
		return "", err
	}
	args := []string{"repo", "create", target.fullName, "--source", ".", "--remote", target.remoteName,
		visibilityFlag(target.visibility)}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	if req.Push {
		args = append(args, "--push")
	}
	out, err := g.run(ctx, dir, args...)
	return lastURL(out), err
}

// PublishRepo creates an empty GitHub repository. Unlike CreateRepo it does
// not pass --source, --remote or --push, leaving all local Git mutations to the
// shared caller-side transaction.
func (g *gh) PublishRepo(ctx context.Context, dir string, req RepoRequest) (CreateRepoResult, error) {
	if !g.Available() {
		return CreateRepoResult{}, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	target, err := resolveRepoRequest(req)
	if err != nil {
		return CreateRepoResult{}, err
	}
	args := []string{"repo", "create", target.fullName, visibilityFlag(target.visibility)}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	out, err := publishRunner(ctx, "gh", dir, args...)
	if err != nil {
		return createRepoResult(GitHub, target, out, false), classifyAuth(GitHub, "gh", err)
	}
	result := createRepoResult(GitHub, target, out, true)
	if result.RemoteURL == "" {
		return result, fmt.Errorf("gh created %q but did not report a repository URL", target.fullName)
	}
	return result, nil
}

// CloneURL renders a clone target. gh accepts a bare owner/name, so a
// shorthand reference is passed through untouched.
func (g *gh) CloneURL(ref string) string {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "git@") {
		return ref
	}
	return "https://github.com/" + strings.TrimSuffix(ref, ".git") + ".git"
}

// lastURL picks the URL out of a CLI's chatter, which may print progress
// lines before the created resource's address.
func lastURL(out string) string {
	found := lastHTTPURL(out)
	if found == "" {
		return strings.TrimSpace(out)
	}
	return found
}

// ListRepos lists GitHub repositories visible to the authenticated user —
// owned, collaborated on, and organisation membership — most recently pushed
// first. gh api is intentionally used instead of a custom HTTP client:
// authentication and token refresh stay owned by the forge CLI.
func (g *gh) ListRepos(ctx context.Context) ([]RemoteRepo, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	const pageSize = 100
	var result []RemoteRepo
	for page := 1; ; page++ {
		out, err := g.run(ctx, "", "api", "user/repos", "--method", "GET",
			"-f", "per_page=100", "-f", fmt.Sprintf("page=%d", page),
			"-f", "sort=pushed", "-f", "direction=desc")
		if err != nil {
			return result, err
		}
		repos, err := parseGitHubRepos(out)
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

func parseGitHubRepos(out string) ([]RemoteRepo, error) {
	var raw []struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Description   string `json:"description"`
		URL           string `json:"html_url"`
		CloneURL      string `json:"clone_url"`
		SSHURL        string `json:"ssh_url"`
		Visibility    string `json:"visibility"`
		Fork          bool   `json:"fork"`
		Archived      bool   `json:"archived"`
		PushedAt      string `json:"pushed_at"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode gh api user/repos: %w", err)
	}
	result := make([]RemoteRepo, 0, len(raw))
	for _, r := range raw {
		updated, _ := time.Parse(time.RFC3339, r.PushedAt)
		result = append(result, RemoteRepo{
			Forge: GitHub, Name: r.Name, FullName: r.FullName,
			Description: r.Description, URL: r.URL,
			CloneURL: r.CloneURL, SSHURL: r.SSHURL,
			Visibility: r.Visibility, DefaultBranch: r.DefaultBranch,
			Archived: r.Archived, Fork: r.Fork, UpdatedAt: updated,
		})
	}
	return result, nil
}

// prSearchFields are the only fields gh search prs can return. Notably absent:
// headRefName, reviewDecision and statusCheckRollup — which is exactly why the
// per-repository surface below has to exist.
const prSearchFields = "number,title,url,state,isDraft,author,repository,createdAt,updatedAt"

// prListFields are what gh pr list adds on top: the head branch that joins a
// request to a local worktree, and the review/check state an inbox needs.
const prListFields = "number,title,url,state,isDraft,author,headRefName,baseRefName," +
	"reviewDecision,mergeable,statusCheckRollup,createdAt,updatedAt"

// ListAccountPRs searches every repository the authenticated user can see. It
// is one call per requested role regardless of how many repositories exist,
// which is what makes an account-wide inbox affordable.
//
// gh search prs supports --limit natively (no pagination loop) but only
// --state open|closed, so a merged-state query has to go through ListRepoPRs.
func (g *gh) ListAccountPRs(ctx context.Context, q PRQuery) ([]PullRequest, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	if state := q.EffectiveState(); state != PRStateOpen {
		return nil, &ErrUnsupported{Kind: GitHub,
			Operation: "account-wide search for " + string(state) + " requests"}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	byKey := map[string]PullRequest{}
	var order []string
	var result []PullRequest
	for _, role := range q.EffectiveRoles() {
		flag := "--author"
		if role == RoleReviewer {
			flag = "--review-requested"
		}
		out, err := g.run(ctx, "", "search", "prs", flag, "@me", "--state", "open",
			"--limit", strconv.Itoa(q.EffectiveLimit()), "--json", prSearchFields)
		if err != nil {
			return collectPRs(byKey, order), err
		}
		prs, err := parseGitHubSearchPRs(out, role)
		if err != nil {
			return collectPRs(byKey, order), err
		}
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
	}
	result = collectPRs(byKey, order)
	return result, nil
}

// ListRepoPRs lists one repository's requests with the head branch, review
// decision and check rollup that the search surface cannot report.
func (g *gh) ListRepoPRs(ctx context.Context, repo string, q PRQuery) ([]PullRequest, error) {
	if !g.Available() {
		return nil, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := g.run(ctx, "", "pr", "list", "--repo", repo,
		"--state", string(q.EffectiveState()),
		"--limit", strconv.Itoa(q.EffectiveLimit()), "--json", prListFields)
	if err != nil {
		return nil, err
	}
	return parseGitHubRepoPRs(out, repo)
}

// collectPRs renders a keyed set back into first-seen order, so that output is
// stable regardless of map iteration.
func collectPRs(byKey map[string]PullRequest, order []string) []PullRequest {
	out := make([]PullRequest, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out
}

type githubPRAuthor struct {
	Login string `json:"login"`
}

func parseGitHubSearchPRs(out string, role PRRole) ([]PullRequest, error) {
	var raw []struct {
		Number     int            `json:"number"`
		Title      string         `json:"title"`
		URL        string         `json:"url"`
		State      string         `json:"state"`
		IsDraft    bool           `json:"isDraft"`
		Author     githubPRAuthor `json:"author"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode gh search prs: %w", err)
	}
	result := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		created, _ := time.Parse(time.RFC3339, r.CreatedAt)
		updated, _ := time.Parse(time.RFC3339, r.UpdatedAt)
		result = append(result, PullRequest{
			Forge: GitHub, Repo: r.Repository.NameWithOwner, Number: r.Number,
			Title: r.Title, URL: r.URL, State: githubPRState(r.State, false),
			Draft: r.IsDraft, Author: r.Author.Login,
			Roles: []PRRole{role}, Detail: PRDetailSummary,
			CreatedAt: created, UpdatedAt: updated,
		})
	}
	return result, nil
}

func parseGitHubRepoPRs(out, repo string) ([]PullRequest, error) {
	var raw []struct {
		Number            int            `json:"number"`
		Title             string         `json:"title"`
		URL               string         `json:"url"`
		State             string         `json:"state"`
		IsDraft           bool           `json:"isDraft"`
		Author            githubPRAuthor `json:"author"`
		HeadRefName       string         `json:"headRefName"`
		BaseRefName       string         `json:"baseRefName"`
		ReviewDecision    string         `json:"reviewDecision"`
		Mergeable         string         `json:"mergeable"`
		StatusCheckRollup []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode gh pr list: %w", err)
	}
	result := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		created, _ := time.Parse(time.RFC3339, r.CreatedAt)
		updated, _ := time.Parse(time.RFC3339, r.UpdatedAt)
		checks := make([]checkOutcome, 0, len(r.StatusCheckRollup))
		for _, c := range r.StatusCheckRollup {
			checks = append(checks, checkOutcome{Status: c.Status, Conclusion: c.Conclusion, State: c.State})
		}
		result = append(result, PullRequest{
			Forge: GitHub, Repo: repo, Number: r.Number,
			Title: r.Title, URL: r.URL, State: githubPRState(r.State, false),
			Draft: r.IsDraft, Author: r.Author.Login, Detail: PRDetailFull,
			HeadBranch: r.HeadRefName, BaseBranch: r.BaseRefName,
			ReviewDecision: strings.ToLower(r.ReviewDecision),
			Mergeable:      strings.ToLower(r.Mergeable),
			Checks:         foldChecks(checks),
			CreatedAt:      created, UpdatedAt: updated,
		})
	}
	return result, nil
}

// githubPRState normalizes gh's uppercase states. gh reports a merged request
// as MERGED rather than CLOSED, so the distinction survives.
func githubPRState(state string, _ bool) PRState {
	switch strings.ToUpper(state) {
	case "OPEN":
		return PRStateOpen
	case "MERGED":
		return PRStateMerged
	case "CLOSED":
		return PRStateClosed
	}
	return PRState(strings.ToLower(state))
}
