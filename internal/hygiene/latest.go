package hygiene

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const (
	latestFile         = "latest.json"
	latestLock         = "latest.lock"
	maxLatestCheckouts = 256
	latestAnyScope     = "any"
)

// ErrNoStoredScan means no scan report was recorded for this checkout.
var ErrNoStoredScan = errors.New("no stored hygiene scan for this checkout; run dev hygiene scan or pass --rescan")

// latestIndex points each checkout at its newest scan per scope. Linked
// worktrees share one hygiene state directory, so pointers are keyed by a
// private digest of the checkout root rather than the repository.
type latestIndex struct {
	Version   int                             `json:"version"`
	Checkouts map[string]map[string]latestRef `json:"checkouts"`
}

type latestRef struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
}

func (s *Service) checkoutKey() string { return keyedID(s.key, "checkout", s.Root) }

// recordLatest is best effort: a lost update only makes `report` show an older
// scan, and must never fail the scan or its pre-commit hook.
func (s *Service) recordLatest(ctx context.Context, report Report) {
	if report.Kind != "hygiene_scan" || !safeRecordID(report.ID) {
		return
	}
	_ = lockx.WithFile(ctx, filepath.Join(s.Dir, latestLock), "hygiene latest scan", func() error {
		index, err := s.readLatest(ctx)
		if err != nil {
			index = latestIndex{}
		}
		if index.Checkouts == nil {
			index.Checkouts = map[string]map[string]latestRef{}
		}
		key := s.checkoutKey()
		scopes := index.Checkouts[key]
		if scopes == nil {
			scopes = map[string]latestRef{}
		}
		ref := latestRef{ID: report.ID, Created: report.Created}
		for _, scope := range []string{latestAnyScope, report.Scope} {
			if current, ok := scopes[scope]; !ok || !current.Created.After(ref.Created) {
				scopes[scope] = ref
			}
		}
		index.Checkouts[key] = scopes
		pruneLatest(&index)
		index.Version = 1
		data, err := json.Marshal(index)
		if err != nil {
			return err
		}
		return configedit.WritePrivate(ctx, filepath.Join(s.Dir, latestFile), data, true)
	})
}

func pruneLatest(index *latestIndex) {
	if len(index.Checkouts) <= maxLatestCheckouts {
		return
	}
	keys := make([]string, 0, len(index.Checkouts))
	for key := range index.Checkouts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return index.Checkouts[keys[i]][latestAnyScope].Created.After(index.Checkouts[keys[j]][latestAnyScope].Created)
	})
	for _, key := range keys[maxLatestCheckouts:] {
		delete(index.Checkouts, key)
	}
}

func (s *Service) readLatest(ctx context.Context) (latestIndex, error) {
	path := filepath.Join(s.Dir, latestFile)
	data, err := safefile.ReadStablePath(ctx, path, 1<<20)
	if errors.Is(err, fs.ErrNotExist) {
		return latestIndex{}, nil
	}
	if err != nil {
		return latestIndex{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return latestIndex{}, err
	}
	if err := privatefile.Check(path, info, false); err != nil {
		return latestIndex{}, err
	}
	var index latestIndex
	if err := json.Unmarshal(data, &index); err != nil || index.Version != 1 {
		return latestIndex{}, errors.New("invalid hygiene latest-scan index")
	}
	return index, nil
}

// LatestReport returns this checkout's newest stored scan ID, optionally for
// one scope.
func (s *Service) LatestReport(ctx context.Context, scope string) (string, error) {
	if err := s.prepare(ctx); err != nil {
		return "", err
	}
	if scope == "" {
		scope = latestAnyScope
	}
	index, err := s.readLatest(ctx)
	if err != nil {
		return "", err
	}
	ref, ok := index.Checkouts[s.checkoutKey()][scope]
	if !ok || ref.ID == "" {
		return "", ErrNoStoredScan
	}
	return ref.ID, nil
}
