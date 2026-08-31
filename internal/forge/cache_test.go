package forge_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forge"
)

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "remotes.json")
	in := []forge.RemoteRepo{{Forge: forge.GitHub, FullName: "owner/repo", Name: "repo"}}
	if err := forge.SaveCache(path, in); err != nil {
		t.Fatal(err)
	}
	got, ok := forge.LoadCache(path, time.Hour)
	if !ok || len(got.Repos) != 1 || got.Repos[0].FullName != "owner/repo" {
		t.Errorf("cache = %+v, ok=%v", got, ok)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("remote inventory can contain private repo names; mode = %o, want private", info.Mode().Perm())
	}
}

func TestCacheExpiryAndMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	if err := forge.SaveCache(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := forge.LoadCache(path, -time.Second); !ok {
		// Negative maxAge intentionally disables expiry, matching zero.
		t.Error("negative maxAge should not expire")
	}
	// A nanosecond TTL is certainly stale after the write.
	time.Sleep(time.Millisecond)
	if _, ok := forge.LoadCache(path, time.Nanosecond); ok {
		t.Error("stale cache should not be returned")
	}
	os.WriteFile(path, []byte("not json"), 0o600)
	if _, ok := forge.LoadCache(path, time.Hour); ok {
		t.Error("malformed cache should be ignored")
	}
	if _, ok := forge.LoadCache(filepath.Join(t.TempDir(), "missing"), time.Hour); ok {
		t.Error("missing cache should be ignored")
	}
}

func TestCacheFreshnessRequiresMatchingSource(t *testing.T) {
	now := time.Now().UTC()
	cache := forge.Cache{
		Version: forge.CacheVersion, SourceID: "source-a", FetchedAt: now, Complete: true,
	}
	if !cache.FreshFor(time.Hour, "source-a") {
		t.Fatal("matching cache source was not fresh")
	}
	if cache.FreshFor(time.Hour, "source-b") || cache.FreshFor(time.Hour, "") {
		t.Fatal("cache remained fresh for a different or absent source identity")
	}
}

func TestLegacyCacheIsUsableButNeverFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	data := `{"fetched_at":"2026-08-28T00:00:00Z","repos":[{"forge":"github","name":"repo","full_name":"owner/repo"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, ok := forge.LoadCacheAny(path)
	if !ok || len(cache.Repos) != 1 || cache.Complete || cache.Fresh(0) {
		t.Fatalf("legacy cache = %+v, ok=%v", cache, ok)
	}
	if _, ok := forge.LoadCache(path, time.Hour); ok {
		t.Fatal("legacy capped cache must trigger refresh")
	}
}

func TestCacheRejectsUnsafeAndOversizedPayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	unsafe := `{"version":2,"fetched_at":"2026-08-31T00:00:00Z","complete":true,"repos":[{"forge":"github","name":"../outside","full_name":"owner/outside"}]}`
	if err := os.WriteFile(path, []byte(unsafe), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := forge.LoadCacheAny(path); ok {
		t.Fatal("cache with a path-traversing repository name was accepted")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((32 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := forge.LoadCacheAny(path); ok {
		t.Fatal("oversized cache was accepted")
	}
}

func TestCanceledCacheSaveDoesNotCreateTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := forge.SaveCacheStateContext(ctx, path, forge.Cache{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("save error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("canceled save created target: %v", err)
	}
}

func TestSaveCacheRejectsEmptyPath(t *testing.T) {
	if err := forge.SaveCache("", nil); err == nil {
		t.Error("empty path should error")
	}
}
