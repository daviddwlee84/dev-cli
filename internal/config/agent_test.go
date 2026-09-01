package config

import (
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestAgentRoundTripsThroughTOML(t *testing.T) {
	cfg := Default()
	body := `
[[agent]]
name = "claude"
command = ["claude", "-p"]
default = true

[[agent]]
name = "codex"
run = "codex exec --file {{prompt_file}}"
input = "file"
interactive = true
timeout = "5m"
`
	if _, err := toml.Decode(body, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("got %d agents", len(cfg.Agents))
	}
	if got := cfg.Agents[0].EffectiveInput(); got != AgentInputStdin {
		t.Errorf("default input = %q, want %q", got, AgentInputStdin)
	}
	if got := cfg.Agents[0].EffectiveTimeout(); got != 10*time.Minute {
		t.Errorf("default timeout = %v", got)
	}
	if got := cfg.Agents[1].EffectiveTimeout(); got != 5*time.Minute {
		t.Errorf("configured timeout = %v", got)
	}
}

func TestAgentByNameResolvesDefaultAndSingleton(t *testing.T) {
	two := Config{Agents: []Agent{
		{Name: "a", Run: "a"},
		{Name: "b", Run: "b", Default: true},
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

	// One configured agent needs no default marker to be unambiguous.
	one := Config{Agents: []Agent{{Name: "only", Run: "only"}}}
	if agent, ok := one.AgentByName(""); !ok || agent.Name != "only" {
		t.Errorf("single agent was not selected: %v %+v", ok, agent)
	}

	// Two agents and no default is genuinely ambiguous, so it must not guess.
	ambiguous := Config{Agents: []Agent{{Name: "a", Run: "a"}, {Name: "b", Run: "b"}}}
	if _, ok := ambiguous.AgentByName(""); ok {
		t.Error("an ambiguous configuration resolved to an agent")
	}
}

func TestAgentValidationNamesTheOffendingKey(t *testing.T) {
	for name, tc := range map[string]struct {
		agents []Agent
		want   string
	}{
		"missing name":     {[]Agent{{Run: "x"}}, "agent[0].name"},
		"duplicate name":   {[]Agent{{Name: "a", Run: "x"}, {Name: "A", Run: "y"}}, "duplicates agent[0]"},
		"no command":       {[]Agent{{Name: "a"}}, "one of command or run"},
		"both forms":       {[]Agent{{Name: "a", Run: "x", Command: []string{"y"}}}, "not both"},
		"unknown input":    {[]Agent{{Name: "a", Run: "x", Input: "telepathy"}}, "input \"telepathy\""},
		"two defaults":     {[]Agent{{Name: "a", Run: "x", Default: true}, {Name: "b", Run: "y", Default: true}}, "already the default"},
		"negative timeout": {[]Agent{{Name: "a", Run: "x", Timeout: Duration{-time.Second}}}, "must not be negative"},
		// A prompt interpolated into a shell string is a command injection,
		// and these prompts contain shell commands on purpose.
		"argv through a shell": {[]Agent{{Name: "a", Run: "x", Input: AgentInputArgv}}, "requires command, not run"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Agents = tc.agents
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%v was accepted", tc.agents)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestNoAgentsAreConfiguredByDefault(t *testing.T) {
	// Shipping a default agent would make dev depend on one particular tool.
	if agents := Default().Agents; len(agents) != 0 {
		t.Errorf("Default() ships agents: %+v", agents)
	}
}
