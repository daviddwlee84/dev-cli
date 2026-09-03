package config

import (
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/handoff"
)

func TestAgentRoundTripsNestedLaunchers(t *testing.T) {
	cfg := Default()
	body := `
[[agent]]
name = "opencode"
description = "Local review and implementation agent"
default = true
[agent.run]
command = ["opencode", "run"]
input = "stdin"
timeout = "5m"
[agent.open]
command = ["opencode", "{{prompt_file}}"]
input = "file"
`
	if _, err := toml.Decode(body, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("got %d agents", len(cfg.Agents))
	}
	agent := cfg.Agents[0]
	if agent.Description != "Local review and implementation agent" {
		t.Errorf("description = %q", agent.Description)
	}
	if got := agent.Run.Handoff(handoff.ModeRun).Timeout; got != 5*time.Minute {
		t.Errorf("run timeout = %v", got)
	}
	if got := agent.Open.Handoff(handoff.ModeOpen).Timeout; got != 0 {
		t.Errorf("open timeout = %v, want no default", got)
	}
}

func TestAgentByNameResolvesDefaultAndSingleton(t *testing.T) {
	launcher := AgentLauncher{Command: []string{"agent"}, Input: "stdin"}
	two := Config{Agents: []Agent{
		{Name: "a", Run: launcher},
		{Name: "b", Run: launcher, Default: true},
	}}
	if agent, ok := two.AgentByName(""); !ok || agent.Name != "b" {
		t.Errorf("empty name did not select the default: %v %+v", ok, agent)
	}
	if agent, ok := two.AgentByName("A"); !ok || agent.Name != "a" {
		t.Errorf("lookup should be case-insensitive: %v %+v", ok, agent)
	}
	if _, ok := two.AgentByName("missing"); ok {
		t.Error("an unknown name resolved")
	}

	one := Config{Agents: []Agent{{Name: "only", Run: launcher}}}
	if agent, ok := one.AgentByName(""); !ok || agent.Name != "only" {
		t.Errorf("single agent was not selected: %v %+v", ok, agent)
	}

	ambiguous := Config{Agents: []Agent{{Name: "a", Run: launcher}, {Name: "b", Run: launcher}}}
	if _, ok := ambiguous.AgentByName(""); ok {
		t.Error("an ambiguous configuration resolved to an agent")
	}
}

func TestAgentValidationNamesTheOffendingLauncher(t *testing.T) {
	stdin := AgentLauncher{Command: []string{"agent"}, Input: "stdin"}
	file := AgentLauncher{Command: []string{"agent", handoff.PromptFilePlaceholder}, Input: "file"}
	for name, tc := range map[string]struct {
		agents []Agent
		want   string
	}{
		"missing name":             {[]Agent{{Run: stdin}}, "agent[0].name"},
		"name whitespace":          {[]Agent{{Name: " agent ", Run: stdin}}, "surrounding whitespace"},
		"duplicate name":           {[]Agent{{Name: "a", Run: stdin}, {Name: "A", Run: stdin}}, "duplicates agent[0]"},
		"no launcher":              {[]Agent{{Name: "a"}}, "configure run or open"},
		"empty executable":         {[]Agent{{Name: "a", Run: AgentLauncher{Command: []string{""}, Input: "stdin"}}}, "agent[0].run"},
		"open stdin":               {[]Agent{{Name: "a", Open: stdin}}, "agent[0].open"},
		"open timeout":             {[]Agent{{Name: "a", Open: AgentLauncher{Command: []string{"agent", handoff.PromptFilePlaceholder}, Input: "file", Timeout: Duration{time.Minute}}}}, "does not support a timeout"},
		"file missing placeholder": {[]Agent{{Name: "a", Run: AgentLauncher{Command: []string{"agent"}, Input: "file"}}}, "exactly one"},
		"shell interpolation":      {[]Agent{{Name: "a", Run: AgentLauncher{Shell: "agent {{prompt}}", Input: "stdin"}}}, "static text"},
		"two defaults":             {[]Agent{{Name: "a", Run: stdin, Default: true}, {Name: "b", Run: stdin, Default: true}}, "already the default"},
		"negative timeout":         {[]Agent{{Name: "a", Run: AgentLauncher{Command: []string{"agent"}, Input: "stdin", Timeout: Duration{-time.Second}}}}, "timeout"},
		"valid run and open":       {[]Agent{{Name: "a", Run: stdin, Open: file}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Agents = tc.agents
			err := cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestRunGetsDefaultTimeoutAndOpenDoesNot(t *testing.T) {
	launcher := AgentLauncher{Command: []string{"agent"}, Input: "stdin"}
	if got := launcher.Handoff(handoff.ModeRun).Timeout; got != 10*time.Minute {
		t.Errorf("run timeout = %v", got)
	}
	// Open cannot use stdin, but timeout resolution is independent of transport.
	if got := launcher.Handoff(handoff.ModeOpen).Timeout; got != 0 {
		t.Errorf("open timeout = %v", got)
	}
}

func TestNoAgentsAreConfiguredByDefault(t *testing.T) {
	if agents := Default().Agents; len(agents) != 0 {
		t.Errorf("Default() ships agents: %+v", agents)
	}
}
