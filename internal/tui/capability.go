package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentmcp"
	"github.com/daviddwlee84/dev-cli/internal/agentskill"
)

func skillFilePath(row agentskill.Skill) (string, error) {
	if row.Presence != agentskill.PresenceMissing && strings.TrimSpace(row.Path) != "" {
		return filepath.Join(row.Path, "SKILL.md"), nil
	}
	if row.Lock != nil && strings.TrimSpace(row.Lock.File) != "" {
		return row.Lock.File, nil
	}
	return "", fmt.Errorf("skill %s has no local skill or lock file", row.Name)
}

func (m Model) capabilityFilePath() (string, error) {
	if row, ok := m.currentSkill(); ok {
		return skillFilePath(row)
	}
	if row, ok := m.currentMCP(); ok {
		if strings.TrimSpace(row.ConfigPath) == "" {
			return "", fmt.Errorf("MCP server %s has no local config path", row.Name)
		}
		return row.ConfigPath, nil
	}
	return "", fmt.Errorf("the selected row has no capability config file")
}

func formatSkillSummary(row agentskill.Skill) string {
	lines := []string{
		"name: " + row.Name,
		"scope: " + string(row.Scope),
	}
	lines = appendSummaryLine(lines, "repository", row.Repository)
	lines = appendSummaryLine(lines, "checkout", row.Checkout)
	lines = appendSummaryLine(lines, "path", row.Path)
	lines = appendSummaryLine(lines, "agents", strings.Join(row.Agents, ", "))
	lines = appendSummaryLine(lines, "managed", string(row.ManagedBy))
	lines = appendSummaryLine(lines, "presence", string(row.Presence))
	lines = appendSummaryLine(lines, "integrity", string(row.Integrity))
	lines = appendSummaryLine(lines, "source", row.Source)
	lines = appendSummaryLine(lines, "source_url", row.SourceURL)
	lines = appendSummaryLine(lines, "update", string(row.UpdateStatus))
	return strings.Join(lines, "\n")
}

func formatMCPSummary(row agentmcp.Declaration) string {
	lines := []string{
		"server: " + row.Name,
		"agent: " + string(row.Agent),
		"scope: " + string(row.Scope),
	}
	lines = appendSummaryLine(lines, "repository", row.Repository)
	lines = appendSummaryLine(lines, "checkout", row.Checkout)
	lines = appendSummaryLine(lines, "config", row.ConfigPath)
	lines = appendSummaryLine(lines, "source", mcpSource(row))
	lines = appendSummaryLine(lines, "state", mcpDeclarationState(row))
	lines = appendSummaryLine(lines, "transport", string(row.Transport))
	lines = appendSummaryLine(lines, "endpoint", row.Endpoint)
	lines = appendSummaryLine(lines, "command", row.Command)
	if row.ArgumentCount > 0 {
		lines = append(lines, fmt.Sprintf("arguments: %d value(s) redacted", row.ArgumentCount))
	}
	lines = appendSummaryLine(lines, "policies", mcpPolicySummary(row))
	if len(row.Credentials) > 0 {
		credentials := make([]string, 0, len(row.Credentials))
		for _, credential := range row.Credentials {
			value := string(credential.Kind)
			if credential.Name != "" {
				value += ":" + credential.Name
			}
			credentials = append(credentials, value)
		}
		lines = appendSummaryLine(lines, "credentials", strings.Join(credentials, ", "))
	}
	if len(row.Redactions) > 0 {
		redactions := make([]string, len(row.Redactions))
		for i, redaction := range row.Redactions {
			redactions[i] = string(redaction)
		}
		lines = appendSummaryLine(lines, "redacted", strings.Join(redactions, ", "))
	}
	if len(row.Coverage) > 0 {
		coverage := make([]string, len(row.Coverage))
		for i, item := range row.Coverage {
			coverage[i] = string(item.Code)
		}
		lines = appendSummaryLine(lines, "coverage", strings.Join(coverage, ", "))
	}
	return strings.Join(lines, "\n")
}

func (m Model) copyBindingHelp() string {
	if m.view == ViewRepos {
		return "y context · p path · b branch · s sessions · w worktree paths"
	}
	parts := make([]string, 0, 4)
	if _, err := m.capabilityFilePath(); err == nil {
		parts = append(parts, "p file path")
	}
	parts = append(parts, "s safe summary")
	if row, ok := m.currentSkill(); ok && row.SourceURL != "" {
		parts = append(parts, "u source URL")
	}
	if _, err := m.capabilityFilePath(); err == nil && m.actions.ReadFile != nil {
		parts = append(parts, "f raw file")
	}
	return strings.Join(parts, " · ")
}

func appendSummaryLine(lines []string, label, value string) []string {
	if strings.TrimSpace(value) == "" {
		return lines
	}
	return append(lines, label+": "+value)
}
