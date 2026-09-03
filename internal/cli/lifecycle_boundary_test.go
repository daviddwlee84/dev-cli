package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestMigratedLifecycleHandlersUseTaskflowMutationBoundary(t *testing.T) {
	files := map[string][]string{
		"park.go":      {"executeTaskLifecycle(", "flow.ParkWarmOptions", "flow.ParkColdOptions"},
		"resume.go":    {"executeTaskLifecycle(", "flow.ResumeOptions"},
		"done_flow.go": {"session.plan(", "session.apply("},
		"retire.go":    {"session.plan(", "session.apply(", "executeNonTaskLifecycle(", "flow.RemoveCheckoutOptions"},
		"adopt.go":     {"executeNonTaskLifecycle(", "flow.AdoptOptions"},
		"worktree.go":  {"executeNonTaskLifecycle(", "flow.RemoveCheckoutOptions"},
		"sweep.go":     {"sweepTaskPlan(", "session.apply("},
	}
	for name, required := range files {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			text := string(source)
			for _, boundary := range required {
				if !strings.Contains(text, boundary) {
					t.Errorf("%s does not use taskflow boundary %q", name, boundary)
				}
			}
			for _, forbidden := range []string{
				"Tasks.Save(", "Tasks.Update(", "Tasks.Delete(",
				"gitx.RemoveWorktree(", "retirement.Service{", "retire.Service{",
			} {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s contains direct lifecycle mutation %q", name, forbidden)
				}
			}

			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(fileSet, name, source, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, expression := range assignment.Lhs {
					selector, ok := expression.(*ast.SelectorExpr)
					if ok && (selector.Sel.Name == "State" || selector.Sel.Name == "WorktreePath") {
						t.Errorf("%s directly assigns lifecycle field %s at %s", name, selector.Sel.Name,
							fileSet.Position(selector.Pos()))
					}
				}
				return true
			})
		})
	}
}

func TestDashboardLifecycleCallbacksKeepGuardedBoundaries(t *testing.T) {
	source, err := os.ReadFile("tui.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	parkStart := strings.Index(text, "Park: func(")
	setNextStart := strings.Index(text, "SetNext: func(")
	startStart := -1
	if setNextStart >= 0 {
		if relative := strings.Index(text[setNextStart:], "Start: func("); relative >= 0 {
			startStart = setNextStart + relative
		}
	}
	if parkStart < 0 || setNextStart <= parkStart || startStart <= setNextStart {
		t.Fatalf("could not isolate dashboard Park and SetNext callbacks")
	}
	park := text[parkStart:setNextStart]
	for _, required := range []string{"executeTaskLifecycle(", "flow.ParkWarmOptions"} {
		if !strings.Contains(park, required) {
			t.Errorf("dashboard Park callback does not use %q", required)
		}
	}
	for _, forbidden := range []string{"Tasks.Save(", "Tasks.Update(", "gitx.RemoveWorktree("} {
		if strings.Contains(park, forbidden) {
			t.Errorf("dashboard Park callback contains direct mutation %q", forbidden)
		}
	}

	setNext := text[setNextStart:startStart]
	for _, required := range []string{"Tasks.GetRecord(", "Tasks.Update("} {
		if !strings.Contains(setNext, required) {
			t.Errorf("dashboard SetNext callback does not use revision-aware %q", required)
		}
	}
	if strings.Contains(setNext, "Tasks.Save(") {
		t.Error("dashboard SetNext callback bypasses compare-and-update with Tasks.Save")
	}
}
