package agentskill

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
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

func scanProjectScope(target agenttarget.Target, definitions []AgentDefinition) ([]Skill, []Diagnostic) {
	root := target.CheckoutRoot
	document := readProjectLock(filepath.Join(root, "skills-lock.json"))
	locations := projectScanLocations(root, definitions)
	return scanScope(ScopeProject, root, target, locations, document)
}

func scanGlobalScope(definitions []AgentDefinition) ([]Skill, []Diagnostic) {
	home := homeDirectory()
	document := readGlobalLock(globalLockPath())
	locations := globalScanLocations(definitions)
	return scanScope(ScopeGlobal, home, agenttarget.Target{}, locations, document)
}

func projectScanLocations(root string, definitions []AgentDefinition) []physicalScanLocation {
	logical := make([]logicalScanLocation, 0, len(definitions))
	for _, definition := range definitions {
		if definition.ProjectSkillsDir == "" {
			continue
		}
		logical = append(logical, logicalScanLocation{
			Path: filepath.Join(root, definition.ProjectSkillsDir), AgentIDs: []string{definition.ID},
		})
	}
	return dedupeScanLocations(logical)
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

func scanScope(scope Scope, scopeRoot string, target agenttarget.Target, locations []physicalScanLocation, document lockDocument) ([]Skill, []Diagnostic) {
	diagnostics := decorateDiagnostics(document.Diagnostics, target)
	var scanned []scannedSkill
	for _, location := range locations {
		found, foundDiagnostics := scanLocation(scope, target, location)
		scanned = append(scanned, found...)
		diagnostics = append(diagnostics, foundDiagnostics...)
	}

	groups := map[string]*skillAggregate{}
	for _, found := range scanned {
		normalized := normalizedName(found.Name)
		group := groups[normalized]
		if group == nil {
			group = &skillAggregate{Installations: map[string]*Installation{}}
			groups[normalized] = group
		}
		group.Names = append(group.Names, found.Name)
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

	normalizedNames := make([]string, 0, len(groups))
	for normalized := range groups {
		normalizedNames = append(normalizedNames, normalized)
	}
	sort.Strings(normalizedNames)
	consumedLocks := map[string]bool{}
	rows := make([]Skill, 0, len(groups)+len(document.Entries))
	for _, normalized := range normalizedNames {
		group := groups[normalized]
		names := uniqueSorted(group.Names)
		if len(names) > 1 {
			diagnostics = append(diagnostics, decorateDiagnostic(Diagnostic{
				Kind: DiagnosticNameCollision, Scope: scope, Name: normalized,
				Message: fmt.Sprintf("installed skill names %s normalize to %q", strings.Join(names, ", "), normalized),
			}, target))
		}
		lock, candidates, matched := selectLock(document, names, normalized)
		if len(candidates) > 0 {
			consumedLocks[normalized] = true
		}
		row := buildPresentRow(scope, scopeRoot, target, names[0], group, lock, candidates, matched)
		rows = append(rows, row)
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

func scanLocation(scope Scope, target agenttarget.Target, location physicalScanLocation) ([]scannedSkill, []Diagnostic) {
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
	file, err := os.Open(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
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
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter.String()), &metadata); err != nil {
		return "", true, fmt.Errorf("invalid SKILL.md YAML frontmatter: %w", err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	if metadata.Name == "" {
		return "", true, errors.New("SKILL.md frontmatter has no name")
	}
	if len(metadata.Name) > 256 || normalizedName(metadata.Name) == "" {
		return "", true, errors.New("SKILL.md frontmatter name is invalid")
	}
	return metadata.Name, true, nil
}

func selectLock(document lockDocument, installedNames []string, normalized string) (LockMetadata, []LockMetadata, bool) {
	candidates := document.candidates(normalized)
	for _, name := range installedNames {
		if exact, ok := document.Entries[name]; ok {
			return exact, candidates, true
		}
	}
	if len(candidates) == 1 {
		return candidates[0], candidates, true
	}
	return LockMetadata{}, candidates, false
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
	agents = uniqueSorted(agents)
	row := Skill{
		Name: name, Names: uniqueSorted(group.Names), Scope: scope, ScopeRoot: scopeRoot,
		Agents: agents, Repository: target.RepoPath, Checkout: target.CheckoutRoot,
		Installations: installations, Presence: PresencePresent,
		Integrity: IntegrityUnknown, Attribution: registryAttribution(agents),
		RegistryVersion: RegistryVersion, LockCandidates: append([]LockMetadata(nil), candidates...),
		ManagedBy: ManagedByExternal, UpdateStatus: UpdateUnknown,
		UpdateDetail: "not tracked by the skills CLI",
	}
	if len(installations) > 0 {
		row.Path = installations[0].Path
	}
	isBundled := name == skill.Name
	if matched {
		copy := lock
		row.Lock, row.lock = &copy, &copy
		row.ManagedBy = ManagedBySkills
		row.UpdateStatus = UpdateUnchecked
		row.UpdateDetail = ""
		row.Source, row.SourceURL, row.SourceType = lock.Source, lock.SourceURL, lock.SourceType
		if isBundled {
			applyBundledIntegrity(&row)
		}
		return row
	}
	if isBundled {
		row.ManagedBy = ManagedByDev
		row.Source = "dev binary"
		row.UpdateDetail = "freshness is managed by the dev binary"
		applyBundledIntegrity(&row)
	} else if len(candidates) > 1 {
		row.UpdateDetail = "ambiguous normalized lock names; no lock entry selected"
	}
	return row
}

func buildMissingRow(scope Scope, scopeRoot string, target agenttarget.Target, lock LockMetadata, candidates []LockMetadata) Skill {
	copy := lock
	return Skill{
		Name: lock.Name, Names: []string{lock.Name}, Scope: scope, ScopeRoot: scopeRoot,
		Source: lock.Source, SourceURL: lock.SourceURL, SourceType: lock.SourceType,
		ManagedBy: ManagedBySkills, UpdateStatus: UpdateUnchecked,
		Repository: target.RepoPath, Checkout: target.CheckoutRoot,
		Presence: PresenceMissing, Integrity: IntegrityUnknown,
		Attribution: registryAttribution(nil), RegistryVersion: RegistryVersion,
		Lock: &copy, lock: &copy, LockCandidates: append([]LockMetadata(nil), candidates...),
	}
}

func applyBundledIntegrity(row *Skill) {
	row.Integrity = IntegrityVerified
	row.IntegrityDetail = "matches this dev binary"
	for index := range row.Installations {
		status, detail := bundledIntegrity(row.Installations[index].RealPath)
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

func bundledIntegrity(directory string) (Integrity, string) {
	files, err := skill.Files()
	if err != nil {
		return IntegrityUnknown, err.Error()
	}
	for relative, want := range files {
		got, err := os.ReadFile(filepath.Join(directory, relative))
		if err != nil || !bytes.Equal(got, want) {
			return IntegrityDrifted, "bundled skill differs; run `dev skill install`"
		}
	}
	return IntegrityVerified, "matches this dev binary"
}

// bundledStatus preserves the former helper while keeping freshness and local
// integrity separate in native inventory rows.
func bundledStatus(directory string) (UpdateStatus, string) {
	status, detail := bundledIntegrity(directory)
	if status == IntegrityVerified {
		return UpdateCurrent, detail
	}
	if status == IntegrityDrifted {
		return UpdateAvailable, detail
	}
	return UpdateFailed, detail
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
	diagnostic.Repository = target.RepoPath
	diagnostic.Checkout = target.CheckoutRoot
	return diagnostic
}
