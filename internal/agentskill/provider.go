package agentskill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

type provider struct {
	bin   string
	label string
}

// MutationProvider describes the explicit installer boundary without running
// it. Repository-scoped mutations require a direct executable: invoking npx in
// an untrusted checkout could substitute a repository-local binary.
type MutationProvider struct {
	Available bool
	Command   string
	Path      string
	Detail    string
}

// MutationCommand keeps provider execution behind one host-wide lock. Callers
// may either Run directly or Prepare the underlying process for Bubble Tea.
type MutationCommand struct {
	Command *exec.Cmd
	ctx     context.Context
}

// Prepare acquires the provider mutation lock and returns an idempotent
// completion callback that joins process and release errors.
func (m *MutationCommand) Prepare() (*exec.Cmd, func(error) error, error) {
	if m == nil || m.Command == nil {
		return nil, nil, errors.New("skill mutation command is empty")
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cache, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(cache) {
		return nil, nil, errors.New("could not resolve the user cache for skill mutation locking")
	}
	lease, err := lockx.AcquireDir(ctx, filepath.Join(cache, "dev-cli", "skill-mutation"), "agent skill mutation")
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	var result error
	finish := func(processErr error) error {
		once.Do(func() { result = errors.Join(processErr, lease.Close()) })
		return result
	}
	return m.Command, finish, nil
}

// Run executes the provider while holding the host-wide mutation lock.
func (m *MutationCommand) Run() error {
	command, finish, err := m.Prepare()
	if err != nil {
		return err
	}
	return finish(command.Run())
}

// MutationProviderStatus performs path lookup only. It is suitable for doctor
// and other read-only status surfaces because it never starts skills, npx, npm,
// or project code.
func MutationProviderStatus() MutationProvider {
	return MutationProviderStatusFor("")
}

// MutationProviderStatusFor rejects a provider inside projectRoot in addition
// to npm-local shims. The selected repository never supplies the program that
// mutates it.
func MutationProviderStatusFor(projectRoot string) MutationProvider {
	var rejected string
	for _, path := range skillsExecutableCandidates() {
		if resolved, trusted := trustedSkillsExecutable(path, projectRoot); trusted {
			return MutationProvider{Available: true, Command: "skills", Path: resolved, Detail: "skills executable available"}
		}
		if rejected == "" {
			rejected = path
		}
	}
	if rejected != "" {
		return MutationProvider{Path: rejected, Detail: "skills executable resolves inside the selected repository or node_modules; install a trusted global executable"}
	}
	if path, err := exec.LookPath("npx"); err == nil {
		return MutationProvider{
			Path:   path,
			Detail: "npx found but not invoked from repository checkouts; install the `skills` executable for add/update",
		}
	}
	return MutationProvider{Detail: "skills executable is not installed"}
}

func skillsExecutableCandidates() []string {
	seen := map[string]bool{}
	var candidates []string
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" {
			directory = "."
		}
		candidate, err := filepath.Abs(filepath.Join(directory, "skills"))
		if err != nil {
			continue
		}
		found, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		key := filepath.Clean(found)
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, found)
	}
	return candidates
}

func trustedSkillsExecutable(path, projectRoot string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	clean := filepath.Clean(path)
	if pathUsesNodeModulesBin(clean) || projectRoot != "" && lexicalPathInside(projectRoot, clean) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !filepath.IsAbs(resolved) || pathUsesNodeModulesBin(resolved) {
		return "", false
	}
	resolved = filepath.Clean(resolved)
	if projectRoot != "" {
		inside, err := pathx.Contains(projectRoot, resolved)
		if err != nil || inside {
			return "", false
		}
	}
	return resolved, true
}

func pathUsesNodeModulesBin(path string) bool {
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if strings.EqualFold(parts[index], "node_modules") && strings.EqualFold(parts[index+1], ".bin") {
			return true
		}
	}
	return false
}

func interactiveProvider(projectRoot string) (provider, error) {
	status := MutationProviderStatusFor(projectRoot)
	if !status.Available {
		return provider{}, errors.New(status.Detail)
	}
	return provider{bin: status.Path, label: "skills"}, nil
}

func (p provider) command(ctx context.Context, cwd string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.bin, args...)
	cmd.Dir = cwd
	return cmd
}

func canonicalMutationRoot(root string, allowMissing bool) (string, error) {
	resolved, err := pathx.Canonical(root)
	if err != nil {
		return "", fmt.Errorf("resolve skill mutation root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if allowMissing && os.IsNotExist(err) {
			return resolved, nil
		}
		return "", errors.New("skill mutation root is not a directory")
	}
	if !info.IsDir() {
		return "", errors.New("skill mutation root is not a directory")
	}
	return resolved, nil
}

func lexicalPathInside(root, candidate string) bool {
	root, rootErr := filepath.Abs(root)
	candidate, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && !filepath.IsAbs(relative) && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeProviderSkillName(name string) bool {
	return name == strings.TrimSpace(name) && !strings.HasPrefix(name, "-") && safeDisplayValue(name)
}

func safeProviderArgument(value string) bool {
	return value == strings.TrimSpace(value) && !strings.HasPrefix(value, "-") && safeDisplayValue(value)
}

// AddCommand opens the upstream interactive installer in projectRoot.
func AddCommand(ctx context.Context, projectRoot, source string) (*MutationCommand, error) {
	if source == "" {
		source = DefaultSource
	}
	if !safeProviderArgument(source) {
		return nil, errors.New("skill source is not safe to pass to the provider")
	}
	root, err := canonicalMutationRoot(projectRoot, false)
	if err != nil {
		return nil, err
	}
	p, err := interactiveProvider(root)
	if err != nil {
		return nil, err
	}
	return &MutationCommand{Command: p.command(ctx, root, "add", source), ctx: ctx}, nil
}

// InstallCommand installs an explicit set of skills for explicit agents in
// project scope. The caller owns confirmation; --yes only suppresses the
// provider's duplicate prompt after that decision.
func InstallCommand(ctx context.Context, projectRoot, source string, names, agents []string) (*MutationCommand, error) {
	if source == "" {
		source = DefaultSource
	}
	if !safeProviderArgument(source) {
		return nil, errors.New("skill source is not safe to pass to the provider")
	}
	if len(names) == 0 {
		return nil, errors.New("at least one skill is required")
	}
	for _, name := range names {
		if !safeProviderSkillName(name) {
			return nil, errors.New("skill name is not safe to pass to the provider")
		}
	}
	if len(agents) == 0 {
		return nil, errors.New("at least one agent is required")
	}
	for _, agent := range agents {
		if !safeProviderArgument(agent) {
			return nil, errors.New("agent name is not safe to pass to the provider")
		}
	}
	root, err := canonicalMutationRoot(ProjectRoot(ctx, projectRoot), true)
	if err != nil {
		return nil, err
	}
	p, err := interactiveProvider(root)
	if err != nil {
		return nil, err
	}
	args := []string{"add", source, "--skill"}
	args = append(args, names...)
	args = append(args, "--agent")
	args = append(args, agents...)
	args = append(args, "--yes")
	return &MutationCommand{Command: p.command(ctx, root, args...), ctx: ctx}, nil
}

// UpdateCommand updates exactly one lock-managed skill in exactly one scope.
func UpdateCommand(ctx context.Context, projectRoot, name string, scope Scope) (*MutationCommand, error) {
	if directManagedSkillName(name) {
		return nil, errors.New("dev-cli is managed by the dev binary, not the skills provider")
	}
	if !safeProviderSkillName(name) {
		return nil, errors.New("skill name is not safe to pass to the provider")
	}
	if scope != ScopeProject && scope != ScopeGlobal {
		return nil, errors.New("skill update requires project or global scope")
	}
	root, err := canonicalMutationRoot(projectRoot, false)
	if err != nil {
		return nil, err
	}
	p, err := interactiveProvider(root)
	if err != nil {
		return nil, err
	}
	args := []string{"update", "--yes"}
	if scope == ScopeGlobal {
		args = append(args, "--global")
	} else {
		args = append(args, "--project")
	}
	args = append(args, name)
	return &MutationCommand{Command: p.command(ctx, root, args...), ctx: ctx}, nil
}

// ProviderVersion reports a directly installed skills executable. It never
// falls back to npx, even with --no-install, because version probes are reads.
func ProviderVersion(ctx context.Context, cwd string) (string, error) {
	status := MutationProviderStatusFor(cwd)
	if !status.Available {
		return "", errors.New(status.Detail)
	}
	p := provider{bin: status.Path, label: "skills"}
	cmd := p.command(ctx, cwd, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("skills: %s", detail)
	}
	return "skills " + strings.TrimSpace(stdout.String()), nil
}
