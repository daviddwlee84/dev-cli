package sshhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type panicRunner struct{}

func (panicRunner) Run(context.Context, RunRequest) (RunResult, error) {
	panic("static discovery invoked Runner")
}

func fixturePaths(t *testing.T) Paths {
	t.Helper()
	home := fixtureHome(t)
	paths, err := NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	makeFixturePrivateDirectory(t, paths.SSHDir)
	return paths
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	protectFixtureFile(t, path)
}

func newFixtureService(t *testing.T, paths Paths, options DiscoverOptions) *Service {
	t.Helper()
	service, err := NewService(paths, panicRunner{}, ServiceOptions{Discovery: options})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestDiscoverExpandsIncludesInInlineLexicalOrder(t *testing.T) {
	paths := fixturePaths(t)
	extra := filepath.Join(paths.SSHDir, "env.conf")
	writeFixture(t, paths.RootConfig, "# root\niNcLuDe \"conf.d/z*.conf\" conf.d/a.conf ${EXTRA} %d/.ssh/token.conf ~/.ssh/tilde.conf # tail\nHoSt root\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "conf.d", "z2.conf"), "Host z-two\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "conf.d", "z1.conf"), "hOsT z-one # comment\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "conf.d", "a.conf"), "Host alpha\n")
	writeFixture(t, extra, "Host env\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "token.conf"), "Host token\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "tilde.conf"), "Host tilde\n")

	service := newFixtureService(t, paths, DiscoverOptions{Environment: func(name string) (string, bool) {
		if name == "EXTRA" {
			return extra, true
		}
		return "", false
	}})
	inventory, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Complete {
		t.Fatalf("inventory unexpectedly incomplete: %#v", inventory.Diagnostics)
	}
	var order []string
	for _, file := range inventory.Files {
		order = append(order, filepath.Base(file.Path))
	}
	wantOrder := []string{"config", "z1.conf", "z2.conf", "a.conf", "env.conf", "token.conf", "tilde.conf"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("scan order = %v, want %v", order, wantOrder)
	}
	for _, name := range []string{"alpha", "env", "root", "tilde", "token", "z-one", "z-two"} {
		alias, ok := inventory.Find(name)
		if !ok {
			t.Errorf("missing alias %q", name)
			continue
		}
		if name != "root" {
			definition := alias.Definitions[0]
			if definition.Source.Line != 1 || len(definition.Provenance) != 1 || definition.Provenance[0].Source.Line != 2 {
				t.Errorf("%s provenance = %#v", name, definition)
			}
		}
	}
}

func TestDiscoverRelativeIncludesUseSSHDirectory(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include sub/one.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "sub", "one.conf"), "Include nested.conf\nHost one\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "nested.conf"), "Host nested\n")
	// This file would be selected if relative paths incorrectly used the source
	// file's directory.
	writeFixture(t, filepath.Join(paths.SSHDir, "sub", "nested.conf"), "Host wrong\n")

	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inventory.Find("nested"); !ok {
		t.Fatal("nested alias not found")
	}
	if _, ok := inventory.Find("wrong"); ok {
		t.Fatal("Include was resolved relative to the including file")
	}
	alias, _ := inventory.Find("nested")
	if got := len(alias.Definitions[0].Provenance); got != 2 {
		t.Fatalf("nested provenance depth = %d, want 2", got)
	}
}

func TestDiscoverCarriesGuardStateAcrossGlobMatches(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include parts/*.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "parts", "01-guard.conf"), "Host prod-*\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "parts", "02-include.conf"), "Include nested.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "nested.conf"), "Host prod-one\nHost dev-one\n")

	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inventory.Find("prod-one"); !ok {
		t.Fatal("guard-compatible alias from later glob match is missing")
	}
	if _, ok := inventory.Find("dev-one"); ok {
		t.Fatal("later glob match lost guard state from the earlier file")
	}
}

func TestDiscoverCyclesAndRepeatedEdgesAreBounded(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include a.conf a.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "a.conf"), "Include b.conf\nHost a\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "b.conf"), "Include a.conf\nHost b\n")

	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Complete {
		t.Fatal("cycle should make closure incomplete")
	}
	if len(inventory.Files) != 5 { // root, a, b, repeated a, repeated b
		t.Fatalf("visited %d files, want 5: %#v", len(inventory.Files), inventory.Files)
	}
	if !hasDiagnostic(inventory.Diagnostics, "include_cycle") {
		t.Fatalf("missing cycle diagnostic: %#v", inventory.Diagnostics)
	}
	repeated := false
	for _, edge := range inventory.Includes {
		repeated = repeated || edge.Repeated
	}
	if !repeated {
		t.Fatal("repeated non-cyclic edge was not marked")
	}
	for _, name := range []string{"a", "b"} {
		alias, ok := inventory.Find(name)
		if !ok || len(alias.Definitions) != 1 {
			t.Errorf("alias %s definitions = %#v, want one physical declaration", name, alias.Definitions)
		}
	}
}

func TestDiscoverDeduplicatesDefinitionsAcrossDistinctRepeatedEdges(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include shared.conf\nInclude shared.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "shared.conf"), "Host shared\n")

	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	alias, ok := inventory.Find("shared")
	if !ok || len(alias.Definitions) != 1 || alias.Conflict {
		t.Fatalf("repeated definition = %#v", alias)
	}
	if len(inventory.Files) != 3 {
		t.Fatalf("file visits = %#v, want root plus two visits", inventory.Files)
	}
	repeated := false
	for _, edge := range inventory.Includes {
		repeated = repeated || edge.Repeated
	}
	if !repeated {
		t.Fatal("second physical edge was not retained as repeated")
	}
}

func TestDiscoverGuardReachabilityAndDynamicMatch(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host prod-*\n  Include guarded.conf\nMatch exec \"never-run\"\n  Include dynamic.conf\nHost root\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "guarded.conf"), "Host prod-one\nHost dev-one\nHost *\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "dynamic.conf"), "Host maybe\n")

	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Complete || !hasDiagnostic(inventory.Diagnostics, "dynamic_match_include") {
		t.Fatalf("dynamic Match did not mark closure incomplete: %#v", inventory.Diagnostics)
	}
	if _, ok := inventory.Find("prod-one"); !ok {
		t.Fatal("reachable guarded alias missing")
	}
	if _, ok := inventory.Find("dev-one"); ok {
		t.Fatal("definitely unreachable alias became selectable")
	}
	if !hasDiagnostic(inventory.Diagnostics, "unreachable_alias") || !hasDiagnostic(inventory.Diagnostics, "wildcard_only_host") {
		t.Fatalf("missing reachability diagnostics: %#v", inventory.Diagnostics)
	}
	maybe, ok := inventory.Find("maybe")
	if !ok || maybe.Definitions[0].Reachability != Unknown {
		t.Fatalf("dynamic alias = %#v, want unknown reachability", maybe)
	}
	prod, _ := inventory.Find("prod-one")
	frame := prod.Definitions[0].Provenance[0]
	if len(frame.Guards) != 1 || frame.Guards[0].Kind != GuardHost || frame.Guards[0].Source.Line != 1 {
		t.Fatalf("guard provenance = %#v", frame)
	}
}

func TestDiscoverMatchHostDoesNotUseOriginalAlias(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host logical\n    HostName real.example\nMatch host real.example\n    Include guarded.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "guarded.conf"), "Host guarded\n")

	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	guarded, ok := inventory.Find("guarded")
	if !ok || len(guarded.Definitions) != 1 || guarded.Definitions[0].Reachability != Unknown {
		t.Fatalf("Match host guarded alias = %#v, want one unknown definition", guarded)
	}
	if inventory.Complete || !hasDiagnostic(inventory.Diagnostics, "dynamic_match_include") {
		t.Fatalf("Match host closure was treated as statically complete: %#v", inventory.Diagnostics)
	}
	guards := guarded.Definitions[0].Provenance[0].Guards
	if len(guards) != 1 || guards[0].Kind != GuardMatch || !guards[0].Dynamic {
		t.Fatalf("Match host provenance = %#v", guards)
	}
}

func TestDiscoverMalformedAndBoundedInputIsIncomplete(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include \"unterminated\nInclude ${MISSING}/x\nInclude %h/x\nInclude [bad\nHost "+strings.Repeat("x", 80)+"\n")
	service := newFixtureService(t, paths, DiscoverOptions{
		MaxLineBytes: 32,
		Environment:  func(string) (string, bool) { return "", false },
	})
	inventory, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Complete {
		t.Fatal("malformed inventory reported complete")
	}
	for _, code := range []string{"malformed_line", "include_expansion_failed", "include_glob_invalid", "line_size_exceeded"} {
		if !hasDiagnostic(inventory.Diagnostics, code) {
			t.Errorf("missing %s in %#v", code, inventory.Diagnostics)
		}
	}
}

func TestDiscoverFileLimitStopsRepeatedEdges(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include a.conf a.conf a.conf\n")
	writeFixture(t, filepath.Join(paths.SSHDir, "a.conf"), "Host a\n")
	inventory, err := newFixtureService(t, paths, DiscoverOptions{MaxFiles: 2}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Files) != 2 || !hasDiagnostic(inventory.Diagnostics, "file_limit_exceeded") {
		t.Fatalf("bounded inventory = %#v", inventory)
	}
}

func TestDiscoverRejectsSymlinkedIncludeSource(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Include linked.conf\n")
	target := filepath.Join(paths.SSHDir, "target.conf")
	writeFixture(t, target, "Host escaped\n")
	if err := os.Symlink(target, filepath.Join(paths.SSHDir, "linked.conf")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Complete || !hasDiagnostic(inventory.Diagnostics, "source_not_regular") {
		t.Fatalf("symlinked source inventory = %#v", inventory)
	}
	if _, ok := inventory.Find("escaped"); ok {
		t.Fatal("scanner followed a symlinked Include source")
	}
}

func TestDiscoverMissingRootIsCompleteAndEmpty(t *testing.T) {
	paths := fixturePaths(t)
	inventory, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.RootMissing || !inventory.Complete || len(inventory.Aliases) != 0 {
		t.Fatalf("missing-root inventory = %#v", inventory)
	}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestExpandEnvironmentRejectsMalformedNames(t *testing.T) {
	lookup := func(string) (string, bool) { return "value", true }
	for _, value := range []string{"${}", "${1BAD}", "${NO_END"} {
		if _, err := expandEnvironment(value, lookup); err == nil {
			t.Errorf("expandEnvironment(%q) succeeded", value)
		}
	}
}

func TestDiscoverHonorsCancellation(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host one\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newFixtureService(t, paths, DiscoverOptions{}).Discover(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover error = %v, want cancellation", err)
	}
}
