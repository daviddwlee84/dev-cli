// Package experiment owns discovery, creation, selection and graduation policy
// for the dated scratch directories managed by dev. It deliberately has no
// terminal or runtime dependencies; callers decide how to render and open the
// paths returned here.
package experiment

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

var (
	// ErrNotFound identifies a Try reference that matched no eligible item.
	ErrNotFound = errors.New("try not found")
	// ErrAmbiguous identifies a reference that needs a more specific name or ID.
	ErrAmbiguous = errors.New("ambiguous try reference")
)

// Diagnostic is one non-fatal inventory failure. Other valid Tries remain in
// the result when a catalog record, directory, or Git probe cannot be read.
type Diagnostic struct {
	Path      string
	Operation string
	Err       error
}

func (d Diagnostic) Error() string {
	operation := d.Operation
	if operation == "" {
		operation = "inspect"
	}
	if d.Path == "" {
		return fmt.Sprintf("%s: %v", operation, d.Err)
	}
	return fmt.Sprintf("%s %s: %v", operation, d.Path, d.Err)
}

func (d Diagnostic) Unwrap() error { return d.Err }

// Candidate is the deterministic, human-readable part of an ambiguous match.
type Candidate struct {
	ID       string
	Name     string
	Basename string
	Path     string
}

// NotFoundError describes an unsuccessful reference lookup.
type NotFoundError struct {
	Ref  string
	Root string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no try matching %q under %s", e.Ref, e.Root)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// AmbiguousError lists every candidate rather than silently picking whichever
// directory happened to have the newest mtime.
type AmbiguousError struct {
	Ref        string
	Candidates []Candidate
}

func (e *AmbiguousError) Error() string {
	labels := make([]string, 0, len(e.Candidates))
	for _, candidate := range e.Candidates {
		label := candidate.Basename
		if label == "" {
			label = candidate.Name
		}
		if candidate.ID != "" {
			label += " (" + candidate.ID + ")"
		}
		labels = append(labels, label)
	}
	return fmt.Sprintf("try reference %q is ambiguous: %s", e.Ref, strings.Join(labels, ", "))
}

func (e *AmbiguousError) Unwrap() error { return ErrAmbiguous }

// AmbiguousMatchError is retained as a descriptive alias for callers that use
// the same naming as catalog matching.
type AmbiguousMatchError = AmbiguousError

// LiveFacts are observations, never persisted status. DiscoverError is kept
// separate from StatusError and LastCommitError so one failed probe does not
// erase the facts gathered by the others.
type LiveFacts struct {
	Present     bool
	CurrentPath string
	RealPath    string

	Repo   *gitx.Repo
	Status *gitx.Status

	LastCommit        time.Time
	LastCommitSubject string

	DiscoverError   error
	StatusError     error
	LastCommitError error
}

// Item joins durable catalog identity with the current host's live facts.
// Entry is a deep copy and may be nil when a live directory could not be
// cataloged; CatalogError then explains why it is untracked.
type Item struct {
	Entry *catalog.Entry

	ID       string
	Kind     catalog.Kind
	Name     string
	Basename string
	Slug     string
	Phase    catalog.ExperimentPhase
	Note     string
	Tags     []string

	Created    time.Time
	Started    time.Time
	LastOpened time.Time

	OriginURL      string
	RemoteIdentity string

	Live         LiveFacts
	CatalogError error
}

// CurrentPath returns the host-local navigation path, if one is known.
func (i Item) CurrentPath() string { return i.Live.CurrentPath }

// IsRepo reports whether Git discovery identified this Try as a repository.
func (i Item) IsRepo() bool { return i.Live.Repo != nil }

// Activity is the newest explicit dev open or Git commit. A zero time means
// activity is unknown; directory mtime is deliberately not a substitute.
func (i Item) Activity() time.Time {
	activity := i.LastOpened
	if i.Live.LastCommit.After(activity) {
		activity = i.Live.LastCommit
	}
	return activity
}

// DisplayName is the stable directory label used by legacy CLI output.
func (i Item) DisplayName() string {
	if i.Basename != "" {
		return i.Basename
	}
	if i.Name != "" {
		return i.Name
	}
	return i.ID
}

// SearchText contains the identity and path terms useful to CLI/TUI filters.
func (i Item) SearchText() string {
	values := []string{
		i.ID, string(i.Kind), i.Name, i.Basename, i.Slug, string(i.Phase),
		i.Note, strings.Join(i.Tags, " "), i.OriginURL, i.RemoteIdentity, i.Live.CurrentPath,
	}
	if i.Entry != nil {
		for host, location := range i.Entry.Locations {
			values = append(values, host, string(location.State), location.CurrentPath,
				location.RestorePath, location.RealPath, location.GitCommonDir)
		}
	}
	return strings.Join(values, " ")
}

// OpenTarget contains everything a caller needs for runtime handoff without
// introducing a runtime dependency into this package.
type OpenTarget struct {
	CatalogID string
	Path      string
	Label     string
}

// OpenTarget returns the selected directory and presentation label.
func (i Item) OpenTarget() OpenTarget {
	return OpenTarget{CatalogID: i.ID, Path: i.Live.CurrentPath, Label: i.DisplayName()}
}

// ListOptions controls durable-history visibility and optional live work.
// The zero value is deliberately narrow: only active, present Tries are
// returned. IncludeNonPresent is retained as the compatibility umbrella for
// archived and evicted locations; All enables every history class.
type ListOptions struct {
	All               bool
	IncludeDeprecated bool
	IncludeArchived   bool
	IncludeEvicted    bool
	IncludeGraduated  bool
	IncludeNonPresent bool
	SkipEnrichment    bool
	Query             string
}

// ResolveOptions makes history visibility explicit. Legacy Resolve and
// ResolveOrCreate use the zero value, so an archived, deprecated, evicted, or
// graduated record can never be opened or revived implicitly.
type ResolveOptions struct {
	IncludeDeprecated bool
	IncludeArchived   bool
	IncludeEvicted    bool
	IncludeGraduated  bool
}

// TransitionOperation identifies a filesystem lifecycle move.
type TransitionOperation string

const (
	TransitionArchive  TransitionOperation = "archive"
	TransitionRestore  TransitionOperation = "restore"
	TransitionGraduate TransitionOperation = "graduate"
)

// TransitionRequest selects a Try and optionally supplies a restore target.
// To is meaningful only for restore. CurrentDir is used by graduation when Ref
// is empty, matching the legacy top-level command.
type TransitionRequest struct {
	Ref        string
	To         string
	CurrentDir string
}

// TransitionPlan is a fully resolved move that can be rendered before apply.
type TransitionPlan struct {
	Operation   TransitionOperation
	Item        Item
	Source      string
	Destination string
	Diagnostics []Diagnostic

	LinkedWorktree bool
}

// TransitionResult reports both the move and its durable catalog outcome.
type TransitionResult struct {
	Plan          TransitionPlan
	Item          Item
	Moved         bool
	RolledBack    bool
	RollbackError error
	Diagnostics   []Diagnostic
}

// PatchRequest applies user-owned metadata to an existing experiment record.
// A non-nil Note represents an explicit update, including clearing it to "".
type PatchRequest struct {
	Ref        string
	AddTags    []string
	RemoveTags []string
	Note       *string
}

// CreateRequest describes either a new empty Try or a local Git clone.
type CreateRequest struct {
	Name  string
	Clone string
	NoGit bool
}

// CreateResult distinguishes filesystem success from catalog success. A
// catalog error is returned alongside a result with Created and Path set.
type CreateResult struct {
	Item        Item
	Path        string
	Created     bool
	Existing    bool
	Cloned      bool
	Tracked     bool
	InitWarning error
	Diagnostics []Diagnostic
}

// OpenTarget returns the selected or newly created Try for caller handoff.
func (r CreateResult) OpenTarget() OpenTarget {
	if r.Item.Live.CurrentPath != "" {
		return r.Item.OpenTarget()
	}
	return OpenTarget{CatalogID: r.Item.ID, Path: r.Path, Label: filepath.Base(r.Path)}
}

// TouchResult records the explicit activity written after a successful caller
// handoff.
type TouchResult struct {
	Item      Item
	TouchedAt time.Time
}

// GraduateRequest selects a Try and its safe project destination. CurrentDir is
// optional; an empty Ref falls back to CurrentDir and then the injected getwd.
type GraduateRequest struct {
	Ref        string
	Category   string
	Name       string
	CurrentDir string
	DryRun     bool
}

// GraduatePlan can be rendered before an apply. Planning may reconcile legacy
// catalog metadata, but never changes the source or destination trees. Apply
// plans again so a destination created after preview cannot be overwritten.
type GraduatePlan struct {
	Item Item

	Source      string
	Destination string
	Category    string
	Name        string

	NeedsGitInit       bool
	NeedsInitialCommit bool
	LinkedWorktree     bool
	DryRun             bool
	Diagnostics        []Diagnostic
}

// GraduateResult reports local work only. Remote creation and push remain an
// explicit caller step after this result succeeds.
type GraduateResult struct {
	Plan GraduatePlan
	Item Item

	Moved             bool
	GitInitialized    bool
	InitialCommitMade bool
	RolledBack        bool
	RollbackError     error
	Diagnostics       []Diagnostic
}

// GitRunFunc and the other hook types are narrow seams used to make destructive
// failure paths deterministic in tests.
type GitRunFunc func(context.Context, string, ...string) (string, error)
type GitDiscoverFunc func(context.Context, string) (gitx.Repo, error)
type GitStatusFunc func(context.Context, string) (gitx.Status, error)
type GitLastCommitFunc func(context.Context, string) (int64, string, error)
type GitWorktreesFunc func(context.Context, string) ([]gitx.Worktree, error)
type RenameFunc func(string, string) error
type MoveWorktreeFunc func(context.Context, string, string, string) error
type SameFilesystemFunc func(string, string) (bool, error)
type CatalogCreateFunc func(*catalog.Entry) error
type CatalogUpdateFunc func(string, func(*catalog.Entry) error) (*catalog.Entry, error)

// Hooks replaces only the supplied operations; zero fields use production
// implementations. It is intentionally small and operation-shaped rather than
// exposing the Service's internals.
type Hooks struct {
	GitRun        GitRunFunc
	GitDiscover   GitDiscoverFunc
	GitStatus     GitStatusFunc
	GitLastCommit GitLastCommitFunc
	GitWorktrees  GitWorktreesFunc

	Rename         RenameFunc
	MoveWorktree   MoveWorktreeFunc
	SameFilesystem SameFilesystemFunc
	Getwd          func() (string, error)

	CatalogCreate CatalogCreateFunc
	CatalogUpdate CatalogUpdateFunc
}

// ServiceConfig injects the catalog and host-specific roots. Registry and Store
// must refer to the same durable catalog; either may be omitted when the other
// is sufficient to derive it.
type ServiceConfig struct {
	Registry *catalog.Registry
	Store    *catalog.Store

	TriesRoot   string
	ProjectRoot string
	Host        string
	Clock       func() time.Time

	MaxEnrichment int
	Hooks         Hooks
}

// Service owns all Try policy while delegating persistence and Git porcelain to
// their focused packages.
type Service struct {
	registry *catalog.Registry
	store    *catalog.Store

	reconcileMu sync.Mutex
	triesRoot   string
	projectRoot string
	host        string
	clock       func() time.Time
	workers     int

	gitRun         GitRunFunc
	gitDiscover    GitDiscoverFunc
	gitStatus      GitStatusFunc
	gitLastCommit  GitLastCommitFunc
	worktrees      GitWorktreesFunc
	rename         RenameFunc
	moveWorktree   MoveWorktreeFunc
	sameFilesystem SameFilesystemFunc
	getwd          func() (string, error)
	catalogCreate  CatalogCreateFunc
	catalogUpdate  CatalogUpdateFunc
}

// NewService validates and normalizes the injected service dependencies.
func NewService(config ServiceConfig) (*Service, error) {
	store := config.Store
	registry := config.Registry
	if store == nil && registry != nil {
		store = registry.Store()
	}
	if registry == nil && store != nil {
		registry = catalog.NewRegistry(store)
	}
	if store == nil || registry == nil {
		return nil, errors.New("experiment service requires a catalog registry and store")
	}
	if registry.Store() != store {
		return nil, errors.New("experiment service registry and store do not share a catalog")
	}

	triesRoot, err := absoluteRoot(config.TriesRoot, "tries root")
	if err != nil {
		return nil, err
	}
	projectRoot, err := absoluteRoot(config.ProjectRoot, "project root")
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(config.Host)
	if host == "" || host != config.Host || strings.ContainsRune(host, '\x00') {
		return nil, errors.New("experiment service host is required and must be normalized")
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	workers := config.MaxEnrichment
	if workers <= 0 {
		workers = 4
	}

	s := &Service{
		registry: registry, store: store,
		triesRoot: triesRoot, projectRoot: projectRoot, host: host,
		clock: clock, workers: workers,
		gitRun: gitx.Run, gitDiscover: gitx.Discover,
		gitStatus: gitx.StatusOf, gitLastCommit: gitx.LastCommit,
		worktrees: gitx.Worktrees,
		rename:    renameExclusive, moveWorktree: gitx.MoveWorktree,
		sameFilesystem: sameFilesystem,
		getwd:          os.Getwd,
	}
	// Every experiment catalog hook runs inside store.WithLock. Use the
	// non-reentrant forms so the outer read-check-write transaction remains one
	// cross-process critical section.
	s.catalogCreate = store.CreateUnderLock
	s.catalogUpdate = store.UpdateUnderLock
	applyHooks(s, config.Hooks)
	return s, nil
}

func applyHooks(service *Service, hooks Hooks) {
	if hooks.GitRun != nil {
		service.gitRun = hooks.GitRun
	}
	if hooks.GitDiscover != nil {
		service.gitDiscover = hooks.GitDiscover
	}
	if hooks.GitStatus != nil {
		service.gitStatus = hooks.GitStatus
	}
	if hooks.GitLastCommit != nil {
		service.gitLastCommit = hooks.GitLastCommit
	}
	if hooks.GitWorktrees != nil {
		service.worktrees = hooks.GitWorktrees
	}
	if hooks.Rename != nil {
		service.rename = hooks.Rename
	}
	if hooks.MoveWorktree != nil {
		service.moveWorktree = hooks.MoveWorktree
	}
	if hooks.SameFilesystem != nil {
		service.sameFilesystem = hooks.SameFilesystem
	}
	if hooks.Getwd != nil {
		service.getwd = hooks.Getwd
	}
	if hooks.CatalogCreate != nil {
		service.catalogCreate = hooks.CatalogCreate
	}
	if hooks.CatalogUpdate != nil {
		service.catalogUpdate = hooks.CatalogUpdate
	}
}

func absoluteRoot(root, label string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("experiment service %s is required", label)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("make %s absolute: %w", label, err)
	}
	return filepath.Clean(absolute), nil
}

func (s *Service) now() time.Time { return s.clock().UTC() }

func (s *Service) ensureTriesRoot() error {
	if err := os.MkdirAll(s.triesRoot, 0o755); err != nil {
		return fmt.Errorf("create tries root %s: %w", s.triesRoot, err)
	}
	return nil
}

func candidatesOf(items []Item) []Candidate {
	candidates := make([]Candidate, len(items))
	for index, item := range items {
		candidates[index] = Candidate{
			ID: item.ID, Name: item.Name, Basename: item.Basename, Path: item.Live.CurrentPath,
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := strings.ToLower(candidates[i].Basename), strings.ToLower(candidates[j].Basename)
		if left != right {
			return left < right
		}
		if candidates[i].Basename != candidates[j].Basename {
			return candidates[i].Basename < candidates[j].Basename
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates
}

func itemFromEntry(entry *catalog.Entry, live LiveFacts) Item {
	if entry == nil {
		return Item{Live: live}
	}
	copy := entry.Clone()
	item := Item{
		Entry: copy,
		ID:    copy.ID, Kind: copy.Kind, Name: copy.Name,
		Note: copy.Note, Tags: append([]string(nil), copy.Tags...),
		Created: copy.Created, LastOpened: copy.LastOpened,
		RemoteIdentity: copy.RemoteIdentity,
		Live:           live,
	}
	if copy.Experiment != nil {
		item.Slug = copy.Experiment.Slug
		item.Phase = copy.Experiment.Phase
		item.Started = copy.Experiment.Started
		item.OriginURL = copy.Experiment.OriginURL
	}
	if item.Live.CurrentPath != "" {
		item.Basename = filepath.Base(item.Live.CurrentPath)
	} else if copy.Experiment != nil && copy.Experiment.OriginalPath != "" {
		item.Basename = filepath.Base(copy.Experiment.OriginalPath)
	}
	return item
}

func catalogDiagnostics(diagnostics []catalog.Diagnostic) []Diagnostic {
	out := make([]Diagnostic, len(diagnostics))
	for index, diagnostic := range diagnostics {
		out[index] = Diagnostic{Path: diagnostic.Path, Operation: "read catalog record", Err: diagnostic.Err}
	}
	return out
}

func directoryExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}
