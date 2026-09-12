//go:build windows

package sshdiscovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func windowsCacheReport() Report {
	return Report{Source: SourceTailscale, Scope: "test-tailnet", Status: StatusReady, Complete: true,
		ObservedAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC), Candidates: []Candidate{}}
}

func TestWindowsCacheCreatesAndChecksProtectedDACLs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discovery")
	report := windowsCacheReport()
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCacheDirectory(dir, info); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, cacheName(report))
	info, err = os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCacheFile(file, info); err != nil {
		t.Fatal(err)
	}
	if reports, err := ReadCache(context.Background(), dir, report.ObservedAt); err != nil || len(reports) != 1 {
		t.Fatalf("%#v %v", reports, err)
	}
	// Replacement uses the same protected staging policy.
	report.ObservedAt = report.ObservedAt.Add(time.Second)
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCacheFile(file, info); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCacheRejectsPublicDACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discovery")
	report := windowsCacheReport()
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, cacheName(report))
	defer setCachePrivate(file, 0o600)
	current, _, err := cachePrivateSIDs()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GR;;;WD)", current))
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(descriptor)
	if _, err := ReadCache(context.Background(), dir, report.ObservedAt); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("public cache read=%v", err)
	}
	if err := WriteCache(context.Background(), dir, report); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("public cache write=%v", err)
	}
}

func TestWindowsCacheRejectsReparseMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discovery")
	report := windowsCacheReport()
	if err := WriteCache(context.Background(), dir, report); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip("Windows symlinks require Developer Mode or privilege")
	}
	if _, err := ReadCache(context.Background(), link, report.ObservedAt); err == nil {
		t.Fatal("reparse root accepted")
	}
}
