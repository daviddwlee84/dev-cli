package scaffold

// Builtins returns a fresh copy of the presets available without any user
// configuration. The default remains minimal so existing scripted repository
// creation cannot acquire agent files or external setup steps implicitly.
func Builtins() Config {
	return Config{
		Version:          CurrentVersion,
		DefaultPreset:    "minimal",
		DefaultAgents:    []string{"claude-code", "codex"},
		Sources:          []string{"builtin"},
		defaultAgentsSet: true,
		Presets: map[string]Preset{
			"minimal": {
				Description:   "README, main branch, and an initial commit",
				Readme:        boolp(true),
				License:       "none",
				Remote:        "none",
				Handoff:       "stay",
				InitialBranch: "main",
				InitialCommit: boolp(true),
				CommitMessage: "chore: initial commit",
				Origin:        "builtin",
				Files: []File{
					{
						ID:             "readme",
						Destination:    "README.md",
						Content:        stringp("# {{name}}\n"),
						Mode:           "0644",
						Enabled:        boolp(true),
						Origin:         "builtin",
						TemplateOrigin: "builtin",
					},
				},
			},
			"agent-ready": {
				Extends:       "minimal",
				Description:   "Local agent guidance and project-scoped Claude plans; optional hygiene and knowledge skills",
				Gitignore:     []string{"common"},
				ClaudePlans:   boolp(true),
				AgentContract: "starter",
				License:       "none",
				Remote:        "ask",
				Handoff:       "cd",
				Origin:        "builtin",
				Inputs: []Input{
					{
						ID:      "deployment",
						Type:    InputChoice,
						Label:   "Deployment mechanism",
						Default: "none",
						Choices: []string{"none", "npm", "pip", "docker", "chezmoi"},
						Origin:  "builtin",
					},
				},
				Files: []File{
					{
						ID:             "agent-contract",
						Destination:    "AGENTS.md",
						Content:        stringp(StarterAgentContract()),
						Mode:           "0644",
						Enabled:        boolp(true),
						Origin:         "builtin",
						TemplateOrigin: "builtin",
					},
					{
						ID:             "claude-settings",
						Destination:    ".claude/settings.json",
						Content:        stringp("{\n  \"plansDirectory\": \"./.claude/plans\"\n}\n"),
						Mode:           "0644",
						Enabled:        boolp(true),
						Origin:         "builtin",
						TemplateOrigin: "builtin",
					},
					{
						ID:             "claude-plans-directory",
						Destination:    ".claude/plans/.gitkeep",
						Content:        stringp(""),
						Mode:           "0644",
						Enabled:        boolp(true),
						Origin:         "builtin",
						TemplateOrigin: "builtin",
					},
				},
				Skills: []Skill{
					{
						ID:      "agent-history-hygiene",
						Source:  "daviddwlee84/agent-skills/skills",
						Name:    "agent-history-hygiene",
						Agents:  []string{"claude-code", "codex"},
						Default: boolp(false),
						Enabled: boolp(true),
						Origin:  "builtin",
						Setup: &SkillSetup{
							Phase:    BeforeCommit,
							Builtin:  "agent-history-hygiene",
							Required: boolp(true),
						},
					},
					{
						ID:      "project-knowledge-harness",
						Source:  "daviddwlee84/agent-skills/skills",
						Name:    "project-knowledge-harness",
						Agents:  []string{"claude-code", "codex"},
						Default: boolp(false),
						Enabled: boolp(true),
						Origin:  "builtin",
						Setup: &SkillSetup{
							Phase:   BeforeCommit,
							Builtin: "project-knowledge-harness",
							Args: []string{
								"--deployment", "{{input.deployment}}",
							},
							Required: boolp(true),
						},
					},
				},
				Catalog: []SkillCatalog{
					{
						ID:          "agent-history-hygiene",
						Source:      "daviddwlee84/agent-skills/skills",
						Label:       "Agent history hygiene",
						Description: "Pre-commit redaction and secret scanning for agent artifacts",
						Default:     boolp(false),
						Enabled:     boolp(true),
						Origin:      "builtin",
					},
					{
						ID:          "project-knowledge-harness",
						Source:      "daviddwlee84/agent-skills/skills",
						Label:       "Project knowledge harness",
						Description: "TODO, backlog, and pitfalls project memory",
						Default:     boolp(false),
						Enabled:     boolp(true),
						Origin:      "builtin",
					},
				},
			},
		},
	}
}
