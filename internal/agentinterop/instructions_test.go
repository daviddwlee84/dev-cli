package agentinterop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstructionsSymlinkImportAdoptionAndUndo(t *testing.T) {
	for _, style := range []string{"symlink", "import", "adopt"} {
		t.Run(style, func(t *testing.T) {
			s, root := testService(t)
			writeFixture(t, filepath.Join(root, "AGENTS.md"), "Shared instructions.\n")
			req := TransferRequest{Kind: "instructions", Mode: "mirror", Style: style, From: ArtifactRef{ScopeRef: ScopeRef{"project", root}}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}}}
			if style == "import" {
				writeFixture(t, filepath.Join(root, "CLAUDE.md"), "Claude-specific instructions.\n")
			}
			if style == "adopt" {
				req.Style = "symlink"
				req.Adopt = true
				writeFixture(t, filepath.Join(root, "CLAUDE.md"), "Shared instructions.\n")
			}
			p, err := s.Plan(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), p.ID); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
			if err != nil {
				t.Fatal(err)
			}
			want := "Shared instructions.\n"
			if style == "import" {
				want = "@AGENTS.md\n\nClaude-specific instructions.\n"
			}
			if string(data) != want {
				t.Fatal("instruction relationship differs")
			}
			u, err := s.Undo(context.Background(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), u.ID); err != nil {
				t.Fatal(err)
			}
			if style == "import" {
				data, _ = os.ReadFile(filepath.Join(root, "CLAUDE.md"))
				if string(data) != "Claude-specific instructions.\n" {
					t.Fatal("overlay lost")
				}
			}
		})
	}
}

func TestInstructionMoveRefusesDanglingCanonicalConsumers(t *testing.T) {
	s, root := testService(t)
	writeFixture(t, filepath.Join(root, "AGENTS.md"), "Shared\n")
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Skip(err)
	}
	req := TransferRequest{Kind: "instructions", Mode: "move", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Path: "AGENTS.md"}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Path: "new.md"}}
	if _, err := s.Plan(context.Background(), req); err == nil {
		t.Fatal("move would leave canonical consumer dangling")
	}
}
