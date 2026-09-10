package agentskill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type provider struct {
	bin    string
	prefix []string
	label  string
}

// MutationProvider describes the explicit installer boundary without running
// it. DownloadsOnRun is true for npx because availability is not a promise that
// the skills package is already cached.
type MutationProvider struct {
	Available      bool
	Command        string
	Path           string
	DownloadsOnRun bool
	Detail         string
}

// MutationProviderStatus performs path lookup only. It is suitable for doctor
// and other read-only status surfaces because it never starts skills, npx, npm,
// or project code.
func MutationProviderStatus() MutationProvider {
	if path, err := exec.LookPath("skills"); err == nil {
		return MutationProvider{Available: true, Command: "skills", Path: path, Detail: "skills executable available"}
	}
	if path, err := exec.LookPath("npx"); err == nil {
		return MutationProvider{
			Available: true, Command: "npx skills", Path: path, DownloadsOnRun: true,
			Detail: "npx available; an explicit mutation may download the skills package",
		}
	}
	return MutationProvider{Detail: "neither `skills` nor `npx` is available"}
}

func interactiveProvider() (provider, error) {
	status := MutationProviderStatus()
	if !status.Available {
		return provider{}, errors.New(status.Detail)
	}
	if status.Command == "skills" {
		return provider{bin: status.Path, label: "skills"}, nil
	}
	return provider{bin: status.Path, prefix: []string{"skills"}, label: "npx skills"}, nil
}

func (p provider) command(ctx context.Context, cwd string, args ...string) *exec.Cmd {
	all := append(append([]string(nil), p.prefix...), args...)
	cmd := exec.CommandContext(ctx, p.bin, all...)
	cmd.Dir = cwd
	return cmd
}

// AddCommand opens the upstream interactive installer in projectRoot.
func AddCommand(ctx context.Context, projectRoot, source string) (*exec.Cmd, error) {
	if source == "" {
		source = DefaultSource
	}
	p, err := interactiveProvider()
	if err != nil {
		return nil, err
	}
	return p.command(ctx, projectRoot, "add", source), nil
}

// InstallCommand installs an explicit set of skills for explicit agents in
// project scope. The caller owns confirmation; --yes only suppresses the
// provider's duplicate prompt after that decision.
func InstallCommand(ctx context.Context, projectRoot, source string, names, agents []string) (*exec.Cmd, error) {
	if source == "" {
		source = DefaultSource
	}
	if len(names) == 0 {
		return nil, errors.New("at least one skill is required")
	}
	if len(agents) == 0 {
		return nil, errors.New("at least one agent is required")
	}
	p, err := interactiveProvider()
	if err != nil {
		return nil, err
	}
	args := []string{"add", source, "--skill"}
	args = append(args, names...)
	args = append(args, "--agent")
	args = append(args, agents...)
	args = append(args, "--yes")
	return p.command(ctx, ProjectRoot(ctx, projectRoot), args...), nil
}

// UpdateCommand updates exactly one lock-managed skill in exactly one scope.
func UpdateCommand(ctx context.Context, projectRoot, name string, scope Scope) (*exec.Cmd, error) {
	if name == "" {
		return nil, errors.New("skill name is required")
	}
	if scope != ScopeProject && scope != ScopeGlobal {
		return nil, errors.New("skill update requires project or global scope")
	}
	p, err := interactiveProvider()
	if err != nil {
		return nil, err
	}
	args := []string{"update", name, "--yes"}
	if scope == ScopeGlobal {
		args = append(args, "--global")
	} else {
		args = append(args, "--project")
	}
	return p.command(ctx, projectRoot, args...), nil
}

// ProviderVersion reports a directly installed skills executable. It never
// falls back to npx, even with --no-install, because version probes are reads.
func ProviderVersion(ctx context.Context, cwd string) (string, error) {
	bin, err := exec.LookPath("skills")
	if err != nil {
		return "", errors.New("skills executable is not installed")
	}
	p := provider{bin: bin, label: "skills"}
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
