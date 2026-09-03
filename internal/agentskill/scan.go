package agentskill

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/skill"
	"go.yaml.in/yaml/v3"
)

const maxFrontmatterBytes int64 = 64 << 10

type logicalScanLocation struct {
	Path     string
	AgentIDs []string
}

type physicalScanLocation struct {
	Path    string
	Logical []logicalScanLocation
}

type scannedSkill struct {
	Name         string
	Installation Installation
}

type skillAggregate struct {
	Names         []string
	Installations map[string]*Installation
}

func scanProjectScope(ctx context.Context, target agenttarget.Target, definitions []AgentDefinition) ([]Skill, []Diagnostic) {
	root := target.CheckoutRoot
	document := readProjectLock(filepath.Join(root, "skills-lock.json"))
	locations := projectScanLocations(root, definitions, document)
	return scanScope(ctx, ScopeProject, root, target, locations, document)
}

func scanGlobalScope(ctx context.Context, definitions []AgentDefinition) ([]Skill, []Diagnostic) {
	home := homeDirectory()
	document := readGlobalLock(globalLockPath())
	locations := globalScanLocations(definitions)
	return scanScope(ctx, ScopeGlobal, home, agenttarget.Target{}, locations, document)
}

func projectScanLocations(root string, definitions []AgentDefinition, document lockDocument) []physicalScanLocation {
	logical := make([]logicalScanLocation, 0, len(definitions))
	for _, definition := range definitions {
		if definition.ProjectSkillsDir == "" {
			continue
		}
		logical = append(logical, logicalScanLocation{
			Path: filepath.Join(root, definition.ProjectSkillsDir), AgentIDs: []string{definition.ID},
		})
	}
	subagents := map[string]bool{}
	for _, entry := range document.Entries {
		for _, subagent := range entry.Subagents {
			if safePathComponent(subagent) {
				subagents[subagent] = true
			}
		}
	}
	subagentRoot := filepath.Join(root, "agent", "subagents")
	if entries, err := os.ReadDir(subagentRoot); err == nil {
		for _, entry := range entries {
			if !safePathComponent(entry.Name()) {
				continue
			}
			if info, err := os.Stat(filepath.Join(subagentRoot, entry.Name())); err == nil && info.IsDir() {
				subagents[entry.Name()] = true
			}
		}
	}
	for subagent := range subagents {
		logical = append(logical, logicalScanLocation{
			Path:     filepath.Join(subagentRoot, subagent, "skills"),
			AgentIDs: []string{"eve"},
		})
	}
	return dedupeScanLocations(logical)
}

func safePathComponent(value string) bool {
	return pathx.ValidateComponent(value) == nil && safeDisplayValue(value)
}

func globalScanLocations(definitions []AgentDefinition) []physicalScanLocation {
	logical := make([]logicalScanLocation, 0, len(definitions))
	for _, definition := range definitions {
		if definition.GlobalSkillsDir == "" {
			continue
		}
		logical = append(logical, logicalScanLocation{
			Path: definition.GlobalSkillsDir, AgentIDs: []string{definition.ID},
		})
	}
	return dedupeScanLocations(logical)
}

// dedupeScanLocations first coalesces identical registry paths, then aliases to
// the same physical directory. The latter retains every logical path and agent
// attribution while ensuring the directory is read only once.
func dedupeScanLocations(input []logicalScanLocation) []physicalScanLocation {
	byLogical := map[string]*logicalScanLocation{}
	for _, location := range input {
		path := filepath.Clean(location.Path)
		current := byLogical[path]
		if current == nil {
			copy := logicalScanLocation{Path: path}
			byLogical[path] = &copy
			current = &copy
		}
		current.AgentIDs = append(current.AgentIDs, location.AgentIDs...)
	}
	logicalPaths := make([]string, 0, len(byLogical))
	for path := range byLogical {
		logicalPaths = append(logicalPaths, path)
	}
	sort.Strings(logicalPaths)

	byPhysical := map[string]*physicalScanLocation{}
	for _, path := range logicalPaths {
		logical := *byLogical[path]
		logical.AgentIDs = uniqueSorted(logical.AgentIDs)
		physical := path
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			physical = filepath.Clean(resolved)
		}
		group := byPhysical[physical]
		if group == nil {
			group = &physicalScanLocation{Path: physical}
			byPhysical[physical] = group
		}
		group.Logical = append(group.Logical, logical)
	}
	physicalPaths := make([]string, 0, len(byPhysical))
	for path := range byPhysical {
		physicalPaths = append(physicalPaths, path)
	}
	sort.Strings(physicalPaths)
	result := make([]physicalScanLocation, 0, len(physicalPaths))
	for _, path := range physicalPaths {
		group := byPhysical[path]
		sort.Slice(group.Logical, func(i, j int) bool { return group.Logical[i].Path < group.Logical[j].Path })
		result = append(result, *group)
	}
	return result
}

func scanScope(ctx context.Context, scope Scope, scopeRoot string, target agenttarget.Target, locations []physicalScanLocation, document lockDocument) ([]Skill, []Diagnostic) {
	diagnostics := decorateDiagnostics(document.Diagnostics, target)
	var scanned []scannedSkill
	for _, location := range locations {
		if ctx.Err() != nil {
			break
		}
		found, foundDiagnostics := scanLocation(ctx, scope, target, location)
		scanned = append(scanned, found...)
		diagnostics = append(diagnostics, foundDiagnostics...)
	}

	groups := map[string]*skillAggregate{}
	normalizedInstalled := map[string][]string{}
	for _, found := range scanned {
		group := groups[found.Name]
		if group == nil {
			group = &skillAggregate{Installations: map[string]*Installation{}}
			groups[found.Name] = group
		}
		group.Names = append(group.Names, found.Name)
		normalized := normalizedName(found.Name)
		normalizedInstalled[normalized] = append(normalizedInstalled[normalized], found.Name)
		physical := found.Installation.RealPath
		installation := group.Installations[physical]
		if installation == nil {
			copy := found.Installation
			group.Installations[physical] = &copy
			installation = &copy
		} else {
			installation.LogicalPaths = append(installation.LogicalPaths, found.Installation.LogicalPaths...)
			installation.AgentIDs = append(installation.AgentIDs, found.Installation.AgentIDs...)
		}
		installation.LogicalPaths = uniqueSorted(installation.LogicalPaths)
		installation.AgentIDs = uniqueSorted(installation.AgentIDs)
		if len(installation.LogicalPaths) > 0 {
			installation.Path = installation.LogicalPaths[0]
		}
		installation.Attribution = registryAttribution(installation.AgentIDs)
	}

	for normalized, names := range normalizedInstalled {
		names = uniqueSorted(names)
		normalizedInstalled[normalized] = names
		if len(names) > 1 {
			diagnostics = append(diagnostics, decorateDiagnostic(Diagnostic{
				Kind: DiagnosticNameCollision, Scope: scope, Name: normalized,
				Message: fmt.Sprintf("installed skill names %s normalize to %q", strings.Join(names, ", "), normalized),
			}, target))
		}
	}
	exactNames := make([]string, 0, len(groups))
	for name := range groups {
		exactNames = append(exactNames, name)
	}
	sort.Strings(exactNames)
	consumedLocks := map[string]bool{}
	rows := make([]Skill, 0, len(groups)+len(document.Entries))
	for _, name := range exactNames {
		group := groups[name]
		normalized := normalizedName(name)
		var lock LockMetadata
		candidates := document.candidates(normalized)
		matched := false
		if exact, ok := document.Entries[name]; ok {
			lock, matched = exact, true
		} else if len(normalizedInstalled[normalized]) == 1 && len(candidates) == 1 {
			lock, matched = candidates[0], true
		}
		if matched {
			consumedLocks[normalized] = true
		}
		rows = append(rows, buildPresentRow(scope, scopeRoot, target, name, group, lock, candidates, matched))
	}

	lockGroups := document.namesByNormalized()
	lockNormalized := make([]string, 0, len(lockGroups))
	for normalized := range lockGroups {
		lockNormalized = append(lockNormalized, normalized)
	}
	sort.Strings(lockNormalized)
	for _, normalized := range lockNormalized {
		if consumedLocks[normalized] {
			continue
		}
		candidates := document.candidates(normalized)
		if len(candidates) == 0 {
			continue
		}
		selected := candidates[0]
		rows = append(rows, buildMissingRow(scope, scopeRoot, target, selected, candidates))
	}
	return rows, diagnostics
}

func scanLocation(ctx context.Context, scope Scope, target agenttarget.Target, location physicalScanLocation) ([]scannedSkill, []Diagnostic) {
	entries, err := os.ReadDir(location.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []Diagnostic{decorateDiagnostic(Diagnostic{
			Kind: DiagnosticSkillUnreadable, Scope: scope, Path: location.Path,
			Message: fmt.Sprintf("could not read skills directory: %v", err),
		}, target)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var skills []scannedSkill
	var diagnostics []Diagnostic
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		child := filepath.Join(location.Path, entry.Name())
		info, err := os.Stat(child)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			diagnostics = append(diagnostics, decorateDiagnostic(Diagnostic{
				Kind: DiagnosticSkillUnreadable, Scope: scope, Path: child,
				Message: fmt.Sprintf("could not inspect skill directory: %v", err),
			}, target))
			continue
		}
		if !info.IsDir() {
			continue
		}
		name, present, err := readSkillName(filepath.Join(child, "SKILL.md"))
		if !present {
			continue
		}
		if err != nil {
			diagnostics = append(diagnostics, decorateDiagnostic(Diagnostic{
				Kind: DiagnosticSkillFrontmatter, Scope: scope,
				Path: filepath.Join(child, "SKILL.md"), Message: err.Error(),
			}, target))
			continue
		}
		physical := child
		if resolved, err := filepath.EvalSymlinks(child); err == nil {
			physical = filepath.Clean(resolved)
		}
		var logicalPaths, agentIDs []string
		for _, logical := range location.Logical {
			logicalPaths = append(logicalPaths, filepath.Join(logical.Path, entry.Name()))
			agentIDs = append(agentIDs, logical.AgentIDs...)
		}
		logicalPaths = uniqueSorted(logicalPaths)
		agentIDs = uniqueSorted(agentIDs)
		installation := Installation{
			RealPath: physical, LogicalPaths: logicalPaths, AgentIDs: agentIDs,
			Attribution: registryAttribution(agentIDs), Integrity: IntegrityUnknown,
		}
		if len(logicalPaths) > 0 {
			installation.Path = logicalPaths[0]
		}
		skills = append(skills, scannedSkill{Name: name, Installation: installation})
	}
	return skills, diagnostics
}

func readSkillName(filename string) (string, bool, error) {
	file, _, err := safefile.OpenRegular(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if errors.Is(err, safefile.ErrNotRegular) {
		return "", true, errors.New("SKILL.md is not a regular file")
	}
	if err != nil {
		return "", true, fmt.Errorf("could not read SKILL.md: %w", err)
	}
	defer file.Close()

	// Bound only the frontmatter, not the skill body. A valid skill may have a
	// large reference body, and inventory never needs to read it.
	reader := bufio.NewReader(io.LimitReader(file, maxFrontmatterBytes+1))
	first, readErr := reader.ReadString('\n')
	consumed := int64(len(first))
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", true, fmt.Errorf("could not read SKILL.md: %w", readErr)
	}
	first = strings.TrimPrefix(first, string(rune(0xfeff)))
	if strings.TrimSpace(first) != "---" {
		return "", true, errors.New("SKILL.md must begin with YAML frontmatter")
	}
	var frontmatter strings.Builder
	closed := false
	for {
		line, lineErr := reader.ReadString('\n')
		consumed += int64(len(line))
		if consumed > maxFrontmatterBytes {
			return "", true, fmt.Errorf("SKILL.md frontmatter exceeds %d bytes", maxFrontmatterBytes)
		}
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		frontmatter.WriteString(line)
		if lineErr != nil {
			if errors.Is(lineErr, io.EOF) {
				break
			}
			return "", true, fmt.Errorf("could not read SKILL.md: %w", lineErr)
		}
	}
	if !closed {
		return "", true, fmt.Errorf("SKILL.md frontmatter is not closed within %d bytes", maxFrontmatterBytes)
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter.String()), &metadata); err != nil {
		return "", true, fmt.Errorf("invalid SKILL.md YAML frontmatter: %w", err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if metadata.Name == "" || metadata.Description == "" {
		return "", true, errors.New("SKILL.md frontmatter requires name and description")
	}
	if len(metadata.Name) > 256 || normalizedName(metadata.Name) == "" || !safeDisplayValue(metadata.Name) {
		return "", true, errors.New("SKILL.md frontmatter name is invalid")
	}
	return metadata.Name, true, nil
}

func buildPresentRow(scope Scope, scopeRoot string, target agenttarget.Target, name string, group *skillAggregate, lock LockMetadata, candidates []LockMetadata, matched bool) Skill {
	installations := make([]Installation, 0, len(group.Installations))
	for _, installation := range group.Installations {
		installation.LogicalPaths = uniqueSorted(installation.LogicalPaths)
		installation.AgentIDs = uniqueSorted(installation.AgentIDs)
		installation.Attribution = registryAttribution(installation.AgentIDs)
		installations = append(installations, *installation)
	}
	sort.Slice(installations, func(i, j int) bool {
		if installations[i].RealPath != installations[j].RealPath {
			return installations[i].RealPath < installations[j].RealPath
		}
		return installations[i].Path < installations[j].Path
	})
	var agents []string
	for _, installation := range installations {
		agents = append(agents, installation.AgentIDs...)
	}
	agentIDs := uniqueSorted(agents)
	agents = agentDisplayNames(agentIDs)
	row := Skill{
		Name: name, Scope: scope, ScopeRoot: scopeRoot,
		Agents: agents, Repository: target.RepoDisplay, RepositoryPath: target.RepoPath,
		Checkout: target.CheckoutRoot, Installations: installations, Presence: PresencePresent,
		Integrity: IntegrityUnknown, Attribution: registryAttribution(agentIDs),
		RegistryVersion: RegistryVersion, LockCandidates: append([]LockMetadata(nil), candidates...),
		ManagedBy: ManagedByExternal, UpdateStatus: UpdateUnknown,
		UpdateDetail: "not tracked by the skills CLI",
	}
	if len(installations) > 0 {
		row.Path = installations[0].Path
	}
	if directManagedSkillName(name) {
		if name == skill.Name {
			row.ManagedBy = ManagedByDev
			row.Source = "dev binary"
			applyBundledIntegrity(&row)
			row.UpdateStatus, row.UpdateDetail = bundledStatus(row.Integrity, row.IntegrityDetail)
		} else {
			row.UpdateDetail = "name is reserved for the binary-managed dev-cli skill"
		}
		return row
	}
	if matched {
		copy := lock
		row.Lock = &copy
		row.ManagedBy = ManagedBySkills
		row.UpdateStatus = UpdateUnchecked
		row.UpdateDetail = ""
		row.Source, row.SourceURL = sourceDisplay(lock.Source), sourceDisplay(lock.SourceURL)
		row.SourceType = safeSourceType(lock.SourceType)
		return row
	}
	if len(candidates) > 1 {
		row.UpdateDetail = "ambiguous normalized lock names; no lock entry selected"
	}
	return row
}

func buildMissingRow(scope Scope, scopeRoot string, target agenttarget.Target, lock LockMetadata, candidates []LockMetadata) Skill {
	copy := lock
	row := Skill{
		Name: lock.Name, Scope: scope, ScopeRoot: scopeRoot, Agents: []string{},
		Source: sourceDisplay(lock.Source), SourceURL: sourceDisplay(lock.SourceURL),
		SourceType: safeSourceType(lock.SourceType),
		ManagedBy:  ManagedBySkills, UpdateStatus: UpdateUnchecked,
		Repository: target.RepoDisplay, RepositoryPath: target.RepoPath,
		Checkout: target.CheckoutRoot, Presence: PresenceMissing, Integrity: IntegrityUnknown,
		Attribution: registryAttribution(nil), RegistryVersion: RegistryVersion,
		Lock: &copy, LockCandidates: append([]LockMetadata(nil), candidates...),
	}
	if directManagedSkillName(lock.Name) {
		row.Lock = nil
		if lock.Name == skill.Name {
			row.ManagedBy = ManagedByDev
			row.Source, row.SourceURL, row.SourceType = "dev binary", "", ""
			row.UpdateStatus = UpdateAvailable
			row.UpdateDetail = "bundled skill is missing; run `dev skill install`"
		} else {
			row.ManagedBy = ManagedByExternal
			row.UpdateStatus = UpdateUnknown
			row.UpdateDetail = "name is reserved for the binary-managed dev-cli skill"
		}
	}
	return row
}

func applyBundledIntegrity(row *Skill) {
	files, err := skill.Files()
	if err != nil {
		row.Integrity = IntegrityUnknown
		row.IntegrityDetail = err.Error()
		return
	}
	row.Integrity = IntegrityVerified
	row.IntegrityDetail = "all embedded files match this dev binary"
	for index := range row.Installations {
		status, detail := bundledIntegrity(row.Installations[index].RealPath, files)
		row.Installations[index].Integrity = status
		row.Installations[index].IntegrityDetail = detail
		if status == IntegrityDrifted {
			row.Integrity = IntegrityDrifted
			row.IntegrityDetail = detail
		} else if status == IntegrityUnknown && row.Integrity != IntegrityDrifted {
			row.Integrity = IntegrityUnknown
			row.IntegrityDetail = detail
		}
	}
}

func bundledIntegrity(directory string, files map[string][]byte) (Integrity, string) {
	for relative, want := range files {
		got, err := safefile.ReadRegular(context.Background(), filepath.Join(directory, relative), int64(len(want))+1)
		if err != nil || !bytes.Equal(got, want) {
			return IntegrityDrifted, "bundled skill differs; run `dev skill install`"
		}
	}
	return IntegrityVerified, "all embedded files match this dev binary"
}

// bundledStatus preserves the legacy update-status contract while deriving it
// from the already-computed aggregate integrity across every installation.
func bundledStatus(integrity Integrity, detail string) (UpdateStatus, string) {
	if integrity == IntegrityVerified {
		return UpdateCurrent, detail
	}
	if integrity == IntegrityDrifted {
		return UpdateAvailable, detail
	}
	return UpdateFailed, detail
}

func sourceDisplay(value string) string {
	if value == "" {
		return ""
	}
	if !safeDisplayValue(value) {
		return "[redacted]"
	}
	value = repo.RedactCloneRef(value)
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return parsed.String()
	}
	return value
}

func safeSourceType(value string) string {
	if value == "" || safeDisplayValue(value) {
		return value
	}
	return "[redacted]"
}

func registryAttribution(agentIDs []string) Attribution {
	return Attribution{Registry: RegistrySource, RegistryVersion: RegistryVersion, AgentIDs: uniqueSorted(agentIDs)}
}

func decorateDiagnostics(input []Diagnostic, target agenttarget.Target) []Diagnostic {
	result := make([]Diagnostic, len(input))
	for index, diagnostic := range input {
		result[index] = decorateDiagnostic(diagnostic, target)
	}
	return result
}

func decorateDiagnostic(diagnostic Diagnostic, target agenttarget.Target) Diagnostic {
	diagnostic.Repository = target.RepoDisplay
	diagnostic.RepositoryPath = target.RepoPath
	diagnostic.Checkout = target.CheckoutRoot
	return diagnostic
}
