package sshactivity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(filepath.Join(root, "state", "ssh", "activity"))
}

func TestReadIsPassiveAndUseSurvivesTests(t *testing.T) {
	s := testStore(t)
	record, err := s.Read(t.Context(), "profile")
	if err != nil || !record.LastUsed.IsZero() {
		t.Fatalf("%+v %v", record, err)
	}
	if _, err := os.Stat(s.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created directory", err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if _, err = s.RecordUse(t.Context(), "profile", "revision-one", at); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"network", "full"} {
		status := "network_ready"
		if mode == "full" {
			status = "ready"
		}
		record, err = s.RecordTest(t.Context(), "profile", TestRecord{ObservedAt: at.Add(time.Minute), Fingerprint: "revision-two", Mode: mode, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if !record.LastUsed.Equal(at) || record.Fingerprint != "revision-one" {
			t.Fatal("test changed usage", record)
		}
		if mode == "network" && !record.LastSuccess.IsZero() {
			t.Fatal("network observation became authentication")
		}
	}
	reloaded, err := NewStore(s.Dir).Read(t.Context(), "profile")
	if err != nil || reloaded.LastTest.Fingerprint != "revision-two" || reloaded.LastSuccessFingerprint != "revision-two" {
		t.Fatalf("%+v %v", reloaded, err)
	}
	path, _ := s.path("profile")
	for _, item := range []struct {
		path string
		dir  bool
	}{{s.Dir, true}, {path, false}} {
		info, err := os.Lstat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := privatefile.Check(item.path, info, item.dir); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentUpdatesDoNotLoseUseOrTest(t *testing.T) {
	for _, seeded := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "first-use"}[seeded], func(t *testing.T) {
			s := testStore(t)
			at := time.Now().UTC()
			if seeded {
				if _, err := s.RecordUse(t.Context(), "profile", "revision", at); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for i := 1; i <= 12; i++ {
				wg.Go(func() {
					_, err := s.RecordUse(t.Context(), "profile", "revision", at.Add(time.Duration(i)*time.Second))
					if err != nil {
						t.Error(err)
					}
				})
				wg.Go(func() {
					_, err := s.RecordTest(t.Context(), "profile", TestRecord{ObservedAt: at.Add(time.Duration(i) * time.Second), Fingerprint: "revision", Mode: "full", Status: "ready"})
					if err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			r, err := s.Read(t.Context(), "profile")
			if err != nil || !r.LastUsed.Equal(at.Add(12*time.Second)) || r.LastTest == nil || !r.LastTest.ObservedAt.Equal(at.Add(12*time.Second)) {
				t.Fatalf("lost update %+v %v", r, err)
			}
		})
	}
}

func TestActivityRejectsSymlinksAndPreservesOtherRecords(t *testing.T) {
	s := testStore(t)
	at := time.Now()
	if _, err := s.RecordUse(t.Context(), "safe", "revision", at); err != nil {
		t.Fatal(err)
	}
	path, _ := s.path("bad")
	target := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if _, err := s.RecordUse(t.Context(), "bad", "revision", at); err == nil {
		t.Fatal("followed symlink")
	}
	rows, err := s.ReadProfiles(t.Context(), []string{"bad", "safe"})
	if err == nil || rows["safe"].LastUsed.IsZero() {
		t.Fatal("lost independently valid record", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "untouched" {
		t.Fatal("foreign file changed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.RecordUse(ctx, "safe", "revision", at); err == nil {
		t.Fatal("canceled write succeeded")
	}
}

func TestUpdateWaitsForExistingLockWithoutReadingItsContents(t *testing.T) {
	s := testStore(t)
	at := time.Now().UTC()
	if _, err := s.RecordUse(t.Context(), "profile", "revision", at); err != nil {
		t.Fatal(err)
	}
	path, _ := s.path("profile")
	held, release := make(chan struct{}), make(chan struct{})
	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- lockx.WithFile(t.Context(), path+".lock", "fixture", func() error { close(held); <-release; return nil })
	}()
	select {
	case <-held:
	case err := <-ownerDone:
		t.Fatal("lock owner failed", err)
	case <-time.After(5 * time.Second):
		t.Fatal("lock owner did not start")
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	updateDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { _, err := s.RecordUse(ctx, "profile", "revision", at.Add(time.Second)); updateDone <- err }()
	select {
	case err := <-updateDone:
		t.Fatalf("update returned before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	released = true
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	record, err := s.Read(t.Context(), "profile")
	if err != nil || !record.LastUsed.Equal(at.Add(time.Second)) {
		t.Fatal(record, err)
	}
}

func TestUnsafeExistingLockCannotAuthorizeAnUpdate(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			at := time.Now().UTC()
			if _, err := s.RecordUse(t.Context(), "profile", "revision", at); err != nil {
				t.Fatal(err)
			}
			path, _ := s.path("profile")
			lockPath := path + ".lock"
			if kind == "hardlink" {
				if err := os.Link(lockPath, lockPath+".other"); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "foreign")
				if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, lockPath); err != nil {
					if runtime.GOOS == "windows" {
						t.Skip(err)
					}
					t.Fatal(err)
				}
			}
			if _, err := s.RecordUse(t.Context(), "profile", "revision", at.Add(time.Second)); err == nil {
				t.Fatal("unsafe lock accepted")
			}
			record, err := s.Read(t.Context(), "profile")
			if err != nil || !record.LastUsed.Equal(at) {
				t.Fatal("unsafe lock changed record", record, err)
			}
		})
	}
}
