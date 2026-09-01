package fleet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lease"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func syncFixture(t *testing.T) (source, target, identity, oid string, cfg devconfig.Config) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gitRun(t, root, "init", "--bare", "--initial-branch=main", bare)
	remoteURL := "https://example.test/team/repo.git"
	for _, name := range []string{"source", "target"} {
		path := filepath.Join(root, name)
		gitRun(t, root, "clone", bare, path)
		gitRun(t, path, "config", "user.email", "dev@example.test")
		gitRun(t, path, "config", "user.name", "dev test")
		gitRun(t, path, "remote", "set-url", "origin", remoteURL)
		gitRun(t, path, "config", "url.file://"+bare+".insteadOf", remoteURL)
	}
	source, target = filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "add", "README.md")
	gitRun(t, source, "commit", "-m", "one")
	gitRun(t, source, "push", "-u", "origin", "main")
	gitRun(t, target, "fetch", "origin")
	gitRun(t, target, "switch", "-C", "main", "origin/main")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "commit", "-am", "two")
	gitRun(t, source, "push")
	oid = stringsTrim(gitRun(t, source, "rev-parse", "HEAD"))
	identity = catalog.NormalizeRemoteIdentity(remoteURL)
	cfg = devconfig.Default()
	cfg.Paths.RepoPaths = []string{target}
	cfg.Paths.ScanRoots = nil
	return
}

func stringsTrim(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}

func TestApplySyncFastForwardsCleanCheckedOutBranch(t *testing.T) {
	_, target, identity, oid, cfg := syncFixture(t)
	result := ApplySync(context.Background(), cfg, SyncRequest{RemoteIdentity: identity, Branch: "main", ExpectedOID: oid})
	if result.State != SyncUpdated {
		t.Fatalf("ApplySync = %+v", result)
	}
	if got := stringsTrim(gitRun(t, target, "rev-parse", "HEAD")); got != oid {
		t.Fatalf("target HEAD = %s, want %s", got, oid)
	}
}

func TestApplySyncUsesCanonicalCheckoutLease(t *testing.T) {
	_, target, identity, oid, cfg := syncFixture(t)
	repository, err := gitx.Discover(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	authority := lease.New(filepath.Join(t.TempDir(), "leases"))
	key := lease.BranchKey(lease.GitCommonDirIdentity(repository.GitCommonDir), "main")
	_, err = authority.Reserve(t.Context(), []lease.Key{key}, lease.Request{
		OperationID: "portable-files-test", Digest: assessment.FingerprintBytes([]byte("test")),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := stringsTrim(gitRun(t, target, "rev-parse", "HEAD"))
	result := ApplySyncWithAuthority(t.Context(), cfg, SyncRequest{RemoteIdentity: identity, Branch: "main", ExpectedOID: oid}, authority)
	if result.State != SyncFailed {
		t.Fatalf("reserved checkout sync = %+v", result)
	}
	if got := stringsTrim(gitRun(t, target, "rev-parse", "HEAD")); got != before {
		t.Fatalf("leased target moved from %s to %s", before, got)
	}
}

func TestApplySyncDoesNotTouchDirtyCheckout(t *testing.T) {
	_, target, identity, oid, cfg := syncFixture(t)
	before := stringsTrim(gitRun(t, target, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := ApplySync(context.Background(), cfg, SyncRequest{RemoteIdentity: identity, Branch: "main", ExpectedOID: oid})
	if result.State != SyncDirty {
		t.Fatalf("ApplySync = %+v", result)
	}
	if got := stringsTrim(gitRun(t, target, "rev-parse", "HEAD")); got != before {
		t.Fatalf("dirty target moved from %s to %s", before, got)
	}
}
