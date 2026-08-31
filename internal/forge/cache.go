package forge

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// Cache is the on-disk forge inventory. It exists for UI latency, not as a
// source of truth: the REMOTE tab should open instantly, and r refreshes it.
type Cache struct {
	Version   int                     `json:"version,omitempty"`
	SourceID  string                  `json:"source_id,omitempty"`
	FetchedAt time.Time               `json:"fetched_at"`
	Complete  bool                    `json:"complete,omitempty"`
	Providers map[Kind]ProviderStatus `json:"providers,omitempty"`
	Repos     []RemoteRepo            `json:"repos"`
}

const (
	CacheVersion      = 2
	maxCacheBytes     = 32 << 20
	maxCachedRepos    = 100_000
	maxCacheClockSkew = 5 * time.Minute
)

var cacheWriteLocks sync.Map

type ProviderStatus struct {
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	Complete  bool      `json:"complete"`
	Error     string    `json:"error,omitempty"`
}

// LoadCache returns only a fresh, complete cache. TUI callers that can label
// stale rows while refreshing use LoadCacheAny instead.
func LoadCache(path string, maxAge time.Duration) (Cache, bool) {
	c, ok := LoadCacheAny(path)
	if !ok || !c.Fresh(maxAge) {
		return Cache{}, false
	}
	return c, true
}

// LoadCacheAny returns a cache regardless of age or completeness. This powers
// stale-while-revalidate UI behavior; callers must inspect Fresh/Complete.
func LoadCacheAny(path string) (Cache, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < 0 || info.Size() > maxCacheBytes {
		return Cache{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, false
	}
	var c Cache
	if json.Unmarshal(b, &c) != nil || !validCache(c, time.Now().UTC()) {
		return Cache{}, false
	}
	if c.Version == 0 {
		// Version 1 caches were valid snapshots but capped per provider, so they
		// are useful for instant display and must immediately be refreshed.
		c.Complete = false
	}
	return c, true
}

func validCache(c Cache, now time.Time) bool {
	if (c.Version != 0 && c.Version != CacheVersion) || c.FetchedAt.IsZero() ||
		c.FetchedAt.After(now.Add(maxCacheClockSkew)) || len(c.Repos) > maxCachedRepos ||
		!validCacheString(c.SourceID, 128, false) {
		return false
	}
	for kind, status := range c.Providers {
		if !validForgeKind(kind) || len(status.Error) > 16<<10 || strings.ContainsRune(status.Error, '\x00') ||
			status.FetchedAt.After(now.Add(maxCacheClockSkew)) {
			return false
		}
	}
	for _, repository := range c.Repos {
		if !validForgeKind(repository.Forge) || pathx.ValidateComponent(repository.Name) != nil ||
			!validCacheString(repository.FullName, 2048, true) ||
			!validCacheString(repository.Description, 64<<10, false) ||
			!validCacheString(repository.URL, 8<<10, false) ||
			!validCacheString(repository.CloneURL, 8<<10, false) ||
			!validCacheString(repository.SSHURL, 8<<10, false) ||
			!validCacheString(repository.Visibility, 128, false) ||
			!validCacheString(repository.DefaultBranch, 1024, false) ||
			repository.UpdatedAt.After(now.Add(maxCacheClockSkew)) {
			return false
		}
	}
	return true
}

func validForgeKind(kind Kind) bool {
	return kind == GitHub || kind == GitLab || kind == AzureDevOps
}

func validCacheString(value string, limit int, required bool) bool {
	return (!required || strings.TrimSpace(value) != "") && len(value) <= limit && !strings.ContainsRune(value, '\x00')
}

// FreshFor additionally requires the cache to describe the same configured
// provider endpoints/targets. Caches without a source ID remain displayable but
// are immediately revalidated.
func (c Cache) FreshFor(maxAge time.Duration, sourceID string) bool {
	return sourceID != "" && c.SourceID == sourceID && c.Fresh(maxAge)
}

func (c Cache) Fresh(maxAge time.Duration) bool {
	if c.Version != CacheVersion || !c.Complete {
		return false
	}
	if maxAge <= 0 {
		return true
	}
	if len(c.Providers) == 0 {
		return time.Since(c.FetchedAt) <= maxAge
	}
	for _, provider := range c.Providers {
		if !provider.Complete || provider.Error != "" || provider.FetchedAt.IsZero() || time.Since(provider.FetchedAt) > maxAge {
			return false
		}
	}
	return true
}

// SaveCache atomically replaces the cache. A partial forge response may be
// cached — its FetchedAt still says exactly when it was observed.
func SaveCache(path string, repos []RemoteRepo) error {
	now := time.Now().UTC()
	providers := map[Kind]ProviderStatus{}
	for _, repo := range repos {
		providers[repo.Forge] = ProviderStatus{FetchedAt: now, Complete: true}
	}
	return SaveCacheState(path, Cache{Version: CacheVersion, FetchedAt: now, Complete: true, Providers: providers, Repos: repos})
}

// SaveCacheState atomically replaces the versioned inventory cache.
func SaveCacheState(path string, cache Cache) error {
	return SaveCacheStateContext(context.Background(), path, cache)
}

// SaveCacheStateContext serializes one cache target and checks cancellation
// while holding that ordering boundary. A superseded generation therefore
// cannot rename after a newer generation has committed.
func SaveCacheStateContext(ctx context.Context, path string, cache Cache) error {
	if path == "" {
		return errors.New("empty cache path")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lock := cacheWriteLock(path)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if cache.Version == 0 {
		cache.Version = CacheVersion
	}
	if cache.FetchedAt.IsZero() {
		cache.FetchedAt = time.Now().UTC()
	}
	b, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".remotes-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func cacheWriteLock(path string) *sync.Mutex {
	loaded, _ := cacheWriteLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return loaded.(*sync.Mutex)
}

// CacheExists reports whether any cache file exists, even stale. Used only for
// diagnostics.
func CacheExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}
