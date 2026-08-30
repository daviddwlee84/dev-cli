package projectconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/projectconfig"
)

func writeProjectFile(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, projectconfig.DirectoryName, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func pointer[T any](value T) *T { return &value }

func TestLoadMissingFilesIsEmpty(t *testing.T) {
	root := t.TempDir()
	result, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigPresent || result.ScaffoldsPresent || result.RequiresTrust() {
		t.Fatalf("unexpected project config: %+v", result)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Paths.Root != canonicalRoot {
		t.Fatalf("root = %q, want %q", result.Paths.Root, canonicalRoot)
	}
}

func TestLoadRejectsMalformedFiles(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
		contents string
	}{
		{name: "config", filename: projectconfig.ConfigFilename, contents: "[worktree\nstrategy = nope"},
		{name: "scaffolds", filename: projectconfig.ScaffoldsFilename, contents: "[[presets]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFile(t, root, test.filename, test.contents)
			if _, err := projectconfig.Load(root, nil); err == nil {
				t.Fatal("malformed TOML should fail")
			} else if !strings.Contains(err.Error(), test.filename) {
				t.Fatalf("error should name %s: %v", test.filename, err)
			}
		})
	}
}

func TestLoadAppliesAllowlistAndReportsIgnoredSections(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, projectconfig.ConfigFilename, `
version = 1

[worktree]
include = []
link = [".cache"]
post_create = ["make bootstrap"]
strategy = "copy"
provision_timeout = "90s"
typo = "ignored"

[worktree.strategies]
node = "link"

[repo.setup]
preset = "agent-ready"
handoff = "open"
commit = false

[runtime]
backend = "tmux"

[mystery]
token = "must-not-be-reported"
`)
	writeProjectFile(t, root, projectconfig.ScaffoldsFilename, `
version = 1
default_preset = "agent-ready"

[presets.agent-ready]
description = "safe"

[paths]
state_dir = "/tmp/not-allowed"

[surprise]
value = true
`)

	result, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigPresent || !result.ScaffoldsPresent {
		t.Fatalf("presence = config %v scaffolds %v", result.ConfigPresent, result.ScaffoldsPresent)
	}
	if result.Effective.Worktree.Include == nil || len(*result.Effective.Worktree.Include) != 0 {
		t.Fatalf("explicit empty include list was not preserved: %+v", result.Effective.Worktree.Include)
	}
	if got := *result.Effective.Worktree.Strategy; got != "copy" {
		t.Fatalf("strategy = %q", got)
	}
	if got := (*result.Effective.Worktree.Strategies)["node"]; got != "link" {
		t.Fatalf("node strategy = %q", got)
	}
	if got := result.Effective.Worktree.ProvisionTimeout.Duration; got != 90*time.Second {
		t.Fatalf("timeout = %s", got)
	}
	if result.Effective.Repo.Setup.Commit == nil || *result.Effective.Repo.Setup.Commit {
		t.Fatalf("explicit false commit default was not preserved")
	}

	want := map[string]projectconfig.DiagnosticKind{
		"worktree.typo": projectconfig.DiagnosticUnknown,
		"runtime":       projectconfig.DiagnosticDenied,
		"mystery":       projectconfig.DiagnosticUnknown,
		"paths":         projectconfig.DiagnosticDenied,
		"surprise":      projectconfig.DiagnosticUnknown,
	}
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic.Message, "must-not-be-reported") || strings.Contains(diagnostic.Message, "/tmp/not-allowed") {
			t.Fatalf("diagnostic leaked a value: %+v", diagnostic)
		}
		if kind, ok := want[diagnostic.Key]; ok {
			if diagnostic.Kind != kind {
				t.Fatalf("diagnostic %s kind = %s, want %s", diagnostic.Key, diagnostic.Kind, kind)
			}
			delete(want, diagnostic.Key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing diagnostics: %v; got %+v", want, result.Diagnostics)
	}
	if _, ok := result.SourceFor("runtime.backend"); ok {
		t.Fatal("denied runtime setting acquired provenance")
	}
}

func TestLoadProjectConfigOverridesLegacyPerField(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, projectconfig.ConfigFilename, `
[worktree]
strategy = "copy"

[worktree.strategies]
node = "link"

[repo.setup]
handoff = "cd"
`)
	legacySource := filepath.Join(root, projectconfig.LegacySource)
	legacy := &projectconfig.Layer{
		Source: legacySource,
		Override: projectconfig.Override{
			Worktree: projectconfig.WorktreeOverride{
				Include:    pointer([]string{".env"}),
				PostCreate: pointer(config.PostCreate{Commands: []string{"make legacy"}}),
				Strategy:   pointer("reinstall"),
				Strategies: pointer(map[string]string{"python": "skip", "node": "copy"}),
			},
		},
	}

	result, err := projectconfig.Load(root, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got := (*result.Effective.Worktree.Include)[0]; got != ".env" {
		t.Fatalf("legacy include lost: %q", got)
	}
	if got := *result.Effective.Worktree.Strategy; got != "copy" {
		t.Fatalf("project strategy did not win: %q", got)
	}
	if got := (*result.Effective.Worktree.Strategies)["python"]; got != "skip" {
		t.Fatalf("legacy per-ecosystem value lost: %q", got)
	}
	if got := (*result.Effective.Worktree.Strategies)["node"]; got != "link" {
		t.Fatalf("project per-ecosystem value did not win: %q", got)
	}
	if got, _ := result.SourceFor("worktree.include"); got != legacySource {
		t.Fatalf("include source = %q", got)
	}
	projectPath, _ := projectconfig.ConfigPath(root)
	if got, _ := result.SourceFor("worktree.strategy"); got != projectPath {
		t.Fatalf("strategy source = %q, want %q", got, projectPath)
	}
	if len(result.Layers) != 2 || result.Layers[0] != legacySource || result.Layers[1] != projectPath {
		t.Fatalf("layers = %v", result.Layers)
	}
}

func TestResolvePathsCanonicalizesAliasesAndRejectsEscape(t *testing.T) {
	parent := t.TempDir()
	physical := filepath.Join(parent, "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	paths, err := projectconfig.ResolvePaths(alias)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPhysical, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Root != canonicalPhysical || paths.Config != filepath.Join(canonicalPhysical, projectconfig.ConfigRelativePath) {
		t.Fatalf("paths were not canonical: %+v", paths)
	}

	escapeRoot := filepath.Join(parent, "escape-root")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(escapeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(escapeRoot, projectconfig.DirectoryName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := projectconfig.ResolvePaths(escapeRoot); err == nil {
		t.Fatal("an escaping .dev-cli symlink should be rejected")
	}
}

func TestExecutionHashChangesOnlyWithExecutableProjectContent(t *testing.T) {
	root := t.TempDir()
	configPath := writeProjectFile(t, root, projectconfig.ConfigFilename, `
[worktree]
post_create = ["make one"]
[repo.setup]
preset = "one"
handoff = "stay"
`)
	scaffoldsPath := writeProjectFile(t, root, projectconfig.ScaffoldsFilename, `
version = 1
[presets.agent-ready]
description = "first"
[[presets.agent-ready.hooks]]
id = "setup"
command = ["tool", "one"]
`)

	first, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.RequiresTrust() || !strings.HasPrefix(first.ExecutionHash, "sha256:") {
		t.Fatalf("execution hash = %q", first.ExecutionHash)
	}
	if err := os.WriteFile(configPath, []byte(`
[worktree]
post_create = ["make one"]
[repo.setup]
preset = "one"
handoff = "cd"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scaffoldsPath, []byte(`
version = 1
[presets.agent-ready]
description = "second"
[[presets.agent-ready.hooks]]
id = "setup"
command = ["tool", "one"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	nonExecutableEdit, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nonExecutableEdit.ExecutionHash != first.ExecutionHash {
		t.Fatalf("non-executable edits changed hash: %s != %s", nonExecutableEdit.ExecutionHash, first.ExecutionHash)
	}
	if err := os.WriteFile(configPath, []byte(`
[worktree]
post_create = ["make one"]
[repo.setup]
preset = "two"
handoff = "cd"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	selectorEdit, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selectorEdit.ExecutionHash == first.ExecutionHash {
		t.Fatal("preset selection edit did not invalidate execution hash")
	}
	if err := os.WriteFile(configPath, []byte(`
[worktree]
post_create = ["make one"]
[repo.setup]
preset = "one"
handoff = "cd"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(scaffoldsPath, []byte(`
version = 1
[presets.agent-ready]
description = "second"
[[presets.agent-ready.hooks]]
id = "setup"
command = ["tool", "two"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	executableEdit, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if executableEdit.ExecutionHash == first.ExecutionHash {
		t.Fatal("hook command edit did not invalidate execution hash")
	}
}

func TestExecutionHashBindsProjectScaffoldSelectors(t *testing.T) {
	root := t.TempDir()
	path := writeProjectFile(t, root, projectconfig.ScaffoldsFilename, `
version = 1
default_preset = "global-one"
[presets.project]
extends = "global-one"
`)
	first, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.RequiresTrust() {
		t.Fatal("project-owned executable preset selectors did not require trust")
	}
	if err := os.WriteFile(path, []byte(`
version = 1
default_preset = "global-two"
[presets.project]
extends = "global-two"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ExecutionHash == first.ExecutionHash {
		t.Fatal("default_preset/extends edits did not invalidate trust")
	}
}

func TestExecutionHashBindsTemplateAndReferencedScriptContent(t *testing.T) {
	root := t.TempDir()
	template := writeProjectFile(t, root, filepath.Join("templates", "setup.sh"), "echo template-one\n")
	script := filepath.Join(root, "scripts", "existing.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo script-one\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, root, projectconfig.ScaffoldsFilename, `
version = 1
[presets.project]
[[presets.project.files]]
id = "setup-script"
source = "setup.sh"
destination = "generated/setup.sh"
[[presets.project.hooks]]
id = "generated"
phase = "before_commit"
command = ["sh", "generated/setup.sh"]
[[presets.project.hooks]]
id = "existing"
phase = "before_commit"
command = ["sh", "scripts/existing.sh"]
`)
	first, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(template, []byte("echo template-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	templateEdit, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if templateEdit.ExecutionHash == first.ExecutionHash {
		t.Fatal("template content edit did not invalidate trust")
	}
	if err := os.WriteFile(template, []byte("echo template-one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo script-two\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptEdit, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scriptEdit.ExecutionHash == first.ExecutionHash {
		t.Fatal("referenced local script edit did not invalidate trust")
	}
}

func TestExecutionHashBindsFileOnlyProjectOverride(t *testing.T) {
	root := t.TempDir()
	template := writeProjectFile(t, root, filepath.Join("templates", "inherited-hook.sh"), "echo one\n")
	writeProjectFile(t, root, projectconfig.ScaffoldsFilename, `
version = 1
[presets.global-exec]
[[presets.global-exec.files]]
id = "inherited-hook-script"
source = "inherited-hook.sh"
destination = "generated/inherited-hook.sh"
`)
	first, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.RequiresTrust() {
		t.Fatal("a project-owned generated file override must be hashable for inherited hooks")
	}
	if err := os.WriteFile(template, []byte("echo two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ExecutionHash == first.ExecutionHash {
		t.Fatal("file-only template edit did not invalidate trust")
	}
}

func TestExecutionHashBindsWorktreePostCreateScript(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "scripts", "post-create.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo one\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, root, projectconfig.ConfigFilename, `
version = 1
[worktree]
post_create = ["./scripts/post-create.sh"]
`)
	first, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo two\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExecutionHash == second.ExecutionHash {
		t.Fatal("worktree post_create script edit did not invalidate trust")
	}
}

func TestExecutionHashBindsLocalSetupSkillTree(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "skills", "demo", "scripts", "setup.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo one\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, root, projectconfig.ScaffoldsFilename, `
version = 1
[presets.project]
[[presets.project.skills]]
id = "demo"
source = "./skills/demo"
name = "demo"
default = true
[presets.project.skills.setup]
phase = "before_commit"
script = "scripts/setup.sh"
`)
	first, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("echo two\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := projectconfig.Load(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ExecutionHash == first.ExecutionHash {
		t.Fatal("local setup skill content edit did not invalidate trust")
	}
}

func TestLoadRejectsUnsafeValuesAndUnsupportedVersions(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
		contents string
	}{
		{name: "bad strategy", filename: projectconfig.ConfigFilename, contents: "[worktree]\nstrategy = \"teleport\"\n"},
		{name: "link traversal", filename: projectconfig.ConfigFilename, contents: "[worktree]\nlink = [\"../outside\"]\n"},
		{name: "bad handoff", filename: projectconfig.ConfigFilename, contents: "[repo.setup]\nhandoff = \"agent\"\n"},
		{name: "config version", filename: projectconfig.ConfigFilename, contents: "version = 2\n"},
		{name: "scaffold version", filename: projectconfig.ScaffoldsFilename, contents: "version = 2\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeProjectFile(t, root, test.filename, test.contents)
			if _, err := projectconfig.Load(root, nil); err == nil {
				t.Fatal("unsafe or unsupported project config should fail")
			}
		})
	}
}
