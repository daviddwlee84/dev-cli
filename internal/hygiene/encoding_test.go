package hygiene

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEncodingRepairBytes(t *testing.T) {
	for _, tt := range []struct {
		name, input, replace, remove string
		invalid, runs, offset, line  int
	}{
		{"empty", "", "", "", 0, 0, 0, 0},
		{"valid", "中文😀�\r\n", "中文😀�\r\n", "中文😀�\r\n", 0, 0, 0, 0},
		{"bom", "\xef\xbb\xbftext\r\n", "\xef\xbb\xbftext\r\n", "\xef\xbb\xbftext\r\n", 0, 0, 0, 0},
		{"chinese-cut", "a\r\n\xe4\n", "a\r\n�\n", "a\r\n\n", 1, 1, 3, 2},
		{"emoji-cut", "x\xf0\x9f\x98", "x�", "x", 3, 1, 1, 1},
		{"continuation", "\x80x", "�x", "x", 1, 1, 0, 1},
		{"runs", "z\xff\xfea\xe4\x80b", "z�a�b", "zab", 4, 2, 1, 1},
		{"replacement-boundary", "\x80�\x81", "���", "�", 2, 2, 0, 1},
		{"overlong", "x\xc0\xaf", "x�", "x", 2, 1, 1, 1},
		{"surrogate", "x\xed\xa0\x80", "x�", "x", 3, 1, 1, 1},
		{"only-invalid", "\x80\x81", "�", "", 2, 1, 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.input)
			issue := inspectEncoding(data)
			if tt.invalid == 0 {
				if issue != nil {
					t.Fatalf("valid text rejected: %+v", issue)
				}
			} else if issue == nil || issue.InvalidBytes != tt.invalid || issue.InvalidSequences != tt.runs || issue.ByteOffset != tt.offset || issue.Line != tt.line {
				t.Fatalf("unexpected diagnostics: %+v", issue)
			}
			for mode, want := range map[string]string{"replace": tt.replace, "remove": tt.remove} {
				got, err := repairUTF8(t.Context(), data, issue, mode, 100)
				if err != nil || string(got) != want || !utf8.Valid(got) || got == nil {
					t.Fatalf("%s: bytes=%x err=%v", mode, got, err)
				}
			}
			if string(data) != tt.input {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestEncodingRepairRefusalsAndLimits(t *testing.T) {
	for _, data := range [][]byte{{0xff, 0xfe, 'a', 0}, {0xfe, 0xff, 0, 'a'}, {0xff, 0xfe, 0, 0}, {0, 0, 0xfe, 0xff}, {'a', 0, 'b'}} {
		if _, err := repairUTF8(t.Context(), data, inspectEncoding(data), "remove", 100); err == nil {
			t.Fatal("non-UTF-8 encoding or NUL accepted")
		}
	}
	data := []byte{'a', 0x80}
	for _, mode := range []string{"unknown", "replace"} {
		if _, err := repairUTF8(t.Context(), data, inspectEncoding(data), mode, 2); err == nil {
			t.Fatal("invalid mode or expansion accepted")
		}
	}
	if _, err := repairUTF8(t.Context(), data, inspectEncoding(data), "remove", 1); err == nil {
		t.Fatal("oversized input accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repairUTF8(ctx, data, inspectEncoding(data), "replace", 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestEncodingRepairPlanPreservesIndexAndRestoresRawBytes(t *testing.T) {
	s, r := testService(t)
	name := ".specstory/history/cut.md"
	staged := "original staged content\n"
	put(t, r.Root, name, staged)
	r.Git("add", name)
	indexBefore := r.Git("ls-files", "--stage", "-z")
	body := []byte("private-context\r\n" + strings.Repeat("a", 199) + "\xe4\n")
	put(t, r.Root, name, string(body))
	plan, err := s.PreviewRepairEncoding(t.Context(), []string{name}, "")
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(plan)
	if bytes.Contains(public, []byte("private-context")) || !plan.RequiresWriterStopped || len(plan.Files) != 1 || plan.Files[0].Encoding.InvalidBytes != 1 {
		t.Fatalf("unsafe/missing plan metadata: %s", public)
	}
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{}); err == nil {
		t.Fatal("writer attestation missing")
	}
	applied, err := s.Apply(t.Context(), plan.ID, ApplyOptions{WriterStopped: true})
	if err != nil || applied.Status != "applied" || len(applied.Recovery) != 1 {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, name))
	if !bytes.Equal(got, bytes.ReplaceAll(body, []byte{0xe4}, []byte("�"))) {
		t.Fatal("valid source bytes changed")
	}
	if r.Git("ls-files", "--stage", "-z") != indexBefore {
		t.Fatal("partial staging changed")
	}
	if _, err = s.Restore(t.Context(), applied.Recovery[0], true, ApplyOptions{WriterStopped: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(r.Root, name))
	if !bytes.Equal(got, body) {
		t.Fatal("invalid original bytes not restored")
	}
}

func TestEncodingRepairPlansFailClosed(t *testing.T) {
	for _, kind := range []string{"stale", "tamper", "review", "writer"} {
		t.Run(kind, func(t *testing.T) {
			s, r := testService(t)
			put(t, r.Root, "a.txt", "x\xe4")
			p, err := s.PreviewRepairEncoding(t.Context(), []string{"a.txt"}, "replace")
			if err != nil {
				t.Fatal(err)
			}
			opts := ApplyOptions{}
			switch kind {
			case "stale":
				put(t, r.Root, "a.txt", "new\xe4")
			case "tamper":
				var record planRecord
				if err = s.load(t.Context(), p.ID, &record); err != nil {
					t.Fatal(err)
				}
				record.Plan.Files[0].Encoding.InvalidBytes++
				if err = s.save(t.Context(), p.ID, record); err != nil {
					t.Fatal(err)
				}
			case "review":
				if err = os.WriteFile(filepath.Join(s.Dir, p.ReviewFile), []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "writer":
				opts.Guard = func(context.Context, string, []string) error { return errors.New("writer active") }
			}
			before, _ := os.ReadFile(filepath.Join(r.Root, "a.txt"))
			if _, err = s.Apply(t.Context(), p.ID, opts); err == nil {
				t.Fatal("unsafe plan applied")
			}
			after, _ := os.ReadFile(filepath.Join(r.Root, "a.txt"))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed working file")
			}
		})
	}
}

func TestEncodingRepairSelectionNoopAndEmptyRemoval(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "a.txt", "\x80")
	for _, files := range [][]string{nil, {"../a"}, {".git/config"}, {".GIT/config"}, {"sub/.git/config"}, {".git./config"}, {"a.txt:stream"}, {"a.txt", "a.txt"}, {"missing"}, {"a.png"}} {
		if _, err := s.PreviewRepairEncoding(t.Context(), files, "replace"); err == nil {
			t.Fatalf("bad selection accepted: %v", files)
		}
	}
	p, err := s.PreviewRepairEncoding(t.Context(), []string{"a.txt"}, "remove")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), p.ID, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(r.Root, "a.txt")); err != nil || len(b) != 0 {
		t.Fatal("empty repair removed file")
	}
	p, err = s.PreviewRepairEncoding(t.Context(), []string{"a.txt"}, "replace")
	if err != nil || len(p.Files) != 0 {
		t.Fatalf("no-op: %+v %v", p, err)
	}
	if _, err = s.Apply(t.Context(), p.ID, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "target")
		if err = os.WriteFile(outside, []byte("\x80"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(outside, filepath.Join(r.Root, "link.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err = s.PreviewRepairEncoding(t.Context(), []string{"link.txt"}, "replace"); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func TestEncodingRepairPartialLedgerAndBackupFailure(t *testing.T) {
	for _, backupFailure := range []bool{false, true} {
		s, r := testService(t)
		put(t, r.Root, "a.txt", "a\x80")
		put(t, r.Root, "b.txt", "b\x80")
		p, err := s.PreviewRepairEncoding(t.Context(), []string{"a.txt", "b.txt"}, "replace")
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		opts := ApplyOptions{Guard: func(context.Context, string, []string) error {
			calls++
			if calls == 3 {
				return errors.New("writer appeared")
			}
			return nil
		}}
		if backupFailure {
			if err = os.WriteFile(filepath.Join(s.Dir, "recovery"), []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := s.Apply(t.Context(), p.ID, opts)
		if err == nil || result.Status != "partial" {
			t.Fatalf("partial result: %+v %v", result, err)
		}
		wantCompleted := 1
		if backupFailure {
			wantCompleted = 0
		}
		if len(result.Completed) != wantCompleted {
			t.Fatalf("completed=%v", result.Completed)
		}
		saved, err := s.ReadPlan(t.Context(), p.ID)
		if err != nil || len(saved.Completed) != wantCompleted || saved.Status != "partial" {
			t.Fatal("ledger lost")
		}
		if b, _ := os.ReadFile(filepath.Join(r.Root, "b.txt")); string(b) != "b\x80" {
			t.Fatal("second file changed")
		}
		if backupFailure {
			if b, _ := os.ReadFile(filepath.Join(r.Root, "a.txt")); string(b) != "a\x80" {
				t.Fatal("backup failure changed source")
			}
		}
	}
}

func TestEncodingGapsIdentifyScannedVersion(t *testing.T) {
	s, r := testService(t)
	put(t, r.Root, "cut.md", "first\nprivate-context\xe4\n")
	r.Git("add", "cut.md")
	r.Git("commit", "-m", "invalid text fixture")
	commit := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	put(t, r.Root, "cut.md", "staged\x80")
	r.Git("add", "cut.md")
	put(t, r.Root, "cut.md", "valid working text\n")
	for _, scope := range []string{"staged", "history"} {
		report, err := s.Scan(t.Context(), ScanOptions{Scope: scope})
		if err == nil || report.Status != "partial" || len(report.Gaps) != 1 {
			t.Fatalf("%s: %+v %v", scope, report, err)
		}
		g := report.Gaps[0]
		if g.Code != "unsupported_text_encoding" || g.Encoding == nil || g.Encoding.InvalidBytes != 1 {
			t.Fatalf("gap: %+v", g)
		}
		if scope == "history" && (g.Commit != commit || g.Encoding.Line != 2) {
			t.Fatal("historical location lost")
		}
		if scope == "staged" && (g.Commit != "" || g.Encoding.ByteOffset != 6) {
			t.Fatal("did not inspect staged bytes")
		}
		public, _ := json.Marshal(report)
		if bytes.Contains(public, []byte("private-context")) {
			t.Fatal("raw source escaped into diagnostics")
		}
	}
	if report, err := s.Scan(t.Context(), ScanOptions{Scope: "worktree"}); err != nil || report.Status != "complete" {
		t.Fatal("valid worktree rejected")
	}
	result, err := s.InspectSnapshot(t.Context(), "cut.md", []byte{'x', 0x80}, false)
	if err == nil || result.Report.Status != "partial" || result.Report.Gaps[0].Encoding.ByteOffset != 1 {
		t.Fatal("snapshot encoding diagnostics missing")
	}
}

func TestEncodingRepairLargeText(t *testing.T) {
	if testing.Short() {
		t.Skip("large encoding repair")
	}
	data := bytes.Repeat([]byte("valid 中文 line\r\n"), 1<<20)
	data = append(data, 0xe4)
	issue := inspectEncoding(data)
	got, err := repairUTF8(t.Context(), data, issue, "replace", MaxFileBytes)
	if err != nil || issue.InvalidBytes != 1 || !bytes.Equal(got[:len(data)-1], data[:len(data)-1]) || string(got[len(data)-1:]) != "�" {
		t.Fatal("large repair mismatch")
	}
}
