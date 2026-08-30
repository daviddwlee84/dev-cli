// Package forge wraps the GitHub, GitLab and Azure DevOps CLIs. All are optional: every
// entry point degrades to plain git, because a workflow that hard-depends on a
// forge CLI being installed and authenticated stops working on exactly the
// machines where you most need it.
package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Kind identifies a forge.
type Kind string

const (
	// GitHub is served by the gh CLI.
	GitHub Kind = "github"
	// GitLab is served by the glab CLI.
	GitLab Kind = "gitlab"
	// AzureDevOps is served by Azure CLI's azure-devops extension.
	AzureDevOps Kind = "azure-devops"
	// Unknown is any other host, or none.
	Unknown Kind = "unknown"
)

// ErrNoCLI reports that the forge's CLI is not installed.
type ErrNoCLI struct {
	Kind Kind
	Bin  string
}

func (e *ErrNoCLI) Error() string {
	return fmt.Sprintf("%s support needs the %q CLI, which is not installed", e.Kind, e.Bin)
}

// Forge is the subset of forge behaviour dev uses.
type Forge interface {
	Kind() Kind
	// Bin is the CLI binary name.
	Bin() string
	Available() bool
	// CreatePR opens a pull/merge request from head into base.
	CreatePR(ctx context.Context, dir string, req PRRequest) (string, error)
	// CreateRepo creates a remote repository from an existing local one.
	CreateRepo(ctx context.Context, dir string, req RepoRequest) (string, error)
	// CloneURL renders the clone target for an owner/name reference.
	CloneURL(ref string) string
	// ListRepos lists every repository visible to the authenticated user.
	ListRepos(ctx context.Context) ([]RemoteRepo, error)
}

// RepoPublisher is implemented by forge adapters that can create an empty
// upstream repository without changing the caller's local Git configuration.
// Keeping this separate from Forge.CreateRepo preserves the existing graduate
// flow, whose compatibility contract lets the provider CLI add a remote and
// optionally push it.
type RepoPublisher interface {
	PublishRepo(ctx context.Context, dir string, req RepoRequest) (CreateRepoResult, error)
}

// RemoteRepo is one repository visible through a forge CLI.
type RemoteRepo struct {
	Forge         Kind      `json:"forge"`
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Description   string    `json:"description,omitempty"`
	URL           string    `json:"url"`
	CloneURL      string    `json:"clone_url"`
	SSHURL        string    `json:"ssh_url,omitempty"`
	Visibility    string    `json:"visibility,omitempty"`
	DefaultBranch string    `json:"default_branch,omitempty"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// Label is the provider-qualified identity shown in a combined list.
func (r RemoteRepo) Label() string { return string(r.Forge) + ":" + r.FullName }

// All returns the default forge adapters plus any explicitly configured ones.
// Azure DevOps is not included by default because its inventory requires an
// organization/project target.
func All(configured ...Forge) []Forge {
	return append([]Forge{&gh{}, &glab{}}, configured...)
}

// PRRequest describes a pull/merge request to open.
type PRRequest struct {
	Base  string
	Head  string
	Title string
	Body  string
	Draft bool
	// Fill asks the CLI to derive title and body from the commits.
	Fill bool
	// Web opens the created request in a browser.
	Web bool
}

// Visibility is a forge repository's access level.
type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityPrivate  Visibility = "private"
	VisibilityInternal Visibility = "internal"
)

// RepoRequest describes a remote repository to create. Name alone retains the
// original API. New callers may provide Namespace plus Name, or an explicit
// FullName; FullName takes precedence and Namespace qualifies only a bare Name.
type RepoRequest struct {
	Name        string
	Namespace   string
	FullName    string
	Description string
	Visibility  Visibility
	// Private is retained for existing callers. An explicit Visibility wins;
	// otherwise Private selects private and its zero value selects public.
	Private bool
	// Push pushes the current branch after creating the remote.
	Push bool
	// RemoteName is the git remote to add, defaulting to origin.
	RemoteName string
}

// CreateRepoResult is the normalized identity of a newly published upstream.
// RemoteURL is the URL callers should add as a Git remote; it currently prefers
// HTTPS because it works without assuming that the host has SSH keys installed.
type CreateRepoResult struct {
	Forge      Kind   `json:"forge"`
	Name       string `json:"name"`
	FullName   string `json:"full_name"`
	URL        string `json:"url"`
	CloneURL   string `json:"clone_url"`
	SSHURL     string `json:"ssh_url,omitempty"`
	RemoteURL  string `json:"remote_url"`
	RemoteName string `json:"remote_name"`
}

// ReadinessStatus describes whether an optional forge CLI can publish now.
type ReadinessStatus string

const (
	ReadinessReady           ReadinessStatus = "ready"
	ReadinessMissingCLI      ReadinessStatus = "missing-cli"
	ReadinessUnauthenticated ReadinessStatus = "unauthenticated"
	ReadinessProbeFailed     ReadinessStatus = "probe-failed"
	ReadinessUnsupported     ReadinessStatus = "unsupported"
)

// Readiness is a non-fatal, read-only forge capability probe. Detail is safe to
// show as a diagnostic; Action is the command or next step a user can take.
type Readiness struct {
	Forge         Kind            `json:"forge"`
	Bin           string          `json:"bin"`
	Status        ReadinessStatus `json:"status"`
	Installed     bool            `json:"installed"`
	Authenticated bool            `json:"authenticated"`
	Detail        string          `json:"detail,omitempty"`
	Action        string          `json:"action,omitempty"`
}

// Ready reports whether the provider CLI is both installed and authenticated.
func (r Readiness) Ready() bool { return r.Installed && r.Authenticated }

// Detect infers the forge for a checkout from its origin remote. A repo with
// no remote, or one on a self-hosted host neither CLI knows, resolves to
// Unknown — which callers must treat as "use plain git".
func Detect(ctx context.Context, dir string) Kind {
	return FromURL(gitx.Remote(ctx, dir, "origin"))
}

// FromURL classifies a remote URL.
func FromURL(url string) Kind {
	if _, _, ok := parseAzureDevOpsRemote(url); ok {
		return AzureDevOps
	}
	u := strings.ToLower(url)
	switch {
	case u == "":
		return Unknown
	case strings.Contains(u, "github.com"), strings.Contains(u, "github."):
		return GitHub
	case strings.Contains(u, "gitlab.com"), strings.Contains(u, "gitlab."):
		return GitLab
	}
	return Unknown
}

// For returns the adapter for a kind.
func For(k Kind) (Forge, error) {
	switch k {
	case GitHub:
		return &gh{}, nil
	case GitLab:
		return &glab{}, nil
	case AzureDevOps:
		return NewAzureDevOps(nil), nil
	}
	return nil, errors.New("no forge CLI for this remote; falling back to plain git")
}

// Preferred returns the first available forge, for commands like `repo new`
// that have no existing remote to infer from.
func Preferred() (Forge, error) {
	for _, f := range []Forge{&gh{}, &glab{}} {
		if f.Available() {
			return f, nil
		}
	}
	return nil, errors.New("neither gh nor glab is installed")
}

// PublishRepo creates an empty upstream through adapters that support the
// caller-managed remote/push flow. It never changes local Git configuration.
func PublishRepo(ctx context.Context, f Forge, dir string, req RepoRequest) (CreateRepoResult, error) {
	publisher, ok := f.(RepoPublisher)
	if !ok {
		return CreateRepoResult{}, &ErrUnsupported{Kind: f.Kind(), Operation: "caller-managed repository publishing"}
	}
	return publisher.PublishRepo(ctx, dir, req)
}

// Probe checks installation and authentication without turning an optional
// integration into a command failure. GitHub and GitLab are intentionally
// probed against the host their create commands will use in a repository-less
// working directory.
func Probe(ctx context.Context, kind Kind) Readiness {
	f, err := For(kind)
	if err != nil {
		return Readiness{
			Forge: kind, Status: ReadinessUnsupported,
			Detail: err.Error(), Action: "use a supported forge or add the remote with git",
		}
	}
	return ProbeForge(ctx, f)
}

// ProbeForge is Probe for an already-selected adapter.
func ProbeForge(ctx context.Context, f Forge) Readiness {
	result := Readiness{Forge: f.Kind(), Bin: f.Bin()}
	if !f.Available() {
		result.Status = ReadinessMissingCLI
		result.Detail = fmt.Sprintf("%s is not installed", f.Bin())
		result.Action = installAndLoginAction(f.Kind())
		return result
	}
	result.Installed = true

	args, action, ok := authProbe(f.Kind())
	if !ok {
		result.Status = ReadinessUnsupported
		result.Detail = fmt.Sprintf("%s authentication probing is not supported", f.Kind())
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := probeRunner(probeCtx, f.Bin(), "", args...); err != nil {
		if probeCtx.Err() != nil {
			result.Status = ReadinessProbeFailed
			result.Detail = probeCtx.Err().Error()
			result.Action = "run `" + f.Bin() + " auth status` to diagnose the provider CLI"
			return result
		}
		result.Status = ReadinessUnauthenticated
		result.Detail = err.Error()
		result.Action = action
		return result
	}
	result.Status = ReadinessReady
	result.Authenticated = true
	return result
}

// ProbeAll returns readiness in stable wizard display order.
func ProbeAll(ctx context.Context) []Readiness {
	kinds := []Kind{GitHub, GitLab}
	results := make([]Readiness, len(kinds))
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = Probe(ctx, kind)
		}()
	}
	wg.Wait()
	return results
}

func authProbe(kind Kind) (args []string, action string, ok bool) {
	switch kind {
	case GitHub:
		host := strings.TrimSpace(os.Getenv("GH_HOST"))
		if host == "" {
			host = "github.com"
		}
		return []string{"auth", "status", "--active", "--hostname", host},
			"run `gh auth login --hostname " + host + "`", true
	case GitLab:
		host := strings.TrimSpace(os.Getenv("GITLAB_HOST"))
		if host == "" {
			host = "gitlab.com"
		}
		return []string{"auth", "status", "--hostname", host},
			"run `glab auth login --hostname " + host + "`", true
	default:
		return nil, "", false
	}
}

func installAndLoginAction(kind Kind) string {
	switch kind {
	case GitHub:
		return "install `gh`, then run `gh auth login`"
	case GitLab:
		return "install `glab`, then run `glab auth login`"
	default:
		return "install and authenticate the provider CLI"
	}
}

type repoTarget struct {
	name       string
	namespace  string
	fullName   string
	visibility Visibility
	remoteName string
}

func resolveRepoRequest(req RepoRequest) (repoTarget, error) {
	fullName := strings.Trim(strings.TrimSpace(req.FullName), "/")
	name := strings.Trim(strings.TrimSpace(req.Name), "/")
	namespace := strings.Trim(strings.TrimSpace(req.Namespace), "/")
	if fullName == "" {
		fullName = name
		if namespace != "" && !strings.Contains(name, "/") {
			fullName = namespace + "/" + name
		}
	}
	if fullName == "" {
		return repoTarget{}, errors.New("repository name is required")
	}
	if strings.Contains(fullName, "://") || strings.HasPrefix(fullName, "git@") {
		return repoTarget{}, fmt.Errorf("repository name %q must not be a URL", fullName)
	}
	parts := strings.Split(fullName, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return repoTarget{}, fmt.Errorf("invalid repository name %q", fullName)
		}
	}
	name = parts[len(parts)-1]
	if len(parts) > 1 {
		namespace = strings.Join(parts[:len(parts)-1], "/")
	}

	visibility := req.Visibility
	if visibility == "" {
		if req.Private {
			visibility = VisibilityPrivate
		} else {
			visibility = VisibilityPublic
		}
	}
	switch visibility {
	case VisibilityPublic, VisibilityPrivate, VisibilityInternal:
	default:
		return repoTarget{}, fmt.Errorf("unsupported repository visibility %q", visibility)
	}
	remoteName := strings.TrimSpace(req.RemoteName)
	if remoteName == "" {
		remoteName = "origin"
	}
	return repoTarget{
		name: name, namespace: namespace, fullName: fullName,
		visibility: visibility, remoteName: remoteName,
	}, nil
}

func visibilityFlag(visibility Visibility) string { return "--" + string(visibility) }

func createRepoResult(kind Kind, target repoTarget, out string, allowFallback bool) CreateRepoResult {
	webURL := lastHTTPURL(out)
	fullName := target.fullName
	if parsed, err := url.Parse(webURL); err == nil && parsed.Host != "" {
		if path := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"); path != "" {
			fullName = path
		}
	}
	if webURL == "" && allowFallback && strings.Contains(fullName, "/") {
		host := defaultForgeHost(kind)
		if host != "" {
			webURL = "https://" + host + "/" + fullName
		}
	}
	webURL = strings.TrimSuffix(strings.TrimRight(webURL, "/"), ".git")
	cloneURL := ""
	sshURL := ""
	if parsed, err := url.Parse(webURL); err == nil && parsed.Host != "" {
		path := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
		cloneURL = strings.TrimRight(webURL, "/") + ".git"
		sshURL = "git@" + parsed.Host + ":" + path + ".git"
	}
	name := target.name
	if slash := strings.LastIndex(fullName, "/"); slash >= 0 {
		name = fullName[slash+1:]
	}
	return CreateRepoResult{
		Forge: kind, Name: name, FullName: fullName, URL: webURL,
		CloneURL: cloneURL, SSHURL: sshURL, RemoteURL: cloneURL,
		RemoteName: target.remoteName,
	}
}

func defaultForgeHost(kind Kind) string {
	switch kind {
	case GitHub:
		if host := strings.TrimSpace(os.Getenv("GH_HOST")); host != "" {
			return host
		}
		return "github.com"
	case GitLab:
		if host := strings.TrimSpace(os.Getenv("GITLAB_HOST")); host != "" {
			return host
		}
		return "gitlab.com"
	default:
		return ""
	}
}

type cliRunner func(context.Context, string, string, ...string) (string, error)

// Separate hooks keep readiness and publishing tests platform-independent;
// production uses the same subprocess runner as the legacy adapter methods.
var (
	probeRunner   cliRunner = run
	publishRunner cliRunner = runCombined
	lookupPath              = exec.LookPath
)

// run executes a forge CLI, streaming stderr through so the user sees the
// CLI's own authentication prompts and error messages verbatim.
func run(ctx context.Context, bin, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return strings.TrimSpace(stdout.String()), fmt.Errorf("%s %s: %s: %w",
				bin, strings.Join(args, " "), detail, err)
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// runCombined is used for non-interactive publishing because gh/glab versions
// differ on whether the created repository URL is written to stdout or stderr.
// Stdin is deliberately left closed so a supposedly complete publish request
// cannot unexpectedly drop into a second provider-owned wizard.
func runCombined(ctx context.Context, bin, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return strings.TrimSpace(stdout.String()), fmt.Errorf("%s %s: %s: %w",
				bin, strings.Join(args, " "), detail, err)
		}
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(strings.TrimSpace(stdout.String()) + "\n" + strings.TrimSpace(stderr.String())), nil
}

func have(bin string) bool {
	_, err := lookupPath(bin)
	return err == nil
}

func lastHTTPURL(out string) string {
	var found string
	for _, field := range strings.Fields(out) {
		candidate := strings.Trim(field, "<>()[]{}\"',;.")
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
			found = candidate
		}
	}
	return found
}

// IdentityFromURL returns a forge and owner/name identity for an HTTPS or SSH
// Git remote. It is used to match remote inventories to local clones without
// assuming the checkout directory has the same name.
func IdentityFromURL(raw string) (Kind, string) {
	kind := FromURL(raw)
	if kind == Unknown {
		return Unknown, ""
	}
	if kind == AzureDevOps {
		identity, _, ok := parseAzureDevOpsRemote(raw)
		if !ok {
			return Unknown, ""
		}
		return kind, identity
	}
	s := strings.TrimSpace(raw)
	if strings.Contains(s, "://") {
		if parsed, err := url.Parse(s); err == nil {
			s = strings.TrimPrefix(parsed.Path, "/")
		}
	} else if at := strings.IndexByte(s, '@'); at >= 0 {
		// SCP-style SSH: git@github.com:owner/repo.git
		s = s[at+1:]
		if colon := strings.IndexByte(s, ':'); colon >= 0 {
			s = s[colon+1:]
		}
	}
	s = strings.Trim(strings.TrimSuffix(s, ".git"), "/")
	return kind, s
}
