package agentskill

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type checkedEntry struct {
	Status UpdateStatus
	Detail string
	At     time.Time
}
type checkCache struct {
	Version int
	Entries map[string]checkedEntry
}

var checkCacheMu sync.Mutex

func CheckCachePath() string { return filepath.Join(config.CacheHome(), "dev", "skill-checks-v1.json") }
func checkKey(ctx context.Context, row Skill) string {
	if row.Lock == nil {
		return ""
	}
	data, err := safefile.ReadRegular(ctx, row.Lock.File, maxLockBytes)
	if err != nil {
		return ""
	}
	metadata, _ := json.Marshal(row.Lock)
	return fmt.Sprintf("%x", sha256.Sum256(append(append([]byte(row.ScopeRoot+string(row.Scope)), metadata...), data...)))
}
func readChecks(ctx context.Context) checkCache {
	cache := checkCache{Version: 1, Entries: map[string]checkedEntry{}}
	data, err := safefile.ReadRegular(ctx, CheckCachePath(), 4<<20)
	if err == nil {
		var loaded checkCache
		if json.Unmarshal(data, &loaded) == nil && loaded.Version == 1 && loaded.Entries != nil {
			cache = loaded
		}
	}
	return cache
}

// CachedChecks supplies dated display evidence only. A lock edit invalidates it.
func CachedChecks(ctx context.Context, rows []Skill) []Skill {
	cache := readChecks(ctx)
	out := append([]Skill(nil), rows...)
	for i, row := range out {
		if entry, ok := cache.Entries[checkKey(ctx, row)]; ok && !entry.At.IsZero() && !entry.At.After(time.Now().Add(time.Minute)) {
			out[i].UpdateStatus, out[i].UpdateDetail, out[i].UpdateCheckedAt = entry.Status, entry.Detail, entry.At
		}
	}
	return out
}
func saveChecks(ctx context.Context, rows []Skill) {
	checkCacheMu.Lock()
	defer checkCacheMu.Unlock()
	cache := readChecks(ctx)
	changed := false
	for _, row := range rows {
		if key := checkKey(ctx, row); key != "" && !row.UpdateCheckedAt.IsZero() {
			changed = true
			cache.Entries[key] = checkedEntry{row.UpdateStatus, gitx.SafeDiagnosticText(row.UpdateDetail), row.UpdateCheckedAt}
		}
	}
	if !changed {
		return
	}
	if len(cache.Entries) > 4096 {
		keys := make([]string, 0, len(cache.Entries))
		for key := range cache.Entries {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return cache.Entries[keys[i]].At.After(cache.Entries[keys[j]].At) })
		for _, key := range keys[4096:] {
			delete(cache.Entries, key)
		}
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	path := CheckCachePath()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".skill-checks-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return
	}
	if file.Close() != nil {
		return
	}
	_ = os.Rename(file.Name(), path)
}
