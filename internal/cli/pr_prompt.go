package cli

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

//go:embed assets/prompts/*.md
var promptTemplates embed.FS

// promptNames are the built-in templates, in the order help lists them.
var promptNames = []string{"triage", "review", "retire"}

func newPRPromptCmd(app *App) *cobra.Command {
	opts := prFlags{}
	var (
		agentName string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:       "prompt [triage|review|retire]",
		Short:     "Render a prompt about the pull request queue, optionally handing it to an agent",
		ValidArgs: promptNames,
		Args:      cobra.MaximumNArgs(1),
		Long: `Scan the queue and render a prompt about it.

With no --agent the prompt goes to stdout, so it can be piped anywhere. With
--agent it is handed to a command from the [[agent]] section of config.toml.

dev does not read the agent's reply, does not iterate, and has no built-in
agent: which one you use is a command string you configure. There is no default
entry, because shipping one would make dev depend on a particular tool.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "triage"
			if len(args) == 1 {
				name = args[0]
			}
			body, err := promptTemplates.ReadFile("assets/prompts/" + name + ".md")
			if err != nil {
				return fmt.Errorf("unknown prompt %q: want %s", name, strings.Join(promptNames, ", "))
			}

			collectOptions, err := opts.collectOptions()
			if err != nil {
				return err
			}
			// The retirement prompt is about local checkouts, so it needs the
			// only surface that reports a head branch.
			if name == "retire" && collectOptions.Scope == scopeAccount {
				collectOptions.Scope = scopeLocal
			}
			rows, statuses, collectErr := collectPullRequests(ctxOf(), app, collectOptions)
			if len(rows) == 0 && collectErr != nil {
				return collectErr
			}
			rows = filterPRRows(rows, "", opts)

			rendered, err := renderPrompt(string(body), rows, statuses, collectOptions, config.Hostname())
			if err != nil {
				return err
			}
			if collectErr != nil {
				app.warnf("partial pull request results: %v", collectErr)
			}

			if agentName == "" && !dryRun {
				fmt.Fprintln(app.Out, rendered)
				return nil
			}
			agent, ok := app.Cfg.AgentByName(agentName)
			if !ok {
				return unknownAgentError(app, agentName)
			}
			return runAgent(ctxOf(), app, agent, rendered, dryRun)
		},
	}
	opts.register(cmd)
	flags := cmd.Flags()
	flags.StringVar(&agentName, "agent", "", "hand the prompt to a configured [[agent]] instead of printing it")
	flags.BoolVar(&dryRun, "dry-run", false, "with --agent, print the command and the prompt without running anything")
	registerFlagCompletion(cmd, "agent", agentCompletions(app))
	return cmd
}

// renderPrompt substitutes the template's variables. Placeholders are replaced
// literally rather than through a template engine: the values are JSON and
// prose destined for a language model, and no evaluation should happen here.
func renderPrompt(body string, rows []prRow, statuses []prProviderStatus, opts prCollectOptions, host string) (string, error) {
	payload, err := json.MarshalIndent(makePRListJSON(rows, statuses, opts), "", "  ")
	if err != nil {
		return "", err
	}
	replacer := strings.NewReplacer(
		"{{pr_json}}", string(payload),
		"{{pr_count}}", fmt.Sprint(len(rows)),
		"{{host}}", host,
		"{{generated_at}}", time.Now().UTC().Format(time.RFC3339),
	)
	rendered := replacer.Replace(body)
	// An unreplaced placeholder means the template asks for something dev does
	// not provide, which would silently reach the agent as literal braces.
	if index := strings.Index(rendered, "{{"); index >= 0 {
		end := strings.Index(rendered[index:], "}}")
		if end >= 0 {
			return "", fmt.Errorf("prompt template uses unknown variable %s", rendered[index:index+end+2])
		}
	}
	return rendered, nil
}

func unknownAgentError(app *App, name string) error {
	if len(app.Cfg.Agents) == 0 {
		return fmt.Errorf(`no [[agent]] is configured; add one to %s, for example:

[[agent]]
name = "claude"
command = ["claude", "-p"]
default = true

then run this again with --agent claude, or drop --agent to print the prompt`,
			config.Contract(config.ConfigFile()))
	}
	if name == "" {
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

// runAgent starts the configured command and hands it the prompt. It streams
// the agent's output straight through: dev does not parse the reply.
func runAgent(ctx context.Context, app *App, agent config.Agent, prompt string, dryRun bool) error {
	ctx, cancel := context.WithTimeout(ctx, agent.EffectiveTimeout())
	defer cancel()

	promptFile := ""
	if agent.EffectiveInput() == config.AgentInputFile {
		dir, err := os.MkdirTemp("", "dev-prompt-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		promptFile = filepath.Join(dir, "prompt.md")
		if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
			return err
		}
	}

	command, display, err := agentProcess(ctx, agent, prompt, promptFile)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(app.Out, "%s\n\n%s\n", display, prompt)
		return nil
	}

	// Announced on stderr so that stdout stays exactly the agent's own output,
	// and so an unattended run leaves a record of what it started.
	app.warnf("running agent %q: %s", agent.Name, display)
	command.Stdout, command.Stderr = app.Out, app.Err
	if agent.EffectiveInput() == config.AgentInputStdin {
		command.Stdin = strings.NewReader(prompt)
	} else {
		command.Stdin = app.In
	}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("agent %q exceeded %s", agent.Name, agent.EffectiveTimeout())
		}
		return fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	return nil
}

// agentProcess builds the command. The argv form never involves a shell, which
// is why it is the only form allowed to carry the prompt as an argument.
func agentProcess(ctx context.Context, agent config.Agent, prompt, promptFile string) (*exec.Cmd, string, error) {
	if len(agent.Command) > 0 {
		argv := make([]string, len(agent.Command))
		for i, part := range agent.Command {
			part = strings.ReplaceAll(part, "{{prompt_file}}", promptFile)
			if agent.EffectiveInput() == config.AgentInputArgv {
				part = strings.ReplaceAll(part, "{{prompt}}", prompt)
			}
			argv[i] = part
		}
		return exec.CommandContext(ctx, argv[0], argv[1:]...), strings.Join(agent.Command, " "), nil
	}

	run := strings.ReplaceAll(agent.Run, "{{prompt_file}}", promptFile)
	if agent.Interactive {
		// Matches [[tui.tools]]: -lic loads the user's rc file so an alias or
		// shell function resolves, and eval "$1" keeps the command out of the
		// argument list.
		return exec.CommandContext(ctx, shellPath(), "-lic", `eval "$1"`, "dev-pr-agent", run), agent.Run, nil
	}
	return exec.CommandContext(ctx, shellPath(), "-c", run), agent.Run, nil
}
