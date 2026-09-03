package templatex

import (
	"strings"
	"testing"
)

func TestRenderSubstitutesDottedScalarsWithoutReparsingValues(t *testing.T) {
	got, err := Render("before {{context.json}} after", map[string]any{
		"context": map[string]any{"json": `{"title":"Fix {{unknown}}"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `before {"title":"Fix {{unknown}}"} after`
	if got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

func TestRenderRejectsMalformedAndUnknownVariables(t *testing.T) {
	for name, tmpl := range map[string]string{
		"unknown":          "{{missing}}",
		"opening":          "{{value",
		"closing":          "value}}",
		"empty":            "{{ }}",
		"invalid operator": "{{value | lower}}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Render(tmpl, map[string]any{"value": "ok"})
			if err == nil {
				t.Fatalf("Render(%q) succeeded", tmpl)
			}
		})
	}
}

func TestRenderRejectsNonScalarValues(t *testing.T) {
	_, err := Render("{{value}}", map[string]any{"value": []string{"x"}})
	if err == nil || !strings.Contains(err.Error(), "want a string") {
		t.Fatalf("err = %v", err)
	}
}
