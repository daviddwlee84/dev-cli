package forge

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Cache is the on-disk forge inventory. It exists for UI latency, not as a
// source of truth: the REMOTE tab should open instantly, and r refreshes it.
type Cache struct {
	Version   int                     `json:"version,omitempty"`
	FetchedAt time.Time               `json:"fetched_at"`
	Complete  bool                    `json:"complete,omitempty"`
	Providers map[Kind]ProviderStatus `json:"providers,omitempty"`
	Repos     []RemoteRepo            `json:"repos"`
}

const CacheVersion = 2

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
	b, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, false
	}
	var c Cache
	if json.Unmarshal(b, &c) != nil || c.FetchedAt.IsZero() {
		return Cache{}, false
	}
	if c.Version == 0 {
		// Version 1 caches were valid snapshots but capped per provider, so they
		// are useful for instant display and must immediately be refreshed.
		c.Complete = false
	}
	return c, true
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
	if path == "" {
		return errors.New("empty cache path")
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
	return os.Rename(name, path)
}

// CacheExists reports whether any cache file exists, even stale. Used only for
// diagnostics.
func CacheExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}
