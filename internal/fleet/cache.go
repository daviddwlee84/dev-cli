package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

const (
	cacheVersion      = 1
	maxCacheBytes     = 32 << 20
	maxCachedRepos    = 100_000
	maxCacheClockSkew = 5 * time.Minute
)

var cacheWriteLocks sync.Map

type cacheFile struct {
	Version    int       `json:"version"`
	EndpointID string    `json:"endpoint_id"`
	FetchedAt  time.Time `json:"fetched_at"`
	Snapshot   Snapshot  `json:"snapshot"`
}

func CacheRoot() string { return filepath.Join(devconfig.CacheHome(), "dev", "fleet", "v1") }

func cachePath(host Host) string {
	name := devconfig.Slug(host.Name)
	return filepath.Join(CacheRoot(), name+".json")
}

func EndpointID(host Host) string {
	parts := []string{
		host.Name, host.MachineID, host.SSHAlias, host.Hostname, host.User, strconv.Itoa(host.Port),
		host.IdentityFile, host.DevPath, host.EffectiveRemoteOS(),
	}
	parts = append(parts, time.Duration(host.ConnectTimeout.Duration).String(), time.Duration(host.CommandTimeout.Duration).String())
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func SaveCache(host Host, snapshot Snapshot) error {
	return SaveCacheContext(context.Background(), host, snapshot)
}

// SaveCacheContext serializes one endpoint and checks cancellation while
// holding the ordering boundary, so an older request cannot rename last.
func SaveCacheContext(ctx context.Context, host Host, snapshot Snapshot) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path := cachePath(host)
	lock := cacheWriteLock(path)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload := cacheFile{Version: cacheVersion, EndpointID: EndpointID(host), FetchedAt: time.Now().UTC(), Snapshot: snapshot}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".fleet-*.tmp")
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
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func cacheWriteLock(path string) *sync.Mutex {
	loaded, _ := cacheWriteLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return loaded.(*sync.Mutex)
}

func LoadCache(host Host) (Snapshot, time.Time, bool) {
	path := cachePath(host)
	info, err := os.Stat(path)
	if err != nil || info.Size() < 0 || info.Size() > maxCacheBytes {
		return Snapshot{}, time.Time{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, time.Time{}, false
	}
	var payload cacheFile
	now := time.Now().UTC()
	if json.Unmarshal(data, &payload) != nil || payload.Version != cacheVersion ||
		payload.EndpointID != EndpointID(host) || payload.FetchedAt.IsZero() ||
		payload.FetchedAt.After(now.Add(maxCacheClockSkew)) || !validSnapshot(payload.Snapshot, now) {
		return Snapshot{}, time.Time{}, false
	}
	return payload.Snapshot, payload.FetchedAt, true
}

func validSnapshot(snapshot Snapshot, now time.Time) bool {
	if snapshot.SchemaVersion != SnapshotSchemaVersion || snapshot.GeneratedAt.IsZero() ||
		snapshot.GeneratedAt.After(now.Add(maxCacheClockSkew)) || len(snapshot.Repositories) > maxCachedRepos ||
		!validFleetString(snapshot.Host, 1024, true) || !validFleetString(snapshot.DevVersion, 1024, false) ||
		!validFleetString(snapshot.Runtime, 128, false) {
		return false
	}
	for _, repository := range snapshot.Repositories {
		if !validFleetString(repository.Name, 1024, true) ||
			!validFleetString(repository.Display, 2048, true) ||
			!validFleetString(repository.Category, 1024, false) ||
			!validFleetString(repository.Path, 16<<10, true) ||
			!validFleetString(repository.RealPath, 16<<10, false) ||
			!validFleetString(repository.Branch, 4096, false) ||
			!validFleetString(repository.Runtime, 128, false) ||
			!validFleetString(repository.RuntimeHandle, 4096, false) ||
			!validFleetString(repository.AgentStatus, 128, false) ||
			!validGitStatus(repository.Status, now) || repository.Worktrees < 0 || repository.Tasks.Hot < 0 || repository.Tasks.Warm < 0 ||
			repository.Tasks.Cold < 0 || repository.Tasks.Done < 0 ||
			repository.LastActivity.After(now.Add(maxCacheClockSkew)) || len(repository.RemoteIdentities) > 1024 {
			return false
		}
		for _, identity := range repository.RemoteIdentities {
			if !validFleetString(identity, 8<<10, true) {
				return false
			}
		}
	}
	return true
}

func validGitStatus(status gitx.Status, now time.Time) bool {
	if status.Ahead < 0 || status.Behind < 0 || status.Changed < 0 || status.Staged < 0 ||
		status.Unstaged < 0 || status.Untracked < 0 || status.Added < 0 || status.Modified < 0 ||
		status.Deleted < 0 || status.Renamed < 0 || status.Conflicted < 0 ||
		status.LatestChange.After(now.Add(maxCacheClockSkew)) {
		return false
	}
	return validFleetString(status.Branch, 4096, false) && validFleetString(status.Upstream, 4096, false)
}

func validFleetString(value string, limit int, required bool) bool {
	return (!required || strings.TrimSpace(value) != "") && len(value) <= limit && !strings.ContainsRune(value, '\x00')
}
