package agentinterop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const skillFixture = "---\nname: example\ndescription: Example fixture\n---\nInstructions.\n"

func TestSecretMaterialDistinguishesDetectionCodeFromKeyBodies(t *testing.T) {
	header := "-----BEGIN " + "PRIVATE KEY-----"
	footer := "-----END " + "PRIVATE KEY-----"
	if secretMaterial("detector.go", []byte("check(\""+header+"\")")) {
		t.Fatal("header detection code treated as a private key")
	}
	if !secretMaterial("material.txt", []byte(header+"\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"+footer)) {
		t.Fatal("key-shaped body was not rejected")
	}
	for _, name := range []string{".credentials.json", "application_default_credentials.json", "id_ecdsa", "key.ppk", "bundle.pfx"} {
		if !secretMaterial(name, []byte("fixture")) {
			t.Fatal("credential file not recognized", name)
		}
	}
}

func skillRequest(root string, mode string) TransferRequest {
	return TransferRequest{Kind: "skill", Mode: mode, Name: "example", From: ArtifactRef{ScopeRef: ScopeRef{Scope: "project", Root: root}, Agent: "universal"}, To: ArtifactRef{ScopeRef: ScopeRef{Scope: "project", Root: root}, Agent: "claude-code"}}
}

func TestSkillMirrorCopyMoveAndUndo(t *testing.T) {
	for _, mode := range []string{"mirror", "copy", "move"} {
		t.Run(mode, func(t *testing.T) {
			s, root := testService(t)
			source := filepath.Join(root, ".agents/skills/example/SKILL.md")
			writeFixture(t, source, skillFixture)
			req := skillRequest(root, mode)
			p, err := s.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), p.ID); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, ".claude/skills/example/SKILL.md")
			data, err := os.ReadFile(target)
			if err != nil || string(data) != skillFixture {
				t.Fatalf("target: %v", err)
			}
			if mode == "mirror" {
				link, err := os.Readlink(filepath.Dir(target))
				if err != nil || filepath.IsAbs(link) {
					t.Fatal("not a relative mirror")
				}
				writeFixture(t, source, skillFixture+"updated\n")
				data, _ = os.ReadFile(target)
				if string(data) != skillFixture+"updated\n" {
					t.Fatal("mirror did not share edits")
				}
				writeFixture(t, source, skillFixture)
			}
			if mode == "move" {
				if _, err = os.Stat(source); !os.IsNotExist(err) {
					t.Fatal("move retained source path")
				}
			}
			u, err := s.Undo(context.Background(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), u.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(source); err != nil {
				t.Fatal("undo lost canonical skill")
			}
		})
	}
}

func TestSkillRejectsForeignTreesSecretsAndEscapes(t *testing.T) {
	for _, fixture := range []string{"conflict", "secret", "escape", "cycle", "case"} {
		t.Run(fixture, func(t *testing.T) {
			s, root := testService(t)
			source := filepath.Join(root, ".agents/skills/example")
			writeFixture(t, filepath.Join(source, "SKILL.md"), skillFixture)
			switch fixture {
			case "conflict":
				writeFixture(t, filepath.Join(root, ".claude/skills/example/SKILL.md"), "foreign")
			case "secret":
				writeFixture(t, filepath.Join(source, ".env"), "TEST_TOKEN=synthetic")
			case "escape":
				if err := os.Symlink(t.TempDir(), filepath.Join(source, "outside")); err != nil {
					t.Skip(err)
				}
			case "cycle":
				if err := os.Symlink("cycle", filepath.Join(source, "cycle")); err != nil {
					t.Skip(err)
				}
			case "case":
				writeFixture(t, filepath.Join(source, "Foo.md"), "a")
				writeFixture(t, filepath.Join(source, "foo.md"), "b")
				entries, _ := os.ReadDir(source)
				if len(entries) < 3 {
					t.Skip("case-insensitive filesystem")
				}
			}
			_, err := s.Plan(context.Background(), skillRequest(root, "copy"))
			if err == nil {
				t.Fatal("unsafe transfer accepted")
			}
			if fixture == "secret" && !errors.Is(err, ErrCredentials) {
				t.Fatal(err)
			}
		})
	}
}
