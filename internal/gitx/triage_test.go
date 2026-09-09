package gitx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestTriageEveryBranchAndMissingUpstream(t *testing.T) {
	r := gittest.New(t)
	r.WithRemote()
	r.Git("branch", "local-only")
	r.Commit("work", "local", "local commit")
	branches, e := gitx.BranchStates(t.Context(), r.Root)
	if e != nil {
		t.Fatal(e)
	}
	if len(branches) != 2 {
		t.Fatalf("branches=%+v", branches)
	}
	for _, b := range branches {
		switch b.Ref {
		case "refs/heads/main":
			if !b.ComparisonKnown || b.Ahead != 1 || b.Behind != 0 || b.PushRef != "refs/heads/main" {
				t.Fatalf("main=%+v", b)
			}
		case "refs/heads/local-only":
			if b.Upstream != "" || b.ComparisonKnown {
				t.Fatalf("local=%+v", b)
			}
		}
	}
	r.Git("update-ref", "-d", "refs/remotes/origin/main")
	branches, e = gitx.BranchStates(t.Context(), r.Root)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range branches {
		if b.Ref == "refs/heads/main" && (b.ComparisonKnown || b.Error == "") {
			t.Fatalf("missing=%+v", b)
		}
	}
}

func TestTriageContentChangesAndIgnoredPolicy(t *testing.T) {
	r := gittest.New(t)
	r.Commit(".gitignore", "cache/\n.env\n", "ignore")
	r.Write("cache/value", "replaceable")
	r.Write(".env", "keep me")
	r.Write("new.txt", "first")
	one, e := gitx.InspectTriageContents(t.Context(), r.Root, []string{"cache"})
	if e != nil {
		t.Fatal(e)
	}
	if len(one.Ignored) != 2 {
		t.Fatalf("ignored=%+v", one.Ignored)
	}
	for _, p := range one.Ignored {
		if (p.Path == ".env" && p.Disposable) || (p.Path == "cache/value" && !p.Disposable) {
			t.Fatalf("policy=%+v", p)
		}
	}
	r.Write("new.txt", "other")
	two, e := gitx.InspectTriageContents(t.Context(), r.Root, []string{"cache"})
	if e != nil {
		t.Fatal(e)
	}
	if one.Fingerprint == two.Fingerprint {
		t.Fatal("same-sized edit disappeared")
	}
	r.Write("README.md", "dirty one")
	one, e = gitx.InspectTriageContents(t.Context(), r.Root, nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Write("README.md", "dirty two")
	two, e = gitx.InspectTriageContents(t.Context(), r.Root, nil)
	if e != nil {
		t.Fatal(e)
	}
	if one.Fingerprint == two.Fingerprint {
		t.Fatal("tracked content change disappeared")
	}
}

func TestTriageNestedRepositoryAndSymlink(t *testing.T) {
	r := gittest.New(t)
	r.Commit(".gitignore", "cache/\n", "ignore")
	r.Write("cache/nested/.git/HEAD", "ref: refs/heads/main\n")
	outside := filepath.Join(t.TempDir(), "outside")
	if e := os.WriteFile(outside, []byte("retained"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, r.Write("cache/link-placeholder", "")); e == nil {
		t.Fatal("unexpected existing destination")
	}
	if e := os.Symlink(outside, filepath.Join(r.Root, "cache", "link")); e != nil {
		t.Skip(e)
	}
	i, e := gitx.InspectTriageContents(t.Context(), r.Root, []string{"cache"})
	if e != nil {
		t.Fatal(e)
	}
	if len(i.Nested) == 0 {
		t.Fatal("nested Git metadata hidden by ignored directory")
	}
	data, e := os.ReadFile(outside)
	if e != nil || string(data) != "retained" {
		t.Fatal("external symlink target changed")
	}
	for _, bad := range []string{".", "../other", "/tmp", "cache/*", "cache/../other", ".git", "cache/.git"} {
		if gitx.ValidateDisposableDirs([]string{bad}) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
