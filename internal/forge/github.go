package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// gh drives the GitHub CLI.
type gh struct{}

func (g *gh) Kind() Kind      { return GitHub }
func (g *gh) Bin() string     { return "gh" }
func (g *gh) Available() bool { return have("gh") }

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
	out, err := run(ctx, "gh", dir, args...)
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
	out, err := run(ctx, "gh", dir, args...)
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
		return createRepoResult(GitHub, target, out, false), err
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
		out, err := run(ctx, "gh", "", "api", "user/repos", "--method", "GET",
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
