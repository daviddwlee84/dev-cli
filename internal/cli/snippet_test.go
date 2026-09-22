package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/snippet"
)

type cliSnippetProvider struct {
	kind         snippet.Kind
	calls, reads int
	request      snippet.CreateRequest
	listed       snippet.ListResult
	listErr      error
	outcome      snippet.CreateOutcome
	createErr    error
	beforeCreate func()
}

func (p *cliSnippetProvider) Kind() snippet.Kind { return p.kind }
func (p *cliSnippetProvider) Host() string {
	if p.kind == snippet.GitLab {
		return "gitlab.com"
	}
	return "github.com"
}
func (p *cliSnippetProvider) Available() bool { return true }
func (p *cliSnippetProvider) List(context.Context, string) (snippet.ListResult, error) {
	p.reads++
	return p.listed, p.listErr
}
func (p *cliSnippetProvider) Get(_ context.Context, id snippet.Identity) (snippet.Item, error) {
	p.reads++
	return snippet.Item{Identity: id, URL: "https://gist.github.com/me/abc123"}, nil
}
func (p *cliSnippetProvider) ReadContent(context.Context, snippet.Item, snippet.File, int64) (snippet.Content, error) {
	p.reads++
	return snippet.Content{Bytes: []byte("test"), Complete: true}, nil
}
func (p *cliSnippetProvider) Account(context.Context) (string, error) { return "fixture-user", nil }
func (p *cliSnippetProvider) Create(_ context.Context, req snippet.CreateRequest) (snippet.CreateResult, error) {
	p.calls++
	if p.beforeCreate != nil {
		p.beforeCreate()
	}
	p.request = req
	outcome := p.outcome
	if outcome == "" {
		outcome = snippet.Created
	}
	return snippet.CreateResult{Outcome: outcome, Item: snippet.Item{Identity: snippet.Identity{Forge: p.kind, Host: p.Host(), ID: "abc123"}, URL: "https://gist.github.com/me/abc123"}}, p.createErr
}

func snippetTestApp(t *testing.T, input string, providers ...*cliSnippetProvider) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	app, out := newRepoWizardApp(t, input)
	app.interactiveCheck = func() bool { return false }
	errOut := &bytes.Buffer{}
	app.Err = errOut
	list := []snippet.Provider{}
	for _, p := range providers {
		list = append(list, p)
	}
	app.snippetService = snippet.New(list...)
	return app, out, errOut
}
func executeSnippet(t *testing.T, app *App, gist bool, args ...string) error {
	t.Helper()
	cmd := newSnippetCmd(app, gist)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestSnippetCreateDryRunFreezesMetadataWithoutProviderOrContentOutput(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, out, _ := snippetTestApp(t, "print('private draft body')\n", p)
	if err := executeSnippet(t, app, false, "create", "-", "--forge", "github", "--filename", "example.py", "--dry-run", "--json"); err != nil {
		t.Fatal(err)
	}
	var preview snippetCreatePreview
	if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Visibility != "secret" || len(preview.Files) != 1 || preview.Files[0].Name != "example.py" || preview.Files[0].Bytes == 0 {
		t.Fatalf("preview: %+v", preview)
	}
	if p.calls != 0 || p.reads != 0 || strings.Contains(out.String(), "private draft body") {
		t.Fatalf("dry run executed provider or disclosed content: calls=%d reads=%d out=%s", p.calls, p.reads, out)
	}
}
func TestSnippetCreateRejectsConflictingNamesBeforePublishing(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, _, _ := snippetTestApp(t, "", p)
	one, two := t.TempDir(), t.TempDir()
	for _, dir := range []string{one, two} {
		if err := os.WriteFile(filepath.Join(dir, "demo.py"), []byte("print('x')"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := executeSnippet(t, app, true, "create", filepath.Join(one, "demo.py"), filepath.Join(two, "demo.py"))
	if err == nil || !strings.Contains(err.Error(), "duplicate") || p.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, p.calls)
	}
}
func TestSnippetCreateStdinExplicitGitLabProject(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitLab}
	app, out, _ := snippetTestApp(t, "print(42)\n", p)
	if err := executeSnippet(t, app, false, "create", "-", "--project", "group/project", "--filename", "answer.py", "--title", "Answer", "--json"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || p.request.Forge != snippet.GitLab || p.request.Project != "group/project" || p.request.Visibility != "private" || string(p.request.Files[0].Content) != "print(42)\n" {
		t.Fatalf("request=%+v calls=%d", p.request, p.calls)
	}
	if strings.Contains(out.String(), "print(42)") {
		t.Fatalf("receipt contains content: %s", out)
	}
}
func TestSnippetCreateUnknownNeverRetries(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub, outcome: snippet.Unknown, createErr: errors.New("connection ended")}
	app, out, errOut := snippetTestApp(t, "text\n", p)
	err := executeSnippet(t, app, true, "create", "-", "--filename", "note.txt", "--json")
	if err == nil || p.calls != 1 || !strings.Contains(out.String(), `"outcome": "unknown"`) || !strings.Contains(errOut.String(), "inspect dev snippet list") {
		t.Fatalf("error=%v calls=%d out=%s err=%s", err, p.calls, out, errOut)
	}
}
func TestSnippetCreateBrowserFailureKeepsSuccess(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, out, errOut := snippetTestApp(t, "text\n", p)
	app.snippetOpenURL = func(context.Context, string) error { return errors.New("no browser") }
	if err := executeSnippet(t, app, true, "create", "-", "--filename", "note.txt", "--web"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || !strings.Contains(out.String(), "https://gist.github.com/me/abc123") || !strings.Contains(errOut.String(), "snippet created") {
		t.Fatalf("calls=%d out=%s err=%s", p.calls, out, errOut)
	}
}
func TestSnippetCreateRequiresExplicitDestinationWithStdin(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, _, _ := snippetTestApp(t, "text\n", p)
	err := executeSnippet(t, app, false, "create", "-", "--filename", "note.txt")
	if err == nil || !strings.Contains(err.Error(), "--forge") || p.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, p.calls)
	}
}
func TestSnippetListPartialJSONPreservesRowsAndError(t *testing.T) {
	item := snippet.Item{Identity: snippet.Identity{Forge: snippet.GitHub, Host: "github.com", ID: "abc123"}, Description: "useful demo", Files: []snippet.File{{Name: "demo.py"}}, FilesComplete: true}
	p := &cliSnippetProvider{kind: snippet.GitHub, listed: snippet.ListResult{Items: []snippet.Item{item}, Complete: false}, listErr: errors.New("second page failed")}
	app, out, _ := snippetTestApp(t, "", p)
	err := executeSnippet(t, app, false, "search", "demo", "--json")
	var result snippet.ListResult
	if e := json.Unmarshal(out.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if err == nil || result.Complete || len(result.Items) != 1 {
		t.Fatalf("error=%v result=%+v", err, result)
	}
}
func TestSnippetContentRequiresQueryWithoutProviderRead(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, _, _ := snippetTestApp(t, "", p)
	err := executeSnippet(t, app, false, "list", "--content")
	if err == nil || p.reads != 0 {
		t.Fatalf("error=%v reads=%d", err, p.reads)
	}
}
func TestSnippetEditorCancellationRetainsDraft(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, out, _ := snippetTestApp(t, "draft.py\n\n\nn\n", p)
	app.interactiveCheck = func() bool { return true }
	useFixtureTextEditor(t, t.TempDir(), "print('keep draft')\n")
	if err := executeSnippet(t, app, true, "create"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || !strings.Contains(out.String(), "Canceled; nothing was published") {
		t.Fatalf("calls=%d out=%s", p.calls, out)
	}
	drafts, err := filepath.Glob(filepath.Join(app.Cfg.StateDir(), "snippets", "drafts", "*"))
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts=%v error=%v", drafts, err)
	}
	body, err := os.ReadFile(drafts[0])
	if err != nil || string(body) != "print('keep draft')\n" {
		t.Fatalf("draft body=%q error=%v", body, err)
	}
}
func TestSnippetOpenPrintDoesNotLaunchBrowser(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, out, _ := snippetTestApp(t, "", p)
	app.snippetOpenURL = func(context.Context, string) error { t.Fatal("browser opened"); return nil }
	if err := executeSnippet(t, app, true, "open", "abc123", "--print"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://gist.github.com/me/abc123") {
		t.Fatalf("output=%s", out)
	}
}

func TestSnippetEditorSuccessRetainsNewerDraft(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, _, errOut := snippetTestApp(t, "draft.py\n\n\ny\n", p)
	app.interactiveCheck = func() bool { return true }
	useFixtureTextEditor(t, t.TempDir(), "print('reviewed')\n")
	p.beforeCreate = func() {
		drafts, err := filepath.Glob(filepath.Join(app.Cfg.StateDir(), "snippets", "drafts", "*"))
		if err != nil || len(drafts) != 1 {
			t.Fatalf("drafts=%v error=%v", drafts, err)
		}
		if err = os.WriteFile(drafts[0], []byte("print('newer user draft')\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := executeSnippet(t, app, true, "create"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || string(p.request.Files[0].Content) != "print('reviewed')\n" {
		t.Fatalf("publication=%+v", p.request)
	}
	drafts, _ := filepath.Glob(filepath.Join(app.Cfg.StateDir(), "snippets", "drafts", "*"))
	if len(drafts) != 1 || !strings.Contains(errOut.String(), "draft retained") {
		t.Fatalf("drafts=%v warnings=%s", drafts, errOut)
	}
	body, err := os.ReadFile(drafts[0])
	if err != nil || string(body) != "print('newer user draft')\n" {
		t.Fatalf("draft=%q error=%v", body, err)
	}
}

func TestSnippetEditorSuccessRemovesOnlyPublishedDraft(t *testing.T) {
	p := &cliSnippetProvider{kind: snippet.GitHub}
	app, _, _ := snippetTestApp(t, "draft.py\n\n\ny\n", p)
	app.interactiveCheck = func() bool { return true }
	useFixtureTextEditor(t, t.TempDir(), "print('published')\n")
	if err := executeSnippet(t, app, true, "create"); err != nil {
		t.Fatal(err)
	}
	drafts, _ := filepath.Glob(filepath.Join(app.Cfg.StateDir(), "snippets", "drafts", "*"))
	if p.calls != 1 || len(drafts) != 0 {
		t.Fatalf("calls=%d drafts=%v", p.calls, drafts)
	}
}
