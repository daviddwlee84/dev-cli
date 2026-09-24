package prflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
)

type mergeProviderFixture struct {
	detail  Detail
	merges  int
	outcome forge.PRMergeOutcome
	err     error
	after   *Detail
}

func (f *mergeProviderFixture) ListPage(context.Context, Query) (Page, error) { return Page{}, nil }
func (f *mergeProviderFixture) Detail(context.Context, Reference) (Detail, error) {
	if f.merges > 0 && f.after != nil {
		return *f.after, nil
	}
	return f.detail, nil
}
func (f *mergeProviderFixture) Diff(context.Context, Detail) (Diff, error) { return Diff{}, nil }
func (f *mergeProviderFixture) Merge(context.Context, Detail) (forge.PRMergeOutcome, error) {
	f.merges++
	return f.outcome, f.err
}
func mergeFixture(t *testing.T) (*Service, *mergeProviderFixture) {
	t.Helper()
	t.Setenv("GH_HOST", "")
	ref := Reference{Forge: forge.GitHub, Host: "github.com", Repo: "acme/demo", Number: 1}
	d := Detail{PullRequest: forge.PullRequest{Forge: forge.GitHub, Host: ref.Host, Repo: ref.Repo, Number: 1, State: forge.PRStateOpen, Title: "PR", HeadBranch: "feature", BaseBranch: "main", Checks: forge.ChecksPassing}, Reference: ref, AccountID: "1", RepositoryID: "7", HeadOID: strings.Repeat("a", 40), BaseOID: strings.Repeat("b", 40), Readiness: "ready", CanMerge: true, SquashAllowed: true}
	f := &mergeProviderFixture{detail: d, outcome: forge.PRMergeOutcome{Status: "merged", MergeOID: strings.Repeat("c", 40)}}
	return &Service{Provider: f, StateDir: t.TempDir()}, f
}

func TestMergeRevalidatesIdentityBeforeEffect(t *testing.T) {
	for _, change := range []string{"head", "account", "base", "queue", "title", "policy"} {
		t.Run(change, func(t *testing.T) {
			s, f := mergeFixture(t)
			p, err := s.PlanMerge(t.Context(), f.detail.Reference)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "head":
				f.detail.HeadOID = strings.Repeat("d", 40)
			case "account":
				f.detail.AccountID = "2"
			case "base":
				f.detail.BaseOID = strings.Repeat("e", 40)
			case "queue":
				f.detail.Queue = true
			case "title":
				f.detail.Title = "changed"
			case "policy":
				f.detail.AutoDeleteBranch = true
			}
			_, err = s.ApplyMerge(t.Context(), p)
			if !errors.Is(err, ErrStaleMerge) || f.merges != 0 {
				t.Fatalf("stale write: %v merges=%d", err, f.merges)
			}
		})
	}
}
func TestMergePlanCannotBeEditedOrReplayed(t *testing.T) {
	s, f := mergeFixture(t)
	p, err := s.PlanMerge(t.Context(), f.detail.Reference)
	if err != nil {
		t.Fatal(err)
	}
	tampered := p
	tampered.Detail.HeadOID = strings.Repeat("e", 40)
	if _, err = s.ApplyMerge(t.Context(), tampered); !errors.Is(err, ErrStaleMerge) {
		t.Fatalf("tampering accepted: %v", err)
	}
	result, err := s.ApplyMerge(t.Context(), p)
	if err != nil || result.Status != "merged" {
		t.Fatalf("%#v %v", result, err)
	}
	if _, err = s.ApplyMerge(t.Context(), p); err == nil || f.merges != 1 {
		t.Fatalf("replayed merges=%d err=%v", f.merges, err)
	}
}
func TestUnknownMergePersistsAndPreventsNewPlanRetry(t *testing.T) {
	s, f := mergeFixture(t)
	f.outcome = forge.PRMergeOutcome{Status: "unknown"}
	f.err = errors.New("connection lost")
	p, _ := s.PlanMerge(t.Context(), f.detail.Reference)
	result, err := s.ApplyMerge(t.Context(), p)
	if !errors.Is(err, ErrUnknownMerge) || result.Status != "unknown" {
		t.Fatalf("%#v %v", result, err)
	}
	if _, err = os.Stat(result.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(strings.TrimSuffix(result.ReceiptPath, ".attempt.json") + ".result.json"); err != nil {
		t.Fatal(err)
	}
	next, _ := s.PlanMerge(t.Context(), f.detail.Reference)
	_, err = s.ApplyMerge(t.Context(), next)
	if !errors.Is(err, ErrUnknownMerge) || f.merges != 1 {
		t.Fatalf("unknown retried: %v calls=%d", err, f.merges)
	}
}
func TestUnknownMergeCanBeConfirmedByReadOnlyReconciliation(t *testing.T) {
	s, f := mergeFixture(t)
	f.outcome = forge.PRMergeOutcome{Status: "unknown"}
	f.err = errors.New("timeout")
	after := f.detail
	after.State = forge.PRStateMerged
	after.MergeOID = strings.Repeat("f", 40)
	f.after = &after
	p, _ := s.PlanMerge(t.Context(), f.detail.Reference)
	result, err := s.ApplyMerge(t.Context(), p)
	if err != nil || result.Status != "merged" || result.MergeOID != after.MergeOID || f.merges != 1 {
		t.Fatalf("%#v %v calls=%d", result, err, f.merges)
	}
}
func TestMergeRequiresPrivateReceiptBeforeWrite(t *testing.T) {
	s, f := mergeFixture(t)
	p, _ := s.PlanMerge(t.Context(), f.detail.Reference)
	s.StateDir = "relative-state"
	_, err := s.ApplyMerge(t.Context(), p)
	if err == nil {
		t.Fatal("relative private path accepted")
	}
	if f.merges != 0 {
		t.Fatal("merge started without receipt")
	}
}
func TestMergeReceiptFailureBeforeWrite(t *testing.T) {
	s, f := mergeFixture(t)
	p, _ := s.PlanMerge(t.Context(), f.detail.Reference)
	if err := os.WriteFile(filepath.Join(s.StateDir, "pr-merges"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyMerge(t.Context(), p)
	if err == nil || f.merges != 0 {
		t.Fatalf("write despite receipt failure: %v", err)
	}
}

func TestMergeKeepsSharedStatePermissionsAndProtectsReceipts(t *testing.T) {
	s, f := mergeFixture(t)
	if err := os.Chmod(s.StateDir, 0755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PlanMerge(t.Context(), f.detail.Reference)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplyMerge(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != before.Mode().Perm() {
		t.Fatal("shared state permissions changed")
	}
	for _, path := range []string{filepath.Join(s.StateDir, "pr-merges"), filepath.Dir(result.ReceiptPath), result.ReceiptPath, strings.TrimSuffix(result.ReceiptPath, ".attempt.json") + ".result.json"} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = privatefile.Check(path, info, info.IsDir()); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestMergeRejectsGitLabTargetAdvanceWithSameDiffVersion(t *testing.T) {
	s, f := mergeFixture(t)
	t.Setenv("GITLAB_HOST", "")
	t.Setenv("GLAB_HOST", "")
	f.detail.Reference = Reference{Forge: forge.GitLab, Host: "gitlab.com", Repo: "acme/demo", Number: 1}
	f.detail.Forge = forge.GitLab
	f.detail.Host = "gitlab.com"
	f.detail.DiffStartOID = strings.Repeat("9", 40)
	p, err := s.PlanMerge(t.Context(), f.detail.Reference)
	if err != nil {
		t.Fatal(err)
	}
	f.detail.BaseOID = strings.Repeat("d", 40)
	_, err = s.ApplyMerge(t.Context(), p)
	if !errors.Is(err, ErrStaleMerge) || f.merges != 0 || f.detail.DiffStartOID != p.Detail.DiffStartOID {
		t.Fatalf("historical diff version authorized changed target: %v calls=%d", err, f.merges)
	}
}
