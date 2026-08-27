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
	FetchedAt time.Time    `json:"fetched_at"`
	Repos     []RemoteRepo `json:"repos"`
}

// LoadCache returns a fresh cache, or false when it is missing, malformed or
// older than maxAge. A stale cache is deliberately not returned: showing a
// deleted private repo as current is more confusing than a brief reload.
func LoadCache(path string, maxAge time.Duration) (Cache, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, false
	}
	var c Cache
	if json.Unmarshal(b, &c) != nil || c.FetchedAt.IsZero() {
		return Cache{}, false
	}
	if maxAge > 0 && time.Since(c.FetchedAt) > maxAge {
		return Cache{}, false
	}
	return c, true
}

// SaveCache atomically replaces the cache. A partial forge response may be
// cached — its FetchedAt still says exactly when it was observed.
func SaveCache(path string, repos []RemoteRepo) error {
	if path == "" {
		return errors.New("empty cache path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(Cache{FetchedAt: time.Now().UTC(), Repos: repos}, "", "  ")
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
