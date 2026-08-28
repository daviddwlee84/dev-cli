package ignore_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/ignore"
)

func TestCanonical(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"python", "Python"}, {"PYTHON", "Python"}, {" Python ", "Python"},
		{"js", "Node"}, {"typescript", "Node"}, {"golang", "Go"},
		{"c#", "VisualStudio"}, {"latex", "TeX"},
		{"somethingelse", "Somethingelse"},
	} {
		if got := ignore.Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetPrefersRemoteThenCachesIt(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if !strings.HasSuffix(r.URL.Path, "/Python") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("__pycache__/\n*.pyc\n"))
	}))
	defer srv.Close()

	cache := t.TempDir()
	f := &ignore.Fetcher{CacheDir: cache, Client: srv.Client()}
	f.BaseURL = srv.URL

	got, err := f.Get(context.Background(), "python")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != ignore.SourceRemote || !strings.Contains(got.Body, "__pycache__") {
		t.Errorf("got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(cache, "Python.gitignore")); err != nil {
		t.Errorf("the fetched template should be cached: %v", err)
	}

	// Offline now resolves from the cache rather than failing.
	f.Offline = true
	got, err = f.Get(context.Background(), "python")
	if err != nil {
		t.Fatalf("offline Get: %v", err)
	}
	if got.Source != ignore.SourceCache {
		t.Errorf("source = %q, want cache", got.Source)
	}
}

// With no network and no cache, the bundled copies must still work — that is
// the whole reason for embedding them.
func TestGetFallsBackToBundled(t *testing.T) {
	f := &ignore.Fetcher{CacheDir: t.TempDir(), Offline: true}
	got, err := f.Get(context.Background(), "go")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != ignore.SourceBundled {
		t.Errorf("source = %q, want bundled", got.Source)
	}
	if !strings.Contains(got.Body, "*.exe") {
		t.Errorf("bundled Go template looks wrong:\n%s", got.Body)
	}
}

func TestGetUnknownOfflineErrorsHelpfully(t *testing.T) {
	f := &ignore.Fetcher{CacheDir: t.TempDir(), Offline: true}
	_, err := f.Get(context.Background(), "brainfuck")
	if err == nil {
		t.Fatal("an unknown offline template should error")
	}
	if !strings.Contains(err.Error(), "available offline") {
		t.Errorf("the error should list what is available, got %v", err)
	}
}

func TestBundledNames(t *testing.T) {
	names := ignore.BundledNames()
	if len(names) == 0 {
		t.Fatal("no templates are bundled")
	}
	for _, want := range []string{"Go", "Node", "Python"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should be bundled, have %v", want, names)
		}
	}
}

func TestComposeIncludesExtras(t *testing.T) {
	block := ignore.Compose(
		[]ignore.Section{{Name: "Go", Body: "*.exe", Source: ignore.SourceBundled}},
		ignore.DefaultExtras(),
	)
	for _, want := range []string{"*.exe", ".env", "Editors and IDEs", "Ephemeral coding-agent state"} {
		if !strings.Contains(block, want) {
			t.Errorf("composed block missing %q:\n%s", want, block)
		}
	}
	if !ignore.HasManagedBlock(block) {
		t.Error("the composed block should carry dev's markers")
	}
}

func TestComposeAgentArtifactPolicy(t *testing.T) {
	block := ignore.Compose(nil, ignore.DefaultExtras())
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "worktrees", pattern: ".claude/worktrees/", want: true},
		{name: "local settings", pattern: ".claude/settings.local.json", want: true},
		{name: "aider state", pattern: ".aider*", want: true},
		{name: "generated cursor rules", pattern: ".cursor/rules/_generated/", want: true},
		{name: "specstory histories", pattern: ".specstory/history/", want: false},
		{name: "claude plans", pattern: ".claude/plans/", want: false},
		{name: "cursor plans", pattern: ".cursor/plans/", want: false},
		{name: "opencode plans", pattern: ".opencode/plans/", want: false},
		{name: "specify artifacts", pattern: ".specify/", want: false},
		{name: "codex artifacts", pattern: ".codex/", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Contains(block, "\n"+tc.pattern+"\n")
			if got != tc.want {
				t.Errorf("rule %q present = %v, want %v:\n%s", tc.pattern, got, tc.want, block)
			}
		})
	}
}

func TestComposeRespectsDisabledExtras(t *testing.T) {
	block := ignore.Compose(nil, ignore.Extras{OS: true})
	if strings.Contains(block, ".claude/worktrees/") {
		t.Error("agents section should be omitted")
	}
	if strings.Contains(block, ".env\n") {
		t.Error("env section should be omitted")
	}
}

// Re-running must update only the managed region: a project accumulates
// hand-written rules, and regenerating must never be what loses them.
func TestMergeReplacesOnlyTheManagedBlock(t *testing.T) {
	first := ignore.Compose([]ignore.Section{{Name: "Go", Body: "*.exe"}}, ignore.Extras{})
	existing := "# my own rules\nsecret-notes.md\n\n" + first + "\n# trailing rule\nlocal-only/\n"

	second := ignore.Compose([]ignore.Section{{Name: "Python", Body: "*.pyc"}}, ignore.Extras{})
	merged := ignore.Merge(existing, second)

	for _, want := range []string{"# my own rules", "secret-notes.md", "# trailing rule", "local-only/", "*.pyc"} {
		if !strings.Contains(merged, want) {
			t.Errorf("merge lost %q:\n%s", want, merged)
		}
	}
	if strings.Contains(merged, "*.exe") {
		t.Errorf("the old managed content should be replaced:\n%s", merged)
	}
	if strings.Count(merged, "# >>> dev gitignore >>>") != 1 {
		t.Error("merging should not duplicate the markers")
	}
}

func TestMergeAppendsWhenNoBlockExists(t *testing.T) {
	block := ignore.Compose(nil, ignore.Extras{OS: true})

	merged := ignore.Merge("", block)
	if merged != block {
		t.Error("an empty file should become just the block")
	}

	merged = ignore.Merge("existing-rule\n", block)
	if !strings.HasPrefix(merged, "existing-rule\n") || !strings.Contains(merged, block) {
		t.Errorf("existing content should be kept and the block appended:\n%s", merged)
	}

	// A file with no trailing newline must not have its last rule glued to the
	// marker comment.
	merged = ignore.Merge("no-newline", block)
	if strings.Contains(merged, "no-newline#") {
		t.Errorf("missing newline separator:\n%s", merged)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)

	got := ignore.Detect(dir)
	if len(got) != 2 {
		t.Fatalf("want Go and Node, got %v", got)
	}
	if got[0] != "Go" || got[1] != "Node" {
		t.Errorf("results should be sorted, got %v", got)
	}
	if len(ignore.Detect(t.TempDir())) != 0 {
		t.Error("an empty directory should detect nothing")
	}
}
