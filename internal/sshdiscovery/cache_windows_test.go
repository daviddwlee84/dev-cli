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

func TestWindowsCachePartialRefreshRetainsOriginalObservationTimes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discovery")
	original := windowsCacheReport()
	first, ok := candidateFromTailscale(tailscalePeer{ID: "first", HostName: "first-host", TailscaleIPs: []string{"100.64.0.1"}}, original.Scope)
	if !ok {
		t.Fatal("first fixture candidate is invalid")
	}
	second, ok := candidateFromTailscale(tailscalePeer{ID: "second", HostName: "second-host", TailscaleIPs: []string{"100.64.0.2"}}, original.Scope)
	if !ok {
		t.Fatal("second fixture candidate is invalid")
	}
	original.Candidates = []Candidate{first, second}
	if err := WriteCache(t.Context(), dir, original); err != nil {
		t.Fatal(err)
	}
	partial := original
	partial.Candidates = []Candidate{second}
	partial.Candidates[0].Name = "new second name"
	partial.ObservedAt = original.ObservedAt.Add(2 * CacheTTL)
	partial.Complete, partial.Status = false, StatusPartial
	if err := WriteCache(t.Context(), dir, partial); err != nil {
		t.Fatal(err)
	}
	reports, err := ReadCache(t.Context(), dir, partial.ObservedAt)
	if err != nil || len(reports) != 2 {
		t.Fatalf("reports=%+v error=%v", reports, err)
	}
	var old, fresh *Report
	for i := range reports {
		if reports[i].ObservedAt.Equal(original.ObservedAt) {
			old = &reports[i]
		} else {
			fresh = &reports[i]
		}
	}
	if old == nil || !old.Stale || len(old.Candidates) != 1 || old.Candidates[0].NativeID != first.NativeID {
		t.Fatalf("old observation lost or refreshed: %+v", reports)
	}
	if fresh == nil || fresh.Stale || fresh.Complete || len(fresh.Candidates) != 1 || fresh.Candidates[0].Name != "new second name" {
		t.Fatalf("partial observation lost: %+v", reports)
	}
	file := filepath.Join(dir, cacheName(partial))
	info, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCacheFile(file, info); err != nil {
		t.Fatalf("partial replacement lost protected DACL: %v", err)
	}
	complete := partial
	complete.Complete, complete.Status = true, StatusReady
	complete.ObservedAt = partial.ObservedAt.Add(time.Second)
	if err := WriteCache(t.Context(), dir, complete); err != nil {
		t.Fatal(err)
	}
	reports, err = ReadCache(t.Context(), dir, complete.ObservedAt)
	if err != nil || len(reports) != 1 || len(reports[0].Candidates) != 1 || reports[0].Candidates[0].NativeID != second.NativeID {
		t.Fatalf("complete scan did not replace history: %+v %v", reports, err)
	}
}
