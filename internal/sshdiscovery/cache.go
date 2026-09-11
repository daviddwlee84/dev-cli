package sshdiscovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const cacheVersion = 1
const maxCacheFileBytes = 4 << 20
const maxCacheTotalBytes = 16 << 20
const maxCacheReports = 128

type cacheRecord struct {
	Version int    `json:"version"`
	Report  Report `json:"report"`
}

func cacheName(report Report) string {
	sum := sha256.Sum256([]byte(report.Scope))
	return report.Source + "-" + hex.EncodeToString(sum[:]) + ".json"
}

// WriteCache stores one observation under a source/scope key. It is explicit:
// discovery and inventory reads never write cache files. cacheDir must be a
// dedicated owner-private directory; missing components are created privately.
// It never follows directory/file symlinks, and unsupported ownership platforms
// fail closed rather than writing nominally private data with unverifiable ACLs.
func WriteCache(ctx context.Context, cacheDir string, report Report) error {
	ctx = nonNilContext(ctx)
	if err := validateReport(report); err != nil {
		return err
	}
	root, verify, err := openCacheRoot(cacheDir, true)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(cacheRecord{Version: cacheVersion, Report: report})
	if err != nil {
		return err
	}
	if len(data) > maxCacheFileBytes {
		return fmt.Errorf("discovery cache report is too large: %w", ErrInvalidData)
	}
	name := cacheName(report)
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
		if err := checkCacheFile(filepath.Join(cacheDir, name), before); err != nil {
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

// ReadCache reads all source reports without creating, refreshing, deleting, or
// changing any file. Missing caches return an empty slice. Corrupt/unsafe entries
// produce an error alongside any independently valid records; callers must keep
// that incomplete observation distinct from an empty successful inventory.
func ReadCache(ctx context.Context, cacheDir string, now time.Time) ([]Report, error) {
	ctx = nonNilContext(ctx)
	if now.IsZero() {
		now = time.Now()
	}
	reports := []Report{}
	root, verify, err := openCacheRoot(cacheDir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return reports, nil
	}
	if err != nil {
		return reports, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return reports, err
	}
	entries, err := directory.ReadDir(maxCacheReports + 1)
	_ = directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return reports, err
	}
	if len(entries) > maxCacheReports {
		return reports, errors.New("too many discovery cache entries")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var failures []error
	var total int64
	for _, entry := range entries {
		if ctx.Err() != nil {
			return reports, ctx.Err()
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") || !(strings.HasPrefix(name, SourceTailscale+"-") || strings.HasPrefix(name, SourceLAN+"-")) {
			continue
		}
		info, err := root.Lstat(name)
		if err == nil {
			err = checkCacheFile(filepath.Join(cacheDir, name), info)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("read discovery cache entry: %w", err))
			continue
		}
		total += info.Size()
		if total > maxCacheTotalBytes {
			failures = append(failures, errors.New("discovery cache exceeds total read bound"))
			break
		}
		data, _, err := safefile.ReadStableRegular(ctx, root, name, info, maxCacheFileBytes)
		if err != nil {
			failures = append(failures, fmt.Errorf("read discovery cache entry: %w", err))
			continue
		}
		var record cacheRecord
		if err := json.Unmarshal(data, &record); err != nil || record.Version != cacheVersion || cacheName(record.Report) != name {
			failures = append(failures, fmt.Errorf("invalid discovery cache version or scope: %w", ErrInvalidData))
			continue
		}
		if err := validateReport(record.Report); err != nil {
			failures = append(failures, err)
			continue
		}
		record.Report.Stale = now.Before(record.Report.ObservedAt) || now.Sub(record.Report.ObservedAt) >= CacheTTL
		reports = append(reports, record.Report)
	}
	if err := verify(); err != nil {
		return []Report{}, err
	}
	return reports, errors.Join(failures...)
}

func validateReport(report Report) error {
	if report.Source != SourceTailscale && report.Source != SourceLAN {
		return ErrInvalidData
	}
	if !safeText(report.Scope, 32768) || report.Scope == "" || report.ObservedAt.IsZero() || len(report.Candidates) > maxLANEndpoints {
		return ErrInvalidData
	}
	switch report.Status {
	case StatusReady:
		if !report.Complete {
			return ErrInvalidData
		}
	case StatusPartial, StatusFailed, StatusUnavailable:
		if report.Complete {
			return ErrInvalidData
		}
	default:
		return ErrInvalidData
	}
	seen := map[string]bool{}
	for _, candidate := range report.Candidates {
		if candidate.Source != report.Source || candidate.Scope != report.Scope || candidate.ID == "" || candidate.NativeID == "" ||
			candidate.ID != candidateID(candidate.Source, candidate.Scope, candidate.NativeID) || seen[candidate.ID] || !safeText(candidate.NativeID, 1024) ||
			!safeText(candidate.Name, 256) || !safeText(candidate.OS, 64) || candidate.DNSName != "" && !validDNS(candidate.DNSName) || candidate.Port < 1 || candidate.Port > 65535 ||
			len(candidate.Addresses) == 0 || len(candidate.Addresses) > 32 {
			return ErrInvalidData
		}
		seen[candidate.ID] = true
		if candidate.State != StateDiscovered && candidate.State != StateSSH && candidate.State != StateOpen {
			return ErrInvalidData
		}
		for _, raw := range candidate.Addresses {
			address, err := netip.ParseAddr(raw)
			if err != nil || address.Zone() != "" || address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() {
				return ErrInvalidData
			}
		}
	}
	return nil
}

// The root walk rejects every symlink component, while held os.Root handles
// prevent a changed path from redirecting reads or writes outside the walked
// tree. verify rechecks every named component before and after publication.
func openCacheRoot(path string, create bool) (*os.Root, func() error, error) {
	if err := cachePlatformSupported(); err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, nil, ErrUnsafeCache
	}
	volume := filepath.VolumeName(path)
	base := volume + string(filepath.Separator)
	root, rootInfo, err := safefile.OpenRoot(base)
	if err != nil {
		return nil, nil, err
	}
	if err := checkCacheAncestor(base, rootInfo); err != nil {
		root.Close()
		return nil, nil, err
	}
	type heldDirectory struct {
		path string
		info fs.FileInfo
	}
	held := []heldDirectory{{path: base, info: rootInfo}}
	components := strings.Split(strings.TrimPrefix(path, base), string(filepath.Separator))
	currentPath := base
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			root.Close()
			return nil, nil, ErrUnsafeCache
		}
		currentPath = filepath.Join(currentPath, component)
		info, err := root.Lstat(component)
		if errors.Is(err, fs.ErrNotExist) && create {
			err = root.Mkdir(component, 0o700)
			if err == nil {
				err = setCachePrivate(currentPath, 0o700)
			}
			if err == nil || errors.Is(err, fs.ErrExist) {
				info, err = root.Lstat(component)
			}
		}
		if err != nil {
			root.Close()
			return nil, nil, err
		}
		if index == len(components)-1 {
			err = checkCacheDirectory(currentPath, info)
		} else {
			err = checkCacheAncestor(currentPath, info)
		}
		if err != nil {
			root.Close()
			return nil, nil, err
		}
		child, childInfo, err := safefile.OpenChildRoot(root, component)
		root.Close()
		if err != nil {
			return nil, nil, err
		}
		root = child
		held = append(held, heldDirectory{path: currentPath, info: childInfo})
	}
	verify := func() error {
		for index, directory := range held {
			if err := safefile.VerifyRoot(directory.path, directory.info); err != nil {
				return err
			}
			info, err := os.Lstat(directory.path)
			if err != nil {
				return err
			}
			if index == len(held)-1 {
				err = checkCacheDirectory(directory.path, info)
			} else {
				err = checkCacheAncestor(directory.path, info)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		root.Close()
		return nil, nil, err
	}
	return root, verify, nil
}

func prepareCacheStage(file *os.File) error {
	return setCachePrivate(file.Name(), 0o600)
}
