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

// RepoRequest describes a remote repository to create.
type RepoRequest struct {
	Name        string
	Description string
	Private     bool
	// Push pushes the current branch after creating the remote.
	Push bool
	// RemoteName is the git remote to add, defaulting to origin.
	RemoteName string
}

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

func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
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
