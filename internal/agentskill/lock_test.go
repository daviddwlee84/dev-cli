package agentskill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectAndGlobalLockSchemasNormalizeMetadata(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "project", "skills-lock.json")
	writeLock(t, projectPath, 1, map[string]any{
		"demo": map[string]any{
			"source": "../catalog", "sourceType": "local", "skillPath": "skills/demo/SKILL.md",
			"computedHash": strings.Repeat("a", 64), "subagents": []string{"one"},
		},
	})
	project := readProjectLock(projectPath)
	entry := project.Entries["demo"]
	if entry.Source != filepath.Clean(filepath.Join(filepath.Dir(projectPath), "../catalog")) {
		t.Errorf("normalized local source = %q", entry.Source)
	}
	if entry.RecordedHash != strings.Repeat("a", 64) || entry.HashKind != "sha256-folder" || len(entry.Subagents) != 1 {
		t.Errorf("project metadata = %+v", entry)
	}

	for _, test := range []struct {
		name     string
		version  int
		fields   map[string]any
		wantHash string
		wantKind string
	}{
		{name: "v1", version: 1, fields: map[string]any{}, wantHash: "", wantKind: ""},
		{name: "v2 content", version: 2, fields: map[string]any{"contentHash": strings.Repeat("b", 64)}, wantHash: strings.Repeat("b", 64), wantKind: "sha256-skill-file"},
		{name: "v3 tree", version: 3, fields: map[string]any{"skillFolderHash": strings.Repeat("c", 40)}, wantHash: strings.Repeat("c", 40), wantKind: "git-tree"},
		{name: "v3 computed folder", version: 3, fields: map[string]any{"skillFolderHash": strings.Repeat("d", 64)}, wantHash: strings.Repeat("d", 64), wantKind: "sha256-folder"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".skill-lock.json")
			fields := map[string]any{"source": "owner/repo", "sourceType": "github", "sourceUrl": "https://example.test/repo.git", "skillPath": "skills/demo/SKILL.md"}
			for key, value := range test.fields {
				fields[key] = value
			}
			writeLock(t, path, test.version, map[string]any{"demo": fields})
			document := readGlobalLock(path)
			if len(document.Diagnostics) != 0 {
				t.Fatalf("diagnostics = %+v", document.Diagnostics)
			}
			got := document.Entries["demo"]
			if got.Version != test.version || got.RecordedHash != test.wantHash || got.HashKind != test.wantKind {
				t.Fatalf("metadata = %+v", got)
			}
		})
	}
}

func TestGlobalLockPathUsesOnlyAbsoluteXDGStateHome(t *testing.T) {
	home := isolateAgentEnvironment(t)
	absolute := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", absolute)
	if got, want := GlobalLockPath(), filepath.Join(absolute, "skills", ".skill-lock.json"); got != want {
		t.Fatalf("absolute XDG path = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "relative/state")
	if got, want := GlobalLockPath(), filepath.Join(home, ".agents", ".skill-lock.json"); got != want {
		t.Fatalf("relative XDG path = %q, want fallback %q", got, want)
	}
}

func TestLockFailuresAreDeterministicDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name  string
		make  func(*testing.T, string)
		kind  DiagnosticKind
		count int
	}{
		{name: "missing", make: func(*testing.T, string) {}, count: 0},
		{name: "malformed", make: func(t *testing.T, path string) { mustMkdir(t, filepath.Dir(path)); mustWrite(t, path, "{") }, kind: DiagnosticLockMalformed, count: 1},
		{name: "unreadable type", make: func(t *testing.T, path string) { mustMkdir(t, path) }, kind: DiagnosticLockUnreadable, count: 1},
		{name: "unsupported", make: func(t *testing.T, path string) { writeLock(t, path, 2, map[string]any{}) }, kind: DiagnosticLockUnsupported, count: 1},
		{name: "oversized", make: func(t *testing.T, path string) {
			mustMkdir(t, filepath.Dir(path))
			if err := os.WriteFile(path, make([]byte, maxLockBytes+1), 0o644); err != nil {
				t.Fatal(err)
			}
		}, kind: DiagnosticLockOversized, count: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "skills-lock.json")
			test.make(t, path)
			document := readProjectLock(path)
			if len(document.Diagnostics) != test.count {
				t.Fatalf("diagnostics = %+v", document.Diagnostics)
			}
			if test.count > 0 && document.Diagnostics[0].Kind != test.kind {
				t.Fatalf("kind = %q, want %q", document.Diagnostics[0].Kind, test.kind)
			}
		})
	}
}

func TestMalformedLockDoesNotAbortFilesystemRows(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "external"), "external", "")
	mustWrite(t, filepath.Join(repository, "skills-lock.json"), "not json")

	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 || result.Skills[0].ManagedBy != ManagedByExternal {
		t.Fatalf("filesystem rows = %+v", result.Skills)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Kind != DiagnosticLockMalformed {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestMalformedLockEntryDoesNotHideValidNeighbors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills-lock.json")
	mustMkdir(t, filepath.Dir(path))
	mustWrite(t, path, `{"version":1,"skills":{"bad":null,"good":{"source":"owner/repo","sourceType":"github","computedHash":"abc"}}}`)
	document := readProjectLock(path)
	if _, ok := document.Entries["bad"]; ok {
		t.Error("null lock entry was accepted")
	}
	if _, ok := document.Entries["good"]; !ok {
		t.Error("valid neighboring lock entry was lost")
	}
	if len(document.Diagnostics) != 1 || document.Diagnostics[0].Kind != DiagnosticLockMalformed {
		t.Fatalf("diagnostics = %+v", document.Diagnostics)
	}
}

func TestExactLockNameWinsNormalizedCollision(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeSkill(t, filepath.Join(repository, ".agents", "skills", "foo"), "foo-bar", "")
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"Foo Bar": map[string]any{"source": "wrong/repo", "sourceType": "github", "skillPath": "skills/foo/SKILL.md", "computedHash": "one"},
		"foo-bar": map[string]any{"source": "exact/repo", "sourceType": "github", "skillPath": "skills/foo/SKILL.md", "computedHash": "two"},
	})

	result, err := Inventory(context.Background(), repository, ListOptions{Project: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 1 {
		t.Fatalf("rows = %+v", result.Skills)
	}
	row := result.Skills[0]
	if row.Lock == nil || row.Lock.Name != "foo-bar" || row.Source != "exact/repo" || len(row.LockCandidates) != 2 {
		t.Fatalf("exact lock did not win: %+v", row)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Kind != DiagnosticNameCollision || !strings.Contains(result.Diagnostics[0].Message, "Foo Bar, foo-bar") {
		t.Fatalf("collision diagnostics = %+v", result.Diagnostics)
	}
	if !Managed(context.Background(), repository, "foo-bar", ScopeProject) {
		t.Error("exact lock should be managed")
	}
	if Managed(context.Background(), repository, "foo_bar", ScopeProject) {
		t.Error("ambiguous normalized lock should not be managed")
	}
}

func TestManagedNameReturnsCanonicalSafeLockKey(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"convex-best-practices": map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/convex/SKILL.md"},
		"--global":              map[string]any{"source": "owner/repo", "sourceType": "github", "skillPath": "skills/global/SKILL.md"},
	})
	if got, ok := ManagedName(context.Background(), repository, "Convex Best Practices", ScopeProject); !ok || got != "convex-best-practices" {
		t.Fatalf("canonical managed name = %q, %v", got, ok)
	}
	if got, ok := ManagedName(context.Background(), repository, "--global", ScopeProject); ok || got != "" {
		t.Fatalf("option-like managed name = %q, %v", got, ok)
	}
}

func TestManagedNameRejectsProviderUnupdateableLock(t *testing.T) {
	isolateAgentEnvironment(t)
	repository := initRepository(t)
	writeLock(t, filepath.Join(repository, "skills-lock.json"), 1, map[string]any{
		"local":  map[string]any{"source": "./local", "sourceType": "local", "skillPath": "SKILL.md"},
		"legacy": map[string]any{"source": "owner/repo", "sourceType": "github"},
	})
	for _, name := range []string{"local", "legacy"} {
		if got, ok := ManagedName(context.Background(), repository, name, ScopeProject); ok || got != "" {
			t.Errorf("unupdateable lock %q = %q, %v", name, got, ok)
		}
	}
}

func mustWrite(t *testing.T, filename, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(filename))
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
