package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func newFeedbackTest(t *testing.T) (Store, string) {
	t.Helper()
	store := Store{Dir: filepath.Join(t.TempDir(), "feedback")}
	r, err := store.Create(context.Background(), DraftRequest{Title: "dev panic", Body: "## Reproduction\n\ndev ssh\n\nExpected menu; actual panic.", Facts: Facts{Version: "v0.2.24", OS: "darwin", Arch: "arm64", Installation: "homebrew"}})
	if err != nil {
		t.Fatal(err)
	}
	return store, r.Report.ID
}
func TestDraftPrivacyAndEditedPublicPreview(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "feedback")}
	private := "User private-account\nHostName secret.internal\nIdentityFile /home/private/key\nhttps://user:password@example.com/path\n192.0.2.30\nSHA256:abcd1234\ntoken=example-secret-value\n"
	r, err := store.Create(context.Background(), DraftRequest{Title: "Connection issue", Body: private})
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.PreviewIssue(context.Background(), IssueRequest{ID: r.Report.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"private-account", "secret.internal", "/home/private/key", "user:password", "192.0.2.30", "abcd1234", "example-secret-value"} {
		if strings.Contains(p.Body, value) {
			t.Fatalf("leaked %q", value)
		}
	}
	contextData, err := store.read(context.Background(), r.Report.ID, "context.json")
	if err != nil || !bytes.Contains(contextData, []byte("private-account")) {
		t.Fatal("private supplied evidence was lost", err)
	}
	if err = store.write(context.Background(), r.Report.ID, "public.md", []byte("password=new-secret-value\nupdated"), true); err != nil {
		t.Fatal(err)
	}
	updated, err := store.PreviewIssue(context.Background(), IssueRequest{ID: r.Report.ID})
	if err != nil || updated.Revision == p.Revision || strings.Contains(updated.Body, "new-secret-value") {
		t.Fatal(updated, err)
	}
	if _, err = store.Load(context.Background(), "../outside"); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestFeedbackInputProjectionRejectsForgedDiagnostic(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "feedback")}
	_, err := store.Create(context.Background(), DraftRequest{Title: "Issue", Body: "body", Diagnostic: []byte(`{"schema_version":9,"kind":"ssh_diagnosis"}`)})
	if err == nil {
		t.Fatal("accepted unknown schema")
	}
	if _, err = os.Stat(store.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid input created state")
	}
}

type feedbackFakeReporter struct {
	publishCount, findCount int
	publishErr              error
	found                   string
	last                    forge.IssuePublication
}

func (f *feedbackFakeReporter) Search(context.Context, forge.IssueTarget, string) ([]forge.IssueSummary, error) {
	return nil, nil
}
func (f *feedbackFakeReporter) Publish(_ context.Context, p forge.IssuePublication) (string, error) {
	f.publishCount++
	f.last = p
	if f.publishErr != nil {
		return "", f.publishErr
	}
	return "https://github.com/daviddwlee84/dev-cli/issues/123", nil
}
func (f *feedbackFakeReporter) FindMarker(context.Context, forge.IssueTarget, int, string) (string, error) {
	f.findCount++
	return f.found, nil
}
func TestIssueRevisionAndUnknownReconciliation(t *testing.T) {
	store, id := newFeedbackTest(t)
	ctx := context.Background()
	preview, err := store.PreviewIssue(ctx, IssueRequest{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	reporter := &feedbackFakeReporter{publishErr: &forge.IssueError{Code: "timeout", Unknown: true}}
	if _, err = store.PublishIssue(ctx, IssueRequest{ID: id, Revision: "stale"}, reporter); !errors.Is(err, ErrStale) || reporter.publishCount != 0 {
		t.Fatal("stale publication executed", err)
	}
	request := IssueRequest{ID: id, Revision: preview.Revision}
	result, err := store.PublishIssue(ctx, request, reporter)
	if err == nil || result.Status != "unknown" || reporter.publishCount != 1 {
		t.Fatal(result, err)
	}
	result, err = store.PublishIssue(ctx, request, reporter)
	if err == nil || result.Status != "unknown" || reporter.publishCount != 1 || reporter.findCount != 1 {
		t.Fatal("retry duplicated unknown request", result, err)
	}
	reporter.found = "https://github.com/daviddwlee84/dev-cli/issues/123"
	result, err = store.PublishIssue(ctx, request, reporter)
	if err != nil || result.Status != "confirmed" || result.URL != reporter.found || reporter.publishCount != 1 {
		t.Fatal(result, err)
	}
	result, err = store.PublishIssue(ctx, request, reporter)
	if err != nil || result.Status != "confirmed" || reporter.publishCount != 1 {
		t.Fatal("confirmed issue recreated", err)
	}
	changed, err := store.PreviewIssue(ctx, IssueRequest{ID: id, Repository: "github.com/example/fork"})
	if err != nil || changed.Revision == preview.Revision {
		t.Fatal("target not bound into revision")
	}
}
func TestIssueKnownFailureCanRetryAndCommentIsExplicit(t *testing.T) {
	store, id := newFeedbackTest(t)
	ctx := context.Background()
	request := IssueRequest{ID: id, Existing: 42}
	preview, _ := store.PreviewIssue(ctx, request)
	request.Revision = preview.Revision
	reporter := &feedbackFakeReporter{publishErr: &forge.IssueError{Code: "unauthenticated"}}
	result, err := store.PublishIssue(ctx, request, reporter)
	if err == nil || result.Status != "not_published" {
		t.Fatal(result, err)
	}
	reporter.publishErr = nil
	result, err = store.PublishIssue(ctx, request, reporter)
	if err != nil || result.Status != "confirmed" || reporter.last.Existing != 42 || reporter.publishCount != 2 {
		t.Fatal(result, err)
	}
	if !strings.Contains(reporter.last.Body, "<!-- dev-feedback:"+id+":") {
		t.Fatal("missing recovery marker")
	}
}

type fakeRepairBackend struct {
	source        RepairSource
	snapshot      RepairSnapshot
	created       int
	invalid       bool
	partial       bool
	beforeObserve func()
}

func (b *fakeRepairBackend) Sources(context.Context, string) ([]RepairSource, error) {
	return []RepairSource{b.source}, nil
}
func (b *fakeRepairBackend) Observe(_ context.Context, _ RepairSource, base, branch string) (RepairSnapshot, error) {
	if b.beforeObserve != nil {
		b.beforeObserve()
	}
	p := b.snapshot
	p.Source = b.source
	p.BaseRef = base
	p.Branch = branch
	if b.invalid {
		p.BaseOID = "changed"
	}
	return p, nil
}
func (b *fakeRepairBackend) CreateLocked(_ context.Context, p RepairSnapshot) (RepairBinding, error) {
	b.created++
	binding := RepairBinding{Snapshot: p, CheckoutIdentity: "checkout", TaskSaved: !b.partial, Partial: b.partial}
	if b.partial {
		return binding, errors.New("task store failed")
	}
	return binding, nil
}
func (b *fakeRepairBackend) ValidateBinding(_ context.Context, p RepairBinding) error {
	if p.Partial {
		return errors.New("partial workspace")
	}
	return nil
}
func testRepairBackend(t *testing.T) *fakeRepairBackend {
	t.Helper()
	repo := gittest.New(t)
	return &fakeRepairBackend{source: RepairSource{Path: repo.Root, CommonDir: filepath.Join(repo.Root, ".git")}, snapshot: RepairSnapshot{Checkout: filepath.Join(t.TempDir(), "repair"), BaseOID: "original", TaskID: "task", ConfigRevision: "config", RootIdentity: "root", CommonIdentity: "git"}}
}
func TestRepairStalePlanCreatesNothing(t *testing.T) {
	for _, change := range []string{"base", "report", "tamper"} {
		t.Run(change, func(t *testing.T) {
			store, id := newFeedbackTest(t)
			backend := testRepairBackend(t)
			ctx := context.Background()
			plan, err := store.PlanRepair(ctx, RepairRequest{ReportID: id, Base: "main"}, backend)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "base":
				backend.invalid = true
			case "report":
				if err = store.write(ctx, id, "public.md", []byte("edited"), true); err != nil {
					t.Fatal(err)
				}
			case "tamper":
				plan.Snapshot.BaseOID = "forged"
				if err = store.writeJSON(ctx, id, "plan-"+plan.ID+".json", plan, true); err != nil {
					t.Fatal(err)
				}
			}
			result, err := store.ApplyRepair(ctx, id, plan.ID, backend)
			if !errors.Is(err, ErrStale) || backend.created != 0 || result.Status != "stale_plan" {
				t.Fatal(result, err, backend.created)
			}
		})
	}
}
func TestRepairResultIsRetainedAndRepeatSafe(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[partial], func(t *testing.T) {
			store, id := newFeedbackTest(t)
			backend := testRepairBackend(t)
			backend.partial = partial
			ctx := context.Background()
			plan, err := store.PlanRepair(ctx, RepairRequest{ReportID: id, Base: "main"}, backend)
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.ApplyRepair(ctx, id, plan.ID, backend)
			if result.Binding == nil || backend.created != 1 || partial != (err != nil) {
				t.Fatal(result, err)
			}
			saved, err := store.Load(ctx, id)
			if err != nil || saved.Repair == nil {
				t.Fatal("binding not retained", err)
			}
			_, _ = store.ApplyRepair(ctx, id, plan.ID, backend)
			if backend.created != 1 {
				t.Fatal("repeat created another workspace")
			}
		})
	}
}
func TestSourceIdentityAndPrecedence(t *testing.T) {
	repo := gittest.New(t)
	repo.Git("remote", "add", "origin", "https://github.com/example/dev-cli.git")
	if _, err := VerifySource(context.Background(), repo.Root); err == nil {
		t.Fatal("directory/fork name accepted as upstream proof")
	}
	repo.Git("remote", "add", "upstream", "https://github.com/daviddwlee84/dev-cli.git")
	source, err := VerifySource(context.Background(), repo.Root)
	if err != nil || !source.Fork || source.Remote != "upstream" {
		t.Fatal(source, err)
	}
	cfg := config.Default()
	cfg.Paths.RepoPaths = []string{repo.Root}
	cfg.Paths.ScanRoots = nil
	if _, err := Sources(context.Background(), cfg, filepath.Join(t.TempDir(), "dev-cli"), repo.Root); err == nil {
		t.Fatal("invalid explicit path fell back")
	}
	found, err := Sources(context.Background(), cfg, "", repo.Root)
	if err != nil || len(found) != 1 {
		t.Fatal(found, err)
	}
	another := gittest.New(t)
	another.Git("remote", "add", "origin", "https://github.com/daviddwlee84/dev-cli.git")
	cfg.Paths.RepoPaths = append(cfg.Paths.RepoPaths, another.Root)
	found, err = Sources(context.Background(), cfg, "", "")
	if err != nil || len(found) != 2 {
		t.Fatal("multiple sources were guessed or lost", found, err)
	}
}
func TestPublicEvidenceRoundTrip(t *testing.T) {
	store, id := newFeedbackTest(t)
	preview, err := store.PreviewIssue(context.Background(), IssueRequest{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(preview)
	if !json.Valid(data) || preview.Target.String() != forge.DefaultIssueRepository {
		t.Fatal(preview)
	}
}

func TestRepairPromptRejectsAnotherReportsBinding(t *testing.T) {
	store, id := newFeedbackTest(t)
	backend := testRepairBackend(t)
	ctx := context.Background()
	plan, err := store.PlanRepair(ctx, RepairRequest{ReportID: id, Base: "main"}, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ApplyRepair(ctx, id, plan.ID, backend); err != nil {
		t.Fatal(err)
	}
	report, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	report.Repair.Snapshot.Branch = "fix/feedback-another-report"
	if err = store.writeJSON(ctx, id, "report.json", report, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.RepairContext(ctx, id, backend); !errors.Is(err, ErrStale) {
		t.Fatal("cross-report binding accepted", err)
	}
}

func TestRepairRechecksReportAfterWaitingForRepositoryAuthority(t *testing.T) {
	store, id := newFeedbackTest(t)
	backend := testRepairBackend(t)
	ctx := context.Background()
	plan, err := store.PlanRepair(ctx, RepairRequest{ReportID: id, Base: "main"}, backend)
	if err != nil {
		t.Fatal(err)
	}
	backend.beforeObserve = func() {
		if err := store.write(ctx, id, "public.md", []byte("changed during final observation"), true); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.ApplyRepair(ctx, id, plan.ID, backend)
	if !errors.Is(err, ErrStale) || backend.created != 0 || result.Status != "stale_plan" {
		t.Fatal(result, err)
	}
}

func TestFeedbackSanitizesKeyRecordsAndTruncatedPrivateKeys(t *testing.T) {
	for _, input := range []string{"ssh-ed25519 AAAABBBBCCCCDDDDEEEEFFFF private-account", "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-content-without-end-marker"} {
		out := Sanitize(input)
		if strings.Contains(out, "AAAABBBB") || strings.Contains(out, "private-account") || strings.Contains(out, "private-content") {
			t.Fatal("key material remained in public text")
		}
	}
}
