package agentmcp

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
)

const (
	DefaultMaxFileBytes int64 = 1 << 20
	maximumMaxFileBytes       = 8 << 20
	defaultConcurrency        = 4
	maximumConcurrency        = 32
	defaultSymlinkDepth       = 8
	maximumSymlinkDepth       = 32
)

// Options supplies host paths and resource bounds. Use DefaultOptions for a
// real host scan. NewScanner intentionally does not fill omitted host paths, so
// callers and tests can select exactly which user/system sources are visible.
type Options struct {
	HomeDir                    string
	WorkingDirectory           string
	XDGConfigHome              string
	CodexHome                  string
	OpenCodeConfigPath         string
	OpenCodeConfigDir          string
	GeminiCLIHome              string
	ClaudeUserConfigPath       string
	ClaudeUserSettingsPath     string
	ClaudeManagedConfigPath    string
	ClaudeManagedSettingsPaths []string
	GeminiSystemDefaultsPath   string
	GeminiSystemSettingsPath   string
	OpenCodeManagedConfigPaths []string
	Concurrency                int
	MaxFileBytes               int64
	MaxSymlinkDepth            int
}

// DefaultOptions resolves only documented static file locations. In
// particular, it ignores OPENCODE_CONFIG_CONTENT and never requests remote
// .well-known configuration.
func DefaultOptions() Options {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil || !filepath.IsAbs(home) {
		home = ""
	}
	cwd, _ := os.Getwd()
	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if !filepath.IsAbs(xdg) && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" && home != "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claudeConfigDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	claudeUserConfig := ""
	if filepath.IsAbs(claudeConfigDir) {
		claudeUserConfig = filepath.Join(claudeConfigDir, ".claude.json")
	} else if home != "" {
		claudeConfigDir = filepath.Join(home, ".claude")
		claudeUserConfig = filepath.Join(home, ".claude.json")
	} else {
		claudeConfigDir = ""
	}
	claudeUserSettings := ""
	if claudeConfigDir != "" {
		claudeUserSettings = filepath.Join(claudeConfigDir, "settings.json")
	}
	// Gemini CLI documents ~/.gemini for user settings; it does not define a
	// relocatable Gemini home for this file.
	geminiHome := home

	defaultsPath := os.Getenv("GEMINI_CLI_SYSTEM_DEFAULTS_PATH")
	settingsPath := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH")
	managedDir := ""
	claudeManaged := ""
	claudeManagedSettings := ""
	switch runtime.GOOS {
	case "darwin":
		if defaultsPath == "" {
			defaultsPath = "/Library/Application Support/GeminiCli/system-defaults.json"
		}
		if settingsPath == "" {
			settingsPath = "/Library/Application Support/GeminiCli/settings.json"
		}
		managedDir = "/Library/Application Support/opencode"
		claudeManaged = "/Library/Application Support/ClaudeCode/managed-mcp.json"
		claudeManagedSettings = "/Library/Application Support/ClaudeCode/managed-settings.json"
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
		programFiles := os.Getenv("ProgramFiles")
		if programFiles == "" {
			programFiles = `C:\Program Files`
		}
		claudeManaged = filepath.Join(programFiles, "ClaudeCode", "managed-mcp.json")
		claudeManagedSettings = filepath.Join(programFiles, "ClaudeCode", "managed-settings.json")
	default:
		if defaultsPath == "" {
			defaultsPath = "/etc/gemini-cli/system-defaults.json"
		}
		if settingsPath == "" {
			settingsPath = "/etc/gemini-cli/settings.json"
		}
		managedDir = "/etc/opencode"
		claudeManaged = "/etc/claude-code/managed-mcp.json"
		claudeManagedSettings = "/etc/claude-code/managed-settings.json"
	}

	managed := []string{
		filepath.Join(managedDir, "opencode.json"),
		filepath.Join(managedDir, "opencode.jsonc"),
	}
	if runtime.GOOS == "darwin" {
		if username := managedPreferenceUsername(home); username != "" {
			managed = append(managed,
				filepath.Join("/Library/Managed Preferences", username, "ai.opencode.managed.plist"),
			)
		}
		managed = append(managed, "/Library/Managed Preferences/ai.opencode.managed.plist")
	}

	return Options{
		HomeDir: home, WorkingDirectory: cwd, XDGConfigHome: xdg,
		CodexHome: codexHome, OpenCodeConfigPath: os.Getenv("OPENCODE_CONFIG"),
		OpenCodeConfigDir: os.Getenv("OPENCODE_CONFIG_DIR"),
		GeminiCLIHome:     geminiHome, ClaudeUserConfigPath: claudeUserConfig,
		ClaudeUserSettingsPath: claudeUserSettings, ClaudeManagedConfigPath: claudeManaged,
		ClaudeManagedSettingsPaths: []string{claudeManagedSettings},
		GeminiSystemDefaultsPath:   defaultsPath, GeminiSystemSettingsPath: settingsPath,
		OpenCodeManagedConfigPaths: managed,
		Concurrency:                defaultConcurrency, MaxFileBytes: DefaultMaxFileBytes,
		MaxSymlinkDepth: defaultSymlinkDepth,
	}
}

func managedPreferenceUsername(home string) string {
	if current, err := user.Current(); err == nil {
		username := strings.TrimSpace(current.Username)
		if username != "" && filepath.Base(username) == username && !stringContainsControl(username) {
			return username
		}
	}
	username := filepath.Base(home)
	if username == "" || username == "." || username == string(filepath.Separator) || stringContainsControl(username) {
		return ""
	}
	return username
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
	agent            Agent
	scope            Scope
	source           DeclarationSource
	path             string
	repository       string
	repositoryPath   string
	checkout         string
	localProjectPath string
	projectRoot      string
	approvalSources  []sourceSpec
	adapter          adapterKind
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
	result := Result{
		Declarations: make([]Declaration, 0),
		Diagnostics:  make([]Diagnostic, 0),
		Coverage:     defaultCoverage(),
	}
	type cachedRead struct {
		ready  chan struct{}
		result readResult
	}
	var readMu sync.Mutex
	reads := map[string]*cachedRead{}
	readSource := func(ctx context.Context, spec sourceSpec) readResult {
		key := spec.path + "\x00" + spec.projectRoot
		readMu.Lock()
		entry := reads[key]
		if entry == nil {
			entry = &cachedRead{ready: make(chan struct{})}
			reads[key] = entry
			readMu.Unlock()
			entry.result = s.readSource(ctx, spec)
			close(entry.ready)
			return entry.result
		}
		readMu.Unlock()
		select {
		case <-ctx.Done():
			return readResult{}
		case <-entry.ready:
			return entry.result
		}
	}

	for _, spec := range s.hostSources() {
		if err := ctx.Err(); err != nil {
			finalizeResult(&result)
			return result, err
		}
		rows, diagnostics := s.scanSource(ctx, spec, targets, readSource)
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
					for _, spec := range s.projectSources(targets[index]) {
						if ctx.Err() != nil {
							return
						}
						rows, diagnostics := s.scanSource(ctx, spec, nil, readSource)
						projectResults[index].Declarations = append(projectResults[index].Declarations, rows...)
						projectResults[index].Diagnostics = append(projectResults[index].Diagnostics, diagnostics...)
					}
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
	if options.XDGConfigHome != "" && !filepath.IsAbs(options.XDGConfigHome) {
		options.XDGConfigHome = ""
	}
	if options.CodexHome != "" && !filepath.IsAbs(options.CodexHome) {
		options.CodexHome = ""
	}
	if options.GeminiCLIHome != "" && !filepath.IsAbs(options.GeminiCLIHome) {
		options.GeminiCLIHome = ""
	}
	for _, path := range []*string{&options.ClaudeUserConfigPath, &options.ClaudeUserSettingsPath, &options.ClaudeManagedConfigPath} {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = ""
		}
	}
	managedSettings := make([]string, 0, len(options.ClaudeManagedSettingsPaths))
	for _, path := range options.ClaudeManagedSettingsPaths {
		if filepath.IsAbs(path) {
			managedSettings = append(managedSettings, filepath.Clean(path))
		}
	}
	options.ClaudeManagedSettingsPaths = managedSettings
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
	out := make([]Target, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.CheckoutRoot) == "" {
			continue
		}
		checkout := target.CheckoutRoot
		if !filepath.IsAbs(checkout) {
			checkout = filepath.Join(cwd, checkout)
		}
		absolute, err := filepath.Abs(checkout)
		if err != nil {
			continue
		}
		target.CheckoutRoot = filepath.Clean(absolute)
		if target.RepoPath == "" {
			target.RepoPath = target.CheckoutRoot
		} else if !filepath.IsAbs(target.RepoPath) {
			target.RepoPath = filepath.Clean(filepath.Join(cwd, target.RepoPath))
		}
		target.RepoDisplay = strings.TrimSpace(target.RepoDisplay)
		if target.RepoDisplay == "" {
			target.RepoDisplay = target.RepoName
		}
		if target.CommonDir == "" {
			target.CommonDir = target.RepoPath
		}
		out = append(out, target)
	}
	return agenttarget.Dedupe(out)
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
	claudeUserConfig := s.options.ClaudeUserConfigPath
	if claudeUserConfig == "" && s.options.HomeDir != "" {
		claudeUserConfig = filepath.Join(s.options.HomeDir, ".claude.json")
	}
	add(sourceSpec{agent: AgentClaudeCode, scope: ScopeUser, source: SourceDirect, path: claudeUserConfig, adapter: adapterClaudeUser})
	if s.options.HomeDir != "" {
		add(sourceSpec{agent: AgentCursor, scope: ScopeUser, source: SourceDirect, path: filepath.Join(s.options.HomeDir, ".cursor", "mcp.json"), adapter: adapterCursor})
	}
	add(sourceSpec{
		agent: AgentClaudeCode, scope: ScopeManaged, source: SourceManaged,
		path: s.options.ClaudeManagedConfigPath, adapter: adapterClaudeProject,
	})
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
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if s.options.OpenCodeConfigDir != "" {
			add(sourceSpec{
				agent: AgentOpenCode, scope: ScopeCustom, source: SourceDirect,
				path: filepath.Join(s.options.OpenCodeConfigDir, name), adapter: adapterOpenCode,
			})
		}
	}
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

func (s *Scanner) projectSources(target Target) []sourceSpec {
	base := sourceSpec{
		scope: ScopeProject, source: SourceDirect, repository: target.RepoDisplay,
		repositoryPath: target.RepoPath, checkout: target.CheckoutRoot, projectRoot: target.CheckoutRoot,
	}
	makeSpec := func(agent Agent, relative string, adapter adapterKind) sourceSpec {
		spec := base
		spec.agent = agent
		spec.path = filepath.Join(target.CheckoutRoot, filepath.FromSlash(relative))
		spec.adapter = adapter
		return spec
	}
	approvalSpec := func(path string, scope Scope, source DeclarationSource, confined bool) sourceSpec {
		spec := base
		spec.agent = AgentClaudeCode
		spec.scope = scope
		spec.source = source
		spec.path = path
		if !confined {
			spec.projectRoot = ""
		}
		return spec
	}
	claude := makeSpec(AgentClaudeCode, ".mcp.json", adapterClaudeProject)
	addApproval := func(path string, scope Scope, source DeclarationSource, confined bool) {
		if strings.TrimSpace(path) != "" {
			claude.approvalSources = append(claude.approvalSources, approvalSpec(path, scope, source, confined))
		}
	}
	addApproval(s.options.ClaudeUserSettingsPath, ScopeUser, SourceDirect, false)
	addApproval(filepath.Join(target.CheckoutRoot, ".claude", "settings.json"), ScopeProject, SourceDirect, true)
	addApproval(filepath.Join(target.CheckoutRoot, ".claude", "settings.local.json"), ScopeLocal, SourceDirect, true)
	for _, path := range s.options.ClaudeManagedSettingsPaths {
		addApproval(path, ScopeManaged, SourceManaged, false)
	}
	return []sourceSpec{
		claude,
		makeSpec(AgentCodex, ".codex/config.toml", adapterCodex),
		makeSpec(AgentCursor, ".cursor/mcp.json", adapterCursor),
		makeSpec(AgentGeminiCLI, ".gemini/settings.json", adapterGemini),
		makeSpec(AgentOpenCode, "opencode.json", adapterOpenCode),
		makeSpec(AgentOpenCode, "opencode.jsonc", adapterOpenCode),
		makeSpec(AgentOpenCode, ".opencode/opencode.json", adapterOpenCode),
		makeSpec(AgentOpenCode, ".opencode/opencode.jsonc", adapterOpenCode),
	}
}

func (s *Scanner) scanSource(ctx context.Context, spec sourceSpec, targets []Target, readSource func(context.Context, sourceSpec) readResult) ([]Declaration, []Diagnostic) {
	read := readSource(ctx, spec)
	if !read.present {
		if read.code == "" {
			return nil, nil
		}
		diagnostics := []Diagnostic{diagnosticFor(spec, read.code)}
		if spec.adapter == adapterClaudeUser && len(targets) > 0 {
			localSpec := spec
			localSpec.scope = ScopeLocal
			diagnostics = append(diagnostics, diagnosticFor(localSpec, read.code))
		}
		return nil, diagnostics
	}
	var rows []Declaration
	var codes []DiagnosticCode
	var diagnostics []Diagnostic
	switch spec.adapter {
	case adapterClaudeProject:
		rows, codes = parseClaudeProject(read.data, spec)
		approvalState := claudeProjectApprovalState{enabled: map[string]bool{}, disabled: map[string]bool{}}
		for _, approvalSpec := range spec.approvalSources {
			approval := readSource(ctx, approvalSpec)
			switch {
			case approval.code != "":
				diagnostics = append(diagnostics, diagnosticFor(approvalSpec, approval.code))
			case approval.present:
				enabled, disabled, enableAll, code := parseClaudeProjectApproval(approval.data)
				if code != "" {
					diagnostics = append(diagnostics, diagnosticFor(approvalSpec, code))
				} else {
					approvalState.merge(enabled, disabled, enableAll)
				}
			}
		}
		applyClaudeProjectApproval(rows, approvalState.enabled, approvalState.disabled, approvalState.enableAll)
	case adapterClaudeUser:
		var localDiagnostics []Diagnostic
		rows, codes, localDiagnostics = parseClaudeUser(read.data, spec, targets)
		diagnostics = append(diagnostics, localDiagnostics...)
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
	if diagnostics == nil {
		diagnostics = make([]Diagnostic, 0, len(codes))
	}
	for _, code := range codes {
		diagnostics = append(diagnostics, diagnosticFor(spec, code))
	}
	return rows, diagnostics
}

func diagnosticFor(spec sourceSpec, code DiagnosticCode) Diagnostic {
	return Diagnostic{
		Agent: spec.agent, Scope: spec.scope, Repository: spec.repository,
		RepositoryPath: spec.repositoryPath, Checkout: spec.checkout,
		LocalProjectPath: spec.localProjectPath, ConfigPath: spec.path,
		Code: code, Message: diagnosticMessage(code),
	}
}

func finalizeResult(result *Result) {
	SortDeclarations(result.Declarations)
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		a, b := result.Diagnostics[i], result.Diagnostics[j]
		ak := []string{string(a.Agent), scopeSortKey(a.Scope), strings.ToLower(a.Repository), a.RepositoryPath, a.Checkout, a.LocalProjectPath, a.ConfigPath, string(a.Code)}
		bk := []string{string(b.Agent), scopeSortKey(b.Scope), strings.ToLower(b.Repository), b.RepositoryPath, b.Checkout, b.LocalProjectPath, b.ConfigPath, string(b.Code)}
		for n := range ak {
			if ak[n] != bk[n] {
				return ak[n] < bk[n]
			}
		}
		return false
	})
}
