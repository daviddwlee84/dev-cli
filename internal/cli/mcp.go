package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/spf13/cobra"
)

func newMCPCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Inspect static agent MCP server declarations",
		Long: `Inventory MCP declarations without starting servers or contacting endpoints.

MCP rows are static, scope-qualified configuration declarations. They do not
claim that a declaration is the effective merged config, connected, healthy, or
authenticated. Secret-bearing values are redacted before rows are returned.`,
	}
	cmd.AddCommand(newMCPListCmd(app))
	return cmd
}

func newMCPListCmd(app *App) *cobra.Command {
	var (
		all     bool
		repoRef string
		agents  []string
		scopes  []string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured MCP server declarations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && repoRef != "" {
				return fmt.Errorf("choose at most one of --all or --repo")
			}
			agentFilter, err := parseMCPAgents(agents)
			if err != nil {
				return err
			}
			scopeFilter, err := parseMCPScopes(scopes)
			if err != nil {
				return err
			}
			project := len(scopeFilter) == 0 || scopeFilter[agentmcp.ScopeProject] || scopeFilter[agentmcp.ScopeLocal]
			if !project && (all || repoRef != "") {
				return fmt.Errorf("--all and --repo require project or local scope")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			targets, err := resolveAgentTargets(cmd.Context(), app, cwd, all, repoRef, project)
			if err != nil {
				return err
			}
			result, err := agentmcp.Scan(cmd.Context(), targets)
			if err != nil {
				return err
			}
			result.Declarations = filterMCPDeclarations(result.Declarations, agentFilter, scopeFilter)
			result.Diagnostics = filterMCPDiagnostics(result.Diagnostics, agentFilter, scopeFilter)
			if jsonOut {
				encoder := json.NewEncoder(app.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			renderMCPDiagnostics(app, result.Diagnostics)
			return renderMCPTable(app, result.Declarations)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&all, "all", false, "scan every configured canonical repository")
	flags.StringVarP(&repoRef, "repo", "r", "", "scan one repository or explicit checkout path")
	flags.StringSliceVar(&agents, "agent", nil, "agent format: claude-code, codex, cursor, gemini-cli, opencode")
	flags.StringSliceVar(&scopes, "scope", nil, "declaration scope: project, local, user, custom, system-defaults, system-override, managed")
	flags.BoolVar(&jsonOut, "json", false, "emit a stable sanitized JSON envelope")
	registerFlagCompletion(cmd, "repo", completeRepoFlag(app))
	registerFlagCompletion(cmd, "agent", fixedCompletions("claude-code", "codex", "cursor", "gemini-cli", "opencode"))
	registerFlagCompletion(cmd, "scope", fixedCompletions("project", "local", "user", "custom", "system-defaults", "system-override", "managed"))
	return cmd
}

func parseMCPAgents(values []string) (map[agentmcp.Agent]bool, error) {
	allowed := map[string]agentmcp.Agent{
		"claude-code": agentmcp.AgentClaudeCode,
		"codex":       agentmcp.AgentCodex,
		"cursor":      agentmcp.AgentCursor,
		"gemini-cli":  agentmcp.AgentGeminiCLI,
		"opencode":    agentmcp.AgentOpenCode,
	}
	result := map[agentmcp.Agent]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		agent, ok := allowed[value]
		if !ok {
			return nil, fmt.Errorf("unsupported MCP agent %q", value)
		}
		result[agent] = true
	}
	return result, nil
}

func parseMCPScopes(values []string) (map[agentmcp.Scope]bool, error) {
	allowed := map[string]agentmcp.Scope{
		"project":         agentmcp.ScopeProject,
		"local":           agentmcp.ScopeLocal,
		"user":            agentmcp.ScopeUser,
		"custom":          agentmcp.ScopeCustom,
		"system-defaults": agentmcp.ScopeSystemDefaults,
		"system-override": agentmcp.ScopeSystemOverride,
		"managed":         agentmcp.ScopeManaged,
	}
	result := map[agentmcp.Scope]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		scope, ok := allowed[value]
		if !ok {
			return nil, fmt.Errorf("unsupported MCP scope %q", value)
		}
		result[scope] = true
	}
	return result, nil
}

func filterMCPDeclarations(rows []agentmcp.Declaration, agents map[agentmcp.Agent]bool, scopes map[agentmcp.Scope]bool) []agentmcp.Declaration {
	filtered := make([]agentmcp.Declaration, 0, len(rows))
	for _, row := range rows {
		if len(agents) > 0 && !agents[row.Agent] || len(scopes) > 0 && !scopes[row.Scope] {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func filterMCPDiagnostics(rows []agentmcp.Diagnostic, agents map[agentmcp.Agent]bool, scopes map[agentmcp.Scope]bool) []agentmcp.Diagnostic {
	filtered := make([]agentmcp.Diagnostic, 0, len(rows))
	for _, row := range rows {
		if len(agents) > 0 && !agents[row.Agent] || len(scopes) > 0 && !scopes[row.Scope] {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func renderMCPTable(app *App, rows []agentmcp.Declaration) error {
	if len(rows) == 0 {
		fmt.Fprintln(app.Out, "No MCP server declarations found.")
		return nil
	}
	table := app.newTable("REPO", "SCOPE", "AGENT", "SERVER", "TRANSPORT", "STATE", "SOURCE", "TARGET", "CONFIG")
	for _, row := range rows {
		source := string(row.Source)
		if row.Plugin != "" {
			source += ":" + row.Plugin
		}
		target := row.Endpoint
		if target == "" {
			target = row.Command
		}
		table.Add(dash(row.Repository), string(row.Scope), string(row.Agent), row.Name,
			string(row.Transport), mcpDeclaredState(row), source, dash(target), config.Contract(row.ConfigPath))
	}
	table.Render(app.Out)
	return nil
}

func renderMCPDiagnostics(app *App, diagnostics []agentmcp.Diagnostic) {
	for _, diagnostic := range diagnostics {
		owner := string(diagnostic.Agent) + "/" + string(diagnostic.Scope)
		if diagnostic.Repository != "" {
			owner = diagnostic.Repository + "/" + owner
		}
		fmt.Fprintf(app.Err, "warning: MCP inventory %s (%s): %s\n",
			owner, config.Contract(diagnostic.ConfigPath), diagnostic.Message)
	}
}

func mcpDeclaredState(row agentmcp.Declaration) string {
	if row.Enabled == nil {
		return "configured"
	}
	if *row.Enabled {
		return "enabled"
	}
	return "disabled"
}
