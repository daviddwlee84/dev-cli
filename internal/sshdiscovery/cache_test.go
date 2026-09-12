//go:build linux || darwin

package sshdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cacheTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func cacheTestReport() Report {
	observed := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	candidate, _ := candidateFromTailscale(tailscalePeer{ID: "test", HostName: "server", DNSName: "server.tail123.ts.net.", TailscaleIPs: []string{"100.64.0.1"}}, "test-scope")
	return Report{Source: SourceTailscale, Scope: "test-scope", Status: StatusReady, Complete: true, ObservedAt: observed, Candidates: []Candidate{candidate}}
}

func TestCacheReadIsReadOnlyAndFreshnessRetainsObservation(t *testing.T) {
	root := cacheTestRoot(t)
	dir := filepath.Join(root, "missing", "discovery")
	reports, err := ReadCache(context.Background(), dir, time.Now())
	if err != nil || len(reports) != 0 {
		t.Fatalf("%#v %v", reports, err)
	}
	if _, err := os.Lstat(filepath.Dir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created a cache directory")
	}
	report := cacheTestReport()
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache directory mode: %v %v", info, err)
	}
	file := filepath.Join(dir, cacheName(report))
	before, err := os.Lstat(file)
	if err != nil || before.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode: %v %v", before, err)
	}
	for _, test := range []struct {
		now   time.Time
		stale bool
	}{
		{report.ObservedAt, false},
		{report.ObservedAt.Add(CacheTTL - time.Nanosecond), false},
		{report.ObservedAt.Add(CacheTTL), true},
		{report.ObservedAt.Add(-time.Second), true},
	} {
		reports, err = ReadCache(context.Background(), dir, test.now)
		if err != nil || len(reports) != 1 || reports[0].Stale != test.stale || reports[0].ObservedAt != report.ObservedAt {
			t.Fatalf("reports=%#v err=%v", reports, err)
		}
	}
	after, err := os.Lstat(file)
	if err != nil || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(before, after) {
		t.Fatal("reading changed cache")
	}
	report.Candidates[0].OS = "linux"
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	reports, err = ReadCache(context.Background(), dir, report.ObservedAt)
	if err != nil || len(reports) != 1 || reports[0].Candidates[0].OS != "linux" {
		t.Fatalf("replacement=%#v %v", reports, err)
	}
}

func TestCacheKeepsSourceScopeAndPortObservationsSeparate(t *testing.T) {
	dir := filepath.Join(cacheTestRoot(t), "discovery")
	report := cacheTestReport()
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	other := report
	other.Scope = "another-tailnet"
	other.Candidates = append([]Candidate(nil), report.Candidates...)
	other.Candidates[0].Scope = other.Scope
	other.Candidates[0].ID = candidateID(other.Source, other.Scope, other.Candidates[0].NativeID)
	if err := WriteCache(context.Background(), dir, other); err != nil {
		t.Fatal(err)
	}
	reports, err := ReadCache(context.Background(), dir, report.ObservedAt)
	if err != nil || len(reports) != 2 {
		t.Fatalf("%#v %v", reports, err)
	}
}

func TestCacheRejectsLinksAndUnprotectedPaths(t *testing.T) {
	for _, kind := range []string{"root link", "ancestor link", "file link", "hardlink", "public directory", "public file"} {
		t.Run(kind, func(t *testing.T) {
			root := cacheTestRoot(t)
			dir := filepath.Join(root, "cache")
			report := cacheTestReport()
			if err := WriteCache(context.Background(), dir, report); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, cacheName(report))
			switch kind {
			case "root link":
				link := filepath.Join(root, "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dir = link
			case "ancestor link":
				link := filepath.Join(root, "link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				dir = filepath.Join(link, "cache")
			case "file link":
				target := filepath.Join(root, "target")
				if err := os.Rename(file, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, filepath.Join(root, "extra-link")); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "public file":
				if err := os.Chmod(file, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadCache(context.Background(), dir, report.ObservedAt); err == nil {
				t.Fatal("unsafe cache read succeeded")
			}
			if err := WriteCache(context.Background(), dir, report); err == nil {
				t.Fatal("unsafe cache write succeeded")
			}
		})
	}
}

func TestCacheVersionAndCorruptionDoNotBecomeCleanEmptyState(t *testing.T) {
	dir := filepath.Join(cacheTestRoot(t), "cache")
	report := cacheTestReport()
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, cacheName(report))
	for _, data := range [][]byte{[]byte("{"), []byte(`{"version":999,"report":{}}`)} {
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if reports, err := ReadCache(context.Background(), dir, report.ObservedAt); err == nil || len(reports) != 0 {
			t.Fatalf("%#v %v", reports, err)
		}
	}
	invalid := report
	invalid.Candidates = append([]Candidate(nil), report.Candidates...)
	invalid.Candidates[0].Name = "server\nHost *"
	data, err := json.Marshal(cacheRecord{Version: cacheVersion, Report: invalid})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(context.Background(), dir, report.ObservedAt); !errors.Is(err, ErrInvalidData) {
		t.Fatal(err)
	}
	if err := WriteCache(context.Background(), dir, invalid); !errors.Is(err, ErrInvalidData) {
		t.Fatal(err)
	}
}

func TestEmptyCacheReadSucceeds(t *testing.T) {
	dir := filepath.Join(cacheTestRoot(t), "cache")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if reports, err := ReadCache(context.Background(), dir, time.Now()); err != nil || len(reports) != 0 {
		t.Fatalf("%#v %v", reports, err)
	}
}
