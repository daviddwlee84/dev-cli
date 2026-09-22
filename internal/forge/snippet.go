package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/forgemetrics"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
)

// SnippetCommand is a fixed API subprocess request. Content travels exclusively
// through Stdin, never through arguments, environment variables or a file.
type SnippetCommand struct {
	Bin            string
	Args           []string
	Stdin          []byte
	MaxOutputBytes int64
}

type SnippetCommandResult struct {
	Stdout   []byte
	Stderr   []byte
	Started  bool
	Overflow bool
}

type SnippetRunner func(context.Context, SnippetCommand) (SnippetCommandResult, error)

type snippetProvider struct {
	kind      snippet.Kind
	host      string
	runner    SnippetRunner
	available func(string) bool
}

func NewSnippetService() *snippet.Service { return newSnippetService(runSnippetCommand, have) }

// NewSnippetServiceWithRunner makes API transports hermetic in UI/CLI tests.
// Injected transports represent available CLIs; they never fall back to native
// executables. Authentication remains an explicit request to the transport.
func NewSnippetServiceWithRunner(runner SnippetRunner) *snippet.Service {
	return newSnippetService(runner, func(string) bool { return true })
}

func newSnippetService(runner SnippetRunner, available func(string) bool) *snippet.Service {
	return snippet.New(
		&snippetProvider{kind: snippet.GitHub, host: defaultForgeHost(GitHub), runner: runner, available: available},
		&snippetProvider{kind: snippet.GitLab, host: gitLabHost(), runner: runner, available: available},
	)
}

func (p *snippetProvider) Kind() snippet.Kind { return p.kind }
func (p *snippetProvider) Host() string       { return p.host }
func (p *snippetProvider) Available() bool    { return p.available(p.bin()) }
func (p *snippetProvider) bin() string {
	if p.kind == snippet.GitLab {
		return "glab"
	}
	return "gh"
}

func (p *snippetProvider) request(ctx context.Context, method, endpoint string, body []byte, maxBytes int64, extra ...string) (SnippetCommandResult, error) {
	if err := snippet.ValidateHost(p.host); err != nil {
		return SnippetCommandResult{}, err
	}
	if !p.Available() {
		return SnippetCommandResult{}, &ErrNoCLI{Kind: Kind(p.kind), Bin: p.bin()}
	}
	args := []string{"api", endpoint, "--hostname", p.host, "--method", method}
	if body != nil {
		args = append(args, "--input", "-", "--header", "Content-Type: application/json", "--include")
	}
	args = append(args, extra...)
	return p.runner(ctx, SnippetCommand{Bin: p.bin(), Args: args, Stdin: body, MaxOutputBytes: maxBytes})
}

func (p *snippetProvider) read(ctx context.Context, endpoint string, maxBytes int64, extra ...string) ([]byte, error) {
	result, err := p.request(ctx, "GET", endpoint, nil, maxBytes, extra...)
	if result.Overflow {
		return nil, errors.New("snippet provider response exceeds the read limit")
	}
	if err != nil {
		classified := classifyAuth(Kind(p.kind), p.bin(), &commandError{Bin: p.bin(), Detail: string(result.Stderr), Err: err})
		if IsAuth(classified) {
			return nil, classified
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s snippet API request failed", p.bin())
	}
	return result.Stdout, nil
}

func (p *snippetProvider) Account(ctx context.Context) (string, error) {
	data, err := p.read(ctx, "user", 1<<20)
	if err != nil {
		return "", err
	}
	var user struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if json.Unmarshal(data, &user) != nil {
		return "", errors.New("forge did not identify an authenticated snippet account")
	}
	name := user.Login
	if p.kind == snippet.GitLab {
		name = user.Username
	}
	if name == "" {
		return "", errors.New("forge did not identify an authenticated snippet account")
	}
	return name, nil
}

func (p *snippetProvider) List(ctx context.Context, project string) (snippet.ListResult, error) {
	result := snippet.ListResult{SchemaVersion: 1, Items: []snippet.Item{}, Complete: false, Issues: []snippet.Issue{}, FetchedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, snippet.OperationTimeout)
	defer cancel()
	endpoint := "gists"
	if p.kind == snippet.GitHub {
		if project != "" {
			return result, errors.New("GitHub gists do not have project scope")
		}
		// Anonymous GET /gists is a public feed. Require account evidence first.
		if _, err := p.Account(ctx); err != nil {
			return result, err
		}
	} else {
		endpoint = "snippets"
		if project != "" {
			if err := snippet.ValidateProject(project); err != nil {
				return result, err
			}
			endpoint = "projects/" + snippetAPISegment(project) + "/snippets"
		}
	}
	seen := map[string]bool{}
	for page := 1; page <= 1000; page++ {
		data, err := p.read(ctx, endpoint, 32<<20, "-f", "per_page=100", "-f", fmt.Sprintf("page=%d", page))
		if err != nil {
			return result, err
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
			return result, errors.New("invalid snippet inventory response")
		}
		newCount := 0
		for _, body := range raw {
			item, err := p.parse(body)
			if err != nil {
				return result, err
			}
			if err := validateSnippetProject(project, item); err != nil {
				return result, err
			}
			if seen[item.Key()] {
				continue
			}
			seen[item.Key()] = true
			newCount++
			result.Items = append(result.Items, item)
			if !item.FilesComplete {
				result.Issues = append(result.Issues, snippet.Issue{Forge: p.kind, Host: p.host, ID: item.ID, Code: "files-incomplete", Message: "provider did not report a complete filename inventory"})
			}
		}
		if len(raw) < 100 {
			result.Complete = len(result.Issues) == 0
			return result, nil
		}
		if newCount == 0 {
			return result, errors.New("snippet provider repeated a pagination page")
		}
	}
	return result, errors.New("snippet inventory exceeds the 100000 item limit")
}

func (p *snippetProvider) endpoint(id snippet.Identity) (string, error) {
	if id.Forge != p.kind || !strings.EqualFold(id.Host, p.host) {
		return "", errors.New("snippet identity belongs to a different provider endpoint")
	}
	if err := snippet.ValidateIdentity(id); err != nil {
		return "", err
	}
	if p.kind == snippet.GitHub {
		return "gists/" + id.ID, nil
	}
	project := id.Project
	if id.ProjectID > 0 {
		project = strconv.FormatInt(id.ProjectID, 10)
	}
	if project != "" {
		return "projects/" + snippetAPISegment(project) + "/snippets/" + id.ID, nil
	}
	return "snippets/" + id.ID, nil
}

func (p *snippetProvider) Get(ctx context.Context, id snippet.Identity) (snippet.Item, error) {
	endpoint, err := p.endpoint(id)
	if err != nil {
		return snippet.Item{}, err
	}
	data, err := p.read(ctx, endpoint, 32<<20)
	if err != nil {
		return snippet.Item{}, err
	}
	item, err := p.parse(data)
	if err != nil {
		return snippet.Item{}, err
	}
	if item.ID != id.ID || (id.ProjectID > 0 && item.ProjectID != id.ProjectID) {
		return snippet.Item{}, errors.New("provider returned a different snippet identity")
	}
	if err := validateSnippetProject(id.Project, item); err != nil {
		return snippet.Item{}, err
	}
	return item, nil
}

func (p *snippetProvider) parse(data []byte) (snippet.Item, error) {
	if p.kind == snippet.GitHub {
		return p.parseGitHub(data)
	}
	return p.parseGitLab(data)
}

func (p *snippetProvider) parseGitHub(data []byte) (snippet.Item, error) {
	var raw struct {
		ID          string          `json:"id"`
		NodeID      json.RawMessage `json:"node_id"`
		Comments    json.RawMessage `json:"comments"`
		Description string          `json:"description"`
		URL         string          `json:"html_url"`
		Public      *bool           `json:"public"`
		Truncated   bool            `json:"truncated"`
		UpdatedAt   time.Time       `json:"updated_at"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
		Files map[string]struct {
			Filename  string `json:"filename"`
			Size      int64  `json:"size"`
			Truncated bool   `json:"truncated"`
		} `json:"files"`
		History []struct {
			Version string `json:"version"`
		} `json:"history"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.Public == nil {
		return snippet.Item{}, errors.New("invalid GitHub gist metadata")
	}
	item := snippet.Item{Identity: snippet.Identity{Forge: p.kind, Host: p.host, ID: raw.ID}, Title: raw.Description, Description: raw.Description, Owner: raw.Owner.Login, URL: raw.URL, Visibility: "secret", UpdatedAt: raw.UpdatedAt, Files: []snippet.File{}, FilesComplete: raw.Files != nil && !raw.Truncated}
	_ = json.Unmarshal(raw.NodeID, &item.NodeID)
	item.Metrics = &forgemetrics.Metrics{Comments: restMetricCount(raw.Comments, time.Now().UTC()), OpenIssues: forgemetrics.Unsupported(), OpenPRs: forgemetrics.Unsupported()}
	if *raw.Public {
		item.Visibility = "public"
	}
	if len(raw.Files) >= 300 {
		item.FilesComplete = false
	}
	for name, file := range raw.Files {
		if file.Filename != "" && file.Filename != name {
			return snippet.Item{}, errors.New("GitHub gist has inconsistent file metadata")
		}
		item.Files = append(item.Files, snippet.File{Name: name, Size: file.Size, Truncated: file.Truncated})
	}
	sort.Slice(item.Files, func(i, j int) bool { return item.Files[i].Name < item.Files[j].Name })
	if item.Title == "" && len(item.Files) > 0 {
		item.Title = item.Files[0].Name
	}
	if len(raw.History) > 0 {
		version := raw.History[0].Version
		if len(version) != 40 || !hexString(version) {
			return snippet.Item{}, errors.New("invalid GitHub gist revision")
		}
		item.Revision = version
	}
	if err := p.validateItem(item); err != nil {
		return snippet.Item{}, err
	}
	return item, nil
}

func (p *snippetProvider) parseGitLab(data []byte) (snippet.Item, error) {
	var raw struct {
		ID          int64     `json:"id"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		URL         string    `json:"web_url"`
		Visibility  string    `json:"visibility"`
		UpdatedAt   time.Time `json:"updated_at"`
		ProjectID   int64     `json:"project_id"`
		Filename    string    `json:"file_name"`
		Author      struct {
			Username string `json:"username"`
		} `json:"author"`
		Files *[]struct {
			Path   string `json:"path"`
			RawURL string `json:"raw_url"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return snippet.Item{}, errors.New("invalid GitLab snippet metadata")
	}
	item := snippet.Item{Identity: snippet.Identity{Forge: p.kind, Host: p.host, ID: strconv.FormatInt(raw.ID, 10), ProjectID: raw.ProjectID}, Title: raw.Title, Description: raw.Description, Owner: raw.Author.Username, URL: raw.URL, Visibility: raw.Visibility, UpdatedAt: raw.UpdatedAt, Files: []snippet.File{}, FilesComplete: raw.Files != nil}
	item.Metrics = snippet.GitLabMetrics()
	if parsed, err := snippet.ParseReference(item.URL, p.kind, map[snippet.Kind]string{p.kind: p.host}); err == nil {
		item.Project = parsed.Project
	}
	if raw.Files != nil {
		for _, file := range *raw.Files {
			ref := gitLabSnippetRef(p.host, item.ID, file.Path, file.RawURL)
			item.Files = append(item.Files, snippet.File{Name: file.Path, Ref: ref})
		}
	} else if raw.Filename != "" {
		item.Files = append(item.Files, snippet.File{Name: raw.Filename})
	}
	sort.Slice(item.Files, func(i, j int) bool { return item.Files[i].Name < item.Files[j].Name })
	if err := p.validateItem(item); err != nil {
		return snippet.Item{}, err
	}
	return item, nil
}

// Extract only the provider-generated ref from an exact matching web raw URL.
// The URL is never fetched; the native API route is reconstructed independently.
func gitLabSnippetRef(host, id, name, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	marker := "/snippets/" + id + "/raw/"
	index := strings.LastIndex(u.Path, marker)
	if index < 0 {
		return ""
	}
	value := u.Path[index+len(marker):]
	suffix := "/" + name
	if !strings.HasSuffix(value, suffix) {
		return ""
	}
	ref := strings.TrimSuffix(value, suffix)
	if !safeSnippetPath(ref) {
		return ""
	}
	return ref
}

func (p *snippetProvider) validateItem(item snippet.Item) error {
	if err := snippet.ValidateIdentity(item.Identity); err != nil {
		return err
	}
	parsed, err := snippet.ParseReference(item.URL, p.kind, map[snippet.Kind]string{p.kind: p.host})
	if err != nil || parsed.ID != item.ID {
		return errors.New("provider returned an unexpected snippet URL")
	}
	if p.kind == snippet.GitLab && ((item.ProjectID == 0 && parsed.Project != "") || (item.ProjectID > 0 && parsed.Project == "") || parsed.Project != item.Project) {
		return errors.New("provider returned inconsistent snippet project scope")
	}
	if p.kind == snippet.GitLab && item.Visibility != "public" && item.Visibility != "private" && item.Visibility != "internal" {
		return errors.New("provider returned unknown GitLab snippet visibility")
	}
	seen := map[string]bool{}
	for _, file := range item.Files {
		if !safeSnippetPath(file.Name) || seen[file.Name] || file.Size < 0 {
			return errors.New("provider returned invalid snippet file metadata")
		}
		seen[file.Name] = true
	}
	return nil
}

func validateSnippetProject(requested string, item snippet.Item) error {
	if requested == "" {
		return nil
	}
	if numeric, err := strconv.ParseInt(requested, 10, 64); err == nil && numeric > 0 {
		if item.ProjectID == numeric {
			return nil
		}
	} else if item.Project == requested {
		return nil
	}
	return errors.New("provider returned a snippet from a different project")
}

func safeSnippetPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") {
		return false
	}
	for _, ch := range value {
		if ch < 32 || ch == 127 {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (p *snippetProvider) ReadContent(ctx context.Context, item snippet.Item, file snippet.File, maxBytes int64) (snippet.Content, error) {
	if maxBytes <= 0 || maxBytes > snippet.MaxFileBytes {
		return snippet.Content{}, errors.New("invalid snippet content byte allowance")
	}
	if !safeSnippetPath(file.Name) {
		return snippet.Content{}, errors.New("invalid snippet file path")
	}
	endpoint, err := p.endpoint(item.Identity)
	if err != nil {
		return snippet.Content{}, err
	}
	if p.kind == snippet.GitHub {
		if item.Revision != "" {
			if len(item.Revision) != 40 || !hexString(item.Revision) {
				return snippet.Content{}, errors.New("invalid gist revision")
			}
			endpoint += "/" + item.Revision
		}
		key, _ := json.Marshal(file.Name)
		projection := ".files[" + string(key) + "] | {content, truncated, size}"
		data, err := p.read(ctx, endpoint, maxBytes*6+2048, "--jq", projection)
		if err != nil {
			return snippet.Content{}, err
		}
		var raw struct {
			Content   *string `json:"content"`
			Truncated bool    `json:"truncated"`
			Size      int64   `json:"size"`
		}
		if json.Unmarshal(data, &raw) != nil || raw.Content == nil {
			return snippet.Content{}, errors.New("gist file content is unavailable")
		}
		content := []byte(*raw.Content)
		complete := !raw.Truncated && int64(len(content)) <= maxBytes && raw.Size <= int64(len(content))
		if int64(len(content)) > maxBytes {
			content = content[:maxBytes]
		}
		return snippet.Content{Bytes: content, Complete: complete}, nil
	}
	if !safeSnippetPath(file.Ref) {
		return snippet.Content{}, errors.New("provider did not report a source reference for this snippet file")
	}
	endpoint += "/files/" + snippetAPISegment(file.Ref) + "/" + snippetAPISegment(file.Name) + "/raw"
	result, err := p.request(ctx, "GET", endpoint, nil, maxBytes+1)
	content := result.Stdout
	complete := !result.Overflow && int64(len(content)) <= maxBytes
	if int64(len(content)) > maxBytes {
		content = content[:maxBytes]
	}
	if err != nil && !result.Overflow {
		if ctx.Err() != nil {
			return snippet.Content{}, ctx.Err()
		}
		return snippet.Content{}, errors.New("GitLab snippet file request failed")
	}
	return snippet.Content{Bytes: content, Complete: complete}, nil
}

func (p *snippetProvider) Create(ctx context.Context, request snippet.CreateRequest) (snippet.CreateResult, error) {
	failed := snippet.CreateResult{Outcome: snippet.NotCreated}
	if request.Host != "" && !strings.EqualFold(request.Host, p.host) {
		return failed, errors.New("creation request belongs to a different provider endpoint")
	}
	request.Host = p.host
	request, err := snippet.ValidateCreate(request)
	if err != nil {
		return failed, err
	}
	if request.Forge != p.kind {
		return failed, errors.New("creation request does not match the selected forge")
	}
	owner, err := p.Account(ctx)
	if err != nil {
		return failed, err
	}
	if request.ExpectedOwner != "" && request.ExpectedOwner != owner {
		return failed, errors.New("authenticated snippet account changed after the preview")
	}
	endpoint := "gists"
	var payload any
	if p.kind == snippet.GitHub {
		files := map[string]map[string]string{}
		for _, file := range request.Files {
			files[file.Name] = map[string]string{"content": string(file.Content)}
		}
		payload = map[string]any{"description": request.Description, "public": request.Visibility == "public", "files": files}
	} else {
		endpoint = "snippets"
		if request.Project != "" {
			endpoint = "projects/" + snippetAPISegment(request.Project) + "/snippets"
		}
		files := make([]map[string]string, 0, len(request.Files))
		for _, file := range request.Files {
			files = append(files, map[string]string{"file_path": file.Name, "content": string(file.Content)})
		}
		payload = map[string]any{"title": request.Title, "description": request.Description, "visibility": request.Visibility, "files": files}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return failed, errors.New("could not encode snippet creation request")
	}
	response, runErr := p.request(ctx, "POST", endpoint, body, 32<<20)
	status, responseBody := snippetHTTPResponse(response.Stdout)
	if status == 201 {
		item, err := p.parse(responseBody)
		if err == nil {
			created := snippet.CreateResult{Outcome: snippet.Created, Item: item}
			if err := validateSnippetProject(request.Project, item); err != nil {
				return created, err
			}
			if request.Project == "" && item.ProjectID > 0 {
				return created, errors.New("provider created a project snippet for a personal request")
			}
			if item.Owner != "" && item.Owner != owner {
				return created, errors.New("provider created the snippet under a different account")
			}
			if item.Visibility != request.Visibility {
				return created, errors.New("provider created the snippet with a different visibility")
			}
			return created, nil
		}
	}
	if !response.Started || status == 400 || status == 401 || status == 403 || status == 404 || status == 405 || status == 413 || status == 415 || status == 422 || status == 429 {
		if status > 0 {
			return failed, fmt.Errorf("%s rejected snippet creation (HTTP %d)", p.kind, status)
		}
		if runErr != nil {
			return failed, fmt.Errorf("%s snippet creation process could not start", p.bin())
		}
	}
	return snippet.CreateResult{Outcome: snippet.Unknown}, errors.New("snippet creation outcome is unknown; check your snippet list before trying again")
}

func snippetHTTPResponse(data []byte) (int, []byte) {
	status := 0
	for bytes.HasPrefix(data, []byte("HTTP/")) {
		line, _, ok := bytes.Cut(data, []byte("\n"))
		if !ok {
			return status, nil
		}
		fields := strings.Fields(string(line))
		if len(fields) < 2 {
			return status, nil
		}
		status, _ = strconv.Atoi(fields[1])
		index := bytes.Index(data, []byte("\r\n\r\n"))
		width := 4
		if index < 0 {
			index = bytes.Index(data, []byte("\n\n"))
			width = 2
		}
		if index < 0 {
			return status, nil
		}
		data = data[index+width:]
	}
	return status, data
}

// glab expands colon-prefixed endpoint placeholders from its current checkout.
// PathEscape leaves colons intact, so additionally encode them before passing
// any remote path/ref to the native CLI. GitLab receives the exact literal value.
func snippetAPISegment(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), ":", "%3A")
}

func hexString(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

type snippetBoundedWriter struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
	cancel   context.CancelFunc
}

func (w *snippetBoundedWriter) Write(data []byte) (int, error) {
	n := len(data)
	remaining := w.limit - int64(w.buffer.Len())
	if int64(n) > remaining {
		if remaining > 0 {
			_, _ = w.buffer.Write(data[:remaining])
		}
		w.overflow = true
		w.cancel()
		return n, io.ErrShortWrite
	}
	return w.buffer.Write(data)
}

func runSnippetCommand(ctx context.Context, request SnippetCommand) (SnippetCommandResult, error) {
	if request.MaxOutputBytes <= 0 {
		return SnippetCommandResult{}, errors.New("invalid snippet output byte bound")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &snippetBoundedWriter{limit: request.MaxOutputBytes, cancel: cancel}
	stderr := &snippetBoundedWriter{limit: 64 << 10, cancel: cancel}
	cmd := exec.CommandContext(ctx, request.Bin, request.Args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(request.Stdin)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return SnippetCommandResult{}, err
	}
	err := cmd.Wait()
	return SnippetCommandResult{Stdout: stdout.buffer.Bytes(), Stderr: stderr.buffer.Bytes(), Started: true, Overflow: stdout.overflow || stderr.overflow}, err
}
