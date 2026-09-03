// Package agentskill inventories agent skills directly from documented install
// locations and lock files. Reads are native and never execute a provider,
// agent detector, skill, or project code; only explicit mutation commands cross
// the upstream interactive provider boundary.
package agentskill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/skill"
)

// DefaultSource is the catalog opened by `dev skill add` when no package is
// supplied. It remains a shortcut to the upstream interactive wizard: dev
// never silently chooses skills, agents, or an install scope.
const DefaultSource = "daviddwlee84/agent-skills/skills"

type Scope string

const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

type ManagedBy string

const (
	ManagedBySkills   ManagedBy = "skills"
	ManagedByDev      ManagedBy = "dev"
	ManagedByExternal ManagedBy = "external"
)

type UpdateStatus string

const (
	UpdateUnchecked UpdateStatus = "unchecked"
	UpdateCurrent   UpdateStatus = "current"
	UpdateAvailable UpdateStatus = "update_available"
	UpdateMissing   UpdateStatus = "upstream_missing"
	UpdateUnknown   UpdateStatus = "unverifiable"
	UpdateFailed    UpdateStatus = "check_failed"
)

// Presence distinguishes an installed skill from a lock-only row.
type Presence string

const (
	PresencePresent Presence = "present"
	PresenceMissing Presence = "missing"
)

// Integrity verifies the files supplied by an authority bundled in dev. Only
// the bundled dev-cli skill has such an authority; extra user files are ignored
// and update freshness is tracked
// independently by UpdateStatus.
type Integrity string

const (
	IntegrityUnknown  Integrity = "unknown"
	IntegrityVerified Integrity = "verified"
	IntegrityDrifted  Integrity = "drifted"
)

// Attribution records why an installation is associated with agent IDs. These
// are registry-compatible IDs, not claims that detector callbacks ran.
type Attribution struct {
	Registry        string
	RegistryVersion string
	AgentIDs        []string
}

// Installation is one physical skill directory plus every logical registry
// path that reaches it. LogicalPaths and AgentIDs preserve symlink aliases and
// shared-directory compatibility while RealPath prevents double-counting.
type Installation struct {
	Path            string
	RealPath        string
	LogicalPaths    []string
	AgentIDs        []string
	Attribution     Attribution
	Integrity       Integrity
	IntegrityDetail string
}

// LockMetadata is the normalized project/global lock representation. It is a
// union of compatible project-v1 and global-v1-v3 fields; fields absent in an
// older schema remain empty rather than making the whole inventory fail.
type LockMetadata struct {
	Name           string
	NormalizedName string
	File           string
	Version        int
	Scope          Scope

	Source          string
	SourceURL       string
	SourceType      string
	Ref             string
	SkillPath       string
	ComputedHash    string
	ContentHash     string
	SkillFolderHash string
	// RecordedHash and HashKind normalize the comparable hash across project
	// v1 and global v1-v3 lock schemas while retaining the original fields.
	RecordedHash    string
	HashKind        string
	InstalledAt     string
	UpdatedAt       string
	PluginName      string
	SourceBaseURL   string
	WellKnownDigest string
	Subagents       []string
}

// Skill is one logical skill in one scope and target. Installations may contain
// several agent paths and physical copies. Lock-only rows have PresenceMissing;
// unlocked filesystem rows remain visible as ManagedByExternal.
type Skill struct {
	Name         string
	Scope        Scope
	ScopeRoot    string
	Path         string
	Agents       []string
	Source       string
	SourceURL    string
	SourceType   string
	ManagedBy    ManagedBy
	UpdateStatus UpdateStatus
	UpdateDetail string

	Repository      string
	RepositoryPath  string
	Checkout        string
	Installations   []Installation
	Presence        Presence
	Integrity       Integrity
	IntegrityDetail string
	Attribution     Attribution
	RegistryVersion string
	Lock            *LockMetadata
	LockCandidates  []LockMetadata
}

// DiagnosticKind identifies a non-fatal inventory problem.
type DiagnosticKind string

const (
	DiagnosticLockUnreadable   DiagnosticKind = "lock_unreadable"
	DiagnosticLockMalformed    DiagnosticKind = "lock_malformed"
	DiagnosticLockUnsupported  DiagnosticKind = "lock_unsupported"
	DiagnosticLockOversized    DiagnosticKind = "lock_oversized"
	DiagnosticSkillUnreadable  DiagnosticKind = "skill_unreadable"
	DiagnosticSkillFrontmatter DiagnosticKind = "skill_frontmatter"
	DiagnosticNameCollision    DiagnosticKind = "name_collision"
)

// Diagnostic reports a skipped or ambiguous input without aborting unrelated
// scopes or repositories.
type Diagnostic struct {
	Kind           DiagnosticKind
	Scope          Scope
	Repository     string
	RepositoryPath string
	Checkout       string
	Path           string
	Name           string
	Message        string
}

// Result is a complete non-mutating native inventory.
type Result struct {
	Skills          []Skill
	Diagnostics     []Diagnostic
	RegistrySource  string
	RegistryVersion string
}

// ListOptions selects scopes and the opt-in network freshness check. With no
// scope selected both are returned.
type ListOptions struct {
	Project bool
	Global  bool
	Check   bool
}

// ProjectRoot uses the current linked-worktree checkout root. Falling back to
// cwd outside Git keeps project-scoped skills useful for ordinary folders too.
func ProjectRoot(ctx context.Context, cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	if repository, err := gitx.Discover(ctx, cwd); err == nil && repository.Root != "" {
		return repository.Root
	}
	return filepath.Clean(cwd)
}

// Inventory scans cwd's current target and the requested global scope.
func Inventory(ctx context.Context, cwd string, options ListOptions) (Result, error) {
	target, err := agenttarget.Current(ctx, cwd)
	if err != nil {
		return Result{}, err
	}
	return Scan(ctx, []agenttarget.Target{target}, options)
}

// Scan inventories the requested targets. Global paths are scanned only once,
// regardless of target count.
func Scan(ctx context.Context, targets []agenttarget.Target, options ListOptions) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	project, global := options.Project, options.Global
	if !project && !global {
		project, global = true, true
	}

	result := Result{RegistrySource: RegistrySource, RegistryVersion: RegistryVersion}
	definitions := Registry()
	if project {
		for _, target := range agenttarget.Dedupe(targets) {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			rows, diagnostics := scanProjectScope(ctx, target, definitions)
			result.Skills = append(result.Skills, rows...)
			result.Diagnostics = append(result.Diagnostics, diagnostics...)
			if err := ctx.Err(); err != nil {
				return result, err
			}
		}
	}
	if global {
		rows, diagnostics := scanGlobalScope(ctx, definitions)
		result.Skills = append(result.Skills, rows...)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}

	sortSkills(result.Skills)
	sortDiagnostics(result.Diagnostics)
	if options.Check {
		result.Skills = CheckUpdates(ctx, result.Skills)
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ScanTargets is an explicit compatibility name for Scan.
func ScanTargets(ctx context.Context, targets []agenttarget.Target, options ListOptions) (Result, error) {
	return Scan(ctx, targets, options)
}

// List preserves the original row-only API. Native diagnostics are available
// through Inventory or Scan and never turn a malformed neighboring scope into a
// fatal whole-scan error.
func List(ctx context.Context, cwd string, options ListOptions) ([]Skill, error) {
	result, err := Inventory(ctx, cwd, options)
	return result.Skills, err
}

// ManagedName returns the canonical lock key for one unambiguous provider-
// managed skill. Direct-managed dev-cli and option-like keys are never handed
// to the external update command.
func directManagedSkillName(name string) bool {
	return reservedSkillNameKey(name) == reservedSkillNameKey(skill.Name)
}

func reservedSkillNameKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func lockUpdateable(entry LockMetadata) bool {
	if _, ok := safeSkillFolder(entry.SkillPath); !ok || !safeProviderSkillName(entry.Name) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(entry.SourceType)) {
	case "github", "git", "gitlab":
		_, ok := sourceURL(entry)
		return ok
	case "well-known":
		return strings.TrimSpace(entry.Source) != "" || strings.TrimSpace(entry.SourceURL) != ""
	default:
		return false
	}
}

// CanUpdate reports whether a listed row has enough safe lock metadata for the
// external provider's named update flow.
func CanUpdate(row Skill) bool {
	return row.ManagedBy == ManagedBySkills && row.Lock != nil && lockUpdateable(*row.Lock) &&
		!directManagedSkillName(row.Lock.Name)
}

func ManagedName(ctx context.Context, cwd, name string, scope Scope) (string, bool) {
	if directManagedSkillName(name) {
		return "", false
	}
	root := ProjectRoot(ctx, cwd)
	var document lockDocument
	if scope == ScopeGlobal {
		document = readGlobalLock(globalLockPath())
	} else {
		document = readProjectLock(filepath.Join(root, "skills-lock.json"))
	}
	entry, ok := document.find(name)
	if !ok || !lockUpdateable(entry) {
		return "", false
	}
	return entry.Name, true
}

// Managed reports whether name has an update-safe upstream lock entry.
func Managed(ctx context.Context, cwd, name string, scope Scope) bool {
	_, ok := ManagedName(ctx, cwd, name, scope)
	return ok
}

// FindProject returns one installed project-scoped skill by exact or normalized
// name. An exact name wins when normalized filesystem names collide.
func FindProject(ctx context.Context, projectRoot, name string) (Skill, error) {
	rows, err := List(ctx, projectRoot, ListOptions{Project: true})
	if err != nil {
		return Skill{}, err
	}
	for _, row := range rows {
		if row.Scope == ScopeProject && row.Presence == PresencePresent && row.Name == name {
			return row, nil
		}
	}
	want := normalizedName(name)
	var matches []Skill
	for _, row := range rows {
		if row.Scope == ScopeProject && row.Presence == PresencePresent && normalizedName(row.Name) == want {
			matches = append(matches, row)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return Skill{}, fmt.Errorf("project skill %q is not installed", name)
}

// MergeResults combines independently scanned targets while restoring the
// domain's deterministic ordering and registry metadata.
func MergeResults(results ...Result) Result {
	merged := Result{RegistrySource: RegistrySource, RegistryVersion: RegistryVersion}
	for _, result := range results {
		merged.Skills = append(merged.Skills, result.Skills...)
		merged.Diagnostics = append(merged.Diagnostics, result.Diagnostics...)
		if result.RegistrySource != "" {
			merged.RegistrySource = result.RegistrySource
		}
		if result.RegistryVersion != "" {
			merged.RegistryVersion = result.RegistryVersion
		}
	}
	sortSkills(merged.Skills)
	sortDiagnostics(merged.Diagnostics)
	return merged
}

func sortSkills(rows []Skill) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.Scope != right.Scope {
			return left.Scope == ScopeProject
		}
		if left.Repository != right.Repository {
			return left.Repository < right.Repository
		}
		if left.Checkout != right.Checkout {
			return left.Checkout < right.Checkout
		}
		leftName, rightName := strings.ToLower(left.Name), strings.ToLower(right.Name)
		if leftName != rightName {
			return leftName < rightName
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Path < right.Path
	})
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		left, right := diagnostics[i], diagnostics[j]
		if left.Repository != right.Repository {
			return left.Repository < right.Repository
		}
		if left.Checkout != right.Checkout {
			return left.Checkout < right.Checkout
		}
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Message < right.Message
	})
}

func safeDisplayValue(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	})
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func homeDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return ""
	}
	return home
}
