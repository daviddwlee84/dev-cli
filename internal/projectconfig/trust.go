package projectconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const TrustStoreVersion = 1

var executionHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// TrustRecord intentionally stores no command, script, config bytes, or
// display metadata. Approval is tied only to canonical repository identity
// and the exact execution-bearing content digest.
type TrustRecord struct {
	Repository    string    `json:"repository"`
	ExecutionHash string    `json:"execution_hash"`
	ApprovedAt    time.Time `json:"approved_at"`
}

type trustFile struct {
	Version int           `json:"version"`
	Records []TrustRecord `json:"records"`
}

// TrustStore is a private, local JSON trust database. Path should live under
// dev's host state directory, never inside the repository.
type TrustStore struct {
	Path  string
	Clock func() time.Time

	mu sync.Mutex
}

// NewTrustStore returns a store at path.
func NewTrustStore(path string) *TrustStore {
	return &TrustStore{Path: path, Clock: time.Now}
}

// CanonicalRepoIdentity returns Git's canonical common directory when path is
// a repository, making every linked worktree share one trust identity. A
// not-yet-initialized project falls back to its canonical physical root.
func CanonicalRepoIdentity(ctx context.Context, repoRoot string) (string, error) {
	root, err := pathx.Canonical(repoRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize repository: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("inspect repository %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %s is not a directory", root)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, err := gitx.Discover(ctx, root)
	if errors.Is(err, gitx.ErrNotARepo) {
		return root, nil
	}
	if err != nil {
		return "", fmt.Errorf("discover repository identity: %w", err)
	}
	identity, err := pathx.Canonical(repository.GitCommonDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	return identity, nil
}

// Approve trusts hash for repoRoot. A new approval replaces every older hash
// for that repository, so changing and later reverting executable config still
// requires a fresh explicit approval.
func (s *TrustStore) Approve(ctx context.Context, repoRoot, hash string) (TrustRecord, error) {
	if err := validateExecutionHash(hash); err != nil {
		return TrustRecord{}, err
	}
	identity, err := CanonicalRepoIdentity(ctx, repoRoot)
	if err != nil {
		return TrustRecord{}, err
	}
	record := TrustRecord{Repository: identity, ExecutionHash: hash, ApprovedAt: s.now()}

	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return TrustRecord{}, err
	}
	kept := records[:0]
	for _, existing := range records {
		if existing.Repository != identity {
			kept = append(kept, existing)
		}
	}
	records = append(kept, record)
	if err := s.saveLocked(records); err != nil {
		return TrustRecord{}, err
	}
	return record, nil
}

// Check reports whether the exact current repository identity and hash are
// approved. Missing stores and missing records are ordinary false results.
func (s *TrustStore) Check(ctx context.Context, repoRoot, hash string) (bool, error) {
	if err := validateExecutionHash(hash); err != nil {
		return false, err
	}
	identity, err := CanonicalRepoIdentity(ctx, repoRoot)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record.Repository == identity && record.ExecutionHash == hash {
			return true, nil
		}
	}
	return false, nil
}

// Revoke removes all approvals for repoRoot and reports whether anything was
// removed.
func (s *TrustStore) Revoke(ctx context.Context, repoRoot string) (bool, error) {
	identity, err := CanonicalRepoIdentity(ctx, repoRoot)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	kept := records[:0]
	removed := false
	for _, record := range records {
		if record.Repository == identity {
			removed = true
			continue
		}
		kept = append(kept, record)
	}
	if !removed {
		return false, nil
	}
	return true, s.saveLocked(kept)
}

// List returns a stable snapshot sorted by canonical repository and hash.
func (s *TrustStore) List() ([]TrustRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := append([]TrustRecord(nil), records...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repository != out[j].Repository {
			return out[i].Repository < out[j].Repository
		}
		return out[i].ExecutionHash < out[j].ExecutionHash
	})
	return out, nil
}

func (s *TrustStore) loadLocked() ([]TrustRecord, error) {
	if s == nil || s.Path == "" {
		return nil, fmt.Errorf("project config trust store path is empty")
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project config trust store: %w", err)
	}
	var file trustFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode project config trust store: %w", err)
	}
	if file.Version != TrustStoreVersion {
		return nil, fmt.Errorf("project config trust store version %d is unsupported", file.Version)
	}
	seen := map[string]bool{}
	for index, record := range file.Records {
		if !filepath.IsAbs(record.Repository) || filepath.Clean(record.Repository) != record.Repository {
			return nil, fmt.Errorf("project config trust record %d has a non-canonical repository identity", index)
		}
		if err := validateExecutionHash(record.ExecutionHash); err != nil {
			return nil, fmt.Errorf("project config trust record %d: %w", index, err)
		}
		if record.ApprovedAt.IsZero() {
			return nil, fmt.Errorf("project config trust record %d has no approval time", index)
		}
		key := record.Repository + "\x00" + record.ExecutionHash
		if seen[key] {
			return nil, fmt.Errorf("project config trust store contains duplicate record %d", index)
		}
		seen[key] = true
	}
	return file.Records, nil
}

func (s *TrustStore) saveLocked(records []TrustRecord) error {
	if s == nil || s.Path == "" {
		return fmt.Errorf("project config trust store path is empty")
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Repository != records[j].Repository {
			return records[i].Repository < records[j].Repository
		}
		return records[i].ExecutionHash < records[j].ExecutionHash
	})
	data, err := json.MarshalIndent(trustFile{Version: TrustStoreVersion, Records: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project config trust store: %w", err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create project config trust directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".project-config-trust-*.tmp")
	if err != nil {
		return fmt.Errorf("create project config trust temporary file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write project config trust store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Rename(name, s.Path); err == nil {
		return nil
	}
	if _, err := os.Lstat(s.Path); err != nil {
		if renameErr := os.Rename(name, s.Path); renameErr != nil {
			return fmt.Errorf("replace project config trust store: %w", renameErr)
		}
		return nil
	}
	backup, err := os.CreateTemp(directory, ".project-config-trust-backup-*.tmp")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(s.Path, backupPath); err != nil {
		return err
	}
	if err := os.Rename(name, s.Path); err != nil {
		_ = os.Rename(backupPath, s.Path)
		return fmt.Errorf("replace project config trust store: %w", err)
	}
	return os.Remove(backupPath)
}

func (s *TrustStore) now() time.Time {
	if s != nil && s.Clock != nil {
		return s.Clock().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func validateExecutionHash(hash string) error {
	if !executionHashPattern.MatchString(hash) {
		return fmt.Errorf("invalid execution hash %q: want sha256 followed by 64 lowercase hexadecimal characters", hash)
	}
	return nil
}
