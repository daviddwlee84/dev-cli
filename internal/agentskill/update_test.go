package agentskill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckUpdatesGroupsSourcesBoundsWorkersPreservesOrderAndCleans(t *testing.T) {
	rows := []Skill{
		managedUpdateRow("first", "https://example.test/shared.git", "main"),
		managedUpdateRow("second", "https://example.test/other.git", ""),
		managedUpdateRow("third", "https://example.test/shared.git", "main"),
		{Name: "external", ManagedBy: ManagedByExternal, UpdateStatus: UpdateUnknown},
	}
	var active, maximum, cleanups atomic.Int32
	calls := map[string]int{}
	var callsMu sync.Mutex
	clone := func(_ context.Context, url, ref string) (sourceCheckout, error) {
		callsMu.Lock()
		calls[url+"\x00"+ref]++
		callsMu.Unlock()
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		return sourceCheckout{dir: url, cleanup: func() {
			active.Add(-1)
			cleanups.Add(1)
		}}, nil
	}
	check := func(_ context.Context, _ string, row Skill) (UpdateStatus, string) {
		time.Sleep(15 * time.Millisecond)
		return UpdateCurrent, "checked " + row.Name
	}

	got := checkUpdatesWith(context.Background(), rows, updateCheckDeps{clone: clone, check: check, workers: 2})
	if calls["https://example.test/shared.git\x00main"] != 1 || calls["https://example.test/other.git\x00"] != 1 || len(calls) != 2 {
		t.Fatalf("clone calls = %v", calls)
	}
	if maximum.Load() > 2 || maximum.Load() < 1 {
		t.Fatalf("maximum active clones = %d", maximum.Load())
	}
	if cleanups.Load() != 2 || active.Load() != 0 {
		t.Fatalf("cleanups = %d active = %d", cleanups.Load(), active.Load())
	}
	for index, want := range []string{"first", "second", "third", "external"} {
		if got[index].Name != want {
			t.Fatalf("row order changed: %+v", got)
		}
	}
	if got[0].UpdateStatus != UpdateCurrent || got[1].UpdateStatus != UpdateCurrent || got[2].UpdateStatus != UpdateCurrent || got[3].UpdateStatus != UpdateUnknown {
		t.Fatalf("statuses = %+v", got)
	}
}

func TestCheckUpdatesCancellationStopsQueuedGroupsAndStillCleans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows := []Skill{
		managedUpdateRow("a", "https://example.test/a.git", ""),
		managedUpdateRow("b", "https://example.test/b.git", ""),
		managedUpdateRow("c", "https://example.test/c.git", ""),
	}
	var clones, cleanups atomic.Int32
	clone := func(context.Context, string, string) (sourceCheckout, error) {
		clones.Add(1)
		cancel()
		return sourceCheckout{dir: "unused", cleanup: func() { cleanups.Add(1) }}, nil
	}

	got := checkUpdatesWith(ctx, rows, updateCheckDeps{
		clone: clone,
		check: func(context.Context, string, Skill) (UpdateStatus, string) {
			t.Fatal("check ran after cancellation")
			return UpdateUnknown, ""
		},
		workers: 1,
	})
	if clones.Load() != 1 || cleanups.Load() != 1 {
		t.Fatalf("clones = %d, cleanups = %d", clones.Load(), cleanups.Load())
	}
	for _, row := range got {
		if row.UpdateStatus != UpdateFailed || !strings.Contains(row.UpdateDetail, "canceled") {
			t.Fatalf("canceled row = %+v", row)
		}
	}
}

func TestCheckUpdatesDiscardsResultCanceledDuringCheckAndCleans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	row := managedUpdateRow("demo", "https://example.test/repo.git", "")
	cleaned := false
	got := checkUpdatesWith(ctx, []Skill{row}, updateCheckDeps{
		clone: func(context.Context, string, string) (sourceCheckout, error) {
			return sourceCheckout{dir: "unused", cleanup: func() { cleaned = true }}, nil
		},
		check: func(context.Context, string, Skill) (UpdateStatus, string) {
			cancel()
			return UpdateCurrent, "stale success"
		},
		workers: 1,
	})
	if !cleaned {
		t.Error("checkout was not cleaned after cancellation")
	}
	if got[0].UpdateStatus != UpdateFailed || !strings.Contains(got[0].UpdateDetail, "canceled") {
		t.Fatalf("canceled in-flight check = %+v", got[0])
	}
}

func TestFolderHashContextCancelsDuringStreaming(t *testing.T) {
	directory := t.TempDir()
	mustWrite(t, filepath.Join(directory, "large.bin"), strings.Repeat("x", 128<<10))
	ctx := &cancelAfterChecks{Context: context.Background(), after: 4}
	if _, err := folderHashContext(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("folderHashContext error = %v, want context.Canceled", err)
	}
}

func TestCheckUpdatesUsesRecordedHashWithoutMutatingInstall(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	clone := filepath.Join(t.TempDir(), "installed-source")
	mustRun(t, "", "git", "clone", "-q", remote, clone)
	skillDir := filepath.Join(clone, "skills", "demo")
	hash, err := folderHash(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(t.TempDir(), "installed")
	mustMkdir(t, installed)
	mustWrite(t, filepath.Join(installed, "SKILL.md"), "installed stays\n")
	lock := LockMetadata{Scope: ScopeProject, Source: remote, SourceType: "git", SkillPath: "skills/demo/SKILL.md", ComputedHash: hash}
	row := Skill{
		Name: "demo", Scope: ScopeProject, Path: installed,
		ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked, Lock: &lock,
	}

	got := CheckUpdates(context.Background(), []Skill{row})
	if got[0].UpdateStatus != UpdateCurrent {
		t.Fatalf("current status = %+v", got[0])
	}
	if body, _ := os.ReadFile(filepath.Join(installed, "SKILL.md")); string(body) != "installed stays\n" {
		t.Fatalf("installed skill was mutated: %q", body)
	}

	work := filepath.Join(t.TempDir(), "work")
	mustRun(t, "", "git", "clone", "-q", remote, work)
	mustWrite(t, filepath.Join(work, "skills", "demo", "SKILL.md"), "two\n")
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-qm", "update")
	mustRun(t, work, "git", "push", "-q", "origin", "HEAD")

	got = CheckUpdates(context.Background(), []Skill{row})
	if got[0].UpdateStatus != UpdateAvailable {
		t.Fatalf("changed status = %+v", got[0])
	}
}

func TestCheckUpdatesAcceptsMissingAndUnverifiableRows(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	gone := LockMetadata{Scope: ScopeProject, Source: remote, SourceType: "git", SkillPath: "skills/gone/SKILL.md", ComputedHash: strings.Repeat("a", 64)}
	local := LockMetadata{Scope: ScopeProject, Source: "./skill", SourceType: "local", SkillPath: "SKILL.md", ComputedHash: strings.Repeat("a", 64)}
	rows := []Skill{
		{Name: "gone", Presence: PresenceMissing, ManagedBy: ManagedBySkills, Lock: &gone},
		{Name: "local", Presence: PresencePresent, ManagedBy: ManagedBySkills, Lock: &local},
		{Name: "external", ManagedBy: ManagedByExternal, UpdateStatus: UpdateUnknown},
	}
	got := CheckUpdates(context.Background(), rows)
	if got[0].UpdateStatus != UpdateMissing || got[1].UpdateStatus != UpdateUnknown || got[2].UpdateStatus != UpdateUnknown {
		t.Fatalf("statuses = %+v", got)
	}
}

func TestCheckUpdatesRejectsGitOptionOperandsWithoutCloning(t *testing.T) {
	badURL := LockMetadata{SourceURL: "--upload-pack=/tmp/owned", SourceType: "git", SkillPath: "skills/demo/SKILL.md", ComputedHash: "hash"}
	badRef := LockMetadata{SourceURL: "https://example.test/repo.git", SourceType: "git", Ref: "--upload-pack=/tmp/owned", SkillPath: "skills/demo/SKILL.md", ComputedHash: "hash"}
	rows := []Skill{
		{Name: "bad-url", ManagedBy: ManagedBySkills, Lock: &badURL},
		{Name: "bad-ref", ManagedBy: ManagedBySkills, Lock: &badRef},
	}
	clones := 0
	got := checkUpdatesWith(context.Background(), rows, updateCheckDeps{
		clone: func(context.Context, string, string) (sourceCheckout, error) {
			clones++
			return sourceCheckout{}, nil
		},
	})
	if clones != 0 {
		t.Fatalf("invalid operands triggered %d clones", clones)
	}
	for _, row := range got {
		if row.UpdateStatus != UpdateUnknown {
			t.Fatalf("invalid operand row = %+v", row)
		}
	}
	if _, err := cloneSource(context.Background(), "-u/tmp/owned", ""); err == nil {
		t.Error("cloneSource accepted a leading-dash URL")
	}
	if _, err := cloneSource(context.Background(), "https://example.test/repo.git", "--upload-pack=/tmp/owned"); err == nil {
		t.Error("cloneSource accepted a leading-dash ref")
	}
}

func TestSafeSkillFolderRejectsTraversalAndAbsolutePaths(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
		ok   bool
	}{
		{"skills/demo/SKILL.md", "skills/demo", true},
		{"SKILL.md", "", true},
		{"../demo/SKILL.md", "", false},
		{"/tmp/demo/SKILL.md", "", false},
		{"C:\\tmp\\demo\\SKILL.md", "", false},
		{"", "", false},
	} {
		got, ok := safeSkillFolder(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("safeSkillFolder(%q) = %q, %v; want %q, %v", test.path, got, ok, test.want, test.ok)
		}
	}
}

type cancelAfterChecks struct {
	context.Context
	calls int32
	after int32
}

func (c *cancelAfterChecks) Err() error {
	if atomic.AddInt32(&c.calls, 1) > c.after {
		return context.Canceled
	}
	return nil
}

func managedUpdateRow(name, url, ref string) Skill {
	lock := &LockMetadata{Scope: ScopeProject, SourceURL: url, SourceType: "git", Ref: ref, SkillPath: "skills/" + name + "/SKILL.md", ComputedHash: strings.Repeat("a", 64)}
	return Skill{Name: name, ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked, Lock: lock}
}

func initSkillRepo(t *testing.T, body string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustRun(t, "", "git", "init", "-q", work)
	mustWrite(t, filepath.Join(work, "skills", "demo", "SKILL.md"), body)
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-qm", "initial")
	mustRun(t, "", "git", "init", "-q", "--bare", remote)
	mustRun(t, work, "git", "remote", "add", "origin", remote)
	mustRun(t, work, "git", "push", "-q", "-u", "origin", "HEAD")
	head := strings.TrimSpace(mustRun(t, work, "git", "branch", "--show-current"))
	mustRun(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/"+head)
	return remote
}
