// Package snippet owns provider-independent small-file sharing and search.
// Snippets never acquire repository, worktree or task lifecycle ownership.
package snippet

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Kind string

const (
	GitHub            Kind  = "github"
	GitLab            Kind  = "gitlab"
	All               Kind  = "all"
	MaxFileBytes      int64 = 1 << 20
	MaxSearchBytes    int64 = 16 << 20
	MaxCreateBytes    int64 = 16 << 20
	SearchConcurrency       = 4
	OperationTimeout        = 60 * time.Second
)

type Identity struct {
	Forge     Kind   `json:"forge"`
	Host      string `json:"host"`
	ID        string `json:"id"`
	ProjectID int64  `json:"project_id,omitempty"`
	Project   string `json:"project,omitempty"`
}

func (id Identity) Key() string {
	project := id.Project
	if id.ProjectID > 0 {
		project = strconv.FormatInt(id.ProjectID, 10)
	}
	return string(id.Forge) + ":" + id.Host + ":" + project + ":" + id.ID
}

type File struct {
	Name      string `json:"name"`
	Ref       string `json:"ref,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Item struct {
	Identity
	Title         string    `json:"title"`
	Description   string    `json:"description,omitempty"`
	Owner         string    `json:"owner,omitempty"`
	URL           string    `json:"url"`
	Visibility    string    `json:"visibility"`
	UpdatedAt     time.Time `json:"updated_at"`
	Files         []File    `json:"files"`
	FilesComplete bool      `json:"files_complete"`
	// Revision is the immutable GitHub revision used for content reads, when
	// provided by the detail endpoint. No content is retained in Item.
	Revision string `json:"revision,omitempty"`
}

type Issue struct {
	Forge   Kind   `json:"forge,omitempty"`
	Host    string `json:"host,omitempty"`
	ID      string `json:"id,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ListOptions struct {
	Forge   Kind
	Project string
	Query   string
	Content bool
}

type ListResult struct {
	SchemaVersion int       `json:"schema_version"`
	Items         []Item    `json:"items"`
	Complete      bool      `json:"complete"`
	Issues        []Issue   `json:"issues"`
	FetchedAt     time.Time `json:"fetched_at"`
}

type Content struct {
	Bytes    []byte
	Complete bool
}

type InputFile struct {
	Name    string
	Content []byte
}

type CreateRequest struct {
	Forge         Kind
	Host          string
	Project       string
	Title         string
	Description   string
	Visibility    string
	ExpectedOwner string
	Files         []InputFile
}

type CreateOutcome string

const (
	Created    CreateOutcome = "created"
	NotCreated CreateOutcome = "not-created"
	Unknown    CreateOutcome = "unknown"
)

type CreateResult struct {
	Outcome CreateOutcome `json:"outcome"`
	Item    Item          `json:"item"`
}

// Provider owns only explicit remote observations and publication. ReadContent
// must honor maxBytes and must never fetch a URL from untrusted response data.
type Provider interface {
	Kind() Kind
	Host() string
	Available() bool
	List(context.Context, string) (ListResult, error)
	Get(context.Context, Identity) (Item, error)
	ReadContent(context.Context, Item, File, int64) (Content, error)
	Create(context.Context, CreateRequest) (CreateResult, error)
}

type Service struct{ providers []Provider }

func New(providers ...Provider) *Service {
	return &Service{providers: append([]Provider(nil), providers...)}
}

func (s *Service) Providers() []Provider { return append([]Provider(nil), s.providers...) }

// ValidateCreate returns a private frozen copy of the exact text to publish.
// Defaults retain the providers' distinct access semantics: GitHub secret is
// link-readable; GitLab private requires authorization.
func ValidateCreate(request CreateRequest) (CreateRequest, error) {
	if request.Forge != GitHub && request.Forge != GitLab {
		return CreateRequest{}, errors.New("choose exactly one snippet forge: github or gitlab")
	}
	if request.Host != "" {
		if err := ValidateHost(request.Host); err != nil {
			return CreateRequest{}, err
		}
	}
	if request.Project != "" {
		if request.Forge != GitLab {
			return CreateRequest{}, errors.New("--project is available only for GitLab snippets")
		}
		if err := ValidateProject(request.Project); err != nil {
			return CreateRequest{}, err
		}
	}
	if request.Visibility == "" {
		if request.Forge == GitHub {
			request.Visibility = "secret"
		} else {
			request.Visibility = "private"
		}
	}
	valid := request.Visibility == "public" || (request.Forge == GitHub && request.Visibility == "secret") ||
		(request.Forge == GitLab && (request.Visibility == "private" || request.Visibility == "internal"))
	if !valid {
		return CreateRequest{}, fmt.Errorf("visibility %q is not supported by %s", request.Visibility, request.Forge)
	}
	if request.Forge == GitLab && strings.EqualFold(request.Host, "gitlab.com") && request.Visibility == "internal" {
		return CreateRequest{}, errors.New("GitLab.com does not support internal snippet visibility")
	}
	if len(request.Files) == 0 {
		return CreateRequest{}, errors.New("at least one text file is required")
	}
	if request.Forge == GitLab && len(request.Files) > 10 {
		return CreateRequest{}, errors.New("GitLab snippets support at most 10 files")
	}
	seen := make(map[string]bool)
	var total int64
	files := make([]InputFile, 0, len(request.Files))
	for _, file := range request.Files {
		if file.Name == "" || file.Name == "." || file.Name == ".." || strings.ContainsAny(file.Name, "/\\\x00\r\n") || !utf8.ValidString(file.Name) {
			return CreateRequest{}, errors.New("snippet filenames must be nonempty basenames without path separators or control characters")
		}
		for _, ch := range file.Name {
			if ch < 32 || ch == 127 {
				return CreateRequest{}, errors.New("snippet filenames cannot contain control characters")
			}
		}
		if seen[file.Name] {
			return CreateRequest{}, fmt.Errorf("duplicate snippet filename %q", file.Name)
		}
		seen[file.Name] = true
		if !utf8.Valid(file.Content) || strings.IndexByte(string(file.Content), 0) >= 0 {
			return CreateRequest{}, fmt.Errorf("%s is not UTF-8 text", file.Name)
		}
		if strings.TrimSpace(string(file.Content)) == "" {
			return CreateRequest{}, fmt.Errorf("%s is empty", file.Name)
		}
		total += int64(len(file.Content))
		if total > MaxCreateBytes {
			return CreateRequest{}, errors.New("snippet text exceeds the 16 MiB creation limit")
		}
		files = append(files, InputFile{Name: file.Name, Content: append([]byte(nil), file.Content...)})
	}
	request.Files = files
	if request.Title == "" {
		request.Title = files[0].Name
	}
	if request.Forge == GitHub && request.Description == "" {
		request.Description = request.Title
	}
	if !utf8.ValidString(request.Title) || !utf8.ValidString(request.Description) || strings.ContainsRune(request.Title+request.Description, '\x00') {
		return CreateRequest{}, errors.New("snippet title and description must be valid text")
	}
	return request, nil
}

func (s *Service) provider(kind Kind, host string) (Provider, error) {
	for _, p := range s.providers {
		if p.Kind() == kind && (host == "" || strings.EqualFold(host, p.Host())) {
			if !p.Available() {
				return nil, fmt.Errorf("%s snippet support needs its installed forge CLI", kind)
			}
			return p, nil
		}
	}
	return nil, fmt.Errorf("no configured snippet provider for %s on %s", kind, host)
}

func (s *Service) Get(ctx context.Context, identity Identity) (Item, error) {
	p, err := s.provider(identity.Forge, identity.Host)
	if err != nil {
		return Item{}, err
	}
	identity.Host = p.Host()
	if err := ValidateIdentity(identity); err != nil {
		return Item{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	return p.Get(ctx, identity)
}

// Account observes the selected endpoint's authenticated account for a create
// preview. Providers may omit this optional capability in read-only adapters.
func (s *Service) Account(ctx context.Context, kind Kind) (string, error) {
	p, err := s.provider(kind, "")
	if err != nil {
		return "", err
	}
	reader, ok := p.(interface {
		Account(context.Context) (string, error)
	})
	if !ok {
		return "", errors.New("snippet provider cannot identify its authenticated account")
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	return reader.Account(ctx)
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (CreateResult, error) {
	p, err := s.provider(request.Forge, request.Host)
	if err != nil {
		return CreateResult{Outcome: NotCreated}, err
	}
	request.Host = p.Host()
	request, err = ValidateCreate(request)
	if err != nil {
		return CreateResult{Outcome: NotCreated}, err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return CreateResult{Outcome: NotCreated}, err
	}
	return p.Create(ctx, request)
}

func (s *Service) List(ctx context.Context, options ListOptions) (ListResult, error) {
	result := ListResult{SchemaVersion: 1, Items: []Item{}, Complete: true, Issues: []Issue{}, FetchedAt: time.Now().UTC()}
	if options.Forge != "" && options.Forge != All && options.Forge != GitHub && options.Forge != GitLab {
		return result, errors.New("snippet forge must be github, gitlab or all")
	}
	if options.Project != "" {
		if options.Forge != GitLab {
			return result, errors.New("--project requires --forge gitlab")
		}
		if err := ValidateProject(options.Project); err != nil {
			return result, err
		}
	}
	terms := strings.Fields(strings.ToLower(options.Query))
	if options.Content && len(terms) == 0 {
		return result, errors.New("--content requires a nonempty search query")
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	var failures []error
	selected := 0
	providers := make(map[string]Provider)
	for _, p := range s.providers {
		if options.Forge != "" && options.Forge != All && p.Kind() != options.Forge {
			continue
		}
		if !p.Available() {
			if options.Forge == "" || options.Forge == All {
				continue
			}
			selected++
			err := fmt.Errorf("%s snippet support needs its installed forge CLI", p.Kind())
			failures = append(failures, err)
			result.Issues = append(result.Issues, Issue{Forge: p.Kind(), Host: p.Host(), Code: "missing-cli", Message: err.Error()})
			continue
		}
		selected++
		observed, err := p.List(ctx, options.Project)
		result.Items = append(result.Items, observed.Items...)
		result.Issues = append(result.Issues, observed.Issues...)
		if !observed.Complete {
			result.Complete = false
		}
		if err != nil {
			failures = append(failures, err)
			result.Issues = append(result.Issues, Issue{Forge: p.Kind(), Host: p.Host(), Code: "provider-failed", Message: err.Error()})
		}
		providers[string(p.Kind())+":"+p.Host()] = p
	}
	if selected == 0 {
		failures = append(failures, errors.New("install and authenticate gh or glab to list snippets"))
	}
	if len(failures) > 0 {
		result.Complete = false
	}
	if options.Content {
		issues := s.searchContent(ctx, &result, providers, terms)
		if len(issues) > 0 {
			result.Complete = false
			result.Issues = append(result.Issues, issues...)
			failures = append(failures, errors.New("snippet content search is incomplete; see issues"))
		}
	} else {
		filtered := make([]Item, 0, len(result.Items))
		for _, item := range result.Items {
			if matches(metadata(item), terms) {
				filtered = append(filtered, item)
			}
		}
		result.Items = filtered
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		if !result.Items[i].UpdatedAt.Equal(result.Items[j].UpdatedAt) {
			return result.Items[i].UpdatedAt.After(result.Items[j].UpdatedAt)
		}
		return result.Items[i].Key() < result.Items[j].Key()
	})
	return result, errors.Join(failures...)
}

func metadata(item Item) string {
	parts := []string{string(item.Forge), item.Host, item.ID, item.Title, item.Description, item.Owner, item.Visibility, item.Project}
	for _, file := range item.Files {
		parts = append(parts, file.Name)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func matches(text string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func (s *Service) searchContent(ctx context.Context, result *ListResult, providers map[string]Provider, terms []string) []Issue {
	items := result.Items
	matched := make([]bool, len(items))
	issuesByItem := make([][]Issue, len(items))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var budgetMu sync.Mutex
	remaining := MaxSearchBytes
	for worker := 0; worker < SearchConcurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				item := items[index]
				hay := metadata(item)
				if matches(hay, terms) {
					matched[index] = true
					continue
				}
				problem := func(code, message string) {
					issuesByItem[index] = append(issuesByItem[index], Issue{Forge: item.Forge, Host: item.Host, ID: item.ID, Code: code, Message: message})
				}
				p := providers[string(item.Forge)+":"+item.Host]
				if p == nil {
					problem("provider-missing", "snippet provider is unavailable")
					continue
				}
				detail, err := p.Get(ctx, item.Identity)
				if err != nil {
					problem("detail-failed", err.Error())
					continue
				}
				items[index] = detail
				hay = metadata(detail)
				if !detail.FilesComplete {
					problem("files-incomplete", "provider did not report a complete file inventory")
				}
				foundTerms := make([]bool, len(terms))
				for i, term := range terms {
					foundTerms[i] = strings.Contains(hay, term)
				}
				for _, file := range detail.Files {
					budgetMu.Lock()
					allowance := min(MaxFileBytes, remaining)
					remaining -= allowance
					budgetMu.Unlock()
					if allowance <= 0 {
						problem("search-limit", "16 MiB content search limit reached")
						break
					}
					content, err := p.ReadContent(ctx, detail, file, allowance)
					if int64(len(content.Bytes)) > allowance {
						content.Bytes = content.Bytes[:allowance]
						content.Complete = false
					}
					budgetMu.Lock()
					remaining += allowance - int64(len(content.Bytes))
					budgetMu.Unlock()
					if err != nil {
						problem("content-failed", fmt.Sprintf("%s: %s", file.Name, err))
						continue
					}
					if !content.Complete {
						problem("content-incomplete", fmt.Sprintf("%s: content is truncated or exceeds the search limit", file.Name))
					}
					if !utf8.Valid(content.Bytes) || strings.IndexByte(string(content.Bytes), 0) >= 0 {
						problem("non-text", fmt.Sprintf("%s is not UTF-8 text", file.Name))
						continue
					}
					text := strings.ToLower(string(content.Bytes))
					all := true
					for i, term := range terms {
						foundTerms[i] = foundTerms[i] || strings.Contains(text, term)
						all = all && foundTerms[i]
					}
					if all {
						matched[index] = true
						break
					}
				}
				if matches(hay, terms) {
					matched[index] = true
				}
			}
		}()
	}
	for i := range items {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	result.Items = make([]Item, 0, len(items))
	var issues []Issue
	for i, item := range items {
		if matched[i] {
			result.Items = append(result.Items, item)
		}
		issues = append(issues, issuesByItem[i]...)
	}
	return issues
}
