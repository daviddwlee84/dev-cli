package localfiles

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestPortableGlobExpansionIsSortedExactAndDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		".env": "one", ".mcp/a.json": "a", ".mcp/nested/b.json": "b",
	} {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(path)), body)
	}
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "leak.json"), "secret")
	if err := os.Symlink(outside, filepath.Join(root, ".mcp", "linked")); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	paths, err := Expand(root, []Pattern{
		{Value: ".mcp/**", Source: "project"},
		{Value: ".env", Source: "--file"},
	}, safefile.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env", ".mcp/a.json", ".mcp/linked", ".mcp/nested/b.json"}
	if runtime.GOOS == "windows" {
		want = []string{".env", ".mcp/a.json", ".mcp/nested/b.json"}
	}
	if len(paths) != len(want) {
		t.Fatalf("expanded paths = %v, want %v", paths, want)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("expanded paths = %v, want %v", paths, want)
		}
	}
	for _, path := range paths {
		if path == ".mcp/linked/leak.json" {
			t.Fatal("glob followed a directory symlink")
		}
	}
}

func TestExpandRejectsDependencyHazardsAndBounds(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "node_modules", "secret"), "x")
	if _, err := Expand(root, []Pattern{{Value: "node_modules/**", Source: "test"}}, safefile.DefaultLimits()); err == nil {
		t.Fatal("dependency pattern was accepted")
	}
	writeTestFile(t, filepath.Join(root, ".env"), "x")
	writeTestFile(t, filepath.Join(root, ".env.local"), "y")
	limits := safefile.DefaultLimits()
	limits.MaxFiles = 1
	if _, err := Expand(root, []Pattern{{Value: "**", Source: "test"}}, limits); err == nil {
		t.Fatal("expanded path count above the negotiated limit was accepted")
	}
}

func TestPrepareSourceRejectsSymlinkSpecialNestedAndPortableCollision(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "real")
	if err := os.Symlink(".env", filepath.Join(p.source, ".env.link")); err == nil {
		_, report, prepareErr := PrepareSource(t.Context(), SourceOptions{
			Checkout: p.source, Binding: p.binding, Patterns: []Pattern{{Value: ".env.link", Source: "test"}}, Limits: p.limits,
		})
		if !errors.Is(prepareErr, ErrPlanBlocked) || len(report.Files) != 1 || report.Files[0].State != StateBlockedUnsafe {
			t.Fatalf("symlink report = %+v, %v", report, prepareErr)
		}
	}

	if runtime.GOOS != "windows" {
		fifo := filepath.Join(p.source, ".env.fifo")
		command := exec.Command("mkfifo", fifo)
		if err := command.Run(); err == nil {
			_, report, prepareErr := PrepareSource(t.Context(), SourceOptions{
				Checkout: p.source, Binding: p.binding, Patterns: []Pattern{{Value: ".env.fifo", Source: "test"}}, Limits: p.limits,
			})
			if !errors.Is(prepareErr, ErrPlanBlocked) || report.Files[0].State != StateBlockedUnsafe {
				t.Fatalf("FIFO report = %+v, %v", report, prepareErr)
			}
		}
	}

	collision := []FileSpec{
		{Path: ".env.A", Size: 1, Mode: "0600", SHA256: digestBytes([]byte("A"))},
		{Path: ".env.a", Size: 1, Mode: "0600", SHA256: digestBytes([]byte("a"))},
	}
	if err := validateFileSpecs(collision, p.limits); !errors.Is(err, pathx.ErrPathCollision) {
		t.Fatalf("portable collision error = %v", err)
	}

	nested := filepath.Join(p.source, "secret-nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "init")
	writeTestFile(t, filepath.Join(nested, ".env"), "nested")
	if _, err := Expand(p.source, []Pattern{{Value: "secret-*/**", Source: "test"}}, p.limits); err == nil {
		t.Fatal("nested repository contents were expanded")
	}

	bare := filepath.Join(p.source, "secret-bare.git")
	runGit(t, p.source, "init", "--bare", bare)
	if _, err := Expand(p.source, []Pattern{{Value: "secret-bare.git/**", Source: "test"}}, p.limits); err == nil {
		t.Fatal("nested bare repository contents were expanded")
	}
}

func TestPrepareSourceEnforcesPerFileAndTotalLimits(t *testing.T) {
	p := newRepoPair(t)
	writeTestFile(t, filepath.Join(p.source, ".env"), "12345")
	limits := p.limits
	limits.MaxFileBytes = 4
	if _, report, err := PrepareSource(t.Context(), SourceOptions{
		Checkout: p.source, Binding: p.binding, Patterns: []Pattern{{Value: ".env", Source: "test"}}, Limits: limits,
	}); !errors.Is(err, ErrPlanBlocked) || report.Files[0].State != StateFailed {
		t.Fatalf("file limit report = %+v, %v", report, err)
	}
}
