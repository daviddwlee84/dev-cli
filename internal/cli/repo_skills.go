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
	type installGroup struct {
		source string
		agents []string
		names  []string
	}
	var groups []installGroup
	for _, spec := range specs {
		key := spec.Source + "\x00" + strings.Join(spec.Agents, "\x00")
		groupIndex := -1
		for index := range groups {
			if groups[index].source+"\x00"+strings.Join(groups[index].agents, "\x00") == key {
				groupIndex = index
				break
			}
		}
		if groupIndex < 0 {
			groups = append(groups, installGroup{source: spec.Source, agents: append([]string(nil), spec.Agents...)})
			groupIndex = len(groups) - 1
		}
		groups[groupIndex].names = append(groups[groupIndex].names, spec.Name)
	}
	for _, group := range groups {
		process, err := agentskill.InstallCommand(ctx, root, group.source, group.names, group.agents)
		if err != nil {
			return result, fmt.Errorf("prepare skills %s install: %w", strings.Join(group.names, ", "), err)
		}
		process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
		if err := process.Run(); err != nil {
			return result, fmt.Errorf("install skills %s: %w", strings.Join(group.names, ", "), err)
		}
		result.Installed = append(result.Installed, group.names...)
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
