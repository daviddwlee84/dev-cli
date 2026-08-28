package pathx_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

func TestCanonicalChildAcceptsChildAndRejectsRoot(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := pathx.CanonicalChild(root, child)
	if err != nil {
		t.Fatalf("CanonicalChild: %v", err)
	}
	want, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("CanonicalChild = %q, want %q", got, want)
	}
	if _, err := pathx.CanonicalChild(root, root); !errors.Is(err, pathx.ErrRoot) {
		t.Errorf("root itself should be rejected with ErrRoot, got %v", err)
	}
}

func TestCanonicalChildRejectsSiblingPrefixAndTraversal(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	sibling := filepath.Join(parent, "repo-archive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := pathx.CanonicalChild(root, sibling); !errors.Is(err, pathx.ErrOutsideRoot) {
		t.Errorf("sibling prefix should not count as containment, got %v", err)
	}
	traversal := filepath.Join(root, "child") + string(filepath.Separator) + ".." + string(filepath.Separator) + "other"
	if _, err := pathx.CanonicalChild(root, traversal); !errors.Is(err, pathx.ErrTraversal) {
		t.Errorf("explicit .. should be rejected before cleaning, got %v", err)
	}
}

func TestValidateComponent(t *testing.T) {
	for _, good := range []string{"Infra", "redis-streams", "name with spaces"} {
		if err := pathx.ValidateComponent(good); err != nil {
			t.Errorf("ValidateComponent(%q): %v", good, err)
		}
	}
	for _, bad := range []string{
		"", "   ", ".", "..", "/absolute", "C:relative", "two/parts", `two\parts`, "bad\x00name",
	} {
		if err := pathx.ValidateComponent(bad); !errors.Is(err, pathx.ErrInvalidComponent) {
			t.Errorf("ValidateComponent(%q) = %v, want ErrInvalidComponent", bad, err)
		}
	}
}

func TestCanonicalChildRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := pathx.CanonicalChild(root, filepath.Join(link, "new")); !errors.Is(err, pathx.ErrOutsideRoot) {
		t.Errorf("symlink escape should be rejected, got %v", err)
	}
}

func TestCanonicalChildRejectsDanglingSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "not-created-yet")
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := pathx.CanonicalChild(root, filepath.Join(link, "new")); !errors.Is(err, pathx.ErrOutsideRoot) {
		t.Errorf("dangling symlink escape should be rejected, got %v", err)
	}
}

func TestCanonicalChildRejectsTraversalInsideDanglingSymlinkTarget(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	inside := filepath.Join(root, "inside")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	pivot := filepath.Join(inside, "pivot")
	if err := os.Symlink(filepath.Join(outside, "not-created"), pivot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	escape := filepath.Join(root, "escape")
	separator := string(filepath.Separator)
	rawTarget := "inside" + separator + "pivot" + separator + ".." + separator + "payload"
	if err := os.Symlink(rawTarget, escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := pathx.CanonicalChild(root, escape); !errors.Is(err, pathx.ErrTraversal) {
		t.Errorf("unresolved target traversal should be rejected, got %v", err)
	}
}

func TestCanonicalChildResolvesNearestExistingAncestor(t *testing.T) {
	parent := t.TempDir()
	physical := filepath.Join(parent, "physical")
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "root-link")
	if err := os.Symlink(physical, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := pathx.JoinChild(root, "category", "new-repo")
	if err != nil {
		t.Fatalf("JoinChild with missing destination: %v", err)
	}
	canonicalPhysical, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalPhysical, "category", "new-repo")
	if got != want {
		t.Errorf("JoinChild = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Errorf("path validation must not create the destination, stat error = %v", err)
	}
}

func TestJoinChildCanonicalizesRootBeforeJoining(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	uncleanRoot := filepath.Join(parent, "unused", "..", "root")
	got, err := pathx.JoinChild(uncleanRoot, "new")
	if err != nil {
		t.Fatalf("JoinChild with a cleanable root: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(canonicalRoot, "new"); got != want {
		t.Errorf("JoinChild = %q, want %q", got, want)
	}
}
