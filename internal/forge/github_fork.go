package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

var (
	forkRunner       cliRunner = runForkCommand
	forkOwnerPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	forkNamePattern            = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	errForkNotFound            = errors.New("GitHub repository not found or inaccessible")
)

type githubForkRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	URL           string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	DefaultBranch string `json:"default_branch"`
	Fork          *bool  `json:"fork"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Parent *githubForkRepository `json:"parent"`
	Source *githubForkRepository `json:"source"`
}

func (g *gh) PlanFork(ctx context.Context, source string) (ForkPlan, error) {
	name, err := githubForkSource(source)
	if err != nil {
		return ForkPlan{}, err
	}
	if !g.Available() {
		return ForkPlan{}, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	owner, err := githubForkOwner(ctx)
	if err != nil {
		return ForkPlan{}, err
	}
	sourceRepo, raw, err := githubReadForkRepository(ctx, name)
	if err != nil {
		return ForkPlan{}, fmt.Errorf("inspect fork source: %w", err)
	}
	plan := ForkPlan{Source: sourceRepo, Owner: owner}
	if strings.EqualFold(sourceRepo.Owner, owner) {
		if !sourceRepo.IsFork {
			return ForkPlan{}, errors.New("the source is already your repository and is not a fork")
		}
		parent, _, err := githubReadForkRepository(ctx, raw.Parent.FullName)
		if err != nil {
			return ForkPlan{}, fmt.Errorf("inspect fork parent: %w", err)
		}
		if parent.ID != raw.Parent.ID || parent.NetworkID != sourceRepo.NetworkID || strings.EqualFold(parent.Owner, owner) {
			return ForkPlan{}, errors.New("GitHub did not confirm the fork's original repository")
		}
		plan.Source, plan.Fork, plan.Exists = parent, sourceRepo, true
		return plan, nil
	}
	_, base, _ := strings.Cut(sourceRepo.FullName, "/")
	plan.Fork = prospectiveGitHubFork(owner, base, sourceRepo)
	fork, _, err := githubReadForkRepository(ctx, plan.Fork.FullName)
	if errors.Is(err, errForkNotFound) {
		return plan, nil
	}
	if err != nil {
		return ForkPlan{}, fmt.Errorf("inspect personal fork: %w", err)
	}
	if err := verifyPersonalFork(fork, sourceRepo, owner); err != nil {
		return ForkPlan{}, err
	}
	plan.Fork, plan.Exists = fork, true
	return plan, nil
}

func (g *gh) ApplyFork(ctx context.Context, plan ForkPlan) (ForkResult, error) {
	result := ForkResult{Plan: plan}
	if err := validateGitHubForkPlan(plan); err != nil {
		return result, err
	}
	if !g.Available() {
		return result, &ErrNoCLI{Kind: GitHub, Bin: "gh"}
	}
	owner, err := githubForkOwner(ctx)
	if err != nil {
		return result, err
	}
	if !strings.EqualFold(owner, plan.Owner) {
		return result, ErrForkPlanChanged
	}
	source, _, err := githubReadForkRepository(ctx, plan.Source.FullName)
	if err != nil {
		return result, fmt.Errorf("recheck fork source: %w", err)
	}
	if !sameForkRepository(source, plan.Source) {
		return result, ErrForkPlanChanged
	}
	fork, _, err := githubReadForkRepository(ctx, plan.Fork.FullName)
	if err == nil {
		if err := verifyPersonalFork(fork, source, owner); err != nil {
			return result, err
		}
		if plan.Exists && !sameForkRepository(fork, plan.Fork) {
			return result, ErrForkPlanChanged
		}
		result.Plan.Fork, result.Plan.Exists, result.Reused = fork, true, true
		return result, nil
	}
	if !errors.Is(err, errForkNotFound) {
		return result, fmt.Errorf("recheck personal fork: %w", err)
	}
	if plan.Exists {
		return result, ErrForkPlanChanged
	}
	createCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	// An explicit repository plus --clone=false is gh's remote-only path.
	// --remote (even false) is rejected whenever a repository is provided.
	_, createErr := forkRunner(createCtx, "gh", "", "repo", "fork", source.URL, "--clone=false")
	cancel()
	createErr = classifyAuth(GitHub, "gh", createErr)
	// A timed-out CLI may already have created the fork. Reconcile by reads only,
	// with a short independent deadline; never retry the write or trust chatter.
	verifyCtx, verifyCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer verifyCancel()
	fork, verifyErr := githubVerifyCreatedFork(verifyCtx, plan.Fork.FullName, createErr == nil)
	if verifyErr == nil {
		verifyErr = verifyPersonalFork(fork, source, owner)
	}
	if verifyErr != nil {
		result.Unknown = true
		return result, fmt.Errorf("fork creation outcome is unconfirmed; inspect %s before retrying: %w", plan.Fork.URL, errors.Join(createErr, verifyErr))
	}
	result.Plan.Fork, result.Plan.Exists = fork, true
	if createErr != nil {
		// The resulting repository is confirmed, but attributing its creation to
		// this invocation after a failed transport would claim too much.
		return result, nil
	}
	result.Created = true
	return result, nil
}

// GitHub creation is asynchronous. A successful request may briefly precede
// repository visibility, so allow two bounded read retries for an actual 404.
// Operational errors and failed writes receive no retry.
func githubVerifyCreatedFork(ctx context.Context, name string, retryAbsent bool) (ForkRepository, error) {
	for attempt := 0; ; attempt++ {
		repo, _, err := githubReadForkRepository(ctx, name)
		if !retryAbsent || !errors.Is(err, errForkNotFound) || attempt == 2 {
			return repo, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ForkRepository{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func githubForkSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if validGitHubForkFullName(source) {
		return source, nil
	}
	if gitx.NetworkCloneURL(source) == nil {
		identity := ParseRemoteIdentity(source)
		if identity.Kind == GitHub && identity.Host == "github.com" && validGitHubForkFullName(identity.Name) {
			return identity.Name, nil
		}
	}
	return "", errors.New("fork source must be a GitHub owner/name or github.com Git URL; other hosts are not supported")
}

func validGitHubForkFullName(name string) bool {
	owner, repo, ok := strings.Cut(name, "/")
	return ok && forkOwnerPattern.MatchString(owner) && forkNamePattern.MatchString(repo) && repo != "." && repo != ".."
}

func prospectiveGitHubFork(owner, name string, source ForkRepository) ForkRepository {
	fullName := owner + "/" + name
	return ForkRepository{
		Host: "github.com", FullName: fullName, Owner: owner,
		URL: "https://github.com/" + fullName, CloneURL: "https://github.com/" + fullName + ".git",
		SSHURL: "git@github.com:" + fullName + ".git", IsFork: true, NetworkID: source.NetworkID,
		DefaultBranch: source.DefaultBranch,
	}
}

func githubForkOwner(ctx context.Context) (string, error) {
	body, err := githubForkRead(ctx, "user")
	if err != nil {
		return "", fmt.Errorf("identify authenticated GitHub account: %w", err)
	}
	var user struct {
		Login string `json:"login"`
	}
	if json.Unmarshal([]byte(body), &user) != nil || !forkOwnerPattern.MatchString(user.Login) {
		return "", errors.New("GitHub returned an invalid authenticated account")
	}
	return user.Login, nil
}

func githubReadForkRepository(ctx context.Context, name string) (ForkRepository, githubForkRepository, error) {
	var raw githubForkRepository
	if !validGitHubForkFullName(name) {
		return ForkRepository{}, raw, errors.New("invalid GitHub repository identity")
	}
	body, err := githubForkRead(ctx, "repos/"+name)
	if err != nil {
		return ForkRepository{}, raw, err
	}
	if json.Unmarshal([]byte(body), &raw) != nil {
		return ForkRepository{}, raw, errors.New("GitHub returned invalid repository JSON")
	}
	repo := ForkRepository{
		ID: raw.ID, Host: "github.com", FullName: raw.FullName, URL: raw.URL,
		CloneURL: raw.CloneURL, SSHURL: raw.SSHURL, DefaultBranch: raw.DefaultBranch,
		Owner: raw.Owner.Login, IsFork: raw.Fork != nil && *raw.Fork, NetworkID: raw.ID,
	}
	if raw.Fork == nil || !validGitHubForkRepository(repo) || !strings.EqualFold(repo.FullName, name) {
		return ForkRepository{}, raw, errors.New("GitHub returned an inconsistent repository identity")
	}
	if repo.IsFork {
		if raw.Parent == nil || raw.Source == nil || raw.Parent.ID <= 0 || raw.Source.ID <= 0 ||
			raw.Source.ID == raw.ID || !validGitHubForkFullName(raw.Parent.FullName) || !validGitHubForkFullName(raw.Source.FullName) {
			return ForkRepository{}, raw, errors.New("GitHub did not report the repository's fork network")
		}
		repo.NetworkID = raw.Source.ID
	}
	return repo, raw, nil
}

func validGitHubForkRepository(repo ForkRepository) bool {
	owner, _, _ := strings.Cut(repo.FullName, "/")
	if repo.ID <= 0 || repo.Host != "github.com" || !validGitHubForkFullName(repo.FullName) || !strings.EqualFold(owner, repo.Owner) ||
		repo.DefaultBranch == "" || strings.IndexFunc(repo.DefaultBranch, unicode.IsControl) >= 0 {
		return false
	}
	u, err := url.Parse(repo.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.EqualFold(u.Path, "/"+repo.FullName) {
		return false
	}
	for _, remote := range []string{repo.CloneURL, repo.SSHURL} {
		if gitx.NetworkCloneURL(remote) != nil {
			return false
		}
		identity := ParseRemoteIdentity(remote)
		if identity.Kind != GitHub || identity.Host != "github.com" || !strings.EqualFold(identity.Name, repo.FullName) {
			return false
		}
	}
	return true
}

func verifyPersonalFork(fork, source ForkRepository, owner string) error {
	if !strings.EqualFold(fork.Owner, owner) || !fork.IsFork || fork.NetworkID != source.NetworkID || fork.ID == source.ID {
		return fmt.Errorf("%s already exists but is not your fork of %s", fork.FullName, source.FullName)
	}
	return nil
}

func sameForkRepository(a, b ForkRepository) bool {
	return a.ID == b.ID && a.Host == b.Host && strings.EqualFold(a.FullName, b.FullName) &&
		strings.EqualFold(a.Owner, b.Owner) && a.NetworkID == b.NetworkID && a.IsFork == b.IsFork &&
		a.DefaultBranch == b.DefaultBranch
}

func validateGitHubForkPlan(plan ForkPlan) error {
	if !validGitHubForkRepository(plan.Source) || plan.Source.NetworkID <= 0 || !forkOwnerPattern.MatchString(plan.Owner) ||
		strings.EqualFold(plan.Source.Owner, plan.Owner) || !strings.EqualFold(plan.Fork.Owner, plan.Owner) {
		return errors.New("invalid GitHub fork plan")
	}
	if plan.Exists {
		if !validGitHubForkRepository(plan.Fork) {
			return errors.New("invalid existing fork identity")
		}
		return verifyPersonalFork(plan.Fork, plan.Source, plan.Owner)
	}
	_, base, _ := strings.Cut(plan.Source.FullName, "/")
	if plan.Fork != prospectiveGitHubFork(plan.Owner, base, plan.Source) {
		return errors.New("invalid prospective fork identity")
	}
	return nil
}

// --include gives an authoritative HTTP status even when gh exits nonzero.
// A rate limit, unavailable network, or malformed response must not become an
// absent fork and thereby authorize a create attempt.
func githubForkRead(ctx context.Context, endpoint string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := forkRunner(ctx, "gh", "", "api", "--hostname", "github.com", "--method", "GET", "--include", endpoint)
	status, body, parseErr := parseForkHTTP(out)
	if parseErr == nil && status == 404 {
		return "", errForkNotFound
	}
	if parseErr == nil && status == 401 {
		_, action, _ := authProbe(GitHub)
		return "", &ErrAuth{Kind: GitHub, Bin: "gh", Action: action, cause: err}
	}
	if parseErr == nil && (status < 200 || status >= 300) {
		// An authoritative 403/5xx is an operational failure, even if its
		// diagnostic happens to suggest "gh auth login".
		if err != nil {
			return "", fmt.Errorf("GitHub repository lookup returned HTTP %d: %w", status, err)
		}
		return "", fmt.Errorf("GitHub repository lookup returned HTTP %d", status)
	}
	if err != nil {
		return "", classifyAuth(GitHub, "gh", err)
	}
	if parseErr != nil {
		return "", parseErr
	}
	return body, nil
}

func parseForkHTTP(out string) (int, string, error) {
	bad := errors.New("GitHub returned an invalid HTTP response")
	if len(out) > 1<<20 {
		return 0, "", errors.New("GitHub repository response is too large")
	}
	header, body, ok := strings.Cut(out, "\r\n\r\n")
	if !ok {
		header, body, ok = strings.Cut(out, "\n\n")
	}
	if !ok {
		return 0, "", bad
	}
	line, _, _ := strings.Cut(header, "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, "", bad
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 100 || status > 599 {
		return 0, "", bad
	}
	return status, body, nil
}

// Forking is a noninteractive remote operation. Close stdin, disable gh's
// prompts, and keep stdout separate so provider chatter cannot become identity.
func runForkCommand(ctx context.Context, bin, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), &commandError{Bin: bin, Args: append([]string(nil), args...), Detail: strings.TrimSpace(stderr.String()), Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}
