package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/handoff"
	"github.com/daviddwlee84/dev-cli/internal/promptkit"
	"github.com/spf13/cobra"
)

type promptMode string

const (
	promptRender promptMode = "render"
	promptRun    promptMode = "run"
	promptOpen   promptMode = "open"
)

type promptCollect func(context.Context) (promptkit.Snapshot, error)

func newPromptCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prompt",
		Short: "Render operational context, or hand it to a configured agent",
		Long: `Collect deterministic dev context and place it into a built-in prompt.

render only prints the exact prompt. run starts a one-shot, non-interactive
command. open gives a foreground child the current terminal so the user can
answer questions. dev never parses the reply or turns it into lifecycle
authorization.`,
	}
	cmd.AddCommand(
		newPromptListCmd(app),
		newPromptModeCmd(app, promptRender),
		newPromptModeCmd(app, promptRun),
		newPromptModeCmd(app, promptOpen),
	)
	return cmd
}

func newPromptListCmd(app *App) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List built-in prompt recipes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			recipes := promptkit.Recipes()
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(recipes)
			}
			table := app.newTable("RECIPE", "SCOPE", "TARGET", "PURPOSE")
			for _, recipe := range recipes {
				target := recipe.TargetUsage
				if target == "" {
					target = "—"
				}
				table.Add(recipe.Name, recipe.Scope, target, recipe.Summary)
			}
			table.Render(app.Out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured recipe metadata")
	return cmd
}

func newPromptModeCmd(app *App, mode promptMode) *cobra.Command {
	var agentName string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   string(mode),
		Short: promptModeSummary(mode),
	}
	if mode != promptRender {
		flags := cmd.PersistentFlags()
		flags.StringVar(&agentName, "agent", "", "configured agent name (default or sole agent when omitted)")
		flags.BoolVar(&dryRun, "dry-run", false, "show the resolved launch and prompt without starting a process")
		registerFlagCompletion(cmd, "agent", agentCompletions(app))
	}
	cmd.AddCommand(
		newPromptPRTriageCmd(app, mode, &agentName, &dryRun),
		newPromptSessionCloseCmd(app, mode, &agentName, &dryRun),
		newPromptWorkspaceCloseoutCmd(app, mode, &agentName, &dryRun),
	)
	return cmd
}

func promptModeSummary(mode promptMode) string {
	switch mode {
	case promptRender:
		return "Render a built-in prompt to stdout"
	case promptRun:
		return "Run a one-shot agent with a built-in prompt"
	case promptOpen:
		return "Open an interactive foreground agent in the current terminal"
	}
	return "Use a built-in prompt"
}

func newPromptPRTriageCmd(app *App, mode promptMode, agentName *string, dryRun *bool) *cobra.Command {
	opts := prFlags{}
	cmd := &cobra.Command{
		Use:   "pr-triage [query]",
		Short: "Prioritize pull requests you opened or were asked to review",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = strings.ToLower(args[0])
			}
			collectOptions, err := opts.collectOptions()
			if err != nil {
				return err
			}
			return executePrompt(app, mode, value(agentName), valueBool(dryRun), promptkit.RecipePRTriage, func(ctx context.Context) (promptkit.Snapshot, error) {
				collection := collectPullRequests(ctx, app, collectOptions)
				if len(collection.Rows) == 0 && collection.Err != nil {
					return promptkit.Snapshot{}, collection.Err
				}
				rows := filterPRRows(collection.Rows, query, opts)
				context := makePRListJSON(rows, collection.Providers, collection.Effective, collection.Err)
				return promptkit.Snapshot{
					Scope: "pull-request-inbox", WorkingDirectory: currentDirectory(),
					Capabilities: prCapabilities(collection.Providers),
					Warnings:     prWarnings(collection.Providers, collection.Err), Context: context,
				}, nil
			})
		},
	}
	opts.registerFilters(cmd)
	return cmd
}

func newPromptSessionCloseCmd(app *App, mode promptMode, agentName *string, dryRun *bool) *cobra.Command {
	return &cobra.Command{
		Use:   promptkit.RecipeSessionClose,
		Short: "Review live agent sessions and what must be saved before closing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executePrompt(app, mode, value(agentName), valueBool(dryRun), promptkit.RecipeSessionClose,
				func(ctx context.Context) (promptkit.Snapshot, error) { return collectSessionClosePrompt(ctx, app) })
		},
	}
}

func newPromptWorkspaceCloseoutCmd(app *App, mode promptMode, agentName *string, dryRun *bool) *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use:   "workspace-closeout [repo-or-checkout]",
		Short: "Review which tasks and worktrees should finish, park, retire, or be inspected",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			return executePrompt(app, mode, value(agentName), valueBool(dryRun), promptkit.RecipeWorkspaceCloseout,
				func(ctx context.Context) (promptkit.Snapshot, error) {
					return collectWorkspaceCloseoutPrompt(ctx, app, ref, base)
				})
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "base for auditing unmanaged linked worktrees")
	return cmd
}

func executePrompt(app *App, mode promptMode, agentName string, dryRun bool, recipeName string, collect promptCollect) error {
	recipe, ok := promptkit.Lookup(recipeName)
	if !ok {
		return fmt.Errorf("unknown prompt recipe %q", recipeName)
	}
	snapshot, err := collect(ctxOf())
	if err != nil {
		return err
	}
	if snapshot.WorkingDirectory == "" {
		snapshot.WorkingDirectory = currentDirectory()
	}
	if snapshot.Target != nil && snapshot.Target.WorkingDirectory == "" {
		snapshot.Target.WorkingDirectory = snapshot.WorkingDirectory
	}
	generatedAt := time.Now().UTC()
	rendered, _, err := promptkit.Render(recipe, snapshot, generatedAt, config.Hostname())
	if err != nil {
		return err
	}
	if mode == promptRender {
		fmt.Fprint(app.Out, rendered)
		if !strings.HasSuffix(rendered, "\n") {
			fmt.Fprintln(app.Out)
		}
		return nil
	}

	agent, ok := app.Cfg.AgentByName(agentName)
	if !ok {
		return promptAgentError(app, agentName)
	}
	handoffMode := handoff.ModeRun
	launcherConfig := agent.Run
	if mode == promptOpen {
		handoffMode = handoff.ModeOpen
		launcherConfig = agent.Open
		if !dryRun && !app.interactive() {
			return errors.New("prompt open needs an interactive terminal; use prompt render or prompt run instead")
		}
	}
	if !launcherConfig.Configured() {
		return fmt.Errorf("agent %q has no [agent.%s] launcher", agent.Name, handoffMode)
	}
	// Starting an external coding agent is a new writer claim even when the
	// recipe asks only for analysis. Unlike an existing agent continuing its own
	// work, this guard does not exclude the caller pane: an agent must not launch
	// a second writer beside itself without the explicit shared-checkout override.
	// Dry-run starts no writer and therefore needs no claim.
	if !dryRun {
		if _, isCheckout := canonicalWorktreeRoot(ctxOf(), snapshot.WorkingDirectory); isCheckout {
			if err := guardNewAgentCheckout(ctxOf(), app, app.Runtime(), snapshot.WorkingDirectory); err != nil {
				return err
			}
		}
	}

	spec := handoff.Spec{
		Mode: handoffMode, Launcher: launcherConfig.Handoff(handoffMode),
		Prompt: rendered, Dir: snapshot.WorkingDirectory, ShellPath: shellPath(),
		In: app.In, Out: app.Out, Err: app.Err,
	}
	preview, err := handoff.Run(ctxOf(), handoff.Spec{
		Mode: spec.Mode, Launcher: spec.Launcher, Prompt: spec.Prompt, Dir: spec.Dir,
		ShellPath: spec.ShellPath, In: spec.In, Out: spec.Out, Err: spec.Err, DryRun: true,
	})
	if err != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	if dryRun {
		renderPromptDryRun(app, agent.Name, preview, rendered)
		return nil
	}
	app.warnf("running agent %q in %s: %s", agent.Name, config.Contract(spec.Dir), previewCommand(preview))
	if _, err := handoff.Run(ctxOf(), spec); err != nil {
		return fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	return nil
}

func promptAgentError(app *App, name string) error {
	if len(app.Cfg.Agents) == 0 {
		return fmt.Errorf(`no [[agent]] is configured; add one to %s, for example:

[[agent]]
name = "my-agent"
default = true
[agent.run]
command = ["my-agent", "--print"]
input = "stdin"
[agent.open]
command = ["my-agent", "{{prompt_file}}"]
input = "file"`, config.Contract(config.ConfigFile()))
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("more than one [[agent]] is configured and none is the default; pass --agent with one of: %s",
			strings.Join(agentNames(app), ", "))
	}
	return fmt.Errorf("unknown agent %q: configured agents are %s", name, strings.Join(agentNames(app), ", "))
}

func agentNames(app *App) []string {
	names := make([]string, 0, len(app.Cfg.Agents))
	for _, agent := range app.Cfg.Agents {
		names = append(names, agent.Name)
	}
	sort.Strings(names)
	return names
}

func agentCompletions(app *App) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return agentNames(app), cobra.ShellCompDirectiveNoFileComp
	}
}

func renderPromptDryRun(app *App, agent string, preview handoff.Preview, prompt string) {
	timeout := "none"
	if preview.Timeout > 0 {
		timeout = preview.Timeout.String()
	}
	fmt.Fprintf(app.Out, "agent: %s\nmode: %s\ncwd: %s\ntransport: %s\ntimeout: %s\ncommand: %s\n\n--- prompt ---\n%s",
		agent, preview.Mode, preview.Dir, preview.Transport, timeout, previewCommand(preview), prompt)
	if !strings.HasSuffix(prompt, "\n") {
		fmt.Fprintln(app.Out)
	}
}

func previewCommand(preview handoff.Preview) string {
	if preview.Shell != "" {
		return preview.Shell
	}
	parts := make([]string, 0, len(preview.Command))
	for _, argument := range preview.Command {
		parts = append(parts, shellQuote(argument))
	}
	return strings.Join(parts, " ")
}

func currentDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func valueBool(pointer *bool) bool {
	return pointer != nil && *pointer
}
