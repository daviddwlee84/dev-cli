package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestColorModeSelection(t *testing.T) {
	var out bytes.Buffer
	if styleForWriter(&out, colorAuto).enabled {
		t.Fatal("auto color should be disabled for a pipe-like writer")
	}
	if !styleForWriter(&out, colorAlways).enabled {
		t.Fatal("always color should style a pipe-like writer")
	}
	if styleForWriter(&out, colorNever).enabled {
		t.Fatal("never color should disable styling")
	}
	t.Setenv("NO_COLOR", "1")
	if !styleForWriter(&out, colorAlways).enabled {
		t.Fatal("explicit --color=always should override NO_COLOR")
	}
	if got := colorModeFromArgs([]string{"status", "--color", "never"}); got != colorNever {
		t.Fatalf("colorModeFromArgs = %q", got)
	}
	if got := colorModeFromArgs([]string{"--color=always", "status"}); got != colorAlways {
		t.Fatalf("colorModeFromArgs = %q", got)
	}
	if err := validateColorMode("rainbow"); err == nil {
		t.Fatal("invalid color mode was accepted")
	}
}

func TestCobraHelpColorModes(t *testing.T) {
	var colored, errOut bytes.Buffer
	root := NewRootCommandWithIO(&colored, &errOut)
	root.SetArgs([]string{"--color=always", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(colored.String(), ansiBold+ansiCyan+"Usage:") {
		t.Fatalf("help heading was not colored:\n%s", colored.String())
	}

	var plain bytes.Buffer
	root = NewRootCommandWithIO(&plain, &errOut)
	root.SetArgs([]string{"--color=never", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain help contains ANSI: %q", plain.String())
	}
	if stripANSI(colored.String()) != plain.String() {
		t.Fatal("colored help changed its plain-text content")
	}
}

func TestColoredTablePreservesPlainAlignment(t *testing.T) {
	plain := NewTable("名稱", "STATE", "GIT")
	plain.Add("專案", "HOT", "clean")
	plain.Add("alpha", "WARM", "⇡2 !1")
	var plainOut bytes.Buffer
	plain.Render(&plainOut)

	style := cliStyle{enabled: true}
	colored := &Table{head: []string{"名稱", "STATE", "GIT"}, style: style}
	colored.Add("專案", style.success("HOT"), style.success("clean"))
	colored.Add("alpha", style.warning("WARM"), style.warning("⇡2 !1"))
	var coloredOut bytes.Buffer
	colored.Render(&coloredOut)

	if got := stripANSI(coloredOut.String()); got != plainOut.String() {
		t.Fatalf("ANSI changed table layout:\nplain:\n%s\ncolored stripped:\n%s", plainOut.String(), got)
	}
}

func TestTruncatePreservesANSIAndDisplayWidth(t *testing.T) {
	style := cliStyle{enabled: true}
	got := truncate(style.warning("中文-alpha"), 6)
	if width(got) != 6 || stripANSI(got) != "中文-…" {
		t.Fatalf("truncate = %q (plain %q, width %d)", got, stripANSI(got), width(got))
	}
	if !strings.HasSuffix(got, ansiReset) {
		t.Fatalf("truncated styled text did not reset color: %q", got)
	}
}

func TestDoneWizardUsesSemanticColors(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out, Err: &out, colorMode: colorAlways}
	item := &task.Task{Name: "color task", Branch: "feat/color"}
	analysis := gitx.FinishAnalysis{
		Status:   gitx.Status{Changed: 2, Unstaged: 1, Untracked: 1},
		Relation: gitx.BranchRelation{BaseOnly: 1, BranchOnly: 2},
		Changes: []gitx.DirtyPath{
			{Path: "same.txt", Unstaged: true, BaseEquivalent: true},
			{Path: "unique.txt", Untracked: true},
		},
	}
	renderDonePreflight(app, item, "main", analysis)
	got := out.String()
	for _, want := range []string{
		ansiBold + ansiCyan + "Finish",
		ansiGreen + "1 match main",
		ansiYellow + "1 unique",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("done preflight missing semantic style %q:\n%s", want, got)
		}
	}

	out.Reset()
	app.In = strings.NewReader("NO\n")
	p := newPrompter(app)
	plan := donePlan{DirtyAction: doneDirtyDiscard, Integration: doneIntegrationPR, Analysis: analysis}
	if ok, err := confirmDonePlan(app, p, item, "main", plan); err != nil || ok {
		t.Fatalf("discard confirmation = %v, %v", ok, err)
	}
	got = out.String()
	if !strings.Contains(got, ansiBold+ansiRed+"discard all") ||
		!strings.Contains(got, ansiMagenta+"open a PR") ||
		!strings.Contains(got, ansiBold+ansiRed+"Type DROP") {
		t.Fatalf("done summary lacks discard/PR colors:\n%s", got)
	}
}

func TestPrompterColorAndPlainFallbackMatch(t *testing.T) {
	run := func(mode string) string {
		var out bytes.Buffer
		app := &App{In: strings.NewReader("\n"), Out: &out, colorMode: mode}
		p := newPrompter(app)
		if _, err := p.line("Task name", "demo"); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	colored := run(colorAlways)
	plain := run(colorNever)
	if !strings.Contains(colored, ansiBold+ansiCyan+"Task name") {
		t.Fatalf("prompt not colored: %q", colored)
	}
	if stripANSI(colored) != plain {
		t.Fatalf("color changed prompt text: colored=%q plain=%q", colored, plain)
	}
}
