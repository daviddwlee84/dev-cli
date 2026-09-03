package agentskill

import (
	"context"
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
	rows[2].Lock.SkillPath = rows[0].Lock.SkillPath
	var active, maximum, cleanups, checks atomic.Int32
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
		checks.Add(1)
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
	if cleanups.Load() != 2 || active.Load() != 0 || checks.Load() != 2 {
		t.Fatalf("cleanups = %d active = %d checks = %d", cleanups.Load(), active.Load(), checks.Load())
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

func TestCheckUpdatesUsesRecordedHashWithoutMutatingInstall(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	clone := filepath.Join(t.TempDir(), "installed-source")
	mustRun(t, "", "git", "clone", "-q", remote, clone)
	skillDir := filepath.Join(clone, "skills", "demo")
	hash, err := folderHash(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	objectHash, err := gitFolderHashContext(context.Background(), remote, "skills/demo")
	if err != nil {
		t.Fatal(err)
	}
	if objectHash != hash {
		t.Fatalf("Git object hash = %s, filesystem hash = %s", objectHash, hash)
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

func TestFolderHashMatchesCheckedInUpstreamLocks(t *testing.T) {
	root := filepath.Join("..", "..")
	document := readProjectLock(filepath.Join(root, "skills-lock.json"))
	if len(document.Diagnostics) != 0 || len(document.Entries) == 0 {
		t.Fatalf("checked-in lock = %+v", document)
	}
	for name, entry := range document.Entries {
		got, err := folderHash(filepath.Join(root, ".agents", "skills", name))
		if err != nil {
			t.Fatal(err)
		}
		if got != entry.ComputedHash {
			t.Errorf("%s hash = %s, want upstream %s", name, got, entry.ComputedHash)
		}
	}
}

func TestCheckUpdatesTreatsMalformedRecordedHashAsUnverifiable(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	lock := &LockMetadata{
		Scope: ScopeProject, Source: remote, SourceType: "git",
		SkillPath: "skills/demo/SKILL.md", ComputedHash: "abc",
	}
	rows := CheckUpdates(context.Background(), []Skill{{
		Name: "demo", ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked, Lock: lock,
	}})
	if rows[0].UpdateStatus != UpdateUnknown {
		t.Fatalf("malformed hash status = %+v", rows[0])
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

func TestCloneSourceRejectsOptionLikeAndControlRefs(t *testing.T) {
	for _, ref := range []string{"--upload-pack=./evil", " main", "main\n--upload-pack=./evil"} {
		if _, err := cloneSource(context.Background(), ".", ref); err == nil {
			t.Errorf("cloneSource accepted unsafe ref %q", ref)
		}
	}
}

func TestCloneSourceFetchesSafeRefAfterOptionTerminator(t *testing.T) {
	remote := initSkillRepo(t, "one\n")
	dir, err := cloneSource(context.Background(), remote, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(dir)) })
	mustRun(t, dir, "git", "cat-file", "-e", "HEAD:skills/demo/SKILL.md")
	if _, err := os.Stat(filepath.Join(dir, "skills", "demo", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("source checkout unexpectedly populated a worktree: %v", err)
	}
}

func TestFileHashRejectsSymlinkTarget(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	mustWrite(t, outside, "local secret")
	link := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := fileHashContext(context.Background(), link); err == nil {
		t.Fatal("skill-file hash followed a symlink outside the fetched source")
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
