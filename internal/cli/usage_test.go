package cli_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/cli"
)

// runCLI executes the tree and returns stdout, stderr and the error, so a test
// can assert on a failure path instead of only on a happy one.
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := cli.NewRootCommandWithIO(&out, &errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestUnknownCommandIsReportedInsteadOfSwallowed(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	_, _, err := runCLI(t, "--config", cfg, "definitelynotacommand")
	if err == nil {
		t.Fatal("an unknown command must fail; it used to exit 1 printing nothing at all")
	}
	if !strings.Contains(err.Error(), `unknown command "definitelynotacommand"`) {
		t.Errorf("error does not name the command: %v", err)
	}
	if !strings.Contains(err.Error(), "dev --help") {
		t.Errorf("error does not point anywhere useful: %v", err)
	}
}

func TestUnknownCommandKeepsCobrasSuggestions(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	_, _, err := runCLI(t, "--config", cfg, "statu")
	if err == nil {
		t.Fatal("expected a near-miss command name to fail")
	}
	// Cobra computes these and, with SilenceErrors set, used to discard them.
	if !strings.Contains(err.Error(), "Did you mean this?") || !strings.Contains(err.Error(), "status") {
		t.Errorf("suggestions were dropped again: %v", err)
	}
}

func TestUnknownSubcommandOfAFamilyFails(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	for _, family := range []string{"wt", "note", "fleet", "ssh", "tries", "skill", "artifact", "cache", "git", "repo", "stats", "config"} {
		t.Run(family, func(t *testing.T) {
			// A family has no Run of its own, so cobra answered a stray
			// argument with its help text and exit code 0, losing the typo.
			_, _, err := runCLI(t, "--config", cfg, family, "definitelynotasubcommand")
			if err == nil {
				t.Fatalf("dev %s definitelynotasubcommand succeeded; the argument was dropped", family)
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Errorf("unexpected error for dev %s: %v", family, err)
			}
			if !strings.Contains(err.Error(), "dev "+family) {
				t.Errorf("error does not name the family it belongs to: %v", err)
			}
		})
	}
}

func TestBareFamilyStillPrintsItsHelp(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	for _, family := range []string{"wt", "note", "fleet", "ssh", "tries"} {
		t.Run(family, func(t *testing.T) {
			out, _, err := runCLI(t, "--config", cfg, family)
			if err != nil {
				t.Fatalf("dev %s must still print help: %v", family, err)
			}
			if !strings.Contains(out, "Available Commands:") {
				t.Errorf("dev %s printed no command list:\n%s", family, out)
			}
		})
	}
}

func TestArgumentCountErrorSurvives(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	_, _, err := runCLI(t, "--config", cfg, "resume")
	if err == nil {
		t.Fatal("dev resume with no task must fail")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHelpResolvesCommandNamesToTopics(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	for command, wantHeading := range map[string]string{
		"wt":      "# Worktrees",
		"tries":   "# Tries and experiments",
		"skill":   "# Agent skills",
		"note":    "# Repository quick notes",
		"retire":  "# Agent-safe retirement",
		"fleet":   "# Remote fleet",
		"ssh":     "# SSH hosts",
		"prepare": "# Agent-safe retirement",
	} {
		t.Run(command, func(t *testing.T) {
			out, _, err := runCLI(t, "--config", cfg, "help", command)
			if err != nil {
				t.Fatalf("dev help %s: %v", command, err)
			}
			if !strings.Contains(out, wantHeading) {
				t.Errorf("dev help %s did not reach %q:\n%s", command, wantHeading, out)
			}
		})
	}
}

func TestFamilyHelpCarriesDiagramAndTopicPointer(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	for _, tc := range []struct{ command, marker, topic string }{
		{"wt", "TL;DR: the checkout is disposable", "dev help worktrees"},
		{"tries", "TL;DR: the experiment keeps its identity", "dev help tries"},
		{"note", "TL;DR: thoughts that outlive the checkout", "dev help notes"},
		{"fleet", "TL;DR: read other machines", "dev help fleet"},
		{"ssh", "TL;DR: OpenSSH stays the source of truth", "dev help ssh"},
		{"skill", "TL;DR: what the agents on this machine", "dev help skills"},
		{"retire", "TL;DR: integrate, exit, then clean up", "dev help retirement"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			out, _, err := runCLI(t, "--config", cfg, tc.command, "--help")
			if err != nil {
				t.Fatalf("dev %s --help: %v", tc.command, err)
			}
			if !strings.Contains(out, tc.marker) {
				t.Errorf("dev %s --help has no orientation diagram:\n%s", tc.command, out)
			}
			if !strings.Contains(out, tc.topic) {
				t.Errorf("dev %s --help never mentions %q", tc.command, tc.topic)
			}
			for _, b := range []byte(out[strings.Index(out, tc.marker):]) {
				if b >= 0x80 {
					t.Fatalf("dev %s diagram must be 7-bit ASCII, found byte %#x", tc.command, b)
				}
			}
		})
	}
}

func TestEveryAdvertisedHelpTopicResolves(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing.toml")
	index, _, err := runCLI(t, "--config", cfg, "help")
	if err != nil {
		t.Fatalf("dev help: %v", err)
	}
	// Anything a command's help points at must be a page that actually exists,
	// or the cross-reference is worse than none.
	for _, topic := range []string{
		"worktrees", "notes", "fleet", "journal", "summary", "retirement",
		"bootstrap", "adopting", "parking", "storage", "tui", "branching",
		"tries", "skills", "git-status", "repositories", "ssh",
	} {
		if !strings.Contains(index, topic) {
			t.Errorf("topic %q is advertised by a command but missing from the index", topic)
		}
		if _, _, err := runCLI(t, "--config", cfg, "help", topic); err != nil {
			t.Errorf("dev help %s: %v", topic, err)
		}
	}
}
