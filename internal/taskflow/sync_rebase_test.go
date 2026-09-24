package taskflow_test

import (
	"os"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/taskflow"
)

func TestSyncRebaseRefusesIgnoredFilesInReplayedTrees(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	r := gittest.New(t)
	r.WithRemote()
	r.Commit(".gitignore", "local-secret\n", "ignore")
	r.Git("push", "origin", "main")
	base := r.Git("rev-parse", "HEAD")
	r.Write("local-secret", "remote tracked version")
	r.Git("add", "-f", "local-secret")
	r.Git("commit", "-m", "remote adds ignored path")
	r.Git("push", "origin", "main")
	r.Git("reset", "--hard", base)
	r.Commit("local.txt", "local", "local change")
	r.Write("local-secret", "keep ignored bytes")
	s, p := syncPlan(t, r, taskflow.RebaseBranch)
	if p.Availability == taskflow.AvailabilityReady {
		t.Fatal("ignored collision plan ready")
	}
	found := false
	for _, c := range p.Conditions() {
		found = found || strings.Contains(c.Evidence, "overwrite ignored")
	}
	if !found {
		t.Fatalf("missing collision detail: %+v", p.Conditions())
	}
	if _, err := s.Apply(t.Context(), p, taskflow.Approve(p.PlanID)); err == nil {
		t.Fatal("ignored collision applied")
	}
	data, err := os.ReadFile(r.Root + "/local-secret")
	if err != nil || string(data) != "keep ignored bytes" {
		t.Fatal("ignored bytes lost")
	}
}
