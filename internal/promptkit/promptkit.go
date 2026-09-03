// Package promptkit owns the built-in prompt recipe registry and the stable
// envelope handed to an external agent. Collection remains in domain providers;
// templates contain instructions, never executable workflow steps.
package promptkit

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/templatex"
)

const SchemaVersion = 1

const (
	RecipePRTriage          = "pr-triage"
	RecipeSessionClose      = "session-close"
	RecipeWorkspaceCloseout = "workspace-closeout"
)

//go:embed templates/*.md
var templates embed.FS

// Recipe is one built-in use of collected context.
type Recipe struct {
	Name           string `json:"name"`
	Summary        string `json:"summary"`
	Scope          string `json:"scope"`
	TargetUsage    string `json:"target_usage,omitempty"`
	ContextVersion int    `json:"context_version"`
	TemplatePath   string `json:"-"`
}

var builtins = []Recipe{
	{
		Name: RecipePRTriage, Summary: "Prioritize pull requests you opened or were asked to review",
		Scope: "account", ContextVersion: 1, TemplatePath: "templates/pr-triage.md",
	},
	{
		Name: RecipeSessionClose, Summary: "Review live agent sessions and the work that must be saved before closing",
		Scope: "machine", ContextVersion: 1, TemplatePath: "templates/session-close.md",
	},
	{
		Name: RecipeWorkspaceCloseout, Summary: "Decide which tasks and worktrees in one repository should finish, park, retire, or be inspected",
		Scope: "repository", TargetUsage: "[repo-or-checkout]", ContextVersion: 1,
		TemplatePath: "templates/workspace-closeout.md",
	},
}

// Recipes returns a sorted copy of the built-in registry.
func Recipes() []Recipe {
	out := append([]Recipe(nil), builtins...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup finds one exact built-in recipe.
func Lookup(name string) (Recipe, bool) {
	for _, recipe := range builtins {
		if recipe.Name == name {
			return recipe, true
		}
	}
	return Recipe{}, false
}

// Provider collects one immutable, read-only context snapshot.
type Provider interface {
	Collect(context.Context) (Snapshot, error)
}

// ProviderFunc adapts a function into a Provider.
type ProviderFunc func(context.Context) (Snapshot, error)

func (f ProviderFunc) Collect(ctx context.Context) (Snapshot, error) { return f(ctx) }

// Target identifies the machine-local object a recipe is about.
type Target struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Path             string `json:"path,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
}

// Capability distinguishes "nothing found" from "the collector could not ask".
type Capability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

// Warning is a partial source failure with an optional next action.
type Warning struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
}

// Snapshot is the provider-neutral result of deterministic collection.
type Snapshot struct {
	Scope            string
	Target           *Target
	WorkingDirectory string
	Capabilities     []Capability
	Warnings         []Warning
	ContextVersion   int
	Context          any
}

// Envelope is the stable machine context embedded in every prompt. Common
// fields are add-only within SchemaVersion; each recipe versions Context
// independently.
type Envelope struct {
	SchemaVersion  int          `json:"schema_version"`
	Recipe         string       `json:"recipe"`
	ContextVersion int          `json:"context_version"`
	GeneratedAt    time.Time    `json:"generated_at"`
	Host           string       `json:"host"`
	Scope          string       `json:"scope"`
	Target         *Target      `json:"target,omitempty"`
	Capabilities   []Capability `json:"capabilities"`
	Warnings       []Warning    `json:"warnings"`
	Context        any          `json:"context"`
}

// Render serializes one snapshot and substitutes it exactly once into the
// built-in Markdown template. Strings inside Context are data, so a literal
// {{value}} in a title or task note is never reparsed.
func Render(recipe Recipe, snapshot Snapshot, generatedAt time.Time, host string) (string, Envelope, error) {
	body, err := templates.ReadFile(recipe.TemplatePath)
	if err != nil {
		return "", Envelope{}, fmt.Errorf("read prompt recipe %q: %w", recipe.Name, err)
	}
	if strings.Count(string(body), "{{context_json}}") != 1 {
		return "", Envelope{}, fmt.Errorf("prompt recipe %q must contain exactly one {{context_json}}", recipe.Name)
	}
	contextVersion := snapshot.ContextVersion
	if contextVersion == 0 {
		contextVersion = recipe.ContextVersion
	}
	capabilities := append([]Capability(nil), snapshot.Capabilities...)
	warnings := append([]Warning(nil), snapshot.Warnings...)
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].Source != warnings[j].Source {
			return warnings[i].Source < warnings[j].Source
		}
		return warnings[i].Code < warnings[j].Code
	})
	envelope := Envelope{
		SchemaVersion: SchemaVersion, Recipe: recipe.Name, ContextVersion: contextVersion,
		GeneratedAt: generatedAt.UTC(), Host: host, Scope: snapshot.Scope,
		Target: snapshot.Target, Capabilities: capabilities, Warnings: warnings,
		Context: snapshot.Context,
	}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return "", Envelope{}, fmt.Errorf("encode prompt context: %w", err)
	}
	rendered, err := templatex.Render(string(body), map[string]any{"context_json": string(encoded)})
	if err != nil {
		return "", Envelope{}, fmt.Errorf("render prompt recipe %q: %w", recipe.Name, err)
	}
	return rendered, envelope, nil
}
