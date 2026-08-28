package diskusage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/diskusage"
)

func cacheUsage(at time.Time) diskusage.Usage {
	private, total := int64(20), int64(120)
	return diskusage.Usage{
		CheckoutBytes: 100, PrivateGitBytes: &private, OwnedBytes: 120, TotalBytes: &total,
		Complete: true, MeasuredAt: at,
	}
}

func TestCacheRoundTripTTLPermissionsAndInvalidation(t *testing.T) {
	now := time.Date(2026, time.August, 28, 2, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cache", "sizes-v1.json")
	target := diskusage.Plain("/tmp/example")
	cache := diskusage.NewCache(path, 10*time.Minute)
	cache.Clock = func() time.Time { return now }
	if err := cache.Set(target, cacheUsage(now)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v, want 0600", info.Mode().Perm())
	}

	loaded := diskusage.NewCache(path, 10*time.Minute)
	loaded.Clock = func() time.Time { return now.Add(5 * time.Minute) }
	usage, ok, err := loaded.Get(target)
	if err != nil || !ok || !usage.Cached || usage.OwnedBytes != 120 {
		t.Fatalf("fresh cache = %+v, %t, %v", usage, ok, err)
	}
	loaded.Clock = func() time.Time { return now.Add(11 * time.Minute) }
	if _, ok, err := loaded.Get(target); err != nil || ok {
		t.Fatalf("stale cache ok=%t err=%v", ok, err)
	}
	if err := loaded.Invalidate(target); err != nil {
		t.Fatal(err)
	}
	loaded.Clock = func() time.Time { return now }
	if _, ok, err := loaded.Get(target); err != nil || ok {
		t.Fatalf("invalidated cache ok=%t err=%v", ok, err)
	}
}

func TestCacheIgnoresUnknownVersionAndRecoversFromMalformedFile(t *testing.T) {
	dir := t.TempDir()
	target := diskusage.Plain("/tmp/example")
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"version":99,"entries":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := diskusage.NewCache(unknown, time.Hour).Get(target); err != nil || ok {
		t.Fatalf("unknown cache version ok=%t err=%v", ok, err)
	}

	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := diskusage.NewCache(malformed, time.Hour)
	if _, _, err := cache.Get(target); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed cache error = %v", err)
	}
	now := time.Now().UTC()
	if err := cache.Set(target, cacheUsage(now)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := diskusage.NewCache(malformed, time.Hour).Get(target); err != nil || !ok {
		t.Fatalf("rewritten malformed cache ok=%t err=%v", ok, err)
	}
}

func TestTargetSignatureChangesWhenDirectChildrenChange(t *testing.T) {
	root := t.TempDir()
	before := diskusage.Plain(root)
	if err := os.WriteFile(filepath.Join(root, "new-file"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(root, future, future); err != nil {
		t.Fatal(err)
	}
	after := diskusage.Plain(root)
	if before.Signature == after.Signature || before.Key == after.Key {
		t.Fatalf("direct child change did not invalidate target: before=%+v after=%+v", before, after)
	}
}

func TestUsageValidationRejectsSharedTotalAndWrongOwnedBytes(t *testing.T) {
	now := time.Now().UTC()
	shared, total := int64(5), int64(10)
	usage := diskusage.Usage{
		CheckoutBytes: 10, OwnedBytes: 9, Complete: true, MeasuredAt: now,
	}
	if err := usage.Validate(); err == nil {
		t.Fatal("wrong owned bytes validated")
	}
	usage.OwnedBytes = 10
	usage.SharedGitBytes, usage.TotalBytes = &shared, &total
	if err := usage.Validate(); err == nil {
		t.Fatal("usage with shared and total bytes validated")
	}
}
