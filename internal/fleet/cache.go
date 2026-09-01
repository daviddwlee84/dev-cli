package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
)

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
	parts := []string{host.Name, host.MachineID, host.SSHAlias, host.Hostname, host.User, strconv.Itoa(host.Port), host.IdentityFile, host.DevPath}
	parts = append(parts, time.Duration(host.ConnectTimeout.Duration).String(), time.Duration(host.CommandTimeout.Duration).String())
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func SaveCache(host Host, snapshot Snapshot) error {
	path := cachePath(host)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload := cacheFile{Version: 1, EndpointID: EndpointID(host), FetchedAt: time.Now().UTC(), Snapshot: snapshot}
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
	return os.Rename(name, path)
}

func LoadCache(host Host) (Snapshot, time.Time, bool) {
	data, err := os.ReadFile(cachePath(host))
	if err != nil {
		return Snapshot{}, time.Time{}, false
	}
	var payload cacheFile
	if json.Unmarshal(data, &payload) != nil || payload.Version != 1 || payload.EndpointID != EndpointID(host) || payload.FetchedAt.IsZero() {
		return Snapshot{}, time.Time{}, false
	}
	if payload.Snapshot.SchemaVersion != SnapshotSchemaVersion {
		return Snapshot{}, time.Time{}, false
	}
	return payload.Snapshot, payload.FetchedAt, true
}
