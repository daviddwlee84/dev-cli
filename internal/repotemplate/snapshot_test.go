package repotemplate_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/repotemplate"
)

func TestPrepareLocalSubdirectoryExcludesGitMetadataAndPreservesModes(t *testing.T) {
	source := t.TempDir()
	root := filepath.Join(source, "templates", "service")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: source, Subdir: "templates/service",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 1 || snapshot.Files[0].Path != "run.sh" || snapshot.Files[0].Mode.Perm() != 0o751 {
		t.Fatalf("files = %+v", snapshot.Files)
	}
	if len(snapshot.Directories) != 1 || snapshot.Directories[0].Path != "empty" {
		t.Fatalf("directories = %+v", snapshot.Directories)
	}
	if summary := snapshot.Summary(); summary.Subdir != "templates/service" || summary.Files != 1 || summary.Directories != 1 ||
		!summary.Local || !summary.Live || summary.GitFiltered || len(summary.PathPreview) == 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestPrepareLiveGitWorktreeExcludesIgnoredUntrackedFiles(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "source", Destination: source,
	}); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".gitignore":     "*.secret\n",
		"tracked.txt":    "tracked\n",
		"tracked.secret": "tracked despite ignore\n",
	} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, source, "add", ".gitignore", "tracked.txt")
	git(t, source, "add", "-f", "tracked.secret")
	git(t, source, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "tracked files")
	if err := os.WriteFile(filepath.Join(source, "visible.txt"), []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "ignored.secret"), []byte("do not copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range snapshot.Files {
		files[file.Path] = string(file.Data)
	}
	for _, expected := range []string{".gitignore", "tracked.txt", "tracked.secret", "visible.txt"} {
		if _, ok := files[expected]; !ok {
			t.Errorf("snapshot omitted %s: %v", expected, files)
		}
	}
	if _, ok := files["ignored.secret"]; ok {
		t.Fatalf("snapshot copied ignored untracked secret: %v", files)
	}
	summary := snapshot.Summary()
	if !summary.Local || !summary.Live || !summary.GitFiltered || len(summary.PathPreview) == 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestPrepareLiveGitSubdirectoryUsesRelativeFilteredPaths(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "source", Destination: source,
	}); err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(source, "templates", "service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".gitignore":                    "*.secret\n",
		"outside.txt":                   "outside\n",
		"templates/service/tracked.txt": "tracked\n",
	} {
		full := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, source, "add", ".")
	git(t, source, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "template subtree")
	if err := os.WriteFile(filepath.Join(service, "visible.txt"), []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service, "ignored.secret"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: source, Subdir: "templates/service",
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, file := range snapshot.Files {
		paths[file.Path] = true
	}
	if !paths["tracked.txt"] || !paths["visible.txt"] || paths["ignored.secret"] || paths["outside.txt"] || paths["templates/service/tracked.txt"] {
		t.Fatalf("subdirectory snapshot paths = %v", paths)
	}
}

func TestPrepareRejectsTraversalSymlinksAndSpecialFiles(t *testing.T) {
	if _, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: t.TempDir(), Subdir: "../escape",
	}); err == nil || !strings.Contains(err.Error(), "parent traversal") {
		t.Fatalf("traversal error = %v", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	topLevelTarget := t.TempDir()
	topLevelLink := filepath.Join(t.TempDir(), "source-link")
	if err := os.Symlink(topLevelTarget, topLevelLink); err != nil {
		t.Fatal(err)
	}
	if _, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: topLevelLink}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("top-level symlink error = %v", err)
	}

	source := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: source}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
	selectedSource := t.TempDir()
	if err := os.Mkdir(filepath.Join(selectedSource, "actual"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual", filepath.Join(selectedSource, "selected")); err != nil {
		t.Fatal(err)
	}
	if _, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: selectedSource, Subdir: "selected",
	}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked subdirectory error = %v", err)
	}

	special := t.TempDir()
	pipe := filepath.Join(special, "events.pipe")
	if err := makeFIFO(pipe); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}
	if _, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: special}); err == nil || !strings.Contains(err.Error(), "special file") {
		t.Fatalf("special-file error = %v", err)
	}

	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, `bad\name`), []byte("bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: invalid}); err == nil || !strings.Contains(err.Error(), "path separator") {
		t.Fatalf("invalid-name error = %v", err)
	}
}

func TestPrepareRedactsGitURLUserinfoFromSummaryAndErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file URL userinfo probe is Unix-specific")
	}
	source := filepath.Join(t.TempDir(), "source")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "source", Destination: source,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", "README.md")
	git(t, source, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "initial")

	credential := "unit-test-secret"
	url := "file://alice:" + credential + "@localhost" + filepath.ToSlash(source)
	snapshot, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: url})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot.Source, credential) || strings.Contains(snapshot.Summary().Source, credential) || strings.Contains(snapshot.Source, "alice@") {
		t.Fatalf("display source leaked userinfo: %q", snapshot.Source)
	}

	missing := "file://alice:" + credential + "@localhost/definitely/not/a/repository"
	_, err = repotemplate.Prepare(t.Context(), repotemplate.Request{Source: missing})
	if err == nil {
		t.Fatal("missing credential URL unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), missing) {
		t.Fatalf("error leaked credential source: %v", err)
	}
}

func TestPrepareGitRefSnapshotsRequestedCommitAndSubdirectory(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "source", Destination: source,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "starters", "go", "version.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", ".")
	git(t, source, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "old")
	old := git(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", ".")
	git(t, source, "-c", "user.name=test", "-c", "user.email=test@example.test", "commit", "-m", "new")

	snapshot, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: source, Ref: old, Subdir: "starters/go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Ref != old || snapshot.Commit != old {
		t.Fatalf("snapshot ref/commit = %q / %q", snapshot.Ref, snapshot.Commit)
	}
	if len(snapshot.Files) != 1 || string(snapshot.Files[0].Data) != "old\n" || snapshot.Files[0].Path != "version.txt" {
		t.Fatalf("files = %+v", snapshot.Files)
	}

	latest, err := repotemplate.Prepare(t.Context(), repotemplate.Request{
		Source: "file://" + source, Subdir: "starters/go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest.Commit == "" || len(latest.Files) != 1 || string(latest.Files[0].Data) != "new\n" {
		t.Fatalf("Git URL snapshot = %+v", latest.Summary())
	}
}

func TestApplyRequiresAnExactNewRepository(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "tool"), []byte("tool\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repotemplate.Prepare(t.Context(), repotemplate.Request{Source: source})
	if err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "destination")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "destination", Destination: destination,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := snapshot.Apply(destination)
	if err != nil {
		t.Fatal(err)
	}
	if result.AppliedFiles != 1 || result.AppliedDirectories != 1 {
		t.Fatalf("result = %+v", result)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, err := snapshot.Apply(destination); err == nil || !strings.Contains(err.Error(), "not a new repository") {
		t.Fatalf("second apply error = %v", err)
	}

	forged := repotemplate.Snapshot{Files: []repotemplate.File{{Path: ".git/config", Mode: 0o644, Data: []byte("bad")}}}
	other := filepath.Join(t.TempDir(), "other")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "other", Destination: other,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := forged.Apply(other); err == nil || !strings.Contains(err.Error(), "reserved Git metadata") {
		t.Fatalf("forged metadata error = %v", err)
	}
	forgedAlias := repotemplate.Snapshot{Files: []repotemplate.File{{Path: ".git./config", Mode: 0o644}}}
	if _, err := forgedAlias.Apply(other); err == nil || !strings.Contains(err.Error(), "reserved Git metadata") {
		t.Fatalf("forged Git alias error = %v", err)
	}
	caseCollision := repotemplate.Snapshot{Files: []repotemplate.File{
		{Path: "README.md", Mode: 0o644},
		{Path: "readme.md", Mode: 0o644},
	}}
	if _, err := caseCollision.Apply(other); err == nil || !strings.Contains(err.Error(), "case-insensitive") {
		t.Fatalf("case-collision error = %v", err)
	}

	actual := filepath.Join(t.TempDir(), "actual")
	if _, err := repo.Acquire(t.Context(), repo.AcquireRequest{
		Kind: repo.AcquireNew, Name: "actual", Destination: actual,
	}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "destination-link")
	if err := os.Symlink(actual, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := snapshot.Apply(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink destination error = %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(t.Context(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
