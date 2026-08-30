package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type repoHookPhase string

const (
	repoHookBeforeCommit repoHookPhase = "before_commit"
	repoHookAfterCommit  repoHookPhase = "after_commit"
	repoHookAfterRemote  repoHookPhase = "after_remote"
)

type repoHookSpec struct {
	ID          string
	Phase       repoHookPhase
	Command     []string
	Run         string
	Interactive bool
	Required    bool
	Timeout     time.Duration
}

type repoHookResult struct {
	Phase    repoHookPhase `json:"phase"`
	Ran      []string      `json:"ran,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
}

func runRepoHooks(ctx context.Context, app *App, root string, phase repoHookPhase, hooks []repoHookSpec) (repoHookResult, error) {
	result := repoHookResult{Phase: phase}
	for _, hook := range hooks {
		if hook.Phase != phase {
			continue
		}
		if err := runRepoHook(ctx, app, root, hook); err != nil {
			if hook.Required {
				return result, fmt.Errorf("hook %s: %w", hook.ID, err)
			}
			warning := fmt.Sprintf("hook %s: %v", hook.ID, err)
			result.Warnings = append(result.Warnings, warning)
			app.warnf("%s", warning)
			continue
		}
		result.Ran = append(result.Ran, hook.ID)
	}
	return result, nil
}

func runRepoHook(ctx context.Context, app *App, root string, hook repoHookSpec) error {
	if (len(hook.Command) == 0) == (strings.TrimSpace(hook.Run) == "") {
		return fmt.Errorf("exactly one of command or run is required")
	}
	if hook.Timeout <= 0 {
		hook.Timeout = 10 * time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, hook.Timeout)
	defer cancel()
	var process *exec.Cmd
	if len(hook.Command) > 0 {
		process = exec.CommandContext(commandCtx, hook.Command[0], hook.Command[1:]...)
	} else if hook.Interactive {
		process = exec.CommandContext(commandCtx, shellPath(), "-lic", `eval "$1"`, "dev-repo-hook", hook.Run)
	} else {
		process = exec.CommandContext(commandCtx, shellPath(), "-c", hook.Run)
	}
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
