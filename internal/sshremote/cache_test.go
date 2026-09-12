package sshremote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
)

func cacheTestDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "private-cache")
}

func TestCacheReadsAbsentWithoutCreatingAnything(t *testing.T) {
	dir := cacheTestDir(t)
	host := fleet.Host{Name: "lab", SSHAlias: "lab"}
	if _, found, err := LoadCache(context.Background(), dir, host, time.Now()); err != nil || found {
		t.Fatalf("absent cache %v %v", found, err)
	}
	if rows, err := ReadCache(context.Background(), dir, time.Now()); err != nil || len(rows) != 0 {
		t.Fatalf("readall %v %v", rows, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("read created cache: %v", err)
	}
}

func TestCacheRoundTripSourceBindingStalenessAndResolvedData(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	profile, _ := inventory.Find("target")
	selection := profile.Selection(inventory.Origin)
	resolved, err := server.Resolve(context.Background(), ResolveRequest{Request: NewRequest(inventory.Origin), Selection: selection})
	if err != nil {
		t.Fatal(err)
	}
	dir := cacheTestDir(t)
	host := fleet.Host{Name: "lab/one", SSHAlias: "lab"}
	if err := SaveCache(context.Background(), dir, host, inventory); err != nil {
		t.Fatal(err)
	}
	if err := SaveResolvedCache(context.Background(), dir, host, resolved); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := LoadCache(context.Background(), dir, host, time.Now())
	if err != nil || !found || loaded.Origin != inventory.Origin {
		t.Fatalf("load %v %v", found, err)
	}
	route, found, err := LoadResolvedCache(context.Background(), dir, host, selection, time.Now())
	if err != nil || !found || route.Effective.Values != nil || len(route.RouteProfiles) != 1 {
		t.Fatalf("resolved cache %v %v", found, err)
	}
	rows, err := ReadCache(context.Background(), dir, time.Now().Add(CacheTTL+time.Second))
	if err != nil || len(rows) != 1 || !rows[0].Stale || rows[0].HostName != host.Name {
		t.Fatalf("stale/source rows=%#v err=%v", rows, err)
	}
	changed := host
	changed.User = "different-user"
	if _, found, err := LoadCache(context.Background(), dir, changed, time.Now()); err != nil || found {
		t.Fatalf("different endpoint reused cache %v %v", found, err)
	}
	selection.Fingerprint = sourceDigest("changed")
	if _, found, err := LoadResolvedCache(context.Background(), dir, host, selection, time.Now()); !errors.Is(err, ErrSourceChanged) || found {
		t.Fatalf("changed source reused resolved %v %v", found, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(dir)
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("cache dir mode %o", info.Mode().Perm())
		}
	}
}

func TestCacheCorruptionAndUnsafePathsDoNotBecomeEmptySuccess(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	dir := cacheTestDir(t)
	host := fleet.Host{Name: "lab", SSHAlias: "lab"}
	if err := SaveCache(context.Background(), dir, host, inventory); err != nil {
		t.Fatal(err)
	}
	name := cacheFilename("inventory", fleet.EndpointID(host), "")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := LoadCache(context.Background(), dir, host, time.Now()); !errors.Is(err, ErrInvalidData) || found {
		t.Fatalf("corruption %v %v", found, err)
	}
	if _, err := ReadCache(context.Background(), dir, time.Now()); err == nil {
		t.Fatal("corrupt cache became complete empty")
	}
	if runtime.GOOS == "windows" {
		return
	}
	unsafe := cacheTestDir(t)
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveCache(context.Background(), unsafe, host, inventory); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("public directory accepted: %v", err)
	}
	link := cacheTestDir(t)
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCache(context.Background(), link, host, time.Now()); err == nil {
		t.Fatal("symlink cache followed")
	}
}

func TestCacheCanceledWriteNeverCreatesDirectory(t *testing.T) {
	server, _, _ := testServer(t, "Host target\n HostName target.example\n")
	inventory := testInventory(t, server)
	dir := cacheTestDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SaveCache(ctx, dir, fleet.Host{Name: "lab", SSHAlias: "lab"}, inventory); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("canceled cache write created directory")
	}
}
