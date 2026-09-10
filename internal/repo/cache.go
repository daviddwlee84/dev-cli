package repo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// CachedRow holds presentation only: no task, runtime or action authority.
type CachedRow struct {
	Repo         Repo
	Status       gitx.Status
	GitKnown     bool
	LastActivity time.Time
	Try          bool
}
type Snapshot struct {
	Version     int
	Fingerprint [32]byte
	ObservedAt  time.Time
	Rows        []CachedRow
}

func SnapshotKey(roots []string, state string) [32]byte {
	data, _ := json.Marshal(struct {
		Roots []string
		State string
	}{roots, state})
	return sha256.Sum256(data)
}
func ReadSnapshot(ctx context.Context, path string, key [32]byte) (Snapshot, error) {
	data, err := safefile.ReadRegular(ctx, path, 8<<20)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version != 1 || snapshot.Fingerprint != key || snapshot.ObservedAt.IsZero() {
		return Snapshot{}, errors.New("repository cache does not match this discovery configuration")
	}
	for _, r := range snapshot.Rows {
		if !filepath.IsAbs(r.Repo.Path) || r.Repo.Name == "" {
			return Snapshot{}, errors.New("invalid cached repository")
		}
	}
	return snapshot, nil
}
func WriteSnapshot(path string, snapshot Snapshot) error {
	snapshot.Version = 1
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("repository cache exceeds 8 MiB")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".repos-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
