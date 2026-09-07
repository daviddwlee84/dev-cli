package agentinterop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setupProvider(t *testing.T, s Service, root, version, body string) TransferRequest {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell provider fixture")
	}
	source := filepath.Join(root, ".agents/skills/example")
	writeFixture(t, filepath.Join(source, "SKILL.md"), skillFixture)
	h := sha256.Sum256([]byte("SKILL.md" + skillFixture))
	entry := map[string]any{"source": "example/skills", "sourceUrl": "https://github.com/example/skills.git", "sourceType": "github", "ref": "v1", "skillPath": "skills/example/SKILL.md", "computedHash": hex.EncodeToString(h[:])}
	lock, _ := json.Marshal(map[string]any{"version": 1, "skills": map[string]any{"example": entry}})
	writeFixture(t, filepath.Join(root, "skills-lock.json"), string(lock))
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\n" + body
	if body == "" {
		script += "mkdir -p .agents/skills/example\ncat > .agents/skills/example/SKILL.md <<'SKILL_END'\n" + skillFixture + "SKILL_END\ncat > skills-lock.json <<'LOCK_END'\n" + string(lock) + "\nLOCK_END\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "skills"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := filepath.Join(filepath.Dir(root), "other")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	req := skillRequest(root, "install")
	req.To.Root = target
	return req
}

func TestProviderPreparationAndVerifiedInstall(t *testing.T) {
	s, root := testService(t)
	req := setupProvider(t, s, root, SkillsProviderVersion, "")
	ctx := context.Background()
	prep, err := s.Prepare(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Status != "prepared" {
		t.Fatal(prep)
	}
	if _, err = os.Stat(filepath.Join(req.To.Root, ".agents")); !os.IsNotExist(err) {
		t.Fatal("prepare changed target")
	}
	req.Prepared = prep.ID
	p, err := s.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".agents/skills/example/SKILL.md", ".claude/skills/example/SKILL.md", "skills-lock.json"} {
		if _, err := os.Stat(filepath.Join(req.To.Root, p)); err != nil {
			t.Fatal(err)
		}
	}
	// Applying does not require the executable and cannot start the provider.
	t.Setenv("PATH", t.TempDir())
	if _, err = s.Apply(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.StateDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".stage") {
			t.Fatal("staging directory leaked")
		}
	}
}

func TestProviderRejectsVersionFailureAndDrift(t *testing.T) {
	for _, test := range []struct{ name, version, body string }{
		{"version", "1.5.24", ""},
		{"failure", SkillsProviderVersion, "echo synthetic-secret-value >&2\nexit 1\n"},
		{"drift", SkillsProviderVersion, "mkdir -p .agents/skills/example\ncat > .agents/skills/example/SKILL.md <<'END'\n" + skillFixture + "changed\nEND\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, root := testService(t)
			req := setupProvider(t, s, root, test.version, test.body)
			_, err := s.Prepare(context.Background(), req)
			if err == nil {
				t.Fatal("accepted incompatible provider output")
			}
			if strings.Contains(err.Error(), "synthetic-secret-value") {
				t.Fatal("provider diagnostics leaked")
			}
			if entries, _ := os.ReadDir(req.To.Root); len(entries) != 0 {
				t.Fatal("failed preparation changed target")
			}
		})
	}
}

func TestLockSchemaAndCommitRefAreNotAssumedCompatible(t *testing.T) {
	if _, _, err := readLock([]byte(`{"version":2,"skills":{"example":{}}}`), "project", "example"); err == nil {
		t.Fatal("future schema accepted")
	}
	if _, _, err := readLock([]byte(`{"version":1,"skills":{"example":{"source":"x/y","sourceType":"github","futureField":true}}}`), "project", "example"); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := sourceArgument(skillLockEntry{Source: "x/y", SourceType: "github", Ref: strings.Repeat("a", 40)}); err == nil {
		t.Fatal("raw SHA treated as branch")
	}
}
