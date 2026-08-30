package cli

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

type repoSkillSetup struct {
	Phase       repoHookPhase
	Interpreter string
	Script      string
	Builtin     string
	Args        []string
	Required    bool
	Timeout     time.Duration
}

type repoSkillSpec struct {
	ID     string
	Source string
	Name   string
	Agents []string
	Setup  *repoSkillSetup
}

type repoSkillResult struct {
	Installed []string `json:"installed,omitempty"`
	Setup     []string `json:"setup,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

func installRepoSkills(ctx context.Context, app *App, root string, specs []repoSkillSpec) (repoSkillResult, error) {
	var result repoSkillResult
	for _, spec := range specs {
		process, err := agentskill.InstallCommand(ctx, root, spec.Source, []string{spec.Name}, spec.Agents)
		if err != nil {
			return result, fmt.Errorf("prepare skill %s install: %w", spec.Name, err)
		}
		process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
		if err := process.Run(); err != nil {
			return result, fmt.Errorf("install skill %s: %w", spec.Name, err)
		}
		result.Installed = append(result.Installed, spec.Name)
	}
	return result, nil
}

func runRepoSkillSetups(ctx context.Context, app *App, root string, phase repoHookPhase, specs []repoSkillSpec) (repoSkillResult, error) {
	var result repoSkillResult
	for _, spec := range specs {
		if spec.Setup == nil || spec.Setup.Phase != phase ||
			(strings.TrimSpace(spec.Setup.Script) == "" && strings.TrimSpace(spec.Setup.Builtin) == "") {
			continue
		}
		if err := runRepoSkillSetup(ctx, app, root, spec.Name, *spec.Setup); err != nil {
			if spec.Setup.Required {
				return result, fmt.Errorf("set up skill %s: %w", spec.Name, err)
			}
			warning := fmt.Sprintf("skill %s setup: %v", spec.Name, err)
			result.Warnings = append(result.Warnings, warning)
			app.warnf("%s", warning)
			continue
		}
		result.Setup = append(result.Setup, spec.Name)
	}
	return result, nil
}

func runRepoSkillSetup(ctx context.Context, app *App, root, name string, setup repoSkillSetup) error {
	if setup.Timeout <= 0 {
		setup.Timeout = 10 * time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, setup.Timeout)
	defer cancel()
	if setup.Builtin != "" {
		return runBuiltinRepoSkillSetup(commandCtx, app, root, setup.Builtin, setup.Args)
	}
	skill, err := agentskill.FindProject(ctx, root, name)
	if err != nil {
		return err
	}
	skillRoot, err := pathx.Canonical(skill.Path)
	if err != nil {
		return err
	}
	script, err := pathx.CanonicalChild(skillRoot, filepath.Join(skillRoot, filepath.FromSlash(setup.Script)))
	if err != nil {
		return fmt.Errorf("setup entrypoint must stay within installed skill: %w", err)
	}
	command := setup.Interpreter
	args := append([]string{script}, setup.Args...)
	if command == "" {
		command = script
		args = setup.Args
	}
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("interpreter/executable %q is unavailable", command)
	}
	process := exec.CommandContext(commandCtx, command, args...)
	process.Dir = root
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	if err := process.Run(); err != nil {
		if commandCtx.Err() != nil {
			return commandCtx.Err()
		}
		return err
	}
	return nil
}

func runUpstreamSkillCatalog(ctx context.Context, app *App, root, source string) error {
	process, err := agentskill.AddCommand(ctx, root, source)
	if err != nil {
		return err
	}
	process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
	return process.Run()
}
