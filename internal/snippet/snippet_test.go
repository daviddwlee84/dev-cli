package snippet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryProvider struct {
	kind                                        Kind
	available                                   bool
	items                                       []Item
	listErr                                     error
	getErr                                      error
	content                                     map[string]Content
	contentErr                                  error
	mu                                          sync.Mutex
	listCalls, getCalls, readCalls, createCalls int
	bytesRequested                              int64
	request                                     CreateRequest
}

func (p *memoryProvider) Kind() Kind      { return p.kind }
func (p *memoryProvider) Host() string    { return string(p.kind) + ".com" }
func (p *memoryProvider) Available() bool { return p.available }
func (p *memoryProvider) List(context.Context, string) (ListResult, error) {
	p.listCalls++
	return ListResult{Items: append([]Item(nil), p.items...), Complete: p.listErr == nil}, p.listErr
}
func (p *memoryProvider) Get(_ context.Context, id Identity) (Item, error) {
	p.mu.Lock()
	p.getCalls++
	p.mu.Unlock()
	for _, item := range p.items {
		if item.ID == id.ID {
			return item, p.getErr
		}
	}
	return Item{}, errors.New("missing")
}
func (p *memoryProvider) ReadContent(_ context.Context, _ Item, file File, limit int64) (Content, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readCalls++
	p.bytesRequested += limit
	return p.content[file.Name], p.contentErr
}
func (p *memoryProvider) Create(_ context.Context, r CreateRequest) (CreateResult, error) {
	p.createCalls++
	p.request = r
	return CreateResult{Outcome: Created}, nil
}

func sampleItem(kind Kind, id string) Item {
	return Item{Identity: Identity{Forge: kind, Host: string(kind) + ".com", ID: id}, Title: "Example", URL: "https://example.invalid/" + id, Files: []File{{Name: "code.py"}}, FilesComplete: true}
}

func TestListMetadataNeverFetchesContentAndPreservesPartialProviders(t *testing.T) {
	github := &memoryProvider{kind: GitHub, available: true, items: []Item{sampleItem(GitHub, "abc")}}
	github.items[0].Description = "Python session helper"
	gitlab := &memoryProvider{kind: GitLab, available: true, listErr: errors.New("GitLab unavailable")}
	result, err := New(github, gitlab).List(t.Context(), ListOptions{Query: "python CODE.py"})
	if err == nil || result.Complete || len(result.Items) != 1 || len(result.Issues) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if github.getCalls != 0 || github.readCalls != 0 {
		t.Fatal("metadata listing fetched content")
	}
}

func TestListAllSkipsUninstalledProviderButExplicitDoesNot(t *testing.T) {
	missing := &memoryProvider{kind: GitLab}
	ready := &memoryProvider{kind: GitHub, available: true}
	s := New(missing, ready)
	result, err := s.List(t.Context(), ListOptions{})
	if err != nil || !result.Complete || missing.listCalls != 0 || ready.listCalls != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = s.List(t.Context(), ListOptions{Forge: GitLab})
	if err == nil || result.Complete {
		t.Fatalf("missing explicit forge: %+v %v", result, err)
	}
}

func TestContentSearchReadsOnlyWhenRequestedAndReportsUnknownNonmatches(t *testing.T) {
	p := &memoryProvider{kind: GitHub, available: true, items: []Item{sampleItem(GitHub, "aaa"), sampleItem(GitHub, "bbb")}, content: map[string]Content{"code.py": {Bytes: []byte("needle"), Complete: false}}}
	p.items[0].Title = "needle metadata"
	result, err := New(p).List(t.Context(), ListOptions{Query: "needle", Content: true})
	if err == nil || result.Complete || len(result.Items) != 2 || p.getCalls != 1 || p.readCalls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d/%d", result, err, p.getCalls, p.readCalls)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), `"content"`) || strings.Contains(string(encoded), `"bytes"`) {
		t.Fatalf("content leaked to JSON: %s", encoded)
	}
	p.content = map[string]Content{"code.py": {Bytes: []byte("other"), Complete: false}}
	result, err = New(p).List(t.Context(), ListOptions{Query: "missing", Content: true})
	if err == nil || result.Complete || len(result.Items) != 0 || len(result.Issues) == 0 {
		t.Fatalf("unknown is not a clean miss: %+v %v", result, err)
	}
}

func TestContentSearchTermsMayMatchAcrossMetadataAndFiles(t *testing.T) {
	item := sampleItem(GitLab, "1")
	item.Title = "alpha"
	item.Files = []File{{Name: "one.py"}, {Name: "two.py"}}
	p := &memoryProvider{kind: GitLab, available: true, items: []Item{item}, content: map[string]Content{"one.py": {Bytes: []byte("BETA"), Complete: true}, "two.py": {Bytes: []byte("gamma"), Complete: true}}}
	result, err := New(p).List(t.Context(), ListOptions{Query: "alpha beta gamma", Content: true})
	if err != nil || !result.Complete || len(result.Items) != 1 || p.readCalls != 2 {
		t.Fatalf("result=%+v err=%v reads=%d", result, err, p.readCalls)
	}
}

func TestContentSearchBudgetBoundsUntrustedProvider(t *testing.T) {
	item := sampleItem(GitLab, "1")
	item.Files = nil
	contents := map[string]Content{}
	for i := 0; i < 20; i++ {
		name := string(rune('a' + i))
		item.Files = append(item.Files, File{Name: name})
		contents[name] = Content{Bytes: []byte(strings.Repeat("x", int(MaxFileBytes)+10)), Complete: true}
	}
	p := &memoryProvider{kind: GitLab, available: true, items: []Item{item}, content: contents}
	result, err := New(p).List(t.Context(), ListOptions{Query: "missing", Content: true})
	if err == nil || result.Complete || p.bytesRequested > MaxSearchBytes || p.readCalls != 16 {
		t.Fatalf("result=%+v err=%v requests=%d bytes=%d", result, err, p.readCalls, p.bytesRequested)
	}
}

func TestContentSearchMissingInventoryAndFetchFailuresRemainIncomplete(t *testing.T) {
	for _, scenario := range []string{"files", "fetch", "detail"} {
		t.Run(scenario, func(t *testing.T) {
			item := sampleItem(GitLab, "1")
			p := &memoryProvider{kind: GitLab, available: true, items: []Item{item}, content: map[string]Content{"code.py": {Bytes: []byte("no match"), Complete: true}}}
			switch scenario {
			case "files":
				p.items[0].FilesComplete = false
			case "fetch":
				p.contentErr = errors.New("unavailable")
			case "detail":
				p.getErr = errors.New("unavailable")
			}
			result, err := New(p).List(t.Context(), ListOptions{Content: true, Query: "wanted"})
			if err == nil || result.Complete || len(result.Issues) == 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestListInvalidScopeDoesNotQueryProvider(t *testing.T) {
	p := &memoryProvider{kind: GitHub, available: true}
	for _, options := range []ListOptions{{Forge: "other"}, {Content: true}, {Project: "group/project"}, {Forge: GitLab, Project: "../project"}} {
		if _, err := New(p).List(t.Context(), options); err == nil {
			t.Fatalf("accepted %+v", options)
		}
	}
	if p.listCalls != 0 {
		t.Fatal("invalid request queried provider")
	}
}

func TestListSortsNewestThenStableIdentity(t *testing.T) {
	p := &memoryProvider{kind: GitHub, available: true, items: []Item{sampleItem(GitHub, "bbb"), sampleItem(GitHub, "aaa"), sampleItem(GitHub, "ccc")}}
	p.items[2].UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := New(p).List(t.Context(), ListOptions{})
	if err != nil || result.Items[0].ID != "ccc" || result.Items[1].ID != "aaa" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestValidateCreateFreezesInputsAndProviderDefaults(t *testing.T) {
	data := []byte("print('ok')\n")
	request, err := ValidateCreate(CreateRequest{Forge: GitHub, Files: []InputFile{{Name: "code.py", Content: data}}})
	if err != nil || request.Visibility != "secret" || request.Title != "code.py" || request.Description != "code.py" {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	data[0] = 'X'
	if request.Files[0].Content[0] != 'p' {
		t.Fatal("request did not freeze the input")
	}
	request, err = ValidateCreate(CreateRequest{Forge: GitLab, Host: "gitlab.com", Files: request.Files})
	if err != nil || request.Visibility != "private" {
		t.Fatalf("request=%+v err=%v", request, err)
	}
}

func TestValidateCreateRejectsPublicationAmbiguities(t *testing.T) {
	base := CreateRequest{Forge: GitLab, Host: "gitlab.com", Files: []InputFile{{Name: "hello.txt", Content: []byte("hello")}}}
	for _, scenario := range []string{"forge", "empty", "duplicate", "binary", "path", "control", "visibility", "internal", "project", "size", "files"} {
		t.Run(scenario, func(t *testing.T) {
			r := base
			r.Files = append([]InputFile(nil), base.Files...)
			switch scenario {
			case "forge":
				r.Forge = All
			case "empty":
				r.Files[0].Content = []byte(" \n")
			case "duplicate":
				r.Files = append(r.Files, r.Files[0])
			case "binary":
				r.Files[0].Content = []byte{0xff, 0}
			case "path":
				r.Files[0].Name = "a/hello.txt"
			case "control":
				r.Files[0].Name = "a\x1b.txt"
			case "visibility":
				r.Visibility = "secret"
			case "internal":
				r.Visibility = "internal"
			case "project":
				r.Project = "../oops"
			case "size":
				r.Files[0].Content = []byte(strings.Repeat("x", int(MaxCreateBytes)+1))
			case "files":
				r.Files = make([]InputFile, 11)
			}
			if _, err := ValidateCreate(r); err == nil {
				t.Fatalf("accepted %s", scenario)
			}
		})
	}
}

func TestCreateValidationAndCancellationDoNotCallProvider(t *testing.T) {
	p := &memoryProvider{kind: GitLab, available: true}
	s := New(p)
	result, err := s.Create(t.Context(), CreateRequest{Forge: GitLab})
	if err == nil || result.Outcome != NotCreated || p.createCalls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, p.createCalls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = s.Create(ctx, CreateRequest{Forge: GitLab, Files: []InputFile{{Name: "file", Content: []byte("text")}}})
	if err == nil || result.Outcome != NotCreated || p.createCalls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, p.createCalls)
	}
}
