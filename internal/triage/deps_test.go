package triage

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(content), 0644); e != nil {
		t.Fatal(e)
	}
}

func age(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-60 * 24 * time.Hour)
	if e := os.Chtimes(path, old, old); e != nil {
		t.Fatal(e)
	}
}

// depsFixture builds an idle checkout whose recognized dependency directories
// carry fresh mtimes, plus look-alike directories that must never qualify.
func depsFixture(t *testing.T, idle bool) (*Service, *gittest.Repo) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("pruning requires stable filesystem identity")
	}
	s, r := fixture(t)
	if idle {
		t.Setenv("GIT_COMMITTER_DATE", "2020-01-01T00:00:00Z")
		t.Setenv("GIT_AUTHOR_DATE", "2020-01-01T00:00:00Z")
	}
	r.Commit(".gitignore", ".venv/\nnode_modules/\ntarget/\nlookalike/\nweb/node_modules\n", "ignore")
	r.Commit("package.json", "{}", "node")
	r.Commit("crates/x/Cargo.toml", "[package]\n", "rust")
	r.Commit("vendored/package.json", "{}", "vendored")
	writeFile(t, filepath.Join(r.Root, "vendored/node_modules/keep.js"), "tracked")
	r.Git("add", "-f", "vendored/node_modules/keep.js")
	r.Git("commit", "-m", "vendor a dependency")
	writeFile(t, filepath.Join(r.Root, ".venv/pyvenv.cfg"), "home = /usr\n")
	writeFile(t, filepath.Join(r.Root, ".venv/lib/site.py"), strings.Repeat("x", 4096))
	writeFile(t, filepath.Join(r.Root, "node_modules/a/index.js"), "module.exports = 1")
	writeFile(t, filepath.Join(r.Root, "node_modules/a/.venv/pyvenv.cfg"), "nested venv is covered by node_modules")
	writeFile(t, filepath.Join(r.Root, "crates/x/target/debug/out"), strings.Repeat("y", 2048))
	writeFile(t, filepath.Join(r.Root, "vendored/node_modules/extra.js"), "ignored next to tracked")
	writeFile(t, filepath.Join(r.Root, "lookalike/target/data"), "no Cargo.toml beside it")
	writeFile(t, filepath.Join(r.Root, "lookalike/.venv/file"), "no pyvenv.cfg inside")
	external := filepath.Join(t.TempDir(), "external-modules")
	writeFile(t, filepath.Join(external, "dep.js"), "outside the checkout")
	writeFile(t, filepath.Join(r.Root, "web/package.json"), "{}")
	r.Git("add", "web/package.json")
	r.Git("commit", "-m", "web")
	if e := os.Symlink(external, filepath.Join(r.Root, "web/node_modules")); e != nil {
		t.Fatal(e)
	}
	if idle {
		// Only recognized dependency directories keep fresh mtimes; every other
		// ignored path is old so that idleness is decided by the exclusion rule.
		for _, rel := range []string{"vendored/node_modules/extra.js", "lookalike/target/data", "lookalike/.venv/file"} {
			age(t, filepath.Join(r.Root, rel))
		}
		if out, e := exec.Command("touch", "-h", "-t", "202001010000", filepath.Join(r.Root, "web/node_modules")).CombinedOutput(); e != nil {
			t.Fatalf("touch symlink: %v %s", e, out)
		}
	}
	return s, r
}

func paths(dirs []DependencyDir) []string {
	out := []string{}
	for _, d := range dirs {
		out = append(out, d.Path)
	}
	return out
}

func TestPruneDepsRecognizesOnlyWhollyIgnoredRegenerableDirs(t *testing.T) {
	s, r := depsFixture(t, true)
	i := checkout(t, collect(t, s), r.Root)
	got := strings.Join(paths(i.Regenerable), ",")
	if got != ".venv,crates/x/target,node_modules" {
		t.Fatalf("regenerable=%s", got)
	}
	for _, d := range i.Regenerable {
		if d.Bytes <= 0 || d.Files == 0 || d.Rebuild == "" {
			t.Fatalf("incomplete dependency facts: %+v", d)
		}
	}
	a, ok := i.Action(ActionPruneDeps)
	if !ok || a.Availability != "candidate" {
		t.Fatalf("fresh dependency mtimes must not make an idle checkout active: %+v findings=%+v", a, i.Findings)
	}
	found := false
	for _, f := range i.Findings {
		found = found || f.Code == "regenerable"
	}
	if !found {
		t.Fatal("regenerable finding missing")
	}
}

func TestPruneDepsBlockedForActiveCheckout(t *testing.T) {
	s, r := depsFixture(t, false)
	i := checkout(t, collect(t, s), r.Root)
	a, ok := i.Action(ActionPruneDeps)
	if !ok || a.Availability != "blocked" || !strings.Contains(a.Reason, "active within") {
		t.Fatalf("active checkout action=%+v", a)
	}
}

func TestPruneDepsRemovesOnlyDependencyDirsAndPreservesWork(t *testing.T) {
	s, r := depsFixture(t, true)
	dirty := r.Write("notes.txt", "uncommitted")
	age(t, dirty)
	head := r.Git("rev-parse", "HEAD")
	i := checkout(t, collect(t, s), r.Root)
	if a, _ := i.Action(ActionPruneDeps); a.Availability != "candidate" {
		t.Fatalf("dirty tree must not block pruning: %+v", a)
	}
	b, e := s.Prepare(t.Context(), []Item{i}, ActionPruneDeps)
	if e != nil {
		t.Fatal(e)
	}
	if b.ReadyCount() != 1 || b.Token != "PRUNE 3" {
		t.Fatalf("token=%q previews=%+v", b.Token, b.Previews())
	}
	if _, e = s.Apply(t.Context(), b, b.ID, "CLEAN 3", nil); e == nil {
		t.Fatal("wrong prune token accepted")
	}
	l, e := s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(l.Outcomes) != 1 || l.Outcomes[0].Status != "completed" || l.Outcomes[0].ReclaimedBytes < 6144 || len(l.Outcomes[0].Removed) != 3 {
		t.Fatalf("ledger=%+v", l)
	}
	for _, gone := range []string{".venv", "node_modules", "crates/x/target"} {
		if _, e := os.Lstat(filepath.Join(r.Root, gone)); !os.IsNotExist(e) {
			t.Fatalf("%s not removed: %v", gone, e)
		}
	}
	for _, kept := range []string{"notes.txt", "vendored/node_modules/keep.js", "vendored/node_modules/extra.js", "lookalike/target/data", "lookalike/.venv/file", "web/node_modules", "crates/x/Cargo.toml"} {
		if _, e := os.Lstat(filepath.Join(r.Root, kept)); e != nil {
			t.Fatalf("%s must be preserved: %v", kept, e)
		}
	}
	if r.Git("rev-parse", "HEAD") != head {
		t.Fatal("branch moved")
	}
	matches, _ := filepath.Glob(filepath.Join(r.Root, ".dev-prune-*"))
	if len(matches) > 0 {
		t.Fatalf("temporary directories left behind: %v", matches)
	}
	if summary, _ := SummarizeLedger(l); !strings.Contains(summary, "reclaimed") {
		t.Fatalf("summary=%s", summary)
	}
}

func TestPruneDepsRevalidatesBeforeRemoval(t *testing.T) {
	s, r := depsFixture(t, true)
	i := checkout(t, collect(t, s), r.Root)
	b, e := s.Prepare(t.Context(), []Item{i}, ActionPruneDeps)
	if e != nil || b.ReadyCount() != 1 {
		t.Fatalf("prepare=%v %+v", e, b.Previews())
	}
	// Replace the reviewed environment with a different directory: same path,
	// new identity. Nothing may be removed under the old approval.
	venv := filepath.Join(r.Root, ".venv")
	if e = os.RemoveAll(venv); e != nil {
		t.Fatal(e)
	}
	writeFile(t, filepath.Join(venv, "pyvenv.cfg"), "replaced")
	l, e := s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if e != nil {
		t.Fatal(e)
	}
	if l.Outcomes[0].Status != "stale" || len(l.Outcomes[0].Removed) != 0 {
		t.Fatalf("outcome=%+v", l.Outcomes[0])
	}
	for _, kept := range []string{".venv/pyvenv.cfg", "node_modules/a/index.js", "crates/x/target/debug/out"} {
		if _, e := os.Lstat(filepath.Join(r.Root, kept)); e != nil {
			t.Fatalf("%s removed after stale approval: %v", kept, e)
		}
	}
}

func TestPruneDepsRejectsDirectoryThatGainedTrackedFile(t *testing.T) {
	s, r := depsFixture(t, true)
	i := checkout(t, collect(t, s), r.Root)
	b, e := s.Prepare(t.Context(), []Item{i}, ActionPruneDeps)
	if e != nil || b.ReadyCount() != 1 {
		t.Fatalf("prepare=%v %+v", e, b.Previews())
	}
	r.Git("add", "-f", ".venv/pyvenv.cfg")
	l, e := s.Apply(t.Context(), b, b.ID, b.Token, nil)
	if e != nil {
		t.Fatal(e)
	}
	if l.Outcomes[0].Status != "stale" {
		t.Fatalf("outcome=%+v", l.Outcomes[0])
	}
	if _, e := os.Lstat(filepath.Join(r.Root, ".venv/pyvenv.cfg")); e != nil {
		t.Fatal("tracked file removed")
	}
}

func TestPruneDepsRepositorySelectionDropsExpansionNoise(t *testing.T) {
	s, r := depsFixture(t, true)
	report := collect(t, s)
	co := checkout(t, report, r.Root)
	selection := []Item{co}
	for _, i := range report.Items {
		if i.RepositoryID == co.RepositoryID && i.Scope == "repository" {
			selection = append(selection, i)
		}
	}
	if len(selection) != 2 {
		t.Fatalf("fixture lacks a repository row: %d", len(selection))
	}
	b, e := s.Prepare(t.Context(), selection, ActionPruneDeps)
	if e != nil {
		t.Fatal(e)
	}
	if p := b.Previews(); len(p) != 1 || !p[0].Ready || len(p[0].Discard) != 0 {
		t.Fatalf("previews=%+v", p)
	}
}
