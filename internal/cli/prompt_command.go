package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		newPromptAgentsCmd(app),
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

type promptAgentInventory struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Default     bool                         `json:"default"`
	Run         promptAgentLauncherInventory `json:"run"`
	Open        promptAgentLauncherInventory `json:"open"`
}

type promptAgentLauncherInventory struct {
	Configured bool   `json:"configured"`
	Kind       string `json:"kind"`
	Executable string `json:"executable"`
}

func newPromptAgentsCmd(app *App) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List configured agent profiles without private launch details",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles := promptAgentInventoryRows(app.Cfg.Agents)
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(profiles)
			}
			if len(profiles) == 0 {
				fmt.Fprintln(app.Out, "No agent profiles configured.")
				return nil
			}
			table := app.newTable("PROFILE", "DEFAULT", "RUN", "OPEN", "DESCRIPTION")
			for _, profile := range profiles {
				defaultValue := "—"
				if profile.Default {
					defaultValue = "yes"
				}
				table.Add(profile.Name, defaultValue, promptAgentLauncherDisplay(profile.Run),
					promptAgentLauncherDisplay(profile.Open), dash(promptAgentHumanDescription(profile.Description)))
			}
			table.Render(app.Out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit stable structured agent metadata")
	return cmd
}

func promptAgentInventoryRows(agents []config.Agent) []promptAgentInventory {
	sorted := append([]config.Agent(nil), agents...)
	sort.Slice(sorted, func(i, j int) bool {
		left, right := strings.ToLower(sorted[i].Name), strings.ToLower(sorted[j].Name)
		if left == right {
			return sorted[i].Name < sorted[j].Name
		}
		return left < right
	})
	rows := make([]promptAgentInventory, 0, len(sorted))
	for _, agent := range sorted {
		rows = append(rows, promptAgentInventory{
			Name: agent.Name, Description: agent.Description, Default: agent.Default,
			Run: promptAgentLauncherInventoryFor(agent.Run), Open: promptAgentLauncherInventoryFor(agent.Open),
		})
	}
	return rows
}

func promptAgentLauncherInventoryFor(launcher config.AgentLauncher) promptAgentLauncherInventory {
	switch {
	case len(launcher.Command) > 0:
		return promptAgentLauncherInventory{Configured: true, Kind: "command", Executable: filepath.Base(launcher.Command[0])}
	case strings.TrimSpace(launcher.Shell) != "":
		return promptAgentLauncherInventory{Configured: true, Kind: "shell"}
	default:
		return promptAgentLauncherInventory{Kind: "none"}
	}
}

func promptAgentLauncherDisplay(launcher promptAgentLauncherInventory) string {
	switch launcher.Kind {
	case "command":
		return launcher.Executable
	case "shell":
		return "shell"
	default:
		return "—"
	}
}

func promptAgentHumanDescription(description string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(strings.TrimSpace(description))
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
		registerFlagCompletion(cmd, "agent", completePromptAgents(app, mode))
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
	var selection promptLauncherSelection
	if mode != promptRender {
		var err error
		selection, err = resolvePromptLauncher(app, mode, agentName, dryRun)
		if err != nil {
			return err
		}
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

	agent := selection.Agent
	handoffMode := selection.Mode
	launcherConfig := selection.Launcher
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

type promptLauncherSelection struct {
	Agent    config.Agent
	Mode     handoff.Mode
	Launcher config.AgentLauncher
}

func resolvePromptLauncher(app *App, mode promptMode, name string, dryRun bool) (promptLauncherSelection, error) {
	handoffMode, ok := promptHandoffMode(mode)
	if !ok {
		return promptLauncherSelection{}, fmt.Errorf("prompt mode %q has no agent launcher", mode)
	}
	agent, ok := app.Cfg.AgentByName(name)
	if !ok {
		return promptLauncherSelection{}, promptAgentError(app, name, handoffMode)
	}
	launcher := promptAgentLauncher(agent, handoffMode)
	if !launcher.Configured() {
		return promptLauncherSelection{}, fmt.Errorf("agent %q has no [agent.%s] launcher; %s",
			agent.Name, handoffMode, promptAgentModeGuidance(app.Cfg.Agents, handoffMode))
	}
	if handoffMode == handoff.ModeOpen && !dryRun && !app.interactive() {
		return promptLauncherSelection{}, errors.New("prompt open needs an interactive terminal; use prompt render or prompt run instead")
	}
	return promptLauncherSelection{Agent: agent, Mode: handoffMode, Launcher: launcher}, nil
}

func promptHandoffMode(mode promptMode) (handoff.Mode, bool) {
	switch mode {
	case promptRun:
		return handoff.ModeRun, true
	case promptOpen:
		return handoff.ModeOpen, true
	default:
		return "", false
	}
}

func promptAgentLauncher(agent config.Agent, mode handoff.Mode) config.AgentLauncher {
	if mode == handoff.ModeOpen {
		return agent.Open
	}
	return agent.Run
}

func promptAgentError(app *App, name string, mode handoff.Mode) error {
	guidance := promptAgentModeGuidance(app.Cfg.Agents, mode)
	if len(app.Cfg.Agents) == 0 {
		return fmt.Errorf(`no [[agent]] is configured; add one to %s, for example:

[[agent]]
name = "my-agent"
description = "My local coding agent"
default = true
[agent.run]
command = ["my-agent", "--print"]
input = "stdin"
[agent.open]
command = ["my-agent", "{{prompt_file}}"]
input = "file"

%s`, config.Contract(config.ConfigFile()), guidance)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("more than one [[agent]] is configured and none is the default; %s", guidance)
	}
	return fmt.Errorf("unknown agent %q; %s", name, guidance)
}

func promptAgentModeGuidance(agents []config.Agent, mode handoff.Mode) string {
	names := promptAgentNamesForMode(agents, mode)
	available := "none"
	if len(names) > 0 {
		available = strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s-capable profiles: %s; inspect with `dev prompt agents`", mode, available)
}

func promptAgentNamesForMode(agents []config.Agent, mode handoff.Mode) []string {
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		if promptAgentLauncher(agent, mode).Configured() {
			names = append(names, agent.Name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := strings.ToLower(names[i]), strings.ToLower(names[j])
		if left == right {
			return names[i] < names[j]
		}
		return left < right
	})
	return names
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
