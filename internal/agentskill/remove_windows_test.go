//go:build windows

package agentskill

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemovalThroughWindowsNPMShim(t *testing.T) {
	removalProvider(t)
	path, err := exec.LookPath("skills")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(filepath.Dir(path), "provider-helper.exe")
	if err = os.Rename(path, helper); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(filepath.Dir(path), "skills.cmd")
	if err = os.WriteFile(shim, []byte("@\""+helper+"\" %*\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := initRepository(t)
	row := managedFixture(t, root, "demo")
	ops := PrepareRemovals(t.Context(), []Skill{row}, removalAgents(row))
	if len(ops) != 1 || ops[0].Blocked != "" {
		t.Fatal(ops)
	}
	result := ApplyManagement(t.Context(), ops, nil)
	if result.Outcomes[0].Status != "completed" {
		t.Fatal(result)
	}
}

func TestWindowsShimRejectsShellExpansionBeforeExecution(t *testing.T) {
	for _, arg := range []string{"name&other", "%PATH%", "name!variable!", "x\ny"} {
		if command := providerCommand(context.Background(), `C:\tools\skills.cmd`, arg); command.Err == nil {
			t.Fatal("shell syntax accepted")
		}
	}
}
