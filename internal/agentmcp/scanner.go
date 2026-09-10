package agentmcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const (
	DefaultMaxFileBytes  int64 = 1 << 20
	maximumMaxFileBytes        = 8 << 20
	defaultConcurrency         = 4
	maximumConcurrency         = 32
	defaultSymlinkDepth        = 8
	maximumSymlinkDepth        = 32
)

// Options supplies host paths and resource bounds. Use DefaultOptions for a
// real host scan. NewScanner intentionally does not fill omitted host paths, so
// callers and tests can select exactly which user/system sources are visible.
type Options struct {
	HomeDir                    string
	WorkingDirectory            string
	XDGConfigHome               string
	CodexHome                   string
	OpenCodeConfigPath          string
	GeminiCLIHome               string
	GeminiSystemDefaultsPath    string
	GeminiSystemSettingsPath    string
	OpenCodeManagedConfigPaths []string
	Concurrency                 int
	MaxFileBytes                int64
	MaxSymlinkDepth             int
}

// DefaultOptions resolves only documented static file locations. In
// particular, it ignores OPENCODE_CONFIG_CONTENT and never requests remote
// .well-known configuration.
func DefaultOptions() Options {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" && home != "" {
		codexHome = filepath.Join(home, ".codex")
	}
	geminiHome := os.Getenv("GEMINI_CLI_HOME")
	if geminiHome == "" {
		geminiHome = home
	}

	defaultsPath := os.Getenv("GEMINI_CLI_SYSTEM_DEFAULTS_PATH")
	settingsPath := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH")
	managedDir := ""
	switch runtime.GOOS {
	case "darwin":
		if defaultsPath == "" {
			defaultsPath = "/Library/Application Support/GeminiCli/system-defaults.json"
		}
		if settingsPath == "" {
			settingsPath = "/Library/Application Support/GeminiCli/settings.json"
		}
		managedDir = "/Library/Application Support/opencode"
	case "windows":
		programData := os.Getenv("ProgramData")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		if defaultsPath == "" {
			defaultsPath = filepath.Join(programData, "gemini-cli", "system-defaults.json")
		}
		if settingsPath == "" {
			settingsPath = filepath.Join(programData, "gemini-cli", "settings.json")
		}
		managedDir = filepath.Join(programData, "opencode")
	default:
		if defaultsPath == "" {
			defaultsPath = "/etc/gemini-cli/system-defaults.json"
		}
		if settingsPath == "" {
			settingsPath = "/etc/gemini-cli/settings.json"
		}
		managedDir = "/etc/opencode"
	}

	managed := []string{
		filepath.Join(managedDir, "opencode.json"),
		filepath.Join(managedDir, "opencode.jsonc"),
	}
	if runtime.GOOS == "darwin" {
		username := filepath.Base(home)
		if username == "" || username == "." || username == string(filepath.Separator) {
			username = "user"
		}
		managed = append(managed,
			filepath.Join("/Library/Managed Preferences", username, "ai.opencode.managed.plist"),
			"/Library/Managed Preferences/ai.opencode.managed.plist",
		)
	}

	return Options{
		HomeDir: home, WorkingDirectory: cwd, XDGConfigHome: xdg,
		CodexHome: codexHome, OpenCodeConfigPath: os.Getenv("OPENCODE_CONFIG"),
		GeminiCLIHome: geminiHome,
		GeminiSystemDefaultsPath: defaultsPath, GeminiSystemSettingsPath: settingsPath,
		OpenCodeManagedConfigPaths: managed,
		Concurrency: defaultConcurrency, MaxFileBytes: DefaultMaxFileBytes,
		MaxSymlinkDepth: defaultSymlinkDepth,
	}
}

type adapterKind uint8

const (
	adapterClaudeProject adapterKind = iota + 1
	adapterClaudeUser
	adapterCodex
	adapterCursor
	adapterGemini
	adapterOpenCode
	adapterOpenCodePlist
)

type sourceSpec struct {
	agent       Agent
	scope       Scope
	source      DeclarationSource
	path        string
	repository  string
	projectPath string
	projectRoot string
	adapter     adapterKind
}

type readResult struct {
	data    []byte
	present bool
	code    DiagnosticCode
}

// Scanner is a reusable, read-only static declaration scanner.
type Scanner struct {
	options    Options
	readSource func(context.Context, sourceSpec) readResult
}

// NewScanner creates a scanner for explicitly supplied host paths.
func NewScanner(options Options) *Scanner {
	options = normalizeOptions(options)
	s := &Scanner{options: options}
	s.readSource = s.readSourceFromDisk
	return s
}

// Scan uses documented host defaults.
func Scan(ctx context.Context, targets []Target) (Result, error) {
	return NewScanner(DefaultOptions()).Scan(ctx, targets)
}

// Scan returns all static declarations it can decode and fixed diagnostics for
// individual source failures. Cancellation is the only normal top-level scan
// failure and is returned as ctx.Err(), with any rows already collected.
func (s *Scanner) Scan(ctx context.Context, targets []Target) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	targets = normalizeTargets(targets, s.options.WorkingDirectory)
	var result Result

	for _, spec := range s.hostSources() {
		if err := ctx.Err(); err != nil {
			finalizeResult(&result)
			return result, err
		}
		rows, diagnostics := s.scanSource(ctx, spec, targets)
		result.Declarations = append(result.Declarations, rows...)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
	}
	if err := ctx.Err(); err != nil {
		finalizeResult(&result)
		return result, err
	}

	projectResults := make([]Result, len(targets))
	jobs := make(chan int)
	workers := s.options.Concurrency
	if workers > len(targets) {
		workers = len(targets)
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					for _, spec := range projectSources(targets[index]) {
						if ctx.Err() != nil {
							return
						}
						rows, diagnostics := s.scanSource(ctx, spec, nil)
						projectResults[index].Declarations = append(projectResults[index].Declarations, rows...)
						projectResults[index].Diagnostics = append(projectResults[index].Diagnostics, diagnostics...)
					}
				}
		}()
	}
	for index := range targets {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			for _, partial := range projectResults {
				result.Declarations = append(result.Declarations, partial.Declarations...)
				result.Diagnostics = append(result.Diagnostics, partial.Diagnostics...)
			}
			finalizeResult(&result)
			return result, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	for _, partial := range projectResults {
		result.Declarations = append(result.Declarations, partial.Declarations...)
		result.Diagnostics = append(result.Diagnostics, partial.Diagnostics...)
	}
	finalizeResult(&result)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func normalizeOptions(options Options) Options {
	if options.WorkingDirectory == "" {
		options.WorkingDirectory, _ = os.Getwd()
	}
	if options.XDGConfigHome == "" && options.HomeDir != "" {
		options.XDGConfigHome = filepath.Join(options.HomeDir, ".config")
	}
	if options.CodexHome == "" && options.HomeDir != "" {
		options.CodexHome = filepath.Join(options.HomeDir, ".codex")
	}
	if options.GeminiCLIHome == "" {
		options.GeminiCLIHome = options.HomeDir
	}
	if options.Concurrency <= 0 {
		options.Concurrency = defaultConcurrency
	} else if options.Concurrency > maximumConcurrency {
		options.Concurrency = maximumConcurrency
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = DefaultMaxFileBytes
	} else if options.MaxFileBytes > maximumMaxFileBytes {
		options.MaxFileBytes = maximumMaxFileBytes
	}
	if options.MaxSymlinkDepth <= 0 {
		options.MaxSymlinkDepth = defaultSymlinkDepth
	} else if options.MaxSymlinkDepth > maximumSymlinkDepth {
		options.MaxSymlinkDepth = maximumSymlinkDepth
	}
	return options
}

func normalizeTargets(targets []Target, cwd string) []Target {
	byPath := map[string]Target{}
	for _, target := range targets {
		if strings.TrimSpace(target.Path) == "" {
			continue
		}
		path := target.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		target.Path = filepath.Clean(path)
		target.Repository = strings.TrimSpace(target.Repository)
		if current, ok := byPath[target.Path]; !ok || strings.ToLower(target.Repository) < strings.ToLower(current.Repository) {
			byPath[target.Path] = target
		}
	}
	out := make([]Target, 0, len(byPath))
	for _, target := range byPath {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return strings.ToLower(out[i].Repository) < strings.ToLower(out[j].Repository)
	})
	return out
}

func (s *Scanner) hostSources() []sourceSpec {
	var sources []sourceSpec
	add := func(spec sourceSpec) {
		if strings.TrimSpace(spec.path) == "" {
			return
		}
		if !filepath.IsAbs(spec.path) {
			spec.path = filepath.Join(s.options.WorkingDirectory, spec.path)
		}
		spec.path = filepath.Clean(spec.path)
		sources = append(sources, spec)
	}
	if s.options.HomeDir != "" {
		add(sourceSpec{agent: AgentClaudeCode, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.HomeDir, ".claude.json"), adapter: adapterClaudeUser})
		add(sourceSpec{agent: AgentCursor, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.HomeDir, ".cursor", "mcp.json"), adapter: adapterCursor})
	}
	if s.options.CodexHome != "" {
		add(sourceSpec{agent: AgentCodex, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.CodexHome, "config.toml"), adapter: adapterCodex})
	}
	if s.options.GeminiCLIHome != "" {
		add(sourceSpec{agent: AgentGeminiCLI, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.GeminiCLIHome, ".gemini", "settings.json"), adapter: adapterGemini})
	}
	if s.options.XDGConfigHome != "" {
		add(sourceSpec{agent: AgentOpenCode, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.XDGConfigHome, "opencode", "opencode.json"), adapter: adapterOpenCode})
		add(sourceSpec{agent: AgentOpenCode, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.XDGConfigHome, "opencode", "opencode.jsonc"), adapter: adapterOpenCode})
	}
	add(sourceSpec{agent: AgentOpenCode, scope: ScopeCustom, source: SourceDirect, path: s.options.OpenCodeConfigPath, adapter: adapterOpenCode})
	add(sourceSpec{agent: AgentGeminiCLI, scope: ScopeSystemDefaults, source: SourceManaged, path: s.options.GeminiSystemDefaultsPath, adapter: adapterGemini})
	add(sourceSpec{agent: AgentGeminiCLI, scope: ScopeSystemOverride, source: SourceManaged, path: s.options.GeminiSystemSettingsPath, adapter: adapterGemini})
	for _, path := range s.options.OpenCodeManagedConfigPaths {
		adapter := adapterOpenCode
		if strings.EqualFold(filepath.Ext(path), ".plist") {
			adapter = adapterOpenCodePlist
		}
		add(sourceSpec{agent: AgentOpenCode, scope: ScopeManaged, source: SourceManaged, path: path, adapter: adapter})
	}
	return sources
}

func projectSources(target Target) []sourceSpec {
	base := sourceSpec{scope: ScopeProject, source: SourceDirect, repository: target.Repository, projectPath: target.Path, projectRoot: target.Path}
	makeSpec := func(agent Agent, relative string, adapter adapterKind) sourceSpec {
		spec := base
		spec.agent = agent
		spec.path = filepath.Join(target.Path, filepath.FromSlash(relative))
		spec.adapter = adapter
		return spec
	}
	return []sourceSpec{
		makeSpec(AgentClaudeCode, ".mcp.json", adapterClaudeProject),
		makeSpec(AgentCodex, ".codex/config.toml", adapterCodex),
		makeSpec(AgentCursor, ".cursor/mcp.json", adapterCursor),
		makeSpec(AgentGeminiCLI, ".gemini/settings.json", adapterGemini),
		makeSpec(AgentOpenCode, "opencode.json", adapterOpenCode),
		makeSpec(AgentOpenCode, "opencode.jsonc", adapterOpenCode),
	}
}

func (s *Scanner) scanSource(ctx context.Context, spec sourceSpec, targets []Target) ([]Declaration, []Diagnostic) {
	read := s.readSource(ctx, spec)
	if !read.present {
		if read.code == "" {
			return nil, nil
		}
		return nil, []Diagnostic{diagnosticFor(spec, read.code)}
	}
	var rows []Declaration
	var codes []DiagnosticCode
	switch spec.adapter {
	case adapterClaudeProject:
		rows, codes = parseClaudeProject(read.data, spec)
	case adapterClaudeUser:
		rows, codes = parseClaudeUser(read.data, spec, targets)
	case adapterCodex:
		rows, codes = parseCodex(read.data, spec)
	case adapterCursor:
		rows, codes = parseCursor(read.data, spec)
	case adapterGemini:
		rows, codes = parseGemini(read.data, spec)
	case adapterOpenCode:
		rows, codes = parseOpenCode(read.data, spec)
	case adapterOpenCodePlist:
		rows, codes = parseOpenCodePlist(read.data, spec)
	default:
		codes = []DiagnosticCode{DiagnosticMalformed}
	}
	diagnostics := make([]Diagnostic, 0, len(codes))
	for _, code := range codes {
		diagnostics = append(diagnostics, diagnosticFor(spec, code))
	}
	return rows, diagnostics
}

func diagnosticFor(spec sourceSpec, code DiagnosticCode) Diagnostic {
	return Diagnostic{
		Agent: spec.agent, Scope: spec.scope, Repository: spec.repository,
		ProjectPath: spec.projectPath, ConfigPath: spec.path,
		Code: code, Message: diagnosticMessage(code),
	}
}

func finalizeResult(result *Result) {
	SortDeclarations(result.Declarations)
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		a, b := result.Diagnostics[i], result.Diagnostics[j]
		ak := []string{string(a.Agent), scopeSortKey(a.Scope), strings.ToLower(a.Repository), a.ProjectPath, a.ConfigPath, string(a.Code)}
		bk := []string{string(b.Agent), scopeSortKey(b.Scope), strings.ToLower(b.Repository), b.ProjectPath, b.ConfigPath, string(b.Code)}
		for n := range ak {
			if ak[n] != bk[n] {
				return ak[n] < bk[n]
			}
		}
		return false
	})
}
