package diskusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const CacheVersion = 1

type cacheFile struct {
	Version int              `json:"version"`
	Entries map[string]Usage `json:"entries"`
}

// Cache is derived latency state, never a source of truth.
type Cache struct {
	Path  string
	TTL   time.Duration
	Clock func() time.Time

	mu      sync.Mutex
	loaded  bool
	entries map[string]Usage
}

// NewCache returns a lazy-loading cache.
func NewCache(path string, ttl time.Duration) *Cache {
	return &Cache{Path: path, TTL: ttl, Clock: time.Now}
}

// Get returns only fresh, structurally valid measurements.
func (c *Cache) Get(target Target) (Usage, bool, error) {
	if c == nil {
		return Usage{}, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return Usage{}, false, err
	}
	usage, ok := c.entries[targetKey(target)]
	if !ok || (c.TTL > 0 && c.now().Sub(usage.MeasuredAt) > c.TTL) {
		return Usage{}, false, nil
	}
	if err := usage.Validate(); err != nil {
		return Usage{}, false, fmt.Errorf("cached disk usage: %w", err)
	}
	usage = cloneUsage(usage)
	usage.Cached = true
	return usage, true, nil
}

// Set updates memory. Call Save once after a measurement batch to avoid one
// filesystem rewrite per row.
func (c *Cache) Set(target Target, usage Usage) error {
	if c == nil {
		return nil
	}
	usage.Cached = false
	if err := usage.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return err
	}
	c.entries[targetKey(target)] = cloneUsage(usage)
	return nil
}

// Invalidate removes targets from memory and the next saved cache snapshot.
func (c *Cache) Invalidate(targets ...Target) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return err
	}
	for _, target := range targets {
		delete(c.entries, targetKey(target))
	}
	return c.saveLocked()
}

// Save atomically persists the current snapshot with private permissions.
func (c *Cache) Save() error {
	if c == nil || c.Path == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return err
	}
	return c.saveLocked()
}

func (c *Cache) loadLocked() error {
	if c.loaded {
		return nil
	}
	c.loaded = true
	c.entries = map[string]Usage{}
	if c.Path == "" {
		return nil
	}
	data, err := os.ReadFile(c.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read disk usage cache: %w", err)
	}
	var payload cacheFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode disk usage cache: %w", err)
	}
	if payload.Version != CacheVersion {
		return nil
	}
	for key, usage := range payload.Entries {
		if key == "" || usage.Validate() != nil {
			continue
		}
		usage.Cached = false
		c.entries[key] = usage
	}
	return nil
}

func (c *Cache) saveLocked() error {
	if c.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return fmt.Errorf("create disk usage cache directory: %w", err)
	}
	payload := cacheFile{Version: CacheVersion, Entries: c.entries}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode disk usage cache: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(c.Path), ".sizes-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Rename(name, c.Path); err != nil {
		return fmt.Errorf("replace disk usage cache: %w", err)
	}
	return nil
}

func (c *Cache) now() time.Time {
	if c.Clock == nil {
		return time.Now().UTC()
	}
	return c.Clock().UTC()
}

func targetKey(target Target) string {
	if target.Key != "" {
		return target.Key
	}
	return target.CacheKey()
}

func cloneUsage(usage Usage) Usage {
	clone := usage
	if usage.PrivateGitBytes != nil {
		value := *usage.PrivateGitBytes
		clone.PrivateGitBytes = &value
	}
	if usage.SharedGitBytes != nil {
		value := *usage.SharedGitBytes
		clone.SharedGitBytes = &value
	}
	if usage.TotalBytes != nil {
		value := *usage.TotalBytes
		clone.TotalBytes = &value
	}
	return clone
}
