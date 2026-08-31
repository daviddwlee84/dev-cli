package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestTUIAppStatePublishesPreparedCopyOnlyOnCommit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	original := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := original.Load(); err != nil {
		t.Fatal(err)
	}
	originalTasks := original.Tasks
	state := newTUIAppState(original)
	next, err := state.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if next == original || state.Current() != original {
		t.Fatal("prepare published an App snapshot before model acceptance")
	}
	state.Commit(next)
	if state.Current() != next {
		t.Fatal("accepted App snapshot was not committed")
	}
	if original.Tasks != originalTasks {
		t.Fatal("reload mutated the App snapshot already used by an older callback")
	}
	if next.Tasks == originalTasks {
		t.Fatal("new App snapshot did not rebuild config-owned stores")
	}

	current := state.Current()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := state.Prepare(ctx); !contextCanceled(err) {
		t.Fatalf("canceled reload error = %v", err)
	}
	if state.Current() != current {
		t.Fatal("canceled reload replaced the current App snapshot")
	}
}

func contextCanceled(err error) bool { return err == context.Canceled }
