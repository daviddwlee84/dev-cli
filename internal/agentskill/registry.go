package agentskill

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The registry is a path-only snapshot. Detection callbacks from the upstream
// module are deliberately not copied or run: an inventory read must never run
// agent or project code.
const (
	RegistrySource  = "vercel-labs/skills"
	RegistryVersion = "v1.5.23"
	RegistryURL     = "https://github.com/vercel-labs/skills/blob/v1.5.23/src/agents.ts"

	// RegistrySHA256 labels the exact src/agents.ts bytes used for this snapshot.
	RegistrySHA256 = "82ad7a09e3a33b6314baab7414d53c72b64a63e1efae7468eba3ad6c4fb2ea5f"
)

// AgentDefinition is one path mapping from the pinned upstream registry.
// GlobalEnvironment names the environment variable that can replace the base
// directory; it is empty for fixed home-relative paths.
type AgentDefinition struct {
	ID                string
	DisplayName       string
	ProjectSkillsDir  string
	GlobalSkillsDir   string
	GlobalEnvironment string
	RegistrySource    string
	RegistryVersion   string
}

type globalBase uint8

const (
	baseNone globalBase = iota
	baseHome
	baseConfigHome
	baseCodexHome
	baseClaudeHome
	baseVibeHome
	baseHermesHome
	baseAutohandHome
	baseGrokHome
	baseOpenClawHome
)

type registryEntry struct {
	id      string
	display string
	project string
	base    globalBase
	global  string
}

// registrySnapshot mirrors every one of the 77 entries in skills v1.5.23
// src/agents.ts. Keep changes versioned rather than silently following latest.
var registrySnapshot = []registryEntry{
	{id: "aider-desk", display: "AiderDesk", project: ".aider-desk/skills", base: baseHome, global: ".aider-desk/skills"},
	{id: "amp", display: "Amp", project: ".agents/skills", base: baseConfigHome, global: "agents/skills"},
	{id: "antigravity", display: "Antigravity", project: ".agents/skills", base: baseHome, global: ".gemini/antigravity/skills"},
	{id: "antigravity-cli", display: "Antigravity CLI", project: ".agents/skills", base: baseHome, global: ".gemini/antigravity-cli/skills"},
	{id: "astrbot", display: "AstrBot", project: "data/skills", base: baseHome, global: ".astrbot/data/skills"},
	{id: "autohand-code", display: "Autohand Code CLI", project: ".autohand/skills", base: baseAutohandHome, global: "skills"},
	{id: "augment", display: "Augment", project: ".augment/skills", base: baseHome, global: ".augment/skills"},
	{id: "bob", display: "IBM Bob", project: ".bob/skills", base: baseHome, global: ".bob/skills"},
	{id: "claude-code", display: "Claude Code", project: ".claude/skills", base: baseClaudeHome, global: "skills"},
	{id: "openclaw", display: "OpenClaw", project: "skills", base: baseOpenClawHome, global: "skills"},
	{id: "cline", display: "Cline", project: ".agents/skills", base: baseHome, global: ".agents/skills"},
	{id: "codearts-agent", display: "CodeArts Agent", project: ".codeartsdoer/skills", base: baseHome, global: ".codeartsdoer/skills"},
	{id: "codebuddy", display: "CodeBuddy", project: ".codebuddy/skills", base: baseHome, global: ".codebuddy/skills"},
	{id: "codemaker", display: "Codemaker", project: ".codemaker/skills", base: baseHome, global: ".codemaker/skills"},
	{id: "codestudio", display: "Code Studio", project: ".codestudio/skills", base: baseHome, global: ".codestudio/skills"},
	{id: "codex", display: "Codex", project: ".agents/skills", base: baseCodexHome, global: "skills"},
	{id: "command-code", display: "Command Code", project: ".commandcode/skills", base: baseHome, global: ".commandcode/skills"},
	{id: "continue", display: "Continue", project: ".continue/skills", base: baseHome, global: ".continue/skills"},
	{id: "cortex", display: "Cortex Code", project: ".cortex/skills", base: baseHome, global: ".snowflake/cortex/skills"},
	{id: "crush", display: "Crush", project: ".crush/skills", base: baseHome, global: ".config/crush/skills"},
	{id: "cursor", display: "Cursor", project: ".agents/skills", base: baseHome, global: ".cursor/skills"},
	{id: "deepagents", display: "Deep Agents", project: ".agents/skills", base: baseHome, global: ".deepagents/agent/skills"},
	{id: "devin", display: "Devin for Terminal", project: ".devin/skills", base: baseConfigHome, global: "devin/skills"},
	{id: "dexto", display: "Dexto", project: ".agents/skills", base: baseHome, global: ".agents/skills"},
	{id: "droid", display: "Droid", project: ".factory/skills", base: baseHome, global: ".factory/skills"},
	{id: "eve", display: "Eve", project: "agent/skills", base: baseNone},
	{id: "firebender", display: "Firebender", project: ".agents/skills", base: baseHome, global: ".firebender/skills"},
	{id: "forgecode", display: "ForgeCode", project: ".forge/skills", base: baseHome, global: ".forge/skills"},
	{id: "gemini-cli", display: "Gemini CLI", project: ".agents/skills", base: baseHome, global: ".gemini/skills"},
	{id: "github-copilot", display: "GitHub Copilot", project: ".agents/skills", base: baseHome, global: ".copilot/skills"},
	{id: "goose", display: "Goose", project: ".goose/skills", base: baseConfigHome, global: "goose/skills"},
	{id: "grok", display: "Grok Build", project: ".grok/skills", base: baseGrokHome, global: "skills"},
	{id: "hermes-agent", display: "Hermes Agent", project: ".hermes/skills", base: baseHermesHome, global: "skills"},
	{id: "inference-sh", display: "inference.sh", project: ".inferencesh/skills", base: baseHome, global: ".inferencesh/skills"},
	{id: "jazz", display: "Jazz", project: ".jazz/skills", base: baseHome, global: ".jazz/skills"},
	{id: "junie", display: "Junie", project: ".junie/skills", base: baseHome, global: ".junie/skills"},
	{id: "iflow-cli", display: "iFlow CLI", project: ".iflow/skills", base: baseHome, global: ".iflow/skills"},
	{id: "kilo", display: "Kilo Code", project: ".kilocode/skills", base: baseHome, global: ".kilocode/skills"},
	{id: "kimchi", display: "Kimchi", project: ".kimchi/skills", base: baseHome, global: ".config/kimchi/harness/skills"},
	{id: "kimi-code-cli", display: "Kimi Code CLI", project: ".agents/skills", base: baseHome, global: ".agents/skills"},
	{id: "kiro-cli", display: "Kiro CLI", project: ".kiro/skills", base: baseHome, global: ".kiro/skills"},
	{id: "kode", display: "Kode", project: ".kode/skills", base: baseHome, global: ".kode/skills"},
	{id: "lingma", display: "Lingma", project: ".lingma/skills", base: baseHome, global: ".lingma/skills"},
	{id: "loaf", display: "Loaf", project: ".agents/skills", base: baseHome, global: ".agents/skills"},
	{id: "mcpjam", display: "MCPJam", project: ".mcpjam/skills", base: baseHome, global: ".mcpjam/skills"},
	{id: "minimax-code", display: "MiniMax Code", project: ".minimax/skills", base: baseHome, global: ".minimax/skills"},
	{id: "mistral-vibe", display: "Mistral Vibe", project: ".vibe/skills", base: baseVibeHome, global: "skills"},
	{id: "moxby", display: "Moxby", project: ".moxby/skills", base: baseHome, global: ".moxby/skills"},
	{id: "mux", display: "Mux", project: ".mux/skills", base: baseHome, global: ".mux/skills"},
	{id: "opencode", display: "OpenCode", project: ".agents/skills", base: baseConfigHome, global: "opencode/skills"},
	{id: "openhands", display: "OpenHands", project: ".openhands/skills", base: baseHome, global: ".openhands/skills"},
	{id: "ona", display: "Ona", project: ".ona/skills", base: baseHome, global: ".ona/skills"},
	{id: "pi", display: "Pi", project: ".pi/skills", base: baseHome, global: ".pi/agent/skills"},
	{id: "posit-assistant", display: "Posit Assistant", project: ".posit/assistant/skills", base: baseHome, global: ".posit/assistant/skills"},
	{id: "qoder", display: "Qoder", project: ".qoder/skills", base: baseHome, global: ".qoder/skills"},
	{id: "qoder-cn", display: "Qoder CN", project: ".qoder/skills", base: baseHome, global: ".qoder-cn/skills"},
	{id: "qwen-code", display: "Qwen Code", project: ".qwen/skills", base: baseHome, global: ".qwen/skills"},
	{id: "replit", display: "Replit", project: ".agents/skills", base: baseConfigHome, global: "agents/skills"},
	{id: "reasonix", display: "Reasonix", project: ".reasonix/skills", base: baseHome, global: ".reasonix/skills"},
	{id: "rovodev", display: "Rovo Dev", project: ".rovodev/skills", base: baseHome, global: ".rovodev/skills"},
	{id: "roo", display: "Roo Code", project: ".roo/skills", base: baseHome, global: ".roo/skills"},
	{id: "tabnine-cli", display: "Tabnine CLI", project: ".tabnine/agent/skills", base: baseHome, global: ".tabnine/agent/skills"},
	{id: "terramind", display: "Terramind", project: ".terramind/skills", base: baseHome, global: ".terramind/skills"},
	{id: "tinycloud", display: "Tinycloud", project: ".tinycloud/skills", base: baseHome, global: ".tinycloud/skills"},
	{id: "trae", display: "Trae", project: ".trae/skills", base: baseHome, global: ".trae/skills"},
	{id: "trae-cn", display: "Trae CN", project: ".trae/skills", base: baseHome, global: ".trae-cn/skills"},
	{id: "warp", display: "Warp", project: ".agents/skills", base: baseHome, global: ".agents/skills"},
	{id: "windsurf", display: "Windsurf", project: ".windsurf/skills", base: baseHome, global: ".codeium/windsurf/skills"},
	{id: "zed", display: "Zed", project: ".agents/skills", base: baseHome, global: ".agents/skills"},
	{id: "zcode", display: "ZCode", project: ".zcode/skills", base: baseHome, global: ".zcode/skills"},
	{id: "zencoder", display: "Zencoder", project: ".zencoder/skills", base: baseHome, global: ".zencoder/skills"},
	{id: "zenflow", display: "Zenflow", project: ".zencoder/skills", base: baseHome, global: ".zencoder/skills"},
	{id: "neovate", display: "Neovate", project: ".neovate/skills", base: baseHome, global: ".neovate/skills"},
	{id: "pochi", display: "Pochi", project: ".pochi/skills", base: baseHome, global: ".pochi/skills"},
	{id: "promptscript", display: "PromptScript", project: ".agents/skills", base: baseNone},
	{id: "adal", display: "AdaL", project: ".adal/skills", base: baseHome, global: ".adal/skills"},
	{id: "universal", display: "Universal", project: ".agents/skills", base: baseConfigHome, global: "agents/skills"},
}

// Registry returns the complete path snapshot with environment overrides
// resolved for this process. Results are sorted by stable agent ID.
func Registry() []AgentDefinition {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		home = ""
	}
	definitions := make([]AgentDefinition, 0, len(registrySnapshot))
	for _, entry := range registrySnapshot {
		base, environment := resolveGlobalBase(entry.base, home)
		global := ""
		if entry.base != baseNone && base != "" {
			global = filepath.Join(base, filepath.FromSlash(entry.global))
		}
		definitions = append(definitions, AgentDefinition{
			ID: entry.id, DisplayName: entry.display,
			ProjectSkillsDir: filepath.FromSlash(entry.project),
			GlobalSkillsDir:  global, GlobalEnvironment: environment,
			RegistrySource: RegistrySource, RegistryVersion: RegistryVersion,
		})
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	return definitions
}

func agentDisplayNames(ids []string) []string {
	byID := make(map[string]string, len(registrySnapshot))
	for _, entry := range registrySnapshot {
		byID[entry.id] = entry.display
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if name := byID[id]; name != "" {
			names = append(names, name)
		} else {
			names = append(names, id)
		}
	}
	return uniqueSorted(names)
}

func resolveGlobalBase(base globalBase, home string) (string, string) {
	switch base {
	case baseNone:
		return "", ""
	case baseHome:
		return home, ""
	case baseConfigHome:
		if value := absoluteEnvironment("XDG_CONFIG_HOME"); value != "" {
			return value, "XDG_CONFIG_HOME"
		}
		if home == "" {
			return "", "XDG_CONFIG_HOME"
		}
		return filepath.Join(home, ".config"), "XDG_CONFIG_HOME"
	case baseCodexHome:
		return environmentBase("CODEX_HOME", filepath.Join(home, ".codex")), "CODEX_HOME"
	case baseClaudeHome:
		return environmentBase("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")), "CLAUDE_CONFIG_DIR"
	case baseVibeHome:
		return environmentBase("VIBE_HOME", filepath.Join(home, ".vibe")), "VIBE_HOME"
	case baseHermesHome:
		return environmentBase("HERMES_HOME", filepath.Join(home, ".hermes")), "HERMES_HOME"
	case baseAutohandHome:
		return environmentBase("AUTOHAND_HOME", filepath.Join(home, ".autohand")), "AUTOHAND_HOME"
	case baseGrokHome:
		return environmentBase("GROK_HOME", filepath.Join(home, ".grok")), "GROK_HOME"
	case baseOpenClawHome:
		if home == "" {
			return "", ""
		}
		for _, candidate := range []string{".openclaw", ".clawdbot", ".moltbot"} {
			path := filepath.Join(home, candidate)
			if _, err := os.Stat(path); err == nil {
				return path, ""
			}
		}
		return filepath.Join(home, ".openclaw"), ""
	default:
		return home, ""
	}
}

func environmentBase(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if filepath.IsAbs(fallback) {
		return fallback
	}
	return ""
}

func absoluteEnvironment(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return ""
}
