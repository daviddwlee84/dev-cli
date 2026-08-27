// Package ignore assembles .gitignore files from GitHub's published templates
// plus the entries that every repository on a given machine wants but no
// language template includes.
//
// The split matters: a Python template says nothing about .DS_Store, and
// nothing at all about the worktree directories an agent harness creates —
// which, left untracked, show up as noise in every status.
package ignore

import (
	"context"
	"embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed all:templates
var bundled embed.FS

// TemplateAPI is GitHub's public gitignore endpoint. It needs no
// authentication, which is what lets this work before `gh auth login`.
const TemplateAPI = "https://api.github.com/gitignore/templates"

// Source records where a section's content came from, so the generated file
// can say so honestly.
type Source string

const (
	// SourceRemote is GitHub's template API.
	SourceRemote Source = "github"
	// SourceCache is a previously fetched template on disk.
	SourceCache Source = "cache"
	// SourceBundled is a template compiled into the binary.
	SourceBundled Source = "bundled"
)

// Section is one named block of the generated file.
type Section struct {
	Name   string
	Body   string
	Source Source
}

// Fetcher resolves template names to content, preferring the network and
// falling back through the cache to the bundled copies.
type Fetcher struct {
	CacheDir string
	Client   *http.Client
	// Offline skips the network entirely.
	Offline bool
	// BaseURL overrides the template endpoint; empty means TemplateAPI.
	BaseURL string
}

// NewFetcher returns a fetcher caching under dir.
func NewFetcher(cacheDir string) *Fetcher {
	return &Fetcher{
		CacheDir: cacheDir,
		Client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Get resolves one template by name, case-insensitively ("python", "Python",
// "PYTHON" all work, because nobody remembers GitHub's capitalisation).
func (f *Fetcher) Get(ctx context.Context, name string) (Section, error) {
	canonical := Canonical(name)

	if !f.Offline {
		body, err := f.fetch(ctx, canonical)
		if err == nil {
			f.writeCache(canonical, body)
			return Section{Name: canonical, Body: body, Source: SourceRemote}, nil
		}
	}
	if body, ok := f.readCache(canonical); ok {
		return Section{Name: canonical, Body: body, Source: SourceCache}, nil
	}
	if body, ok := bundledTemplate(canonical); ok {
		return Section{Name: canonical, Body: body, Source: SourceBundled}, nil
	}
	return Section{}, fmt.Errorf("no gitignore template for %q (offline, and no cached or bundled copy);"+
		" available offline: %s", name, strings.Join(BundledNames(), ", "))
}

// Canonical normalises a template name to GitHub's own capitalisation where
// it is known, and otherwise title-cases the input.
func Canonical(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if c, ok := knownNames[lower]; ok {
		return c
	}
	if lower == "" {
		return ""
	}
	return strings.ToUpper(lower[:1]) + lower[1:]
}

// knownNames maps the spellings people type to GitHub's file names. Only the
// cases where they differ need an entry.
var knownNames = map[string]string{
	"python": "Python", "node": "Node", "nodejs": "Node", "js": "Node",
	"javascript": "Node", "typescript": "Node", "ts": "Node",
	"go": "Go", "golang": "Go", "rust": "Rust", "java": "Java",
	"ruby": "Ruby", "elixir": "Elixir", "c": "C", "c++": "C++", "cpp": "C++",
	"swift": "Swift", "kotlin": "Kotlin", "scala": "Scala", "haskell": "Haskell",
	"r": "R", "julia": "Julia", "dart": "Dart", "flutter": "Dart",
	"terraform": "Terraform", "unity": "Unity", "godot": "Godot",
	"tex": "TeX", "latex": "TeX", "zig": "Zig", "lua": "Lua", "perl": "Perl",
	"php": "Composer", "laravel": "Laravel", "rails": "Rails",
	"android": "Android", "objective-c": "Objective-C", "objc": "Objective-C",
	"csharp": "VisualStudio", "c#": "VisualStudio", "dotnet": "VisualStudio",
}

func (f *Fetcher) fetch(ctx context.Context, name string) (string, error) {
	base := f.BaseURL
	if base == "" {
		base = TemplateAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+name, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.raw")
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned %s for %s", resp.Status, name)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// The raw media type returns the file itself; if a proxy ignored the
	// Accept header we would get JSON, which is not a usable gitignore.
	if strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		return "", fmt.Errorf("unexpected JSON response for %s", name)
	}
	return strings.TrimRight(string(body), "\n"), nil
}

func (f *Fetcher) cachePath(name string) string {
	if f.CacheDir == "" {
		return ""
	}
	return filepath.Join(f.CacheDir, name+".gitignore")
}

func (f *Fetcher) readCache(name string) (string, bool) {
	p := f.cachePath(name)
	if p == "" {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(b), "\n"), true
}

func (f *Fetcher) writeCache(name, body string) {
	p := f.cachePath(name)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return // caching is an optimisation; failing to cache is not an error
	}
	_ = os.WriteFile(p, []byte(body+"\n"), 0o644)
}

func bundledTemplate(name string) (string, bool) {
	b, err := bundled.ReadFile("templates/" + name + ".gitignore")
	if err != nil {
		return "", false
	}
	return strings.TrimRight(string(b), "\n"), true
}

// BundledNames lists the templates available with no network at all.
func BundledNames() []string {
	entries, err := bundled.ReadDir("templates")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".gitignore"))
	}
	sort.Strings(out)
	return out
}
