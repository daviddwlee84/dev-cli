package projectconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const projectConfigVersion = 1

type projectFile struct {
	Version  *int             `toml:"version"`
	Worktree WorktreeOverride `toml:"worktree"`
	Repo     RepoOverride     `toml:"repo"`
}

var deniedTopLevelSections = map[string]bool{
	"paths": true, "runtime": true, "stats": true, "tui": true,
	"bootstrap": true, "forge": true, "update": true,
}

var allowedScaffoldTopLevel = map[string]bool{
	"version": true, "default_preset": true, "default_agents": true, "presets": true,
}

// Load reads the two fixed project files without changing the repository.
// legacy may be nil. When supplied, its values have lower precedence than
// .dev-cli/config.toml and its source is retained per effective field.
func Load(repoRoot string, legacy *Layer) (Result, error) {
	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		return Result{}, err
	}
	result := Result{Paths: paths, Sources: map[string]string{}}

	if legacy != nil {
		source := strings.TrimSpace(legacy.Source)
		if source == "" {
			source = LegacySource
		}
		if err := legacy.Override.validate(); err != nil {
			return Result{}, fmt.Errorf("validate legacy project config %s: %w", source, err)
		}
		result.Effective = overlay(result.Effective, legacy.Override, source, result.Sources)
		result.Layers = append(result.Layers, source)
	}

	configData, present, err := readOptional(paths.Config)
	if err != nil {
		return Result{}, err
	}
	result.ConfigPresent = present
	if present {
		project, metadata, err := decodeProjectConfig(configData)
		if err != nil {
			return Result{}, fmt.Errorf("parse %s: %w", paths.Config, err)
		}
		if project.Version != nil && *project.Version != projectConfigVersion {
			return Result{}, fmt.Errorf("parse %s: version %d is unsupported (want %d)", paths.Config, *project.Version, projectConfigVersion)
		}
		result.Project = Override{Worktree: project.Worktree, Repo: project.Repo}
		if err := result.Project.validate(); err != nil {
			return Result{}, fmt.Errorf("validate %s: %w", paths.Config, err)
		}
		result.Diagnostics = append(result.Diagnostics, diagnosticsForUndecoded(paths.Config, metadata.Undecoded())...)
		result.Effective = overlay(result.Effective, result.Project, paths.Config, result.Sources)
		result.Layers = append(result.Layers, paths.Config)
	}

	scaffoldData, present, err := readOptional(paths.Scaffolds)
	if err != nil {
		return Result{}, err
	}
	result.ScaffoldsPresent = present
	var scaffoldDocument map[string]any
	if present {
		if _, err := toml.Decode(string(scaffoldData), &scaffoldDocument); err != nil {
			return Result{}, fmt.Errorf("parse %s: %w", paths.Scaffolds, err)
		}
		if err := validateScaffoldVersion(scaffoldDocument); err != nil {
			return Result{}, fmt.Errorf("parse %s: %w", paths.Scaffolds, err)
		}
		result.Diagnostics = append(result.Diagnostics, scaffoldDiagnostics(paths.Scaffolds, scaffoldDocument)...)
	}

	result.ExecutionHash, err = executionHash(paths.Root, result.Effective, scaffoldDocument)
	if err != nil {
		return Result{}, fmt.Errorf("hash executable project config: %w", err)
	}
	sortDiagnostics(result.Diagnostics)
	return result, nil
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

func decodeProjectConfig(data []byte) (projectFile, toml.MetaData, error) {
	var project projectFile
	metadata, err := toml.Decode(string(data), &project)
	return project, metadata, err
}

func diagnosticsForUndecoded(source string, keys []toml.Key) []Diagnostic {
	diagnostics := make([]Diagnostic, 0, len(keys))
	seenDenied := map[string]bool{}
	seenUnknown := map[string]bool{}
	for _, undecoded := range keys {
		key := undecoded.String()
		top, _, _ := strings.Cut(key, ".")
		if deniedTopLevelSections[top] {
			if seenDenied[top] {
				continue
			}
			seenDenied[top] = true
			diagnostics = append(diagnostics, Diagnostic{
				Kind: DiagnosticDenied, Source: source, Key: top,
				Message: fmt.Sprintf("project config cannot override host section %s", top),
			})
			continue
		}
		if seenUnknown[key] {
			continue
		}
		seenUnknown[key] = true
		diagnostics = append(diagnostics, Diagnostic{
			Kind: DiagnosticUnknown, Source: source, Key: key,
			Message: fmt.Sprintf("unknown project config key %s was ignored", key),
		})
	}
	return diagnostics
}

func scaffoldDiagnostics(source string, document map[string]any) []Diagnostic {
	var diagnostics []Diagnostic
	for key := range document {
		if allowedScaffoldTopLevel[key] {
			continue
		}
		kind := DiagnosticUnknown
		message := fmt.Sprintf("unknown scaffold config section %s was ignored", key)
		if deniedTopLevelSections[key] {
			kind = DiagnosticDenied
			message = fmt.Sprintf("scaffold config cannot override host section %s", key)
		}
		diagnostics = append(diagnostics, Diagnostic{Kind: kind, Source: source, Key: key, Message: message})
	}
	return diagnostics
}

func validateScaffoldVersion(document map[string]any) error {
	value, ok := document["version"]
	if !ok {
		return nil
	}
	version, ok := value.(int64)
	if !ok {
		return fmt.Errorf("version must be an integer")
	}
	if version != projectConfigVersion {
		return fmt.Errorf("version %d is unsupported (want %d)", version, projectConfigVersion)
	}
	return nil
}

func overlay(base, next Override, source string, sources map[string]string) Override {
	if next.Worktree.Include != nil {
		value := append([]string(nil), (*next.Worktree.Include)...)
		base.Worktree.Include = &value
		sources["worktree.include"] = source
	}
	if next.Worktree.Link != nil {
		value := append([]string(nil), (*next.Worktree.Link)...)
		base.Worktree.Link = &value
		sources["worktree.link"] = source
	}
	if next.Worktree.PostCreate != nil {
		value := *next.Worktree.PostCreate
		value.Commands = append([]string(nil), value.Commands...)
		base.Worktree.PostCreate = &value
		sources["worktree.post_create"] = source
	}
	if next.Worktree.Strategy != nil {
		value := *next.Worktree.Strategy
		base.Worktree.Strategy = &value
		sources["worktree.strategy"] = source
	}
	if next.Worktree.Strategies != nil {
		merged := map[string]string{}
		if base.Worktree.Strategies != nil {
			for key, value := range *base.Worktree.Strategies {
				merged[key] = value
			}
		}
		for key, value := range *next.Worktree.Strategies {
			merged[key] = value
			sources["worktree.strategies."+key] = source
		}
		base.Worktree.Strategies = &merged
	}
	if next.Worktree.ProvisionTimeout != nil {
		value := *next.Worktree.ProvisionTimeout
		base.Worktree.ProvisionTimeout = &value
		sources["worktree.provision_timeout"] = source
	}
	if next.Repo.Setup.Preset != nil {
		value := *next.Repo.Setup.Preset
		base.Repo.Setup.Preset = &value
		sources["repo.setup.preset"] = source
	}
	if next.Repo.Setup.Handoff != nil {
		value := *next.Repo.Setup.Handoff
		base.Repo.Setup.Handoff = &value
		sources["repo.setup.handoff"] = source
	}
	if next.Repo.Setup.Commit != nil {
		value := *next.Repo.Setup.Commit
		base.Repo.Setup.Commit = &value
		sources["repo.setup.commit"] = source
	}
	return base
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Source != diagnostics[j].Source {
			return diagnostics[i].Source < diagnostics[j].Source
		}
		if diagnostics[i].Key != diagnostics[j].Key {
			return diagnostics[i].Key < diagnostics[j].Key
		}
		return diagnostics[i].Kind < diagnostics[j].Kind
	})
}

// executionHash fingerprints only project-owned settings that can cause an
// external command to run. The scaffold schema lives in internal/scaffold;
// this package intentionally extracts only hooks and skill setup generically.
func executionHash(repoRoot string, effective Override, scaffoldDocument map[string]any) (string, error) {
	payload := map[string]any{}
	if effective.Worktree.PostCreate != nil {
		payload["worktree.post_create"] = effective.Worktree.PostCreate
	}
	// A project-selected preset can switch which hooks or skill setup runs even
	// when the executable definitions themselves live in a global layer.
	if effective.Repo.Setup.Preset != nil {
		payload["repo.setup.preset"] = *effective.Repo.Setup.Preset
	}
	presets, hasPresets := scaffoldDocument["presets"]
	selectors, hasSelectors := extractScaffoldSelectors(scaffoldDocument)
	if hasSelectors {
		payload["scaffolds.selectors"] = selectors
	}
	hasExecutableScaffold := false
	if hasPresets {
		if extracted, ok := extractExecutableScaffold(presets); ok {
			hasExecutableScaffold = true
			payload["scaffolds.presets"] = extracted
		}
	}
	if files, ok := extractNamedScaffoldValues(presets, map[string]bool{"files": true}); ok {
		// A project can override only a generated file while an inherited global
		// hook executes it. Bind every project-owned file declaration even when the
		// executable selector/definition lives in another layer.
		payload["scaffolds.files"] = files
		templates, err := hashProjectTemplateSources(repoRoot, presets)
		if err != nil {
			return "", err
		}
		if len(templates) > 0 {
			payload["scaffolds.template_content"] = templates
		}
	}
	if effective.Worktree.PostCreate != nil || hasExecutableScaffold || hasSelectors {
		localFiles, err := hashLocalExecutionFiles(repoRoot, effective, presets)
		if err != nil {
			return "", err
		}
		if len(localFiles) > 0 {
			payload["local_execution_content"] = localFiles
		}
	}
	if hasExecutableScaffold || hasSelectors {
		localSkills, err := hashLocalSkillSources(repoRoot, presets)
		if err != nil {
			return "", err
		}
		if len(localSkills) > 0 {
			payload["local_skill_content"] = localSkills
		}
	}
	if len(payload) == 0 {
		return "", nil
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("dev-project-execution-v1\x00"))
	_, _ = hash.Write(canonical)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func extractScaffoldSelectors(document map[string]any) (any, bool) {
	out := map[string]any{}
	for _, key := range []string{"default_preset", "default_agents"} {
		if value, ok := document[key]; ok {
			out[key] = value
		}
	}
	if presets, ok := document["presets"]; ok {
		if inherited, found := extractNamedScaffoldValues(presets, map[string]bool{"extends": true, "catalog": true}); found {
			out["presets"] = inherited
		}
	}
	return out, len(out) > 0
}

func extractNamedScaffoldValues(value any, names map[string]bool) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range typed {
			if names[key] {
				out[key] = child
				continue
			}
			if nested, ok := extractNamedScaffoldValues(child, names); ok {
				out[key] = nested
			}
		}
		return out, len(out) > 0
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for index, child := range typed {
			if nested, ok := extractNamedScaffoldValues(child, names); ok {
				out = append(out, map[string]any{"index": index, "value": nested})
			}
		}
		return out, len(out) > 0
	case []any:
		out := make([]any, 0, len(typed))
		for index, child := range typed {
			if nested, ok := extractNamedScaffoldValues(child, names); ok {
				out = append(out, map[string]any{"index": index, "value": nested})
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func hashProjectTemplateSources(repoRoot string, presets any) (map[string]string, error) {
	sources := map[string]bool{}
	collectFileSources(presets, false, sources)
	result := map[string]string{}
	base := filepath.Join(repoRoot, DirectoryName)
	templatesRoot := filepath.Join(base, "templates")
	for source := range sources {
		normalized := path.Clean(strings.ReplaceAll(source, `\`, "/"))
		if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || path.IsAbs(normalized) {
			return nil, fmt.Errorf("project scaffold template source %q must stay inside %s/templates", source, DirectoryName)
		}
		candidate := filepath.Join(templatesRoot, filepath.FromSlash(normalized))
		if normalized == "templates" || strings.HasPrefix(normalized, "templates/") {
			candidate = filepath.Join(base, filepath.FromSlash(normalized))
		}
		resolved, err := pathx.CanonicalChild(templatesRoot, candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve project scaffold template source %q: %w", source, err)
		}
		digest, err := hashRegularFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("hash project scaffold template source %q: %w", source, err)
		}
		result[normalized] = digest
	}
	return result, nil
}

func collectFileSources(value any, inFiles bool, result map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		if inFiles {
			if source, ok := typed["source"].(string); ok && strings.TrimSpace(source) != "" {
				result[source] = true
			}
		}
		for key, child := range typed {
			collectFileSources(child, inFiles || key == "files", result)
		}
	case []map[string]any:
		for _, child := range typed {
			collectFileSources(child, inFiles, result)
		}
	case []any:
		for _, child := range typed {
			collectFileSources(child, inFiles, result)
		}
	}
}

func hashLocalExecutionFiles(repoRoot string, effective Override, presets any) (map[string]string, error) {
	candidates := map[string]bool{}
	if effective.Worktree.PostCreate != nil {
		for _, command := range effective.Worktree.PostCreate.Commands {
			collectCommandFileCandidate(strings.Fields(command), candidates)
		}
	}
	collectScaffoldCommandFiles(presets, candidates)
	result := map[string]string{}
	for candidate := range candidates {
		if strings.Contains(candidate, "{{") {
			continue
		}
		candidate = strings.Trim(candidate, `"'`)
		if !looksLikeLocalExecutionPath(candidate) {
			continue
		}
		resolved := candidate
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repoRoot, filepath.FromSlash(strings.ReplaceAll(resolved, `\`, "/")))
		}
		canonical, err := pathx.Canonical(resolved)
		if err != nil {
			continue // Generated scaffold destinations may not exist until apply.
		}
		info, err := os.Stat(canonical)
		if errors.Is(err, fs.ErrNotExist) || err == nil && !info.Mode().IsRegular() {
			continue
		}
		if err != nil {
			return nil, err
		}
		digest, err := hashRegularFile(canonical)
		if err != nil {
			return nil, err
		}
		result[stableExecutionPath(repoRoot, canonical)] = digest
	}
	return result, nil
}

func collectScaffoldCommandFiles(value any, result map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "command":
				collectCommandFileCandidate(stringSlice(child), result)
			case "run":
				if command, ok := child.(string); ok {
					collectCommandFileCandidate(strings.Fields(command), result)
				}
			default:
				collectScaffoldCommandFiles(child, result)
			}
		}
	case []map[string]any:
		for _, child := range typed {
			collectScaffoldCommandFiles(child, result)
		}
	case []any:
		for _, child := range typed {
			collectScaffoldCommandFiles(child, result)
		}
	}
}

func collectCommandFileCandidate(command []string, result map[string]bool) {
	if len(command) == 0 {
		return
	}
	if looksLikeLocalExecutionPath(command[0]) {
		result[command[0]] = true
		return
	}
	switch strings.ToLower(filepath.Base(command[0])) {
	case "sh", "bash", "dash", "zsh", "python", "python3", "ruby", "perl", "node", "pwsh", "powershell", "powershell.exe":
		for _, argument := range command[1:] {
			if argument == "-c" || argument == "-e" || argument == "-m" || strings.HasPrefix(argument, "-") {
				if argument == "-c" || argument == "-e" || argument == "-m" {
					return
				}
				continue
			}
			if looksLikeLocalExecutionPath(argument) {
				result[argument] = true
			}
			return
		}
	}
}

func stringSlice(value any) []string {
	var result []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
	case []string:
		result = append(result, typed...)
	}
	return result
}

func looksLikeLocalExecutionPath(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	return value != "" && (filepath.IsAbs(value) || filepath.VolumeName(value) != "" ||
		strings.HasPrefix(value, "./") || strings.HasPrefix(value, ".\\") ||
		strings.Contains(value, "/") || strings.Contains(value, `\`))
}

func hashLocalSkillSources(repoRoot string, presets any) (map[string]string, error) {
	sources := map[string]bool{}
	collectLocalSkillSources(presets, sources)
	result := map[string]string{}
	for source := range sources {
		resolved := source
		if strings.HasPrefix(resolved, "file://") {
			resolved = strings.TrimPrefix(resolved, "file://")
		}
		if strings.HasPrefix(resolved, "~/") || strings.HasPrefix(resolved, "~\\") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			resolved = filepath.Join(home, resolved[2:])
		} else if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repoRoot, resolved)
		}
		if info, err := os.Lstat(resolved); err != nil {
			return nil, fmt.Errorf("inspect local setup skill source %q: %w", source, err)
		} else if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("local setup skill source %q must not be a symlink", source)
		}
		canonical, err := pathx.Canonical(resolved)
		if err != nil {
			return nil, fmt.Errorf("resolve local setup skill source %q: %w", source, err)
		}
		digest, err := hashTree(canonical)
		if err != nil {
			return nil, fmt.Errorf("hash local setup skill source %q: %w", source, err)
		}
		result[stableExecutionPath(repoRoot, canonical)] = digest
	}
	return result, nil
}

func stableExecutionPath(repoRoot, filename string) string {
	if inside, err := pathx.Contains(repoRoot, filename); err == nil && inside {
		if relative, err := filepath.Rel(repoRoot, filename); err == nil {
			return "repo:" + filepath.ToSlash(relative)
		}
	}
	return "external:" + filepath.Clean(filename)
}

func collectLocalSkillSources(value any, result map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		if _, hasSetup := typed["setup"]; hasSetup {
			if source, ok := typed["source"].(string); ok && IsLocalSkillSource(source) {
				result[source] = true
			}
		}
		for _, child := range typed {
			collectLocalSkillSources(child, result)
		}
	case []map[string]any:
		for _, child := range typed {
			collectLocalSkillSources(child, result)
		}
	case []any:
		for _, child := range typed {
			collectLocalSkillSources(child, result)
		}
	}
}

// IsLocalSkillSource reports whether the upstream skills CLI will resolve a
// source from the local filesystem instead of fetching mutable remote content.
func IsLocalSkillSource(source string) bool {
	source = strings.TrimSpace(source)
	return source == "." || source == ".." || filepath.IsAbs(source) || filepath.VolumeName(source) != "" ||
		strings.HasPrefix(source, "./") || strings.HasPrefix(source, ".\\") ||
		strings.HasPrefix(source, "../") || strings.HasPrefix(source, "..\\") ||
		strings.HasPrefix(source, "~/") || strings.HasPrefix(source, "~\\") || strings.HasPrefix(source, "file://")
}

func hashRegularFile(filename string) (string, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", filename)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "mode:%04o\x00", info.Mode().Perm())
	_, _ = hash.Write(data)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func hashTree(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("local setup skill root must not be a symlink")
	}
	if info.Mode().IsRegular() {
		return hashRegularFile(root)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local setup skill source is not a directory or regular file")
	}
	hash := sha256.New()
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filename == root {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("local setup skill source contains symlink %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("local setup skill source contains non-regular file %s", relative)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%04o\x00", filepath.ToSlash(relative), info.Mode().Perm())
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func extractExecutableScaffold(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range typed {
			switch key {
			case "hooks", "skills", "setup":
				out[key] = child
			default:
				if nested, ok := extractExecutableScaffold(child); ok {
					out[key] = nested
				}
			}
		}
		return out, len(out) > 0
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for index, child := range typed {
			if nested, ok := extractExecutableScaffold(child); ok {
				out = append(out, map[string]any{"index": index, "value": nested})
			}
		}
		return out, len(out) > 0
	case []any:
		out := make([]any, 0, len(typed))
		for index, child := range typed {
			if nested, ok := extractExecutableScaffold(child); ok {
				out = append(out, map[string]any{"index": index, "value": nested})
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}
