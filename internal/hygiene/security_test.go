package hygiene

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlanTamperingAndLateWriterGuardAreRejected(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "f", "private-host-unique\n")
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PreviewRedact(t.Context(), report.ID, []string{"f"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), p.ID, ApplyOptions{Guard: func(context.Context, string, []string) error { return errors.New("writer active") }}); err == nil {
		t.Fatal("writer was ignored")
	}
	var record planRecord
	if err = s.load(t.Context(), p.ID, &record); err != nil {
		t.Fatal(err)
	}
	record.Changes[0].Desired = []byte("unauthorized replacement")
	if err = s.save(t.Context(), p.ID, record); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), p.ID, ApplyOptions{}); !errors.Is(err, ErrStale) {
		t.Fatalf("tampered plan accepted: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(r.Root, "f"))
	if string(data) != "private-host-unique\n" {
		t.Fatal("guard failure changed file")
	}
}
func TestScanDoesNotFollowSwappedParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege is host-dependent")
	}
	s, r := testService(t)
	put(t, r.Root, "nested/f", "safe\n")
	r.Git("add", "nested/f")
	r.Git("commit", "-m", "nested")
	outside := t.TempDir()
	put(t, outside, "f", "private-host-unique\n")
	if e := os.RemoveAll(filepath.Join(r.Root, "nested")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(r.Root, "nested")); e != nil {
		t.Fatal(e)
	}
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err == nil || report.Status != "partial" {
		t.Fatalf("unsafe read reported clean: %+v %v", report, err)
	}
	if len(report.Findings) != 0 {
		t.Fatal("read private bytes through swapped parent")
	}
}
func TestRedactLargeFileWithOverlappingRulesIsBoundedAndRestorable(t *testing.T) {
	if testing.Short() {
		t.Skip("large text transaction")
	}
	s, r := testService(t)
	body := strings.Repeat("padding line with no sensitive data\n", 90000) + strings.Repeat("private-host-unique\n", 100)
	put(t, r.Root, "large.txt", body)
	s.Policy.Rules = append(s.Policy.Rules, Rule{ID: "narrow", Kind: "literal", Value: "private-host", Replacement: "narrow"})
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PreviewRedact(t.Context(), report.ID, []string{"large.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := s.Apply(t.Context(), p.ID, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(r.Root, "large.txt"))
	if strings.Contains(string(out), "private-host") || strings.Count(string(out), "host.example.invalid") != 100 {
		t.Fatal("overlap replacement lost context")
	}
	if _, err = s.Restore(t.Context(), applied.Recovery[0], true, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	out, _ = os.ReadFile(filepath.Join(r.Root, "large.txt"))
	if string(out) != body {
		t.Fatal("large restore mismatch")
	}
}
func TestHookConfigurationRejectsLookalikeAndPreservesForeignHooks(t *testing.T) {
	foreign := []byte("# keep this comment\nrepos:\n  - repo: local\n    hooks:\n      - id: other\n        entry: another-command\n        language: system\n")
	merged, err := mergeHook(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "keep this comment") || !strings.Contains(string(merged), "another-command") {
		t.Fatal("foreign hook lost")
	}
	if _, err = mergeHook([]byte(strings.ReplaceAll(HookConfig, "dev hygiene scan --scope staged", "echo pass"))); err == nil {
		t.Fatal("lookalike accepted")
	}
}

func TestExplicitOffSkipsFileAndEngineReads(t *testing.T) {
	s, r := testService(t)
	s.Policy.Secrets = Off
	s.Policy.Known = Off
	s.Policy.Generic = Off
	put(t, r.Root, "private.txt", "private-host-unique\n")
	s.Engine = engineFunc(func(context.Context, EngineRequest) ([]Detection, error) {
		t.Fatal("disabled scanner executed")
		return nil, nil
	})
	report, err := s.Scan(t.Context(), ScanOptions{})
	if err != nil || !report.OK() || report.Status != "skipped" || !report.PolicyDisabled || report.Files != 0 {
		t.Fatalf("disabled policy not respected: %+v %v", report, err)
	}
}

func TestHookGitEnvironmentCannotMutateCallerIndexOrConfig(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "script.sh", "#!/bin/sh\nprintf fixture\n")
	r.Git("add", "script.sh")
	r.Git("update-index", "--chmod=+x", "script.sh")
	dir := filepath.Join(r.Root, ".git")
	index := filepath.Join(dir, "index")
	config := filepath.Join(dir, "config")
	beforeIndex, e := os.ReadFile(index)
	if e != nil {
		t.Fatal(e)
	}
	beforeConfig, e := os.ReadFile(config)
	if e != nil {
		t.Fatal(e)
	}
	alternate := filepath.Join(t.TempDir(), "commit-index")
	if e = os.WriteFile(alternate, beforeIndex, 0o600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("GIT_DIR", dir)
	t.Setenv("GIT_WORK_TREE", r.Root)
	t.Setenv("GIT_INDEX_FILE", alternate)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(dir, "objects"))
	s.Policy.Secrets = Block
	s.Engine = engineFunc(func(ctx context.Context, q EngineRequest) ([]Detection, error) {
		data, e := snapshotGitBytes(ctx, q.Root, nil, "show", ":script.sh")
		if e != nil || !strings.Contains(string(data), "printf fixture") {
			t.Fatalf("snapshot did not own its index: %v", e)
		}
		return nil, nil
	})
	report, e := s.Scan(t.Context(), ScanOptions{Scope: "staged"})
	if e != nil || !report.OK() {
		t.Fatalf("hook scan: %+v %v", report, e)
	}
	for path, want := range map[string][]byte{index: beforeIndex, alternate: beforeIndex, config: beforeConfig} {
		got, e := os.ReadFile(path)
		if e != nil || string(got) != string(want) {
			t.Fatalf("caller Git state changed at %s", filepath.Base(path))
		}
	}
}
