//go:build linux || darwin

package configedit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestPlanApplyRestoreAndStale(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	recovery := filepath.Join(dir, "state", "recovery")
	put(t, path, "old")
	p, err := New(ctx, []string{path}, [][]byte{[]byte("new")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recovery); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("planning wrote recovery")
	}
	p.Changes[0].Path = filepath.Join(dir, "wrong") // public previews have no authority
	result, err := Apply(ctx, p, recovery)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	restore, err := RestorePlan(ctx, recovery, result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(ctx, restore, recovery); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "old" {
		t.Fatalf("restored %q", b)
	}
	p, err = New(ctx, []string{path}, [][]byte{[]byte("new")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, "external")
	if _, err = Apply(ctx, p, recovery); !errors.Is(err, ErrStale) {
		t.Fatalf("expected stale: %v", err)
	}
}
func TestInterruptedApplyCanRestoreOnlyCompletedSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	first := filepath.Join(dir, "group", "new.conf")
	root := filepath.Join(dir, "config")
	recovery := filepath.Join(dir, "recovery")
	put(t, root, "original")
	p, err := New(ctx, []string{first, root}, [][]byte{[]byte("fragment"), []byte("includes")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.beforePublish = func(i int) {
		if i == 1 {
			cancel()
		}
	}
	result, err := Apply(ctx, p, recovery)
	if err == nil || result.Receipt == "" {
		t.Fatalf("missing partial receipt: %+v %v", result, err)
	}
	b, _ := os.ReadFile(root)
	if string(b) != "original" {
		t.Fatal("root changed before publication")
	}
	restore, err := RestorePlan(context.Background(), recovery, result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(context.Background(), restore, recovery); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created fragment not restored")
	}
}
func TestRestoreRejectsSubsequentEditAndUnsafeParents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	recovery := filepath.Join(dir, "recovery")
	put(t, path, "old")
	p, err := New(ctx, []string{path}, [][]byte{[]byte("new")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Apply(ctx, p, recovery)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, "human edit")
	if _, err = RestorePlan(ctx, recovery, r.Receipt); !errors.Is(err, ErrStale) {
		t.Fatalf("restore overwrites later edit: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err = New(ctx, []string{filepath.Join(link, "config")}, [][]byte{[]byte("bad")}, nil); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err = RestorePlan(ctx, recovery, "../config"); err == nil {
		t.Fatal("receipt traversal accepted")
	}
}
func TestRecoveryCannotBeInGitAndGenerationRace(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	put(t, path, "old")
	p, err := New(ctx, []string{path}, [][]byte{[]byte("new")}, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.RequireContents(map[string][]byte{path: []byte("outdated")}); !errors.Is(err, ErrStale) {
		t.Fatal("render race not rejected")
	}
	put(t, filepath.Join(dir, ".git"), "gitdir: someplace")
	if _, err = Apply(ctx, p, filepath.Join(dir, "recovery")); err == nil {
		t.Fatal("recovery stored in Git")
	}
}

func TestLargeRecoveryReceiptRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	old := strings.Repeat("a", 1100*1024)
	put(t, path, old)
	p, err := New(context.Background(), []string{path}, [][]byte{[]byte(strings.Repeat("b", 1100*1024))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(dir, "recovery")
	result, err := Apply(context.Background(), p, recovery)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := RestorePlan(context.Background(), recovery, result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(context.Background(), restore, recovery); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != old {
		t.Fatal("large receipt lost original bytes")
	}
}

func TestRestoreRejectsReplacementWithIdenticalBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	put(t, path, "before")
	p, err := New(context.Background(), []string{path}, [][]byte{[]byte("after")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(dir, "recovery")
	r, err := Apply(context.Background(), p, recovery)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	put(t, replacement, "after")
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, err = RestorePlan(context.Background(), recovery, r.Receipt); !errors.Is(err, ErrStale) {
		t.Fatal("undo accepted a different file with matching bytes", err)
	}
}
