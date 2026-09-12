package hygiene

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRedactionPreservesSourceAndIndex(t *testing.T) {
	s, r := testService(t)
	input := []byte(strings.Repeat("ordinary line\n", 1<<21) + "private-host-unique\n")
	r.Write("history.md", string(input))
	r.Git("add", "history.md")
	index := r.Git("write-tree")
	result, err := s.InspectSnapshot(t.Context(), "history.md", input, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replacements != 1 || bytes.Contains(result.Data, []byte("private-host-unique")) || !result.Report.OK() {
		t.Fatal("incorrect snapshot redaction")
	}
	source, err := os.ReadFile(filepath.Join(r.Root, "history.md"))
	if err != nil || !bytes.Equal(source, input) || r.Git("write-tree") != index {
		t.Fatal("snapshot operation changed source or index")
	}
	public, _ := json.Marshal(result)
	if bytes.Contains(public, []byte("private-host-unique")) {
		t.Fatal("raw snapshot escaped JSON")
	}
	if _, err = s.InspectSnapshot(t.Context(), "history.md", input, false); err == nil {
		t.Fatal("unredacted blocking content passed")
	}
}

func TestSnapshotCacheBindsPathContentPolicyAndIntegrity(t *testing.T) {
	s, _ := testService(t)
	data := []byte("ordinary text\n")
	a, e := s.InspectSnapshot(t.Context(), "first.md", data, false)
	if e != nil || a.Cached {
		t.Fatalf("first: %v", e)
	}
	b, e := s.InspectSnapshot(t.Context(), "first.md", data, false)
	if e != nil || !b.Cached {
		t.Fatalf("cache: %v", e)
	}
	c, e := s.InspectSnapshot(t.Context(), "second.md", data, false)
	if e != nil || c.Cached {
		t.Fatalf("path binding: %v", e)
	}
	s.Policy.Rules = append(s.Policy.Rules, Rule{ID: "new-rule", Kind: "literal", Value: "ordinary", Replacement: "example"})
	d, e := s.InspectSnapshot(t.Context(), "first.md", data, false)
	if e == nil || d.Cached {
		t.Fatal("new rule did not invalidate cache")
	}
	s.Policy.Rules = s.Policy.Rules[:1]
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "snapshots"))
	for _, entry := range entries {
		p := filepath.Join(s.Dir, "snapshots", entry.Name())
		if e = os.WriteFile(p, []byte("{}"), 0o600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.InspectSnapshot(t.Context(), "first.md", data, false); e == nil {
		t.Fatal("tampered cache accepted")
	}
}

func TestSnapshotDisabledPolicyIsSkipped(t *testing.T) {
	s, _ := testService(t)
	s.Policy.Known = Off
	r, e := s.InspectSnapshot(t.Context(), "file.md", []byte("private-host-unique"), false)
	if e != nil || r.Report.Status != "skipped" || !r.Report.PolicyDisabled {
		t.Fatalf("disabled policy: %+v %v", r.Report, e)
	}
}
