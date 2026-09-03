package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AzureDevOpsTarget identifies one Azure DevOps Services project to query.
type AzureDevOpsTarget struct {
	Organization string
	Project      string
}

// ErrNoExtension reports that Azure CLI is present without the Azure DevOps
// extension. Checking explicitly prevents az from prompting for a dynamic
// extension installation inside the TUI.
type ErrNoExtension struct{ Name string }

func (e *ErrNoExtension) Error() string {
	return fmt.Sprintf("Azure DevOps support needs the %q Azure CLI extension; install it with `az extension add --name %s`", e.Name, e.Name)
}

// ErrUnsupported reports an operation a forge adapter intentionally does not
// implement yet.
type ErrUnsupported struct {
	Kind      Kind
	Operation string
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("%s does not support %s through dev yet", e.Kind, e.Operation)
}

// az drives Azure CLI's azure-devops extension.
type az struct {
	targets []AzureDevOpsTarget
}

// NewAzureDevOps creates an Azure DevOps adapter for configured inventory
// targets. A target-less adapter is still useful for PR creation in an Azure
// checkout, where az auto-detects organization, project and repository.
func NewAzureDevOps(targets []AzureDevOpsTarget) Forge {
	copyTargets := append([]AzureDevOpsTarget(nil), targets...)
	return &az{targets: copyTargets}
}

func (a *az) Kind() Kind      { return AzureDevOps }
func (a *az) Bin() string     { return "az" }
func (a *az) Available() bool { return have("az") }

func (a *az) ensureExtension(ctx context.Context) error {
	return a.ensureExtensionWithRunner(ctx, run)
}

func (a *az) ensureExtensionWithRunner(ctx context.Context, runner cliRunner) error {
	if !a.Available() {
		return &ErrNoCLI{Kind: AzureDevOps, Bin: "az"}
	}
	out, err := runner(ctx, "az", "", "extension", "show", "--name", "azure-devops",
		"--query", "name", "--output", "tsv", "--only-show-errors")
	if err != nil || strings.TrimSpace(out) != "azure-devops" {
		return &ErrNoExtension{Name: "azure-devops"}
	}
	return nil
}

// CheckAzureDevOps verifies that both Azure CLI and its Azure DevOps extension
// are available without invoking an az repos command or dynamic installation.
func CheckAzureDevOps(ctx context.Context) error {
	return (&az{}).ensureExtension(ctx)
}

// CreatePR opens an Azure Repos pull request. The branch has already been
// pushed by dev; --detect resolves the target from the checkout's Azure remote.
func (a *az) CreatePR(ctx context.Context, dir string, req PRRequest) (string, error) {
	if err := a.ensureExtension(ctx); err != nil {
		return "", err
	}
	args := []string{"repos", "pr", "create", "--detect", "true",
		"--source-branch", req.Head, "--target-branch", req.Base}
	if req.Title != "" {
		args = append(args, "--title", req.Title)
	}
	if req.Body != "" {
		args = append(args, "--description", req.Body)
	}
	if req.Draft {
		args = append(args, "--draft", "true")
	}
	if req.Web {
		args = append(args, "--open")
	}
	args = append(args, "--query", "{pullRequestId:pullRequestId,remoteUrl:repository.remoteUrl}",
		"--output", "json", "--only-show-errors")
	out, err := run(ctx, "az", dir, args...)
	if err != nil {
		return "", err
	}
	var result struct {
		PullRequestID int    `json:"pullRequestId"`
		RemoteURL     string `json:"remoteUrl"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", fmt.Errorf("decode az repos pr create: %w", err)
	}
	if result.PullRequestID <= 0 {
		return "", errors.New("decode az repos pr create: response has no pullRequestId")
	}
	_, repoURL, ok := parseAzureDevOpsRemote(result.RemoteURL)
	if !ok {
		return "", fmt.Errorf("decode az repos pr create: unrecognized repository remote %q", result.RemoteURL)
	}
	return strings.TrimRight(repoURL, "/") + "/pullrequest/" + fmt.Sprint(result.PullRequestID), nil
}

const azureReviewJSONQuery = "[].{pullRequestId:pullRequestId,status:status,isDraft:isDraft,sourceRefName:sourceRefName,targetRefName:targetRefName,remoteUrl:remoteUrl,repositoryRemoteUrl:repository.remoteUrl,forkSource:forkSource}"

// QueryReview manually observes an exact Azure Repos pull request relationship.
func (a *az) QueryReview(ctx context.Context, dir string, query ReviewQuery) (*Review, error) {
	if err := validateReviewQuery(query); err != nil {
		return nil, err
	}
	if err := a.ensureExtensionWithRunner(ctx, reviewRunner); err != nil {
		return nil, err
	}

	effectiveQuery, scopeArgs := a.reviewQueryScope(query)
	const pageSize = 100
	var candidates []reviewCandidate
	for skip := 0; ; skip += pageSize {
		args := []string{"repos", "pr", "list"}
		args = append(args, scopeArgs...)
		args = append(args,
			"--source-branch", query.Head, "--target-branch", query.Base,
			"--status", "all", "--top", strconv.Itoa(pageSize), "--skip", strconv.Itoa(skip),
			"--query", azureReviewJSONQuery, "--output", "json", "--only-show-errors")
		out, err := reviewRunner(ctx, "az", dir, args...)
		if err != nil {
			return nil, err
		}
		pageCandidates, err := parseAzureReviewCandidates(out)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, pageCandidates...)
		if len(pageCandidates) < pageSize {
			break
		}
	}
	return selectExactReview(AzureDevOps, effectiveQuery, candidates, normalizeAzureReviewState)
}

func (a *az) reviewQueryScope(query ReviewQuery) (ReviewQuery, []string) {
	organization, project, repository, fullIdentity := splitAzureReviewIdentity(query.Repository)
	if fullIdentity {
		organizationURL := "https://dev.azure.com/" + url.PathEscape(organization)
		for _, target := range a.targets {
			if strings.EqualFold(target.Project, project) && strings.EqualFold(azureTargetOrganization(target.Organization), organization) {
				organizationURL = target.Organization
				break
			}
		}
		query.Repository = strings.Join([]string{organization, project, repository}, "/")
		query.Head = azureReviewBranch(query.Head)
		query.Base = azureReviewBranch(query.Base)
		return query, []string{
			"--detect", "false", "--organization", organizationURL,
			"--project", project, "--repository", repository,
		}
	}

	if len(a.targets) == 1 {
		target := a.targets[0]
		repository := strings.Trim(query.Repository, "/")
		if organization := azureTargetOrganization(target.Organization); organization != "" {
			query.Repository = strings.Join([]string{organization, target.Project, repository}, "/")
		}
		query.Head = azureReviewBranch(query.Head)
		query.Base = azureReviewBranch(query.Base)
		return query, []string{
			"--detect", "false", "--organization", target.Organization,
			"--project", target.Project, "--repository", repository,
		}
	}

	query.Head = azureReviewBranch(query.Head)
	query.Base = azureReviewBranch(query.Base)
	return query, []string{"--detect", "true", "--repository", query.Repository}
}

func splitAzureReviewIdentity(identity string) (organization, project, repository string, ok bool) {
	parts := strings.Split(strings.Trim(identity, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func azureTargetOrganization(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Hostname() != "" {
		host := strings.ToLower(parsed.Hostname())
		switch {
		case host == "dev.azure.com":
			segments := escapedSegments(parsed.EscapedPath())
			if len(segments) == 1 {
				return segments[0]
			}
		case strings.HasSuffix(host, ".visualstudio.com"):
			return strings.TrimSuffix(host, ".visualstudio.com")
		}
		return ""
	}
	if !strings.ContainsAny(trimmed, "/:") {
		return trimmed
	}
	return ""
}

func parseAzureReviewCandidates(out string) ([]reviewCandidate, error) {
	var raw []struct {
		PullRequestID       *int            `json:"pullRequestId"`
		Status              string          `json:"status"`
		IsDraft             *bool           `json:"isDraft"`
		SourceRefName       string          `json:"sourceRefName"`
		TargetRefName       string          `json:"targetRefName"`
		RemoteURL           string          `json:"remoteUrl"`
		RepositoryRemoteURL string          `json:"repositoryRemoteUrl"`
		ForkSource          json.RawMessage `json:"forkSource"`
	}
	if err := decodeReviewJSON(AzureDevOps, out, &raw); err != nil {
		return nil, err
	}
	candidates := make([]reviewCandidate, 0, len(raw))
	for _, item := range raw {
		id, number := "", 0
		if item.PullRequestID != nil {
			number = *item.PullRequestID
			id = strconv.Itoa(number)
		}
		reviewURL := item.RemoteURL
		if !validReviewURL(reviewURL) && item.RepositoryRemoteURL != "" && number > 0 {
			if _, repositoryURL, ok := parseAzureDevOpsRemote(item.RepositoryRemoteURL); ok {
				reviewURL = strings.TrimRight(repositoryURL, "/") + "/pullrequest/" + strconv.Itoa(number)
			}
		}
		var sourceMatches *bool
		switch strings.TrimSpace(string(item.ForkSource)) {
		case "null":
			matches := true
			sourceMatches = &matches
		case "":
			// Missing projection data is not proof that the source is local.
		default:
			matches := false
			sourceMatches = &matches
		}
		candidates = append(candidates, reviewCandidate{
			ID: id, Number: number, State: item.Status, Draft: item.IsDraft,
			URL: reviewURL, Head: azureReviewBranch(item.SourceRefName), Base: azureReviewBranch(item.TargetRefName),
			SourceMatchesRepository: sourceMatches,
		})
	}
	return candidates, nil
}

func azureReviewBranch(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

func normalizeAzureReviewState(state string, draft bool) (ReviewState, error) {
	switch strings.ToLower(state) {
	case "active":
		if draft {
			return ReviewDraft, nil
		}
		return ReviewOpen, nil
	case "completed":
		return ReviewMerged, nil
	case "abandoned":
		return ReviewClosed, nil
	default:
		return "", errors.New("unrecognized Azure DevOps review state")
	}
}

// CreateRepo is deliberately deferred: Azure repository visibility belongs to
// the team project, so dev's current per-repository private/public contract does
// not map safely.
func (a *az) CreateRepo(context.Context, string, RepoRequest) (string, error) {
	return "", &ErrUnsupported{Kind: AzureDevOps, Operation: "repository creation"}
}

// CloneURL preserves explicit Azure clone URLs. Azure has no unambiguous bare
// owner/name shorthand because organization and project are both required.
func (a *az) CloneURL(ref string) string { return ref }

// ListRepos queries every configured organization/project concurrently. Azure
// CLI already returns every repository in the selected team project.
func (a *az) ListRepos(ctx context.Context) ([]RemoteRepo, error) {
	if len(a.targets) == 0 {
		return nil, errors.New("Azure DevOps remote inventory needs at least one [[forge.azure_devops]] target")
	}
	if err := a.ensureExtension(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	type result struct {
		repos []RemoteRepo
		err   error
	}
	ch := make(chan result, len(a.targets))
	var wg sync.WaitGroup
	for _, target := range a.targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := run(ctx, "az", "", "repos", "list", "--detect", "false",
				"--organization", target.Organization, "--project", target.Project,
				"--output", "json", "--only-show-errors")
			if err != nil {
				ch <- result{err: fmt.Errorf("Azure DevOps %s / %s: %w", target.Organization, target.Project, err)}
				return
			}
			repos, err := parseAzureDevOpsRepos(out)
			if err != nil {
				err = fmt.Errorf("Azure DevOps %s / %s: %w", target.Organization, target.Project, err)
			}
			ch <- result{repos: repos, err: err}
		}()
	}
	wg.Wait()
	close(ch)

	var repos []RemoteRepo
	var errs []error
	for item := range ch {
		repos = append(repos, item.repos...)
		if item.err != nil {
			errs = append(errs, item.err)
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Label() < repos[j].Label() })
	unique := repos[:0]
	seen := map[string]bool{}
	for _, repo := range repos {
		key := strings.ToLower(repo.Label())
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, repo)
	}
	return unique, errors.Join(errs...)
}

func parseAzureDevOpsRepos(out string) ([]RemoteRepo, error) {
	var raw []struct {
		Name          string `json:"name"`
		DefaultBranch string `json:"defaultBranch"`
		IsFork        bool   `json:"isFork"`
		RemoteURL     string `json:"remoteUrl"`
		SSHURL        string `json:"sshUrl"`
		Project       struct {
			Name       string `json:"name"`
			Visibility string `json:"visibility"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("decode az repos list: %w", err)
	}
	repos := make([]RemoteRepo, 0, len(raw))
	for _, item := range raw {
		identity, webURL, ok := parseAzureDevOpsRemote(item.RemoteURL)
		if !ok {
			return nil, fmt.Errorf("decode az repos list: unrecognized remoteUrl %q", item.RemoteURL)
		}
		repos = append(repos, RemoteRepo{
			Forge: AzureDevOps, Name: item.Name, FullName: identity,
			URL: webURL, CloneURL: webURL, SSHURL: item.SSHURL,
			Visibility:    strings.ToLower(item.Project.Visibility),
			DefaultBranch: strings.TrimPrefix(item.DefaultBranch, "refs/heads/"),
			Fork:          item.IsFork,
		})
	}
	return repos, nil
}

// parseAzureDevOpsRemote returns the provider identity and browser-safe HTTPS
// repository URL for supported Azure DevOps Services clone URL forms.
func parseAzureDevOpsRemote(raw string) (identity, webURL string, ok bool) {
	if !safeRawURLText(raw) {
		return "", "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Fragment != "" || u.RawQuery != "" {
			return "", "", false
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			if u.Port() != "" && u.Port() != "443" {
				return "", "", false
			}
			return parseAzureDevOpsHTTPS(u)
		case "ssh":
			if u.User == nil || (u.Port() != "" && u.Port() != "22") {
				return "", "", false
			}
			if _, hasPassword := u.User.Password(); hasPassword {
				return "", "", false
			}
			return parseAzureDevOpsSSH(u.User.Username(), strings.ToLower(u.Hostname()), escapedSegments(u.EscapedPath()))
		default:
			return "", "", false
		}
	}
	at := strings.IndexByte(raw, '@')
	if at <= 0 {
		return "", "", false
	}
	colon := strings.IndexByte(raw[at+1:], ':')
	if colon < 0 {
		return "", "", false
	}
	colon += at + 1
	path := raw[colon+1:]
	if strings.ContainsAny(path, "?#") {
		return "", "", false
	}
	return parseAzureDevOpsSSH(raw[:at], strings.ToLower(raw[at+1:colon]), decodeSegments(strings.Split(strings.Trim(path, "/"), "/")))
}

func parseAzureDevOpsHTTPS(u *url.URL) (string, string, bool) {
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return "", "", false
		}
	}
	host := strings.ToLower(u.Hostname())
	segments := escapedSegments(u.EscapedPath())
	var organization, project, repo string
	switch {
	case host == "dev.azure.com":
		if len(segments) != 4 || !strings.EqualFold(segments[2], "_git") {
			return "", "", false
		}
		organization, project, repo = segments[0], segments[1], segments[3]
		if u.User != nil && !strings.EqualFold(u.User.Username(), organization) {
			return "", "", false
		}
	case strings.HasSuffix(host, ".visualstudio.com") && host != "vs-ssh.visualstudio.com":
		if u.User != nil {
			return "", "", false
		}
		switch {
		case len(segments) == 3 && strings.EqualFold(segments[1], "_git"):
			project, repo = segments[0], segments[2]
		case len(segments) == 4 && strings.EqualFold(segments[0], "DefaultCollection") && strings.EqualFold(segments[2], "_git"):
			project, repo = segments[1], segments[3]
		default:
			return "", "", false
		}
		organization = strings.TrimSuffix(host, ".visualstudio.com")
	default:
		return "", "", false
	}
	repo = strings.TrimSuffix(repo, ".git")
	if organization == "" || project == "" || repo == "" {
		return "", "", false
	}
	identity := strings.Join([]string{organization, project, repo}, "/")
	webURL := azureDevOpsWebURL(host, organization, project, repo)
	return identity, webURL, true
}

func parseAzureDevOpsSSH(user, host string, segments []string) (string, string, bool) {
	var organization, project, repo string
	switch {
	case host == "ssh.dev.azure.com" && strings.EqualFold(user, "git") && len(segments) == 4 && strings.EqualFold(segments[0], "v3"):
		organization, project, repo = segments[1], segments[2], segments[3]
		host = "dev.azure.com"
	case host == "vs-ssh.visualstudio.com" && len(segments) == 3 && strings.EqualFold(segments[1], "_ssh"):
		organization, project, repo = user, segments[0], segments[2]
		host = strings.ToLower(organization) + ".visualstudio.com"
		if !validWebHost(host) {
			return "", "", false
		}
	default:
		return "", "", false
	}
	repo = strings.TrimSuffix(repo, ".git")
	if organization == "" || project == "" || repo == "" {
		return "", "", false
	}
	identity := strings.Join([]string{organization, project, repo}, "/")
	return identity, azureDevOpsWebURL(host, organization, project, repo), true
}

func azureDevOpsWebURL(host, organization, project, repo string) string {
	if host == "dev.azure.com" {
		return "https://dev.azure.com/" + url.PathEscape(organization) + "/" + url.PathEscape(project) + "/_git/" + url.PathEscape(repo)
	}
	return "https://" + host + "/" + url.PathEscape(project) + "/_git/" + url.PathEscape(repo)
}

func escapedSegments(path string) []string {
	return decodeSegments(strings.Split(strings.Trim(path, "/"), "/"))
}

func decodeSegments(segments []string) []string {
	if len(segments) == 1 && segments[0] == "" {
		return nil
	}
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || hasEncodedSeparator(segment) {
			return nil
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || !safeRawURLText(decoded) ||
			strings.ContainsAny(decoded, `/\\`) || hasEncodedSeparator(decoded) {
			return nil
		}
		out = append(out, decoded)
	}
	return out
}
