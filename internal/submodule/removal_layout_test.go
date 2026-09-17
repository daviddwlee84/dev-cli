package submodule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestCleanupRegressionSubmoduleLayoutPathSpelling(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		state := "empty"
		if initialized {
			state = "initialized"
		}
		for _, spelling := range []string{"native", "git-slashes", "git-slashes-dot"} {
			t.Run(state+"-"+spelling, func(t *testing.T) {
				base := t.TempDir()
				repo := gitx.Repo{Root: filepath.Join(base, "Checkout"), GitDir: filepath.Join(base, "Admin")}
				modules := filepath.Join(repo.GitDir, "modules")
				store := filepath.Join(modules, "Child")
				for _, path := range []string{filepath.Join(repo.Root, "Child"), filepath.Join(modules, "unused", "nested")} {
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				graph := gitx.SubmoduleGraph{Root: repo.Root, Nodes: []gitx.SubmoduleNode{{Path: "Child"}}}
				if initialized {
					if err := os.Mkdir(store, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(store, "HEAD"), []byte("retained repository data\n"), 0600); err != nil {
						t.Fatal(err)
					}
					graph.Nodes[0].Initialized = true
					graph.Nodes[0].GitDir, graph.Nodes[0].CommonDir = store, store
				}
				baseline, err := observeRemovalLayout(t.Context(), repo, graph)
				if err != nil {
					t.Fatal(err)
				}
				adminInfo := baseline.Directories[repo.GitDir].info
				spell := func(path string) string {
					if spelling == "native" {
						return path
					}
					path = filepath.ToSlash(path)
					if spelling == "git-slashes-dot" {
						// Also exercise equivalent non-clean spelling on POSIX,
						// where Git and filepath otherwise use the same separator.
						path += "/."
					}
					return path
				}
				repo.Root, repo.GitDir = spell(repo.Root), spell(repo.GitDir)
				if initialized {
					graph.Nodes[0].GitDir = spell(store)
				}
				layout, err := observeRemovalLayout(t.Context(), repo, graph)
				if err != nil {
					t.Fatal(err)
				}
				if layout.fingerprint() != baseline.fingerprint() {
					t.Fatal("path spelling changed physical layout authority")
				}
				// Empty-root pruning looks up the parent via filepath.Dir, not
				// the original Git-returned spelling. Its identity must be held.
				if !os.SameFile(adminInfo, layout.Directories[filepath.Dir(modules)].info) {
					t.Fatal("native parent lookup lost administration identity")
				}
				if !initialized {
					return
				}
				// Staging must recognize the same known store after a native-path
				// move, including the lease entries excluded during observation.
				if err := os.Mkdir(filepath.Join(store, "dev-taskflow"), 0700); err != nil {
					t.Fatal(err)
				}
				staged := filepath.Join(base, "git-modules")
				if err := os.Rename(modules, staged); err != nil {
					t.Fatal(err)
				}
				if err := inspectModuleStorage(staged, graph.Nodes, modules); err != nil {
					t.Fatal(err)
				}
				plan := &Removal{layout: layout, initialized: graph.Nodes}
				if err := plan.verifyStagedLayout(t.Context(), []moveRecord{{From: modules, To: staged}}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCleanupRegressionSubmoduleLayoutPathSpellingRejectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	layout := &removalLayout{Directories: map[string]layoutDirectory{}, Absent: map[string]bool{}}
	if err := layout.path(t.Context(), path, ".", true); err != nil {
		t.Fatal(err)
	}
	before := layout.fingerprint()
	alternate := filepath.ToSlash(path) + "/."
	if err := layout.path(t.Context(), alternate, ".", true); err != nil {
		t.Fatal(err)
	}
	if layout.fingerprint() != before || len(layout.Directories) != 1 {
		t.Fatal("equivalent path spelling created another directory identity")
	}
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := layout.path(t.Context(), alternate, ".", true); !errors.Is(err, safefile.ErrChanged) {
		t.Fatalf("replacement accepted through alternate path spelling: %v", err)
	}
}
