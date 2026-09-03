package lease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
)

func testRequest(id string) Request {
	return Request{OperationID: id, Digest: assessment.FingerprintBytes([]byte(id)), MachineID: "00000000-0000-4000-8000-000000000001"}
}

func TestCanonicalKeysSortsAndDeduplicates(t *testing.T) {
	repository := "github.com/team/project"
	branch := BranchKey(repository, "feature/z")
	repositoryKey := RepositoryKey(repository)
	other := BranchKey("github.com/another/project", "main")
	got, err := CanonicalKeys([]Key{branch, repositoryKey, branch, other})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]Key(nil), got...)
	for left := 0; left < len(want); left++ {
		for right := left + 1; right < len(want); right++ {
			if want[right].canonical() < want[left].canonical() {
				want[left], want[right] = want[right], want[left]
			}
		}
	}
	if len(got) != 3 || !reflect.DeepEqual(got, want) {
		t.Fatalf("CanonicalKeys = %+v, want three sorted keys", got)
	}
}

func TestFenceDrainsStartedMutationThenBlocksNewMutation(t *testing.T) {
	authority := New(t.TempDir())
	repository := "github.com/team/project"
	mutationKey := BranchKey(repository, "main")
	fenceKey := RepositoryKey(repository)
	entered := make(chan struct{})
	release := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- authority.WithMutation(context.Background(), []Key{mutationKey}, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := authority.Fence(waitCtx, []Key{fenceKey}, testRequest("transfer-drain")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Fence while mutation active = %v, want deadline", err)
	}
	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatalf("mutation: %v", err)
	}

	token, err := authority.Fence(context.Background(), []Key{fenceKey}, testRequest("transfer-drain"))
	if err != nil {
		t.Fatalf("Fence after drain: %v", err)
	}
	called := false
	if err := authority.WithMutation(context.Background(), []Key{mutationKey}, func() error { called = true; return nil }); !errors.Is(err, ErrBlocked) {
		t.Fatalf("guard after fence = %v, want blocked", err)
	}
	if called {
		t.Fatal("blocked mutation callback ran")
	}
	if err := authority.WithGuard(context.Background(), []Key{fenceKey}, &token, func() error { called = true; return nil }); err != nil {
		t.Fatalf("owner guard: %v", err)
	}
	if !called {
		t.Fatal("owner callback did not run")
	}
}

func TestRepositoryFenceDrainsTokenOwnedBranchMutation(t *testing.T) {
	authority := New(t.TempDir())
	repository := "github.com/team/project"
	branchKey := BranchKey(repository, "main")
	token, err := authority.Reserve(context.Background(), []Key{branchKey}, testRequest("branch-owner"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- authority.WithGuard(context.Background(), []Key{branchKey}, &token, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := authority.Fence(waitCtx, []Key{RepositoryKey(repository)}, testRequest("repository-fence")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repository Fence while branch owner mutates = %v, want deadline", err)
	}
	close(release)
	if err := <-guardDone; err != nil {
		t.Fatalf("owner guard: %v", err)
	}
	if _, err := authority.Fence(context.Background(), []Key{RepositoryKey(repository)}, testRequest("repository-fence")); !errors.Is(err, ErrBlocked) {
		t.Fatalf("repository Fence over active branch reservation = %v, want blocked", err)
	}
}

func TestReservationBlocksOrdinaryGuardAndStaleEpoch(t *testing.T) {
	authority := New(t.TempDir())
	key := BranchKey("github.com/team/project", "main")
	token, err := authority.Reserve(context.Background(), []Key{key}, testRequest("transfer-reserve"))
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.WithMutation(context.Background(), []Key{key}, func() error { return nil }); !errors.Is(err, ErrBlocked) {
		t.Fatalf("ordinary guard = %v, want blocked", err)
	}
	if err := authority.WithGuard(context.Background(), []Key{key}, &token, func() error { return nil }); err != nil {
		t.Fatalf("reservation owner guard: %v", err)
	}
	advanced, err := authority.ReleaseReservation(context.Background(), []Key{key}, token)
	if err != nil {
		t.Fatal(err)
	}
	if advanced <= token.Epoch {
		t.Fatalf("release epoch = %d, reservation epoch = %d", advanced, token.Epoch)
	}
	if err := authority.WithGuard(context.Background(), []Key{key}, &token, func() error { return nil }); !errors.Is(err, ErrEpochMismatch) {
		t.Fatalf("stale owner guard = %v, want epoch mismatch", err)
	}
	if err := authority.WithMutation(context.Background(), []Key{key}, func() error { return nil }); err != nil {
		t.Fatalf("ordinary guard after release: %v", err)
	}
	changedDigest := testRequest("transfer-reserve")
	changedDigest.Digest = assessment.FingerprintBytes([]byte("different-manifest"))
	if _, err := authority.Reserve(context.Background(), []Key{key}, changedDigest); !errors.Is(err, ErrConflict) {
		t.Fatalf("reuse released operation ID with another digest = %v, want conflict", err)
	}
}

func TestMutationGuardHonorsRepositoryAndBranchHierarchy(t *testing.T) {
	repository := "github.com/team/project"
	t.Run("repository fence blocks branch", func(t *testing.T) {
		authority := New(t.TempDir())
		if _, err := authority.Fence(context.Background(), []Key{RepositoryKey(repository)}, testRequest("repository-fence")); err != nil {
			t.Fatal(err)
		}
		if err := authority.WithMutation(context.Background(), []Key{BranchKey(repository, "main")}, func() error { return nil }); !errors.Is(err, ErrBlocked) {
			t.Fatalf("branch mutation under repository fence = %v, want blocked", err)
		}
		if _, err := authority.Fence(context.Background(), []Key{BranchKey(repository, "main")}, testRequest("branch-under-repository")); !errors.Is(err, ErrBlocked) {
			t.Fatalf("branch fence under repository fence = %v, want blocked", err)
		}
	})
	t.Run("branch fence permits other branch", func(t *testing.T) {
		authority := New(t.TempDir())
		if _, err := authority.Fence(context.Background(), []Key{BranchKey(repository, "feature/one")}, testRequest("branch-fence")); err != nil {
			t.Fatal(err)
		}
		if err := authority.WithMutation(context.Background(), []Key{BranchKey(repository, "feature/two")}, func() error { return nil }); err != nil {
			t.Fatalf("unrelated branch mutation = %v", err)
		}
		if _, err := authority.Fence(context.Background(), []Key{BranchKey(repository, "feature/two")}, testRequest("second-branch-fence")); err != nil {
			t.Fatalf("independent branch fence: %v", err)
		}
		if _, err := authority.Fence(context.Background(), []Key{RepositoryKey(repository)}, testRequest("repository-over-branches")); !errors.Is(err, ErrBlocked) {
			t.Fatalf("repository fence over branch fences = %v, want blocked", err)
		}
		if err := authority.WithMutation(context.Background(), []Key{RepositoryKey(repository)}, func() error { return nil }); !errors.Is(err, ErrBlocked) {
			t.Fatalf("repository mutation under branch fences = %v, want blocked", err)
		}
		if err := authority.WithMutation(context.Background(), []Key{BranchKey(repository, "feature/one")}, func() error { return nil }); !errors.Is(err, ErrBlocked) {
			t.Fatalf("same branch mutation = %v, want blocked", err)
		}
	})
}

func TestEpochPromotionAndReturnTombstoneAreIdempotent(t *testing.T) {
	root := t.TempDir()
	authority := New(root)
	repository := "github.com/team/project"
	keys := []Key{BranchKey(repository, "main"), RepositoryKey(repository)}
	request := testRequest("transfer-return")

	reservation, err := authority.Reserve(context.Background(), keys, request)
	if err != nil {
		t.Fatal(err)
	}
	repeatedReservation, err := authority.Reserve(context.Background(), []Key{keys[1], keys[0]}, request)
	if err != nil || !reservation.matches(repeatedReservation) {
		t.Fatalf("idempotent reservation = %+v, %v; want %+v", repeatedReservation, err, reservation)
	}
	fence, err := authority.Fence(context.Background(), keys, request)
	if err != nil {
		t.Fatal(err)
	}
	if fence.Epoch <= reservation.Epoch {
		t.Fatalf("fence epoch = %d, reservation epoch = %d", fence.Epoch, reservation.Epoch)
	}
	repeatedFence, err := authority.Fence(context.Background(), []Key{keys[1], keys[0]}, request)
	if err != nil || !fence.matches(repeatedFence) {
		t.Fatalf("idempotent fence = %+v, %v; want %+v", repeatedFence, err, fence)
	}
	guardCalled := false
	if err := authority.WithGuard(context.Background(), keys[:1], &fence, func() error { guardCalled = true; return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial owner guard = %v, want key-set conflict", err)
	}
	if guardCalled {
		t.Fatal("partial owner guard callback ran")
	}
	if _, err := authority.Return(context.Background(), keys[:1], fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial Return = %v, want key-set conflict", err)
	}
	recordsBeforeReturn, err := authority.Inspect(context.Background(), keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range recordsBeforeReturn {
		if record.Fence == nil || !record.Fence.Token.matches(fence) {
			t.Fatalf("partial Return changed record: %+v", record)
		}
	}

	returnedEpoch, err := authority.Return(context.Background(), keys, fence)
	if err != nil {
		t.Fatal(err)
	}
	if returnedEpoch <= fence.Epoch {
		t.Fatalf("returned epoch = %d, fence epoch = %d", returnedEpoch, fence.Epoch)
	}
	repeatedReturn, err := authority.Return(context.Background(), []Key{keys[1], keys[0]}, fence)
	if err != nil || repeatedReturn != returnedEpoch {
		t.Fatalf("idempotent Return = %d, %v; want %d", repeatedReturn, err, returnedEpoch)
	}
	if err := authority.WithGuard(context.Background(), keys, &fence, func() error { return nil }); !errors.Is(err, ErrReturned) {
		t.Fatalf("delayed owner guard = %v, want terminal", err)
	}
	if _, err := authority.Fence(context.Background(), keys, request); !errors.Is(err, ErrReturned) {
		t.Fatalf("delayed Fence = %v, want terminal", err)
	}
	if err := authority.WithMutation(context.Background(), keys, func() error { return nil }); err != nil {
		t.Fatalf("ordinary guard after return: %v", err)
	}

	reloaded := New(root)
	records, err := reloaded.Inspect(context.Background(), keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Fence != nil || record.Reservation != nil || len(record.Tombstones) != 1 {
			t.Fatalf("reloaded returned record = %+v", record)
		}
		if got := record.Tombstones[0]; got.Kind != TombstoneReturned || !got.Token.matches(fence) || got.AdvancedEpoch != returnedEpoch {
			t.Fatalf("reloaded tombstone = %+v", got)
		}
	}

	newFence, err := reloaded.Fence(context.Background(), keys, testRequest("transfer-new"))
	if err != nil {
		t.Fatal(err)
	}
	if newFence.Epoch <= returnedEpoch {
		t.Fatalf("new fence epoch = %d, returned epoch = %d", newFence.Epoch, returnedEpoch)
	}
}

func TestAbortReservationLeavesTerminalTombstone(t *testing.T) {
	authority := New(t.TempDir())
	key := RepositoryKey("github.com/team/project")
	request := testRequest("transfer-abort")
	token, err := authority.Reserve(context.Background(), []Key{key}, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AbortReservation(context.Background(), []Key{key}, token); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Reserve(context.Background(), []Key{key}, request); !errors.Is(err, ErrReturned) {
		t.Fatalf("delayed Reserve = %v, want terminal", err)
	}
}

func TestOppositeMultiKeyOrderSerializesWithoutDeadlock(t *testing.T) {
	authority := New(t.TempDir())
	first := RepositoryKey("github.com/a/project")
	second := RepositoryKey("github.com/b/project")
	entered := make(chan string, 2)
	release := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	for name, keys := range map[string][]Key{
		"forward": {first, second},
		"reverse": {second, first},
	} {
		go func(name string, keys []Key) {
			defer wait.Done()
			_ = authority.WithMutation(context.Background(), keys, func() error {
				entered <- name
				<-release
				return nil
			})
		}(name, keys)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("neither multi-key guard acquired")
	}
	select {
	case secondEntry := <-entered:
		t.Fatalf("both overlapping guards entered concurrently; second = %s", secondEntry)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	done := make(chan struct{})
	go func() { wait.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("opposite-order guards deadlocked")
	}
}

func TestAuthorityUsesPrivateModesAndStrictState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "authority")
	authority := New(root)
	key := RepositoryKey("github.com/team/project")
	if _, err := authority.Reserve(context.Background(), []Key{key}, testRequest("transfer-private")); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0o700 {
				t.Errorf("directory %s mode = %04o, want 0700", path, info.Mode().Perm())
			}
		} else if info.Mode().Perm() != 0o600 {
			t.Errorf("file %s mode = %04o, want 0600", path, info.Mode().Perm())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, stateFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 1, "unknown": true`, 1)
	if corrupt == string(data) {
		t.Fatal("test did not modify authority state")
	}
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Inspect(context.Background(), []Key{key}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Inspect corrupt state = %v", err)
	}
}

func TestAuthorityRejectsWrongPersistenceVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "authority")
	authority := New(root)
	key := RepositoryKey("github.com/team/project")
	if _, err := authority.Reserve(context.Background(), []Key{key}, testRequest("transfer-version")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, stateFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion := strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 2`, 1)
	if wrongVersion == string(data) {
		t.Fatal("test did not modify authority state")
	}
	if err := os.WriteFile(path, []byte(wrongVersion), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Inspect(context.Background(), []Key{key}); err == nil || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("Inspect wrong-version state = %v", err)
	}
}

func TestDefaultRootUsesPrivateXDGDataNotRepository(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	got := DefaultRoot()
	want := filepath.Join(data, "dev", "leases", "v1")
	if got != want {
		t.Fatalf("DefaultRoot = %q, want %q", got, want)
	}
}
