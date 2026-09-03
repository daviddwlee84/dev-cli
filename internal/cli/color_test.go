package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/task"
	flow "github.com/daviddwlee84/dev-cli/internal/taskflow"
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
	plain := &Table{head: []string{"名稱", "STATE", "GIT"}}
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
	item := task.Task{Name: "color task", Branch: "feat/color"}
	request, err := flow.NewRequest(flow.Locator{Mode: task.ModeWorktree, State: task.Hot}, flow.ReviewHandoffOptions{
		Dirty: flow.DirtyDiscard,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := flow.BuildPlan(request, flow.PlanSpec{
		Authority: map[string]string{
			"completion.base-ref":             "main",
			"finish.base-only":                "1",
			"finish.branch-only":              "2",
			"finish.equivalent-dirty":         "1",
			"finish.unique-dirty":             "1",
			"finish.change-count":             "2",
			"finish.change.0.path":            "same.txt",
			"finish.change.0.base-equivalent": "true",
			"finish.change.1.path":            "unique.txt",
			"finish.change.1.base-equivalent": "false",
			"git.changed":                     "2",
			"git.unstaged":                    "1",
			"git.untracked":                   "1",
		},
		Effects: []flow.Effect{
			flow.NewEffect(flow.EffectDiscardAll, "discard changes", "checkout", true, false, nil),
			flow.NewEffect(flow.EffectPushBranch, "publish branch", "feat/color", false, true, nil),
		},
		Confirmation: flow.Confirmation{Kind: flow.ConfirmationTyped, Prompt: "Type DROP", Token: "DROP"},
	})
	if err != nil {
		t.Fatal(err)
	}
	renderDonePreflight(app, item, plan)
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
	if ok, _, err := confirmDonePlan(app, p, item, plan); err != nil || ok {
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

func TestStateColorizersSeparateHealthFromTrouble(t *testing.T) {
	style := cliStyle{enabled: true}
	for name, tc := range map[string]struct{ got, want string }{
		"host ok":            {style.hostState("ok"), style.success("ok")},
		"host stale":         {style.hostState("stale"), style.warning("stale")},
		"host unreachable":   {style.hostState("unreachable"), style.danger("unreachable")},
		"skill current":      {style.updateState("current"), style.success("current")},
		"skill update":       {style.updateState("update"), style.warning("update")},
		"skill failed":       {style.updateState("failed"), style.danger("failed")},
		"skill unchecked":    {style.updateState("unchecked"), style.dim("unchecked")},
		"intent finalized":   {style.artifactState("finalized"), style.success("finalized")},
		"intent armed":       {style.artifactState("armed"), style.warning("armed")},
		"intent discarded":   {style.artifactState("discarded"), style.dim("discarded")},
		"intent failed hard": {style.artifactState("failed"), style.danger("failed")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", name, tc.got, tc.want)
		}
	}
}

func TestStateColorizersAreInertWhenDisabled(t *testing.T) {
	style := cliStyle{}
	for _, got := range []string{
		style.hostState("unreachable"), style.updateState("update"), style.artifactState("armed"),
		style.strong("x"), style.code("y"),
	} {
		if strings.Contains(got, "\x1b[") {
			t.Errorf("disabled style emitted ANSI: %q", got)
		}
	}
}

func TestCobraHelpColorsNamesAndLeavesDescriptionsAlone(t *testing.T) {
	body := strings.Join([]string{
		"Available Commands:",
		"  create      Create a worktree at the configured path",
		"",
		"Flags:",
		"  -h, --help   help for wt",
		"      --json   emit a stable machine-readable JSON array",
		"",
	}, "\n")

	colored := renderCobraHelp(body, cliStyle{enabled: true})
	if stripANSI(colored) != body {
		t.Fatalf("coloring changed the plain text:\n%q\n%q", stripANSI(colored), body)
	}
	style := cliStyle{enabled: true}
	for _, want := range []string{style.code("create"), style.code("-h, --help"), style.code("--json")} {
		if !strings.Contains(colored, want) {
			t.Errorf("help did not color %q:\n%q", stripANSI(want), colored)
		}
	}
	// A description must stay plain, or the whole page reads as one color.
	if strings.Contains(colored, style.code("Create a worktree at the configured path")) {
		t.Error("help colored a description")
	}
	if plain := renderCobraHelp(body, cliStyle{}); plain != body {
		t.Error("disabled style rewrote the help body")
	}
}
