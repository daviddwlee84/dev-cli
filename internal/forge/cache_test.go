package forge_test

import (
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

func TestSaveCacheRejectsEmptyPath(t *testing.T) {
	if err := forge.SaveCache("", nil); err == nil {
		t.Error("empty path should error")
	}
}
