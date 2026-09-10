// Package agentskill inventories agent skills directly from documented install
// locations and lock files. Inventory, list, Managed, and FindProject reads are
// native and never execute a provider, agent detector, skill, or project code;
// only explicit mutation commands cross the interactive provider boundary.
package agentskill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
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

// Integrity is local byte integrity against an authority bundled in dev. Only
// the bundled dev-cli skill has such an authority; update freshness is tracked
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

// lockEntry and lock remain for package-level compatibility with focused tests
// and old callers while public code uses LockMetadata.
type lockEntry = LockMetadata

// Skill is one logical skill in one scope and target. Installations may contain
// several agent paths and physical copies. Lock-only rows have PresenceMissing;
// unlocked filesystem rows remain visible as ManagedByExternal.
type Skill struct {
	Name string
	// Names retains every exact frontmatter name merged into this normalized row.
	// More than one name is a diagnostic collision and is not executable through
	// FindProject.
	Names        []string
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
	Checkout        string
	Installations   []Installation
	Presence        Presence
	Integrity       Integrity
	IntegrityDetail string
	Attribution     Attribution
	RegistryVersion string
	Lock            *LockMetadata
	LockCandidates  []LockMetadata

	lock *lockEntry
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
	Kind       DiagnosticKind
	Scope      Scope
	Repository string
	Checkout   string
	Path       string
	Name       string
	Message    string
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
	root := ProjectRoot(ctx, cwd)
	target, err := agenttarget.Current(ctx, root)
	if err != nil {
		name := filepath.Base(root)
		target = agenttarget.Target{
			RepoName: name, RepoDisplay: name, RepoPath: root, CheckoutRoot: root,
		}
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
			rows, diagnostics := scanProjectScope(target, definitions)
			result.Skills = append(result.Skills, rows...)
			result.Diagnostics = append(result.Diagnostics, diagnostics...)
		}
	}
	if global {
		rows, diagnostics := scanGlobalScope(definitions)
		result.Skills = append(result.Skills, rows...)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
	}

	sortSkills(result.Skills)
	sortDiagnostics(result.Diagnostics)
	if options.Check {
		result.Skills = CheckUpdates(ctx, result.Skills)
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

// Managed reports whether name has an unambiguous upstream lock entry in the
// requested scope. It performs native file reads only.
func Managed(ctx context.Context, cwd, name string, scope Scope) bool {
	root := ProjectRoot(ctx, cwd)
	var document lockDocument
	if scope == ScopeGlobal {
		document = readGlobalLock(globalLockPath())
	} else {
		document = readProjectLock(filepath.Join(root, "skills-lock.json"))
	}
	_, ok := document.find(name)
	return ok
}

// FindProject returns one installed project-scoped skill by exact or normalized
// name. An exact name wins when normalized filesystem names collide.
func FindProject(ctx context.Context, projectRoot, name string) (Skill, error) {
	rows, err := List(ctx, projectRoot, ListOptions{Project: true})
	if err != nil {
		return Skill{}, err
	}
	want := normalizedName(name)
	for _, row := range rows {
		if row.Scope != ScopeProject || row.Presence != PresencePresent || normalizedName(row.Name) != want {
			continue
		}
		if len(row.Names) > 1 {
			return Skill{}, fmt.Errorf("project skill %q is ambiguous: installed names %s normalize to %q", name, strings.Join(row.Names, ", "), want)
		}
		if row.Name == name {
			return row, nil
		}
	}
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
	home, _ := os.UserHomeDir()
	return home
}
