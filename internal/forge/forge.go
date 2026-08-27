// Package forge wraps the GitHub and GitLab CLIs. Both are optional: every
// entry point degrades to plain git, because a workflow that hard-depends on a
// forge CLI being installed and authenticated stops working on exactly the
// machines where you most need it.
package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

// Kind identifies a forge.
type Kind string

const (
	// GitHub is served by the gh CLI.
	GitHub Kind = "github"
	// GitLab is served by the glab CLI.
	GitLab Kind = "gitlab"
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
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
