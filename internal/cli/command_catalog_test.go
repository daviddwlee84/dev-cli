package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestCanonicalCommandCatalogAndShortcutIsolation(t *testing.T) {
	root := NewRootCommandWithIO(&bytes.Buffer{}, &bytes.Buffer{})
	var visible []string
	for _, cmd := range root.Commands() {
		if !cmd.Hidden {
			visible = append(visible, cmd.Name())
		}
	}
	want := strings.Fields("activity agent dotfile fleet git help pr repo self snippet ssh status summary triage tries tui work")
	if !reflect.DeepEqual(visible, want) {
		t.Fatalf("visible roots = %v, want %v", visible, want)
	}
	for _, shortcut := range root.Commands() {
		if shortcut.Annotations[shortcutTargetAnnotation] == "" {
			continue
		}
		canonical, err := resolveHelpCommand(root, []string{shortcut.Name()})
		if err != nil {
			t.Fatalf("resolve %s: %v", shortcut.Name(), err)
		}
		if canonical == shortcut || !shortcut.Hidden || canonical.Hidden {
			t.Fatalf("shortcut %s did not get an independent hidden route", shortcut.Name())
		}
		// The GitHub-only preset deliberately fixes flags that snippet exposes.
		if shortcut.Name() != "gist" {
			assertShortcutFlags(t, shortcut, canonical)
		}
	}
	legacy, _, err := root.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := root.Find([]string{"work", "start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Flags().Set("task", "legacy-only"); err != nil {
		t.Fatal(err)
	}
	if got, _ := canonical.Flags().GetString("task"); got != "" || canonical.Flags().Changed("task") {
		t.Fatalf("compatibility route leaked parsed task flag: %q", got)
	}
}

func assertShortcutFlags(t *testing.T, old, canonical *cobra.Command) {
	t.Helper()
	check := func(set *pflag.FlagSet) {
		set.VisitAll(func(flag *pflag.Flag) {
			other := canonical.Flags().Lookup(flag.Name)
			if other == nil {
				other = canonical.PersistentFlags().Lookup(flag.Name)
			}
			if other == nil || other.Value.Type() != flag.Value.Type() || other.DefValue != flag.DefValue || other.Shorthand != flag.Shorthand {
				t.Errorf("%s --%s differs from canonical %s", old.CommandPath(), flag.Name, canonical.CommandPath())
			}
		})
	}
	check(old.Flags())
	check(old.PersistentFlags())
	for _, child := range old.Commands() {
		for _, target := range canonical.Commands() {
			if child.Name() == target.Name() {
				assertShortcutFlags(t, child, target)
			}
		}
	}
}

func runCatalogHelp(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCommandWithIO(&out, &errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestCommandTreeDepthPathsAndInternalBoundaries(t *testing.T) {
	defaultTree, err := runCatalogHelp(t, "help", "--tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  work  ", "    start  ", "  agent  ", "    artifact  "} {
		if !strings.Contains(defaultTree, want) {
			t.Errorf("default tree missing %q: %s", want, defaultTree)
		}
	}
	for _, absent := range []string{"      prepare  ", "\n  start  Track", "__retire", "__complete", "no-help"} {
		if strings.Contains(defaultTree, absent) {
			t.Errorf("default tree exposed %q: %s", absent, defaultTree)
		}
	}
	full, err := runCatalogHelp(t, "help", "--tree", "--depth", "0")
	if err != nil || !strings.Contains(full, "      prepare  ") {
		t.Fatalf("unlimited tree omitted deep command: %v\n%s", err, full)
	}
	canonical, err := runCatalogHelp(t, "help", "agent", "artifact", "--tree", "--depth", "1")
	if err != nil || !strings.HasPrefix(canonical, "dev agent artifact  ") || !strings.Contains(canonical, "  prepare  ") {
		t.Fatalf("scoped tree: %v\n%s", err, canonical)
	}
	alias, err := runCatalogHelp(t, "help", "artifact", "--tree", "--depth", "1")
	if err != nil || alias != canonical {
		t.Fatalf("shortcut subtree differs: %v\n%s", err, alias)
	}
	for _, args := range [][]string{
		{"help", "--tree", "--depth", "-1"},
		{"help", "--depth", "1"},
		{"help", "__retire-coordinator", "--tree"},
		{"help", "agent", "missing", "--tree"},
	} {
		if out, err := runCatalogHelp(t, args...); err == nil || out != "" {
			t.Errorf("invalid tree request %v returned %q, %v", args, out, err)
		}
	}
}

func TestCommandAliasInventoryAndGeneratedReferenceAgree(t *testing.T) {
	out, err := runCatalogHelp(t, "help", "--aliases")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dev start -> dev work start", "dev list -> dev work list",
		"dev try -> dev tries try", "dev gitignore -> dev git ignore",
		"dev worktree -> dev git worktree", "dev gist -> dev snippet (GitHub only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("alias inventory missing %q", want)
		}
	}
	if strings.Contains(out, "__") || strings.Contains(out, "no-help") {
		t.Fatalf("alias inventory includes a protocol helper: %s", out)
	}
	root := NewRootCommandWithIO(&bytes.Buffer{}, &bytes.Buffer{})
	reference := generateCommandReference(root)
	for _, alias := range commandAliases(root) {
		if !strings.Contains(reference, "| `"+alias.from+"` | `"+alias.to+"` |") {
			t.Errorf("generated reference omitted %s", alias.from)
		}
	}
	if strings.Contains(reference, "### `dev start`") || !strings.Contains(reference, "### `dev work start`") {
		t.Fatal("generated command docs must prefer canonical paths")
	}
	scoped, err := runCatalogHelp(t, "help", "work", "--aliases")
	if err != nil || !strings.Contains(scoped, "dev start ->") || strings.Contains(scoped, "dev gist ->") {
		t.Fatalf("scoped aliases leaked another family: %v\n%s", err, scoped)
	}
}

func TestGitIgnoreCanonicalAllowsStandalonePreview(t *testing.T) {
	// Unlike Git transactions, listing bundled templates requires no repository.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	out, err := runCatalogHelp(t, "--no-runtime", "git", "ignore", "--list", "--offline")
	if err != nil || !strings.Contains(out, "Available without a network") {
		t.Fatalf("standalone git ignore --list: %v\n%s", err, out)
	}
}
