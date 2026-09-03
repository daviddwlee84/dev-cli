package promptkit

import (
	"strings"
	"testing"
	"time"
)

func TestRecipesAreSortedAndUnique(t *testing.T) {
	recipes := Recipes()
	seen := map[string]bool{}
	for i, recipe := range recipes {
		if seen[recipe.Name] {
			t.Fatalf("duplicate recipe %q", recipe.Name)
		}
		seen[recipe.Name] = true
		if i > 0 && recipes[i-1].Name > recipe.Name {
			t.Fatalf("recipes are not sorted: %q before %q", recipes[i-1].Name, recipe.Name)
		}
		if found, ok := Lookup(recipe.Name); !ok || found != recipe {
			t.Errorf("Lookup(%q) = %+v, %v", recipe.Name, found, ok)
		}
	}
	for _, want := range []string{"pr-triage", "session-close", "workspace-closeout"} {
		if !seen[want] {
			t.Errorf("missing recipe %q", want)
		}
	}
}

func TestRenderBuildsVersionedDeterministicEnvelope(t *testing.T) {
	recipe, _ := Lookup("pr-triage")
	when := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rendered, envelope, err := Render(recipe, Snapshot{
		Scope:        "account",
		Capabilities: []Capability{{Name: "z", Available: false}, {Name: "a", Available: true}},
		Warnings:     []Warning{{Source: "gitlab", Code: "signed-out", Message: "x"}},
		Context:      map[string]any{"title": "Fix {{unknown}}"},
	}, when, "host")
	if err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.ContextVersion != 1 || envelope.GeneratedAt != when {
		t.Errorf("envelope versions/time = %+v", envelope)
	}
	if len(envelope.Capabilities) != 2 || envelope.Capabilities[0].Name != "a" {
		t.Errorf("capabilities not sorted: %+v", envelope.Capabilities)
	}
	for _, want := range []string{`"recipe": "pr-triage"`, `"title": "Fix {{unknown}}"`, `"host": "host"`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered prompt missing %q:\n%s", want, rendered)
		}
	}
}

func TestEveryBuiltinTemplateRenders(t *testing.T) {
	for _, recipe := range Recipes() {
		t.Run(recipe.Name, func(t *testing.T) {
			_, _, err := Render(recipe, Snapshot{Scope: recipe.Scope, Context: map[string]any{}}, time.Unix(0, 0), "test")
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
