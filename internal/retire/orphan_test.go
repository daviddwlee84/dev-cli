package retire

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectOrphanAcceptsAnArtifactOnlyShell(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".specstory", "history", "a.md"), "one")
	writeFile(t, filepath.Join(dir, ".claude", "plans", "p.md"), "two")
	writeFile(t, filepath.Join(dir, ".DS_Store"), "noise")

	orphan, ok, err := InspectOrphan(dir)
	if err != nil || !ok {
		t.Fatalf("InspectOrphan = %v, %v", ok, err)
	}
	if len(orphan.Files) != 3 {
		t.Errorf("expected every artifact file, got %v", orphan.Files)
	}
}

func TestInspectOrphanRejectsAnythingItDoesNotRecognise(t *testing.T) {
	// The whole point is to never delete a directory holding unknown work, so
	// a single unrecognised entry disqualifies it.
	for name, entry := range map[string]string{
		"source file":  "main.go",
		"real content": "README.md",
		"git checkout": ".git",
		"node modules": "node_modules",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, ".specstory", "history", "a.md"), "one")
			writeFile(t, filepath.Join(dir, entry), "x")
			if _, ok, err := InspectOrphan(dir); ok || err != nil {
				t.Errorf("InspectOrphan accepted a directory containing %s (ok=%v err=%v)", entry, ok, err)
			}
		})
	}
}

func TestInspectOrphanIgnoresAMissingPath(t *testing.T) {
	if _, ok, err := InspectOrphan(filepath.Join(t.TempDir(), "nope")); ok || err != nil {
		t.Errorf("InspectOrphan on a missing path = %v, %v", ok, err)
	}
}

func TestUnsalvagedFindsContentTheRepositoryDoesNotHave(t *testing.T) {
	orphanDir, repoDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(orphanDir, ".specstory", "history", "same.md"), "identical")
	writeFile(t, filepath.Join(repoDir, ".specstory", "history", "same.md"), "identical")
	writeFile(t, filepath.Join(orphanDir, ".specstory", "history", "only-here.md"), "unique")

	orphan, ok, err := InspectOrphan(orphanDir)
	if err != nil || !ok {
		t.Fatalf("InspectOrphan = %v, %v", ok, err)
	}
	unsalvaged, err := Unsalvaged(orphan, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsalvaged) != 1 || filepath.Base(unsalvaged[0]) != "only-here.md" {
		t.Fatalf("Unsalvaged = %v, want just only-here.md", unsalvaged)
	}
}

func TestUnsalvagedTreatsALongerTranscriptAsUnsalvaged(t *testing.T) {
	// A writer that outlives its worktree flushes a longer final transcript
	// than the copy committed earlier. Mere existence in the repository is not
	// enough; the bytes have to match, or the tail is lost.
	orphanDir, repoDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(orphanDir, ".specstory", "history", "s.md"), "start\nmiddle\nend\n")
	writeFile(t, filepath.Join(repoDir, ".specstory", "history", "s.md"), "start\n")

	orphan, _, err := InspectOrphan(orphanDir)
	if err != nil {
		t.Fatal(err)
	}
	unsalvaged, err := Unsalvaged(orphan, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(unsalvaged) != 1 {
		t.Fatalf("a truncated repository copy was treated as salvaged: %v", unsalvaged)
	}
}

func TestUnsalvagedIgnoresDerivedNoise(t *testing.T) {
	orphanDir, repoDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(orphanDir, ".specstory", "statistics.json"), `{"n":1}`)
	writeFile(t, filepath.Join(orphanDir, ".specstory", "nested", ".specstory", "statistics.json"), "unique")
	writeFile(t, filepath.Join(orphanDir, ".DS_Store"), "junk")

	orphan, ok, err := InspectOrphan(orphanDir)
	if err != nil || !ok {
		t.Fatalf("InspectOrphan = %v, %v", ok, err)
	}
	unsalvaged, err := Unsalvaged(orphan, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join(".specstory", "nested", ".specstory", "statistics.json"))
	if len(unsalvaged) != 1 || filepath.ToSlash(unsalvaged[0]) != want {
		t.Fatalf("exact derived file policy lost nested user data: %v", unsalvaged)
	}
}
