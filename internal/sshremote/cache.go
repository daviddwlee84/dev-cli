package sshremote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const maxCacheRecords = 128
const maxCacheTotalBytes = 16 << 20

var cacheLocks sync.Map

type cacheRecord struct {
	Version    int        `json:"version"`
	Kind       string     `json:"kind"`
	HostName   string     `json:"host_name"`
	EndpointID string     `json:"endpoint_id"`
	FetchedAt  time.Time  `json:"fetched_at"`
	Inventory  *Inventory `json:"inventory,omitempty"`
	Resolved   *Resolved  `json:"resolved,omitempty"`
}

type CachedInventory struct {
	HostName   string    `json:"host_name"`
	EndpointID string    `json:"endpoint_id"`
	FetchedAt  time.Time `json:"fetched_at"`
	Stale      bool      `json:"stale"`
	Inventory  Inventory `json:"inventory"`
}

func cacheFilename(kind, endpointID, profileID string) string {
	return kind + "-" + sourceDigest([]string{endpointID, profileID}) + ".json"
}
func (r cacheRecord) filename() string {
	profileID := ""
	if r.Resolved != nil {
		profileID = r.Resolved.Profile.ID
	}
	return cacheFilename(r.Kind, r.EndpointID, profileID)
}
func (r cacheRecord) validate() error {
	if r.Version != 1 || !validDigest(r.EndpointID) || !safeText(r.HostName, 1024, false) || r.FetchedAt.IsZero() {
		return ErrInvalidData
	}
	switch r.Kind {
	case "inventory":
		if r.Inventory == nil || r.Resolved != nil {
			return ErrInvalidData
		}
		return r.Inventory.Validate()
	case "resolved":
		if r.Resolved == nil || r.Inventory != nil {
			return ErrInvalidData
		}
		return r.Resolved.Validate()
	default:
		return ErrInvalidData
	}
}
func cacheStale(fetched, observed, now time.Time) bool {
	return now.Before(fetched) || now.Before(observed) || now.Sub(fetched) >= CacheTTL || now.Sub(observed) >= CacheTTL
}

func SaveCache(ctx context.Context, dir string, host fleet.Host, inventory Inventory) error {
	if err := inventory.Validate(); err != nil {
		return err
	}
	if host.MachineID != "" && host.MachineID != inventory.Origin.MachineID {
		return ErrSourceChanged
	}
	return saveCacheRecord(ctx, dir, cacheRecord{Version: 1, Kind: "inventory", HostName: host.Name, EndpointID: fleet.EndpointID(host), FetchedAt: time.Now().UTC(), Inventory: &inventory})
}
func SaveResolvedCache(ctx context.Context, dir string, host fleet.Host, resolved Resolved) error {
	if err := resolved.Validate(); err != nil {
		return err
	}
	if host.MachineID != "" && host.MachineID != resolved.Origin.MachineID {
		return ErrSourceChanged
	}
	return saveCacheRecord(ctx, dir, cacheRecord{Version: 1, Kind: "resolved", HostName: host.Name, EndpointID: fleet.EndpointID(host), FetchedAt: time.Now().UTC(), Resolved: &resolved})
}

func saveCacheRecord(ctx context.Context, dir string, record cacheRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := record.validate(); err != nil {
		return err
	}
	data, err := fleet.MarshalBounded(record, MaxResponseBytes)
	if err != nil {
		return ErrInvalidData
	}
	lockValue, _ := cacheLocks.LoadOrStore(filepath.Join(dir, record.filename()), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, verify, err := openCacheRoot(dir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	name := record.filename()
	before, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := verify(); err != nil {
			return err
		}
		_, err = safefile.CreateNoClobberPrepared(ctx, root, name, data, 0o600, prepareCacheStage)
	} else {
		if err != nil {
			return err
		}
		if err := checkCacheFile(filepath.Join(dir, name), before); err != nil {
			return err
		}
		if err := verify(); err != nil {
			return err
		}
		_, err = safefile.AtomicReplacePrepared(ctx, root, name, before, data, 0o600, prepareCacheStage)
	}
	if err != nil {
		return err
	}
	return verify()
}

// LoadCache never refreshes or creates a path. A found stale observation is
// returned intact; callers use Inventory.ObservedAt or ReadCache.Stale to label
// it. Neither fresh nor stale cache is connection/apply authority.
func LoadCache(ctx context.Context, dir string, host fleet.Host, now time.Time) (Inventory, bool, error) {
	record, found, err := loadCacheRecord(ctx, dir, cacheFilename("inventory", fleet.EndpointID(host), ""))
	if err != nil || !found {
		return Inventory{}, found, err
	}
	if record.Kind != "inventory" || record.EndpointID != fleet.EndpointID(host) || record.HostName != host.Name {
		return Inventory{}, false, ErrInvalidData
	}
	if host.MachineID != "" && record.Inventory.Origin.MachineID != host.MachineID {
		return Inventory{}, false, ErrSourceChanged
	}
	if now.IsZero() {
		now = time.Now()
	}
	if record.FetchedAt.After(now.Add(5*time.Minute)) || record.Inventory.ObservedAt.After(now.Add(5*time.Minute)) {
		return Inventory{}, false, ErrInvalidData
	}
	return *record.Inventory, true, nil
}

func LoadResolvedCache(ctx context.Context, dir string, host fleet.Host, selection Selection, now time.Time) (Resolved, bool, error) {
	if selection.Validate() != nil {
		return Resolved{}, false, ErrInvalidData
	}
	record, found, err := loadCacheRecord(ctx, dir, cacheFilename("resolved", fleet.EndpointID(host), selection.ProfileID))
	if err != nil || !found {
		return Resolved{}, found, err
	}
	if record.Kind != "resolved" || record.EndpointID != fleet.EndpointID(host) || record.HostName != host.Name {
		return Resolved{}, false, ErrInvalidData
	}
	resolved := *record.Resolved
	if resolved.Profile.Selection(resolved.Origin) != selection || host.MachineID != "" && resolved.Origin.MachineID != host.MachineID {
		return Resolved{}, false, ErrSourceChanged
	}
	if now.IsZero() {
		now = time.Now()
	}
	if record.FetchedAt.After(now.Add(5*time.Minute)) || resolved.ObservedAt.After(now.Add(5*time.Minute)) {
		return Resolved{}, false, ErrInvalidData
	}
	return resolved, true, nil
}

func loadCacheRecord(ctx context.Context, dir, name string) (cacheRecord, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return cacheRecord{}, false, err
	}
	root, verify, err := openCacheRoot(dir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return cacheRecord{}, false, nil
	}
	if err != nil {
		return cacheRecord{}, false, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return cacheRecord{}, false, nil
	}
	if err != nil {
		return cacheRecord{}, false, err
	}
	if err := checkCacheFile(filepath.Join(dir, name), info); err != nil {
		return cacheRecord{}, false, err
	}
	data, _, err := safefile.ReadStableRegular(ctx, root, name, info, MaxResponseBytes)
	if err != nil {
		return cacheRecord{}, false, err
	}
	var record cacheRecord
	if fleet.UnmarshalStrict(data, MaxResponseBytes, &record) != nil || record.validate() != nil || record.filename() != name {
		return cacheRecord{}, false, ErrInvalidData
	}
	if err := verify(); err != nil {
		return cacheRecord{}, false, err
	}
	return record, true, nil
}

// ReadCache is a bounded passive inventory read across independently cached
// source hosts. Invalid records report incompleteness alongside valid records.
func ReadCache(ctx context.Context, dir string, now time.Time) ([]CachedInventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	result := []CachedInventory{}
	root, verify, err := openCacheRoot(dir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return result, err
	}
	entries, err := directory.ReadDir(maxCacheRecords + 1)
	_ = directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return result, err
	}
	if len(entries) > maxCacheRecords {
		return result, ErrInvalidData
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var failures []error
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "inventory-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := root.Lstat(name)
		if err == nil {
			err = checkCacheFile(filepath.Join(dir, name), info)
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		total += info.Size()
		if total > maxCacheTotalBytes {
			failures = append(failures, ErrInvalidData)
			break
		}
		data, _, err := safefile.ReadStableRegular(ctx, root, name, info, MaxResponseBytes)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var record cacheRecord
		if fleet.UnmarshalStrict(data, MaxResponseBytes, &record) != nil || record.validate() != nil || record.Kind != "inventory" || record.filename() != name {
			failures = append(failures, ErrInvalidData)
			continue
		}
		if record.FetchedAt.After(now.Add(5*time.Minute)) || record.Inventory.ObservedAt.After(now.Add(5*time.Minute)) {
			failures = append(failures, ErrInvalidData)
			continue
		}
		result = append(result, CachedInventory{HostName: record.HostName, EndpointID: record.EndpointID, FetchedAt: record.FetchedAt, Stale: cacheStale(record.FetchedAt, record.Inventory.ObservedAt, now), Inventory: *record.Inventory})
	}
	if err := verify(); err != nil {
		return []CachedInventory{}, err
	}
	return result, errors.Join(failures...)
}
