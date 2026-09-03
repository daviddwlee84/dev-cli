package claudeworkflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/ephemeral"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

var fixtureNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func TestAdapterCompletedAndKilledClaims(t *testing.T) {
	for _, status := range []string{"completed", "killed"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			f.writeWorkflow(status, `,"unknown":{"prompt":"TOP SECRET PROMPT"}`)
			f.writeMeta("done", `,"newUpstreamField":[1,2,3]`)
			f.write(f.metaPath("agent-coordinator.meta.json"), `{"agentType":"coordinator","spawnDepth":1}`)
			f.writeJournal(true, `,"result":{"body":"TOP SECRET RESULT"}`)

			got := f.collect()
			if !got.Complete || len(got.Claims) != 1 {
				t.Fatalf("result = %+v", got)
			}
			claim := got.Claims[0]
			if claim.WorkflowState != status || !claim.WorkflowTerminal.Value || !claim.AgentDone.Value ||
				!claim.JournalStarted.Value || !claim.JournalResult.Value || !claim.NotResumed.Value ||
				claim.GitIdentity.Known || !claim.LastActivityKnown {
				t.Fatalf("claim = %+v", claim)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"TOP SECRET PROMPT", "TOP SECRET RESULT"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("adapter output leaked %q: %s", secret, encoded)
				}
			}
		})
	}
}

func TestAdapterStrictV1Liveness(t *testing.T) {
	tests := map[string]struct {
		workflow string
		agent    string
		result   bool
		resume   bool
		assert   func(*testing.T, ephemeral.Claim)
	}{
		"progress": {workflow: "progress", agent: "done", result: true, assert: func(t *testing.T, c ephemeral.Claim) {
			if c.WorkflowTerminal.Value || !c.WorkflowTerminal.Known {
				t.Fatalf("workflow terminal = %+v", c.WorkflowTerminal)
			}
		}},
		"no result": {workflow: "killed", agent: "done", result: false, assert: func(t *testing.T, c ephemeral.Claim) {
			if c.JournalResult.Value || !c.JournalResult.Known {
				t.Fatalf("journal result = %+v", c.JournalResult)
			}
		}},
		"agent not done": {workflow: "completed", agent: "progress", result: true, assert: func(t *testing.T, c ephemeral.Claim) {
			if c.AgentDone.Value || !c.AgentDone.Known {
				t.Fatalf("agent done = %+v", c.AgentDone)
			}
		}},
		"resumed": {workflow: "completed", agent: "done", result: true, resume: true, assert: func(t *testing.T, c ephemeral.Claim) {
			if c.NotResumed.Value || !c.NotResumed.Known {
				t.Fatalf("not resumed = %+v", c.NotResumed)
			}
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.agentState = tc.agent
			f.writeWorkflow(tc.workflow, "")
			f.writeMeta(tc.agent, "")
			f.writeJournal(tc.result, "")
			if tc.resume {
				f.writeResume("TOP SECRET TRANSCRIPT")
			}
			got := f.collect()
			if !got.Complete || len(got.Claims) != 1 {
				t.Fatalf("result = %+v", got)
			}
			tc.assert(t, got.Claims[0])
		})
	}
}

func TestAdapterJournalRequiresOneSharedOpaqueKey(t *testing.T) {
	t.Run("different result key", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournalKeys("started-key", "different-result-key")
		got := f.collect()
		if !got.Complete || len(got.Claims) != 1 {
			t.Fatalf("result = %+v", got)
		}
		claim := got.Claims[0]
		if !claim.JournalStarted.Known || !claim.JournalStarted.Value || !claim.JournalResult.Known || claim.JournalResult.Value {
			t.Fatalf("different-key result authorized linkage: %+v", claim)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		lines := []string{
			f.journalRecordWithoutKey("started", f.agentID, f.startedTime),
			f.journalRecordWithoutKey("result", f.agentID, f.resultTime),
		}
		f.write(filepath.Join(f.mappingDir(), "journal.jsonl"), strings.Join(lines, "\n")+"\n")
		got := f.collect()
		if got.Complete || !hasCode(got, "invalid-journal-key") {
			t.Fatalf("relevant records without key should fail closed: %+v", got)
		}
	})

	for name, rawKey := range map[string]string{"empty key": `""`, "non-string key": `7`} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.writeWorkflow("completed", "")
			f.writeMeta("done", "")
			lines := []string{
				f.journalRecordWithRawKey("started", f.agentID, rawKey, f.startedTime, ""),
				f.journalRecordWithRawKey("result", f.agentID, rawKey, f.resultTime, ""),
			}
			f.write(filepath.Join(f.mappingDir(), "journal.jsonl"), strings.Join(lines, "\n")+"\n")
			got := f.collect()
			if got.Complete || !hasCode(got, "invalid-journal-key") {
				t.Fatalf("invalid relevant key should fail closed: %+v", got)
			}
		})
	}

	t.Run("unknown event without key", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		path := filepath.Join(f.mappingDir(), "journal.jsonl")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"type":"future-event","payload":{"prompt":"TOP SECRET UNKNOWN EVENT"}}` + "\n"); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		got := f.collect()
		if !got.Complete || len(got.Claims) != 1 || !got.Claims[0].JournalResult.Value {
			t.Fatalf("unknown event kind should be add-only tolerant: %+v", got)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "TOP SECRET UNKNOWN EVENT") {
			t.Fatalf("unknown event payload leaked: %s", encoded)
		}
	})

	t.Run("key remains private", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournalKeys("TOP SECRET OPAQUE KEY", "TOP SECRET OPAQUE KEY")
		got := f.collect()
		if !got.Complete || len(got.Claims) != 1 || !got.Claims[0].JournalResult.Value {
			t.Fatalf("matching opaque key should link: %+v", got)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "TOP SECRET OPAQUE KEY") {
			t.Fatalf("opaque journal key leaked: %s", encoded)
		}
	})
}

func TestAdapterRejectsMalformedTypesButToleratesUnknownFields(t *testing.T) {
	f := newFixture(t)
	f.writeWorkflow("completed", `,"futureAddOnlyField":{"nested":true}`)
	f.writeMeta("done", `,"anotherUnknown":"accepted"`)
	f.writeJournal(true, `,"unknown":false`)
	if got := f.collect(); !got.Complete || len(got.Claims) != 1 {
		t.Fatalf("unknown fields should be tolerated: %+v", got)
	}

	f = newFixture(t)
	f.writeWorkflowRaw(`{"runId":"wf_run-1","status":7,"timestamp":"2026-08-01T00:03:00Z","workflowProgress":[]}`)
	f.writeMeta("done", "")
	f.writeJournal(true, "")
	got := f.collect()
	if got.Complete || len(got.Claims) != 0 || !hasCode(got, "invalid-workflow-status") {
		t.Fatalf("malformed required type should fail closed: %+v", got)
	}

	f = newFixture(t)
	f.writeWorkflowRaw(`{"runId":"wf_run-1","status":"completed","status":"killed","workflowProgress":[]}`)
	f.writeMeta("done", "")
	f.writeJournal(true, "")
	if got := f.collect(); got.Complete || !hasCode(got, "invalid-workflow") {
		t.Fatalf("duplicate JSON fields should fail closed: %+v", got)
	}
}

func TestAdapterRejectsDuplicateAndMismatchedClaims(t *testing.T) {
	f := newFixture(t)
	f.writeWorkflow("completed", "")
	f.writeMeta("done", "")
	f.writeMetaAs("agent-agent-2.meta.json", "agent-2", f.worktree, "wf_run-1", "wf_run-1", "done", "")
	f.writeJournalFor("agent-2", true, "")
	got := f.collect()
	if !got.Complete || len(got.Claims) != 2 || got.Claims[0].Unique.Value || got.Claims[1].Unique.Value ||
		!hasCode(got, "duplicate-worktree-claim") {
		t.Fatalf("duplicate path should be rejected explicitly: %+v", got)
	}

	f = newFixture(t)
	f.writeWorkflow("completed", "")
	f.writeMetaAs("agent-agent-1.meta.json", "different-agent", f.worktree, "wf_run-1", "wf_run-1", "done", "")
	f.writeJournal(true, "")
	got = f.collect()
	if !got.Complete || len(got.Claims) != 1 || got.Claims[0].Mapping.Value {
		t.Fatalf("filename/content mismatch should produce a blocked claim: %+v", got)
	}
}

func TestAdapterRejectsOneProviderIdentityMappedToMultiplePaths(t *testing.T) {
	result := ephemeral.SourceResult{Claims: []ephemeral.Claim{
		{Provider: "claude-workflow", SessionID: "session", RunID: "wf_run", AgentID: "agent", WorktreePath: "/one", Unique: ephemeral.KnownFact(true)},
		{Provider: "claude-workflow", SessionID: "session", RunID: "wf_run", AgentID: "agent", WorktreePath: "/two", Unique: ephemeral.KnownFact(true)},
	}}
	New("unused").rejectDuplicateClaims(&result)
	if result.Claims[0].Unique.Value || result.Claims[1].Unique.Value || !hasCode(result, "duplicate-provider-identity") {
		t.Fatalf("duplicate provider identity should be rejected: %+v", result)
	}
}

func TestAdapterIgnoresValidMappingsForOtherRepositories(t *testing.T) {
	f := newFixture(t)
	f.writeWorkflow("completed", "")
	f.writeMeta("done", "")
	f.writeMetaAs("agent-other.meta.json", "other", filepath.Join(f.t.TempDir(), "other-repo", ".claude", "worktrees", "other"), "wf_run-1", "wf_run-1", "done", "")
	f.writeJournal(true, "")
	got := f.collect()
	if !got.Complete || len(got.Claims) != 1 || got.Claims[0].WorktreePath == "" {
		t.Fatalf("unrelated valid mapping should be skipped: %+v", got)
	}
}

func TestAdapterRegisteredTargetCannotBypassProviderNamespace(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.t.TempDir(), "ordinary-worktree")
	f.writeWorkflow("completed", "")
	f.writeMetaAs("agent-agent-1.meta.json", "agent-1", outside, "wf_run-1", "wf_run-1", "done", "")
	f.writeJournal(true, "")
	query := f.query()
	query.Targets = []ephemeral.Target{{Path: outside, Branch: "ordinary", Registered: true, Hint: true}}
	got := New(f.root).Collect(context.Background(), query)
	if !got.Complete || len(got.Claims) != 0 {
		t.Fatalf("registered path outside provider namespace must not become a claim: %+v", got)
	}
}

func TestAdapterRejectsTraversalSymlinksAndPublicMetadata(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMetaAs("agent-agent-1.meta.json", "agent-1", filepath.Join(f.repo, ".claude", "worktrees", "..", "escape"), "wf_run-1", "wf_run-1", "done", "")
		f.writeJournal(true, "")
		got := f.collect()
		if got.Complete || !hasCode(got, "invalid-worktree-path") {
			t.Fatalf("traversal should fail closed: %+v", got)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("symlink", func(t *testing.T) {
			f := newFixture(t)
			f.writeWorkflow("completed", "")
			f.writeMeta("done", "")
			f.writeJournal(true, "")
			meta := f.metaPath("agent-agent-1.meta.json")
			target := meta + ".target"
			if err := os.Rename(meta, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, meta); err != nil {
				t.Fatal(err)
			}
			got := f.collect()
			if got.Complete || !hasCode(got, "unsafe-metadata") {
				t.Fatalf("symlink should fail closed: %+v", got)
			}
		})

		t.Run("group writable", func(t *testing.T) {
			f := newFixture(t)
			f.writeWorkflow("completed", "")
			f.writeMeta("done", "")
			f.writeJournal(true, "")
			if err := os.Chmod(f.metaPath("agent-agent-1.meta.json"), 0o660); err != nil {
				t.Fatal(err)
			}
			got := f.collect()
			if got.Complete || !hasCode(got, "unsafe-metadata") {
				t.Fatalf("group-writable file should fail closed: %+v", got)
			}
		})
	}
}

func TestAdapterEnforcesSizeCountAndMutationBounds(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflowRaw(strings.Repeat("x", 257))
		adapter := New(f.root, WithLimits(Limits{MaxProjects: 8, MaxSessions: 8, MaxWorkflows: 8, MaxAgents: 8, MaxJSONBytes: 256, MaxJournalBytes: 1024, MaxJournalRecords: 8}))
		got := adapter.Collect(context.Background(), f.query())
		if got.Complete || !hasCode(got, "metadata-bound") {
			t.Fatalf("oversized metadata should fail closed: %+v", got)
		}
	})

	t.Run("over count", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		adapter := New(f.root, WithLimits(Limits{MaxProjects: 8, MaxSessions: 8, MaxWorkflows: 8, MaxAgents: 0, MaxJSONBytes: 4096, MaxJournalBytes: 4096, MaxJournalRecords: 8}))
		got := adapter.Collect(context.Background(), f.query())
		if got.Complete || !hasCode(got, "metadata-bound") {
			t.Fatalf("over-count metadata should fail closed: %+v", got)
		}
	})

	t.Run("nonmatching workflow entry exhausts directory bound", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		f.write(filepath.Join(filepath.Dir(f.workflowPath()), "future.add-only"), "opaque")
		adapter := New(f.root, WithLimits(Limits{MaxProjects: 8, MaxSessions: 8, MaxWorkflows: 1, MaxAgents: 8, MaxJSONBytes: 4096, MaxJournalBytes: 4096, MaxJournalRecords: 8}))
		got := adapter.Collect(context.Background(), f.query())
		if got.Complete || !hasCode(got, "metadata-bound") {
			t.Fatalf("nonmatching workflow entry bypassed bound: %+v", got)
		}
	})

	t.Run("nonmatching mapping entry exhausts directory bound", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		f.write(filepath.Join(f.mappingDir(), "future.add-only"), "opaque")
		adapter := New(f.root, WithLimits(Limits{MaxProjects: 8, MaxSessions: 8, MaxWorkflows: 8, MaxAgents: 1, MaxJSONBytes: 4096, MaxJournalBytes: 4096, MaxJournalRecords: 8}))
		got := adapter.Collect(context.Background(), f.query())
		if got.Complete || !hasCode(got, "metadata-bound") {
			t.Fatalf("nonmatching mapping entry bypassed bound: %+v", got)
		}
	})

	t.Run("unknown files below directory bound remain tolerated", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		f.write(filepath.Join(filepath.Dir(f.workflowPath()), "future.add-only"), "opaque")
		f.write(filepath.Join(f.mappingDir(), "future.add-only"), "opaque")
		adapter := New(f.root, WithLimits(Limits{MaxProjects: 8, MaxSessions: 8, MaxWorkflows: 2, MaxAgents: 2, MaxJSONBytes: 4096, MaxJournalBytes: 4096, MaxJournalRecords: 8}))
		got := adapter.Collect(context.Background(), f.query())
		if !got.Complete || len(got.Claims) != 1 {
			t.Fatalf("bounded add-only files should be tolerated: %+v", got)
		}
	})

	t.Run("mutation", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		mutated := false
		adapter := New(f.root, withAfterRead(func(path string) {
			if mutated || path != f.metaPath("agent-agent-1.meta.json") {
				return
			}
			mutated = true
			_ = os.WriteFile(path, []byte(`{"changed":true}`), 0o600)
		}))
		got := adapter.Collect(context.Background(), f.query())
		if got.Complete || !hasCode(got, "metadata-mutated") {
			t.Fatalf("source mutation should fail closed: %+v", got)
		}
	})

	t.Run("directory mutation", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		mutated := false
		adapter := New(f.root, withAfterRead(func(path string) {
			if mutated || path != f.mappingDir() {
				return
			}
			mutated = true
			_ = os.WriteFile(filepath.Join(path, "appeared-during-read"), []byte("opaque"), 0o600)
		}))
		got := adapter.Collect(context.Background(), f.query())
		if got.Complete || !hasCode(got, "metadata-mutated") {
			t.Fatalf("directory mutation should fail closed: %+v", got)
		}
	})
}

func TestAdapterConservativeTimestamps(t *testing.T) {
	t.Run("future", func(t *testing.T) {
		f := newFixture(t)
		f.workflowTime = fixtureNow.Add(time.Minute)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		got := f.collect()
		if !got.Complete || len(got.Claims) != 1 || got.Claims[0].LastActivityKnown {
			t.Fatalf("future time should make activity unknown: %+v", got)
		}
	})

	t.Run("unparseable", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflowRaw(`{"runId":"wf_run-1","status":"completed","timestamp":7,"workflowProgress":[{"agentId":"agent-1","isolation":"worktree","state":"done"}]}`)
		f.writeMeta("done", "")
		f.writeJournal(true, "")
		got := f.collect()
		if !got.Complete || len(got.Claims) != 1 || got.Claims[0].LastActivityKnown {
			t.Fatalf("unparseable provider time should remain unknown: %+v", got)
		}
	})

	t.Run("conflicting order", func(t *testing.T) {
		f := newFixture(t)
		f.writeWorkflow("completed", "")
		f.writeMeta("done", "")
		f.writeJournalTimes(fixtureNow.Add(-time.Hour), fixtureNow.Add(-2*time.Hour), "")
		got := f.collect()
		if !got.Complete || len(got.Claims) != 1 || got.Claims[0].LastActivityKnown {
			t.Fatalf("result before start should make activity unknown: %+v", got)
		}
	})
}

func TestAdapterNeverReadsResumeTranscriptContent(t *testing.T) {
	f := newFixture(t)
	f.writeWorkflow("completed", "")
	f.writeMeta("done", "")
	f.writeJournal(true, "")
	transcript := f.resumePath()
	if err := os.WriteFile(transcript, []byte("TOP SECRET TRANSCRIPT"), 0); err != nil {
		t.Fatal(err)
	}
	// Existence and lstat must suffice; unreadable content cannot prevent resume
	// detection. On privileged Windows runners mode 000 is not meaningful, but
	// the same assertion still proves no decode is attempted.
	got := f.collect()
	if !got.Complete || len(got.Claims) != 1 || got.Claims[0].NotResumed.Value {
		t.Fatalf("resume transcript existence should be detected without reading it: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "TOP SECRET TRANSCRIPT") {
		t.Fatalf("transcript content leaked: %s", encoded)
	}
}

func TestRealMetadataReportOnlySmoke(t *testing.T) {
	if os.Getenv("DEV_REAL_CLAUDE_WORKFLOW_SMOKE") != "1" {
		t.Skip("set DEV_REAL_CLAUDE_WORKFLOW_SMOKE=1 for the bounded report-only host smoke")
	}
	repository, err := gitx.Discover(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	root, err := pathx.Canonical(repository.MainRoot)
	if err != nil {
		t.Fatal(err)
	}
	commonDir, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		t.Fatal(err)
	}
	worktrees, err := gitx.Worktrees(t.Context(), repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]ephemeral.Target, 0, len(worktrees))
	for _, worktree := range worktrees {
		path, canonicalErr := pathx.Canonical(worktree.Path)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		targets = append(targets, ephemeral.Target{
			Path: path, Branch: worktree.Branch, RegistryHead: worktree.Head,
			Main: worktree.Main, Bare: worktree.Bare, Detached: worktree.Detached,
			Locked: worktree.Locked, Prunable: worktree.Prunable, Registered: true,
			Hint: inventory.LooksEphemeralWorktree(path, worktree.Branch),
		})
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	metadataRoot := filepath.Join(home, ".claude", "projects")
	result := New(metadataRoot).Collect(t.Context(), ephemeral.SourceQuery{
		Repository: ephemeral.RepositoryIdentity{Root: root, CommonDir: commonDir, Name: repository.Name},
		Targets:    targets, Now: time.Now().UTC(),
	})
	codes := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	t.Logf("complete=%v claims=%d diagnostic_codes=%v", result.Complete, len(result.Claims), codes)
	if !result.Complete {
		t.Fatalf("bounded report-only metadata scan failed; diagnostic_codes=%v", codes)
	}
}

type fixture struct {
	t            *testing.T
	root         string
	repo         string
	worktree     string
	project      string
	session      string
	workflowID   string
	agentID      string
	agentState   string
	workflowTime time.Time
	metaTime     time.Time
	startedTime  time.Time
	resultTime   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, ".claude", "projects")
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(repo, ".claude", "worktrees", "wf_run-1-agent-1")
	for _, dir := range []string{root, repo, worktree} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &fixture{
		t: t, root: root, repo: repo, worktree: worktree, project: "project-1", session: "session-1",
		workflowID: "wf_run-1", agentID: "agent-1", agentState: "done",
		workflowTime: time.Date(2026, 8, 1, 0, 3, 0, 0, time.UTC),
		metaTime:     time.Date(2026, 8, 1, 0, 2, 0, 0, time.UTC),
		startedTime:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		resultTime:   time.Date(2026, 8, 1, 0, 1, 0, 0, time.UTC),
	}
}

func (f *fixture) query() ephemeral.SourceQuery {
	return ephemeral.SourceQuery{
		Repository: ephemeral.RepositoryIdentity{Root: f.repo, CommonDir: filepath.Join(f.repo, ".git"), Name: "repo"},
		Targets:    []ephemeral.Target{{Path: f.worktree, Branch: "worktree-wf_run-1-agent-1", Registered: true, Hint: true}},
		Now:        fixtureNow,
	}
}

func (f *fixture) collect() ephemeral.SourceResult {
	f.t.Helper()
	return New(f.root).Collect(context.Background(), f.query())
}

func (f *fixture) workflowPath() string {
	return filepath.Join(f.root, f.project, f.session, "workflows", f.workflowID+".json")
}

func (f *fixture) mappingDir() string {
	return filepath.Join(f.root, f.project, f.session, "subagents", "workflows", f.workflowID)
}

func (f *fixture) metaPath(name string) string { return filepath.Join(f.mappingDir(), name) }
func (f *fixture) resumePath() string {
	return filepath.Join(f.root, f.project, f.session, "subagents", "agent-"+f.agentID+".jsonl")
}

func (f *fixture) writeWorkflow(status, extra string) {
	progress := `[{"agentId":"` + f.agentID + `","isolation":"worktree","state":"` + f.agentState +
		`","queuedAt":` + fmtInt(f.startedTime.Add(-time.Minute).UnixMilli()) + `,"startedAt":` + fmtInt(f.startedTime.UnixMilli()) +
		`,"lastProgressAt":` + fmtInt(f.workflowTime.UnixMilli()) + `}]`
	f.writeWorkflowRaw(`{"runId":"` + f.workflowID + `","status":"` + status + `","timestamp":"` +
		f.workflowTime.Format(time.RFC3339Nano) + `","startTime":` + fmtInt(f.startedTime.UnixMilli()) +
		`,"workflowProgress":` + progress + extra + `}`)
}

func (f *fixture) writeWorkflowRaw(content string) {
	f.write(f.workflowPath(), content)
}

func (f *fixture) writeMeta(state, extra string) {
	f.writeMetaAs("agent-"+f.agentID+".meta.json", f.agentID, f.worktree, f.workflowID, f.workflowID, state, extra)
}

func (f *fixture) writeMetaAs(name, agentID, worktree, workflowID, runID, _ string, extra string) {
	content := `{"workflowId":"` + workflowID + `","runId":"` + runID + `","agentId":"` + agentID +
		`","worktreePath":` + quote(worktree) + `,"spawnedWithWorktree":true,"updatedAt":"` +
		f.metaTime.Format(time.RFC3339Nano) + `"` + extra + `}`
	f.write(f.metaPath(name), content)
}

func (f *fixture) writeJournal(result bool, extra string) {
	f.writeJournalFor(f.agentID, result, extra)
}

func (f *fixture) writeJournalFor(agentID string, result bool, extra string) {
	lines := []string{f.journalRecord("started", agentID, f.startedTime, extra)}
	if result {
		lines = append(lines, f.journalRecord("result", agentID, f.resultTime, extra))
	}
	f.write(filepath.Join(f.mappingDir(), "journal.jsonl"), strings.Join(lines, "\n")+"\n")
}

func (f *fixture) writeJournalTimes(started, result time.Time, extra string) {
	lines := []string{f.journalRecord("started", f.agentID, started, extra), f.journalRecord("result", f.agentID, result, extra)}
	f.write(filepath.Join(f.mappingDir(), "journal.jsonl"), strings.Join(lines, "\n")+"\n")
}

func (f *fixture) writeJournalKeys(startedKey, resultKey string) {
	lines := []string{
		f.journalRecordWithKey("started", f.agentID, startedKey, f.startedTime, ""),
		f.journalRecordWithKey("result", f.agentID, resultKey, f.resultTime, ""),
	}
	f.write(filepath.Join(f.mappingDir(), "journal.jsonl"), strings.Join(lines, "\n")+"\n")
}

func (f *fixture) journalRecord(kind, agentID string, at time.Time, extra string) string {
	return f.journalRecordWithKey(kind, agentID, "link-"+agentID, at, extra)
}

func (f *fixture) journalRecordWithKey(kind, agentID, key string, at time.Time, extra string) string {
	return f.journalRecordWithRawKey(kind, agentID, quote(key), at, extra)
}

func (f *fixture) journalRecordWithRawKey(kind, agentID, rawKey string, at time.Time, extra string) string {
	return `{"type":"` + kind + `","workflowId":"` + f.workflowID + `","runId":"` + f.workflowID +
		`","agentId":"` + agentID + `","key":` + rawKey + `,"timestamp":"` + at.Format(time.RFC3339Nano) + `"` + extra + `}`
}

func (f *fixture) journalRecordWithoutKey(kind, agentID string, at time.Time) string {
	return `{"type":"` + kind + `","workflowId":"` + f.workflowID + `","runId":"` + f.workflowID +
		`","agentId":"` + agentID + `","timestamp":"` + at.Format(time.RFC3339Nano) + `"}`
}

func (f *fixture) writeResume(content string) { f.write(f.resumePath(), content) }

func (f *fixture) write(path, content string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(path, f.resultTime, f.resultTime); err != nil {
		f.t.Fatal(err)
	}
}

func fmtInt(value int64) string { return strconv.FormatInt(value, 10) }

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func hasCode(result ephemeral.SourceResult, code string) bool {
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
