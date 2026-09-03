package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/ephemeral"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

type ephemeralSweepFixture struct {
	t        *testing.T
	repo     *gittest.Repo
	worktree string
	home     string
	state    string
	app      *App
	out      *bytes.Buffer
	errOut   *bytes.Buffer
}

func newEphemeralSweepFixture(t *testing.T, uniqueCommit bool) *ephemeralSweepFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	r := gittest.New(t)
	r.Write(".gitignore", ".claude/worktrees/\nignored.env\n")
	r.Git("add", ".gitignore")
	r.Git("commit", "-m", "test: ignore workflow worktrees")
	worktree := filepath.Join(r.Root, ".claude", "worktrees", "wf_run-1-agent-1")
	if err := gitx.AddWorktree(t.Context(), r.Root, worktree, "worktree-wf_run-1-agent-1", "main"); err != nil {
		t.Fatal(err)
	}
	if uniqueCommit {
		if err := os.WriteFile(filepath.Join(worktree, "unique.txt"), []byte("unique\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.GitIn(worktree, "add", "unique.txt")
		r.GitIn(worktree, "commit", "-m", "test: unique worktree commit")
	}
	writeWorkflowFixture(t, home, worktree, "completed", "done", true, false, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	state := filepath.Join(home, "state")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := &App{
		Cfg:   config.Config{Paths: config.Paths{StateDir: state}, Runtime: config.Runtime{Backend: "none"}},
		Tasks: task.NewStore(filepath.Join(state, "tasks")), In: strings.NewReader(""), Out: out, Err: errOut,
		interactiveCheck: func() bool { return false }, runtimeInstance: runtime.None{},
	}
	t.Chdir(r.Root)
	return &ephemeralSweepFixture{t: t, repo: r, worktree: worktree, home: home, state: state, app: app, out: out, errOut: errOut}
}

func (f *ephemeralSweepFixture) run(args ...string) (string, string, error) {
	f.t.Helper()
	f.out.Reset()
	f.errOut.Reset()
	command := newSweepCmd(f.app)
	command.SetArgs(args)
	err := command.Execute()
	return f.out.String(), f.errOut.String(), err
}

func TestSweepEphemeralJSONSchemaPrivacyAndReportImmutability(t *testing.T) {
	f := newEphemeralSweepFixture(t, true)
	beforeWorktrees := f.repo.Git("worktree", "list", "--porcelain")
	metadata := workflowFixtureFiles(t, f.home)
	beforeMetadata := snapshotFiles(t, metadata)
	out, errOut, err := f.run("--ephemeral-worktrees", "--json")
	if err != nil {
		t.Fatalf("report: %v\nstderr=%s", err, errOut)
	}
	var report ephemeral.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not pure report JSON: %v\n%s", err, out)
	}
	if report.SchemaVersion != 1 || report.Repository.Root != f.repo.Root || report.Repository.CommonDir == "" ||
		report.StaleDays != 14 || len(report.Candidates) != 1 || report.Candidates[0].Classification != ephemeral.Unknown {
		t.Fatalf("report = %+v", report)
	}
	candidate := report.Candidates[0]
	if candidate.Provider != "claude-workflow" || candidate.RunID != "wf_run-1" || candidate.AgentID != "agent-1" ||
		candidate.Git.Untracked != 0 || candidate.Git.Ignored != 0 ||
		ephemeralCheckStatus(candidate, ephemeral.CheckProviderIdentity) != ephemeral.Unknown || candidate.BranchDeletion.Requested ||
		candidate.PlannedActions[1].Kind != "retain-branch" {
		t.Fatalf("candidate = %+v", candidate)
	}
	for _, secret := range []string{"TOP SECRET PROMPT", "TOP SECRET SCRIPT", "TOP SECRET RESULT", "TOP SECRET TRANSCRIPT"} {
		if strings.Contains(out, secret) {
			t.Fatalf("JSON leaked %q: %s", secret, out)
		}
	}
	if after := f.repo.Git("worktree", "list", "--porcelain"); after != beforeWorktrees {
		t.Fatalf("report changed worktree inventory\nbefore=%s\nafter=%s", beforeWorktrees, after)
	}
	if after := snapshotFiles(t, metadata); !bytes.Equal(after, beforeMetadata) {
		t.Fatal("report changed provider metadata")
	}
	if _, err := os.Lstat(filepath.Join(f.repo.Root, ".git", "dev-ephemeral-cleanup-v1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report created cleanup lock state: %v", err)
	}
}

func TestSweepEphemeralFlagContracts(t *testing.T) {
	tests := map[string]struct {
		args      []string
		noRuntime bool
		want      string
	}{
		"mode conflict":              {[]string{"--ephemeral-worktrees", "--merged-worktrees"}, false, "mutually exclusive"},
		"age minimum":                {[]string{"--ephemeral-worktrees", "--stale-days", "0"}, false, "at least 1"},
		"json apply":                 {[]string{"--ephemeral-worktrees", "--json", "--apply"}, false, "report-only"},
		"yes rejected":               {[]string{"--ephemeral-worktrees", "--apply", "--yes"}, false, "does not accept --yes"},
		"close unknown rejected":     {[]string{"--ephemeral-worktrees", "--apply", "--close-unknown"}, false, "does not accept --close-unknown"},
		"assume runtime rejected":    {[]string{"--ephemeral-worktrees", "--apply", "--assume-no-runtime"}, false, "does not accept --assume-no-runtime"},
		"no runtime apply":           {[]string{"--ephemeral-worktrees", "--apply"}, true, "cannot apply with --no-runtime"},
		"delete needs apply":         {[]string{"--ephemeral-worktrees", "--delete-branches", "--base", "main"}, false, "requires --apply"},
		"delete needs explicit base": {[]string{"--ephemeral-worktrees", "--apply", "--delete-branches"}, false, "requires an explicit --base"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newEphemeralSweepFixture(t, false)
			f.app.noRuntime = tc.noRuntime
			_, _, err := f.run(tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(f.worktree); statErr != nil {
				t.Fatalf("rejected flags changed worktree: %v", statErr)
			}
		})
	}
}

func TestSweepEphemeralNoRuntimeReportStaysUnknown(t *testing.T) {
	f := newEphemeralSweepFixture(t, false)
	f.app.noRuntime = true
	out, _, err := f.run("--ephemeral-worktrees", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report ephemeral.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].Classification != ephemeral.Unknown ||
		report.Candidates[0].Safety.RuntimeKnown.Known {
		t.Fatalf("disabled runtime report = %+v", report)
	}
}

func TestSweepEphemeralRequiresCanonicalCheckout(t *testing.T) {
	f := newEphemeralSweepFixture(t, false)
	t.Chdir(f.worktree)
	_, _, err := f.run("--ephemeral-worktrees", "--json")
	if err == nil || !strings.Contains(err.Error(), "canonical checkout") {
		t.Fatalf("linked checkout error = %v", err)
	}
}

func TestSweepEphemeralApplyRequiresTTYAndUnknownIdentityNeverPrompts(t *testing.T) {
	t.Run("non tty", func(t *testing.T) {
		f := newEphemeralSweepFixture(t, true)
		_, _, err := f.run("--ephemeral-worktrees", "--apply")
		if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
			t.Fatalf("error = %v", err)
		}
		if _, err := os.Stat(f.worktree); err != nil {
			t.Fatalf("non-TTY apply changed worktree: %v", err)
		}
	})

	t.Run("identity unknown", func(t *testing.T) {
		f := newEphemeralSweepFixture(t, true)
		f.app.interactiveCheck = func() bool { return true }
		input := strings.NewReader("yes\n")
		f.app.In = input
		out, _, err := f.run("--ephemeral-worktrees", "--apply")
		if err != nil {
			t.Fatal(err)
		}
		if input.Len() != len("yes\n") {
			t.Fatal("unknown candidate consumed confirmation input")
		}
		if _, err := os.Stat(f.worktree); err != nil {
			t.Fatalf("unknown candidate was removed: %v", err)
		}
		if !strings.Contains(out, "No eligible worktree was approved") {
			t.Fatalf("apply output = %q", out)
		}
	})
}

func TestSweepEphemeralExplicitBaseCannotBypassUnknownProviderIdentity(t *testing.T) {
	f := newEphemeralSweepFixture(t, false)
	f.app.interactiveCheck = func() bool { return true }
	f.app.In = strings.NewReader("yes\n")
	out, _, err := f.run("--ephemeral-worktrees", "--apply", "--delete-branches", "--base", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.worktree); err != nil {
		t.Fatalf("unknown worktree was removed: %v", err)
	}
	if !gitx.BranchExists(t.Context(), f.repo.Root, "worktree-wf_run-1-agent-1") {
		t.Fatalf("unknown branch was deleted: %s", out)
	}
	if !strings.Contains(out, "No eligible worktree was approved") {
		t.Fatalf("apply output = %q", out)
	}
}

func TestSweepEphemeralStaleClaimCannotDeleteReplacementWorktree(t *testing.T) {
	f := newEphemeralSweepFixture(t, false)
	f.repo.Git("worktree", "remove", f.worktree)
	if err := gitx.AddWorktree(t.Context(), f.repo.Root, f.worktree, "ordinary-replacement", "main"); err != nil {
		t.Fatal(err)
	}
	out, _, err := f.run("--ephemeral-worktrees", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report ephemeral.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].Branch != "ordinary-replacement" ||
		report.Candidates[0].Classification != ephemeral.Unknown ||
		ephemeralCheckStatus(report.Candidates[0], ephemeral.CheckProviderIdentity) != ephemeral.Unknown {
		t.Fatalf("replacement report = %+v", report)
	}
	f.app.interactiveCheck = func() bool { return true }
	f.app.In = strings.NewReader("yes\n")
	if _, _, err := f.run("--ephemeral-worktrees", "--apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.worktree); err != nil {
		t.Fatalf("stale claim removed replacement worktree: %v", err)
	}
	if !gitx.BranchExists(t.Context(), f.repo.Root, "ordinary-replacement") {
		t.Fatal("stale claim deleted replacement branch")
	}
}

func TestSweepEphemeralSafetyClassifications(t *testing.T) {
	tests := map[string]struct {
		mutate func(*ephemeralSweepFixture)
		want   ephemeral.Classification
	}{
		"tracked dirty": {func(f *ephemeralSweepFixture) {
			if err := os.WriteFile(filepath.Join(f.worktree, "README.md"), []byte("dirty\n"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Blocked},
		"untracked": {func(f *ephemeralSweepFixture) {
			if err := os.WriteFile(filepath.Join(f.worktree, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Blocked},
		"ignored": {func(f *ephemeralSweepFixture) {
			if err := os.WriteFile(filepath.Join(f.worktree, "ignored.env"), []byte("secret\n"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Blocked},
		"operation": {func(f *ephemeralSweepFixture) {
			repository, err := gitx.Discover(f.t.Context(), f.worktree)
			if err != nil {
				f.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repository.GitDir, "MERGE_HEAD"), []byte(strings.Repeat("a", 40)+"\n"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Blocked},
		"task claim": {func(f *ephemeralSweepFixture) {
			record := &task.Task{ID: "repo__workflow", Repo: "repo", RepoPath: f.repo.Root, Branch: "worktree-wf_run-1-agent-1", Base: "main", WorktreePath: f.worktree, Mode: task.ModeWorktree, State: task.Warm}
			if err := f.app.Tasks.Save(record); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Blocked},
		"malformed task inventory": {func(f *ephemeralSweepFixture) {
			if err := os.MkdirAll(f.app.Tasks.Dir, 0o700); err != nil {
				f.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.app.Tasks.Dir, "broken.toml"), []byte("not = [valid"), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Unknown},
		"artifact intent": {func(f *ephemeralSweepFixture) {
			repository, err := gitx.Discover(f.t.Context(), f.worktree)
			if err != nil {
				f.t.Fatal(err)
			}
			store := artifactStore(f.app)
			intent := &artifact.Intent{RunID: "run-1", Provider: "claude", SessionID: "12345678-1234-1234-1234-123456789abc", RepoPath: f.repo.Root, GitCommonDir: repository.GitCommonDir, WorktreePath: f.worktree, Branch: "worktree-wf_run-1-agent-1", Base: "main", Head: f.repo.GitIn(f.worktree, "rev-parse", "HEAD")}
			if err := store.Create(f.t.Context(), intent); err != nil {
				f.t.Fatal(err)
			}
		}, ephemeral.Blocked},
		"runtime coverage": {func(f *ephemeralSweepFixture) {
			f.app.runtimeInstance = &sweepRuntime{sessions: []runtime.Session{{Handle: "live", Dirs: []string{f.worktree}}}}
		}, ephemeral.Blocked},
		"locked": {func(f *ephemeralSweepFixture) { f.repo.Git("worktree", "lock", f.worktree) }, ephemeral.Blocked},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newEphemeralSweepFixture(t, false)
			tc.mutate(f)
			out, _, err := f.run("--ephemeral-worktrees", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var report ephemeral.Report
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Candidates) != 1 || report.Candidates[0].Classification != tc.want {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestSweepEphemeralMissingAndOrphanRemainReportOnly(t *testing.T) {
	t.Run("missing prunable", func(t *testing.T) {
		f := newEphemeralSweepFixture(t, false)
		if err := os.RemoveAll(f.worktree); err != nil {
			t.Fatal(err)
		}
		out, _, err := f.run("--ephemeral-worktrees", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var report ephemeral.Report
		_ = json.Unmarshal([]byte(out), &report)
		if len(report.Candidates) != 1 || report.Candidates[0].Classification != ephemeral.NotApplicable {
			t.Fatalf("report = %+v", report)
		}
	})

	t.Run("unregistered orphan", func(t *testing.T) {
		f := newEphemeralSweepFixture(t, false)
		f.repo.Git("worktree", "remove", f.worktree)
		if err := os.MkdirAll(f.worktree, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.worktree, "orphan.txt"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, err := f.run("--ephemeral-worktrees", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var report ephemeral.Report
		_ = json.Unmarshal([]byte(out), &report)
		if len(report.Candidates) != 1 || report.Candidates[0].Classification != ephemeral.NotApplicable {
			t.Fatalf("report = %+v", report)
		}
		if content, err := os.ReadFile(filepath.Join(f.worktree, "orphan.txt")); err != nil || string(content) != "keep" {
			t.Fatalf("orphan changed: %q, %v", content, err)
		}
	})
}

type sweepRuntime struct {
	sessions []runtime.Session
}

func (*sweepRuntime) Name() string    { return "fixture" }
func (*sweepRuntime) Available() bool { return true }
func (*sweepRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	return runtime.OpenResult{}, nil
}
func (*sweepRuntime) Close(context.Context, string) error { return nil }
func (r *sweepRuntime) List(context.Context) ([]runtime.Session, error) {
	return append([]runtime.Session(nil), r.sessions...), nil
}
func (*sweepRuntime) Annotate(context.Context, string, map[string]string) error { return nil }

func writeWorkflowFixture(t *testing.T, home, worktree, workflowState, agentState string, result, resumed bool, at time.Time) {
	t.Helper()
	session := filepath.Join(home, ".claude", "projects", "project-1", "session-1")
	workflowDir := filepath.Join(session, "workflows")
	mappingDir := filepath.Join(session, "subagents", "workflows", "wf_run-1")
	for _, dir := range []string{workflowDir, mappingDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workflow := `{"runId":"wf_run-1","status":"` + workflowState + `","timestamp":"` +
		at.Add(3*time.Minute).Format(time.RFC3339) + `","startTime":` + strconv.FormatInt(at.UnixMilli(), 10) +
		`,"workflowProgress":[{"agentId":"agent-1","isolation":"worktree","state":"` + agentState +
		`","queuedAt":` + strconv.FormatInt(at.Add(-time.Minute).UnixMilli(), 10) + `,"startedAt":` +
		strconv.FormatInt(at.UnixMilli(), 10) + `,"lastProgressAt":` + strconv.FormatInt(at.Add(2*time.Minute).UnixMilli(), 10) +
		`}],"prompt":"TOP SECRET PROMPT"}`
	meta := `{"worktreePath":` + jsonQuote(worktree) + `,"spawnedWithWorktree":true,"script":"TOP SECRET SCRIPT"}`
	journal := `{"type":"started","agentId":"agent-1","key":"safe-link"}` + "\n"
	if result {
		journal += `{"type":"result","agentId":"agent-1","key":"safe-link","result":{"body":"TOP SECRET RESULT"}}` + "\n"
	}
	paths := map[string]string{
		filepath.Join(workflowDir, "wf_run-1.json"):          workflow,
		filepath.Join(mappingDir, "agent-agent-1.meta.json"): meta,
		filepath.Join(mappingDir, "journal.jsonl"):           journal,
	}
	if resumed {
		paths[filepath.Join(session, "subagents", "agent-agent-1.jsonl")] = "TOP SECRET TRANSCRIPT"
	}
	for path, content := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func workflowFixtureFiles(t *testing.T, home string) []string {
	t.Helper()
	root := filepath.Join(home, ".claude", "projects")
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func snapshotFiles(t *testing.T, paths []string) []byte {
	t.Helper()
	var snapshot bytes.Buffer
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.WriteString(path)
		snapshot.WriteByte(0)
		snapshot.Write(content)
		snapshot.WriteByte(0)
	}
	return snapshot.Bytes()
}

func ephemeralCheckStatus(candidate ephemeral.Candidate, id string) ephemeral.Classification {
	for _, check := range candidate.Checks {
		if check.ID == id {
			return check.Classification
		}
	}
	return ""
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
