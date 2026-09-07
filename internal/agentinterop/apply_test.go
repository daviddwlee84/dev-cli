package agentinterop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func testService(t *testing.T) (Service, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native transfer mutation requires a verified private Windows ACL adapter")
	}
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(base, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	return Service{StateDir: filepath.Join(base, "private", "interop")}, root
}
func testPlan(t *testing.T, s Service, fn func(*builder) error) Plan {
	t.Helper()
	st, err := s.open(true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	b := newBuilder(context.Background(), st, TransferRequest{Kind: "test", Mode: "copy"})
	defer b.close()
	if err = fn(b); err != nil {
		t.Fatal(err)
	}
	p, err := b.save()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func writeFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGuardedApplyUndoAndPrivateReports(t *testing.T) {
	s, root := testService(t)
	ctx := context.Background()
	target := filepath.Join(root, "settings.json")
	secret := "synthetic-private-value"
	writeFixture(t, target, secret)
	p := testPlan(t, s, func(b *builder) error {
		return b.write(Location{root, "settings.json"}, []byte("new settings"), 0o644, true)
	})
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("public plan contains payload")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Apply(ctx, p.ID); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.Undo(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != secret {
		t.Fatal("undo did not restore private preimage")
	}
	entries, _ := os.ReadDir(s.StateDir)
	for _, e := range entries {
		info, _ := e.Info()
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatal("public recovery file")
		}
		if strings.HasSuffix(e.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(s.StateDir, e.Name()))
			if strings.Contains(string(data), secret) {
				t.Fatal("payload serialized in operation record")
			}
		}
	}
}

func TestStaleTransferHasNoNewEffect(t *testing.T) {
	for _, change := range []string{"source", "destination", "parent", "root"} {
		t.Run(change, func(t *testing.T) {
			s, root := testService(t)
			writeFixture(t, filepath.Join(root, "source"), "a")
			if err := os.Mkdir(filepath.Join(root, "out"), 0o755); err != nil {
				t.Fatal(err)
			}
			p := testPlan(t, s, func(b *builder) error {
				_, data, err := b.read(Location{root, "source"})
				if err != nil {
					return err
				}
				return b.write(Location{root, "out/dest"}, data, 0o644, false)
			})
			switch change {
			case "source":
				writeFixture(t, filepath.Join(root, "source"), "b")
			case "destination":
				writeFixture(t, filepath.Join(root, "out/dest"), "foreign")
			case "parent":
				if err := os.Rename(filepath.Join(root, "out"), filepath.Join(root, "old")); err != nil {
					t.Fatal(err)
				}
				_ = os.Mkdir(filepath.Join(root, "out"), 0o755)
			case "root":
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				_ = os.MkdirAll(filepath.Join(root, "out"), 0o755)
				writeFixture(t, filepath.Join(root, "source"), "a")
			}
			r, err := s.Apply(context.Background(), p.ID)
			if !errors.Is(err, ErrStale) || r.Completed != 0 {
				t.Fatalf("stale result %+v: %v", r, err)
			}
			if change != "destination" {
				if _, err := os.Stat(filepath.Join(root, "out/dest")); !os.IsNotExist(err) {
					t.Fatal("stale apply wrote target")
				}
			}
		})
	}
}

func TestUndoPreservesInterveningEdits(t *testing.T) {
	s, root := testService(t)
	p := testPlan(t, s, func(b *builder) error { return b.write(Location{root, "a/b.txt"}, []byte("owned"), 0o644, false) })
	if _, err := s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "a/b.txt"), "user edit")
	if _, err := s.Undo(context.Background(), p.ID); !errors.Is(err, ErrStale) {
		t.Fatalf("undo: %v", err)
	}
}

func TestUndoCreatedTreeAndRejectUnsafeParent(t *testing.T) {
	s, root := testService(t)
	p := testPlan(t, s, func(b *builder) error {
		for _, path := range []string{"a/b/x", "a/y"} {
			if err := b.write(Location{root, path}, []byte("data"), 0o644, false); err != nil {
				return err
			}
		}
		return nil
	})
	if _, err := s.Apply(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	u, err := s.Undo(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatal("created tree remained")
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, "a")); err != nil {
		t.Skip(err)
	}
	st, err := s.open(true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	b := newBuilder(context.Background(), st, TransferRequest{})
	defer b.close()
	if err = b.write(Location{root, "a/escape"}, []byte("bad"), 0o644, false); err == nil {
		t.Fatal("followed parent link")
	}
}

func TestTamperedRecordRejected(t *testing.T) {
	s, root := testService(t)
	p := testPlan(t, s, func(b *builder) error { return b.write(Location{root, "x"}, []byte("x"), 0o644, false) })
	path := filepath.Join(s.StateDir, p.ID+".json")
	data, _ := os.ReadFile(path)
	data = []byte(strings.ReplaceAll(string(data), `"path":"x"`, `"path":"other"`))
	_ = os.WriteFile(path, data, 0o600)
	if _, err := s.Apply(context.Background(), p.ID); err == nil {
		t.Fatal("accepted edited authority")
	}
}

func TestInterruptedAndConfirmedPartialTransfersRetainSource(t *testing.T) {
	for _, kind := range []string{"unconfirmed", "confirmed", "destination-changed"} {
		t.Run(kind, func(t *testing.T) {
			s, root := testService(t)
			writeFixture(t, filepath.Join(root, "source"), "recoverable")
			p := testPlan(t, s, func(b *builder) error {
				_, data, err := b.read(Location{root, "source"})
				if err != nil {
					return err
				}
				if err = b.write(Location{root, "destination"}, data, 0o600, false); err != nil {
					return err
				}
				return b.remove(Location{root, "source"})
			})
			s.hooks = &applyHooks{}
			if kind == "unconfirmed" {
				s.hooks.afterPublish = func(int) error { return errors.New("simulated interruption") }
			} else {
				s.hooks.afterConfirm = func(step int) error {
					if step != 0 {
						return nil
					}
					if kind == "destination-changed" {
						writeFixture(t, filepath.Join(root, "destination"), "user edit")
						return nil
					}
					return errors.New("simulated failure after confirmation")
				}
			}
			result, err := s.Apply(context.Background(), p.ID)
			if err == nil {
				t.Fatal("injected failure ignored")
			}
			if _, err = os.Stat(filepath.Join(root, "source")); err != nil {
				t.Fatal("source retired before destination confirmation")
			}
			s.hooks = nil
			if kind == "unconfirmed" {
				if !result.Uncertain || result.Completed != 0 {
					t.Fatal("uncertain effect overstated")
				}
				if _, err = s.Undo(context.Background(), p.ID); err == nil {
					t.Fatal("unknown ownership was guessed")
				}
			} else {
				if result.Status != "partial" || result.Completed != 1 || result.Uncertain {
					t.Fatalf("partial result = %+v", result)
				}
				u, err := s.Undo(context.Background(), p.ID)
				if kind == "destination-changed" {
					if !errors.Is(err, ErrStale) {
						t.Fatal("undo would overwrite intervening edit")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if _, err = s.Apply(context.Background(), u.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestRecoveryStateCannotEnterGitAndMissingKeysAreNotReset(t *testing.T) {
	s, root := testService(t)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := Service{StateDir: filepath.Join(root, "state")}
	if st, err := blocked.open(true); err == nil {
		st.close()
		t.Fatal("private recovery was placed in a checkout")
	}
	st, err := s.open(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.put(context.Background(), []byte("recovery")); err != nil {
		t.Fatal(err)
	}
	st.close()
	if err = os.Remove(filepath.Join(s.StateDir, "key")); err != nil {
		t.Fatal(err)
	}
	if st, err := s.open(true); err == nil {
		st.close()
		t.Fatal("missing authority key was silently replaced")
	}
}
