//go:build windows

package agentinterop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsTransferFailsBeforeStateOrAgentWrites(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "private-state")
	s := Service{StateDir: state}
	_, err := s.Plan(context.Background(), TransferRequest{Kind: "skill", Name: "example", Mode: "copy", From: ArtifactRef{ScopeRef: ScopeRef{"project", root}}, To: ArtifactRef{ScopeRef: ScopeRef{"project", root}, Agent: "claude-code"}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("unsupported operation created state")
	}
	if _, err = os.Stat(filepath.Join(root, ".claude")); !os.IsNotExist(err) {
		t.Fatal("unsupported operation changed agent files")
	}
}
