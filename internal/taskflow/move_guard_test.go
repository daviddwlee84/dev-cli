package taskflow

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func TestMoveClaimsRejectsOccupiedAndUnknownRuntime(t *testing.T) {
	for _, scenario := range []string{"empty", "none", "failed", "unknown-path", "session", "workspace-only", "workspace-with-other-pane", "unknown-pane", "idle-agent", "unknown-agent-path", "agent-error", "second-backend"} {
		t.Run(scenario, func(t *testing.T) {
			r := gittest.New(t)
			rt := newLifecycleFakeRuntime()
			g := MoveClaims{Tasks: task.NewStore(t.TempDir()), Artifacts: artifact.NewStore(t.TempDir()), Runtime: rt}
			switch scenario {
			case "none":
				g.Runtime = runtime.None{}
			case "failed":
				rt.listErr = errors.New("offline")
			case "unknown-path":
				rt.sessions = []runtime.Session{{Handle: "unknown"}}
			case "session":
				rt.sessions = []runtime.Session{{Handle: "occupied", Dirs: []string{r.Root}}}
			case "workspace-only":
				rt.sessions = []runtime.Session{{Handle: "occupied", WorkspaceCheckout: r.Root}}
			case "workspace-with-other-pane":
				rt.sessions = []runtime.Session{{Handle: "occupied", WorkspaceCheckout: r.Root, Panes: []runtime.Pane{{CWD: t.TempDir()}}}}
			case "unknown-pane":
				rt.sessions = []runtime.Session{{Handle: "partial", Panes: []runtime.Pane{{CWD: t.TempDir()}, {}}}}
			case "idle-agent":
				rt.agents = []runtime.AgentActivity{{CWD: r.Root, Agent: "codex", Status: "idle"}}
			case "unknown-agent-path":
				rt.agents = []runtime.AgentActivity{{Agent: "codex", Status: "idle"}}
			case "agent-error":
				rt.agentErr = errors.New("unavailable")
			case "second-backend":
				other := newLifecycleFakeRuntime()
				other.name = "other"
				other.sessions = []runtime.Session{{Handle: "occupied", Dirs: []string{r.Root}}}
				g.Runtimes = func() []runtime.Runtime { return []runtime.Runtime{rt, other} }
			}
			proof, err := g.Inspect(t.Context(), r.Root, filepath.Join(t.TempDir(), "destination"))
			if scenario == "empty" {
				if err != nil || proof == "" {
					t.Fatalf("empty observation: %q %v", proof, err)
				}
			} else if err == nil {
				t.Fatal("unproven checkout accepted")
			}
		})
	}
}

func TestMoveClaimsBindsTaskAndArtifactState(t *testing.T) {
	r := gittest.New(t)
	destination := filepath.Join(t.TempDir(), "try")
	g := MoveClaims{Tasks: task.NewStore(t.TempDir()), Artifacts: artifact.NewStore(t.TempDir()), Runtime: lifecycleObservedEmptyRuntime{}}
	before, err := g.Inspect(t.Context(), r.Root, destination)
	if err != nil {
		t.Fatal(err)
	}
	tracked := &task.Task{Name: "reserved", Repo: "repo", RepoPath: r.Root, Branch: "main", Base: "main", State: task.Cold, Mode: task.ModeDirect}
	if err := g.Tasks.Save(tracked); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Inspect(t.Context(), r.Root, destination); err == nil || !strings.Contains(err.Error(), "claimed") {
		t.Fatalf("task claim: %v", err)
	}
	if err := g.Tasks.Delete(tracked.ID); err != nil {
		t.Fatal(err)
	}
	intent := &artifact.Intent{ID: "moving", RunID: "moving", Provider: "codex", SessionID: "01a0438b-5d41-7e60-b11f-ef9f2ab4c7b2", RepoPath: r.Root, GitCommonDir: filepath.Join(r.Root, ".git"), WorktreePath: r.Root, Branch: "main", Head: r.Git("rev-parse", "HEAD")}
	if err := g.Artifacts.Create(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Inspect(t.Context(), r.Root, destination); err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("artifact claim: %v", err)
	}
	record, err := g.Artifacts.GetRecord(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Artifacts.UpdateIfRevision(t.Context(), intent.ID, record.Revision, func(i *artifact.Intent) error { i.Status = artifact.Discarded; return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := g.Inspect(t.Context(), r.Root, destination)
	if err != nil || before == after {
		t.Fatalf("changed authority was not bound: %v", err)
	}
}
