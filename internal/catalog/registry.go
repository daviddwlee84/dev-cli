package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

var (
	ErrAmbiguous         = errors.New("ambiguous catalog match")
	ErrIncompleteCatalog = errors.New("catalog contains unreadable records")
)

// AmbiguousMatchError reports records that cannot be safely distinguished. A
// remote-only ambiguity is never resolved by picking the first entry.
type AmbiguousMatchError struct {
	Observation  Observation
	CandidateIDs []string
	Reason       string
}

func (e *AmbiguousMatchError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "multiple records match"
	}
	return fmt.Sprintf("%s: %s (%s)", ErrAmbiguous, reason, strings.Join(e.CandidateIDs, ", "))
}

func (e *AmbiguousMatchError) Unwrap() error { return ErrAmbiguous }

// Observation is live repository identity gathered from discovery. Path is the
// navigation path, RealPath its physical target, and either common-dir field may
// carry Git's shared administrative directory. CommonDir matches repo.Repo;
// GitCommonDir is accepted for callers using gitx.Repo terminology.
type Observation struct {
	Host           string
	Path           string
	RealPath       string
	CommonDir      string
	GitCommonDir   string
	Name           string
	RemoteIdentity string
}

// Registry adds live matching and lazy repository persistence to a Store.
type Registry struct {
	store *Store
	mu    sync.Mutex
}

// NewRegistry returns a registry backed by store.
func NewRegistry(store *Store) *Registry { return &Registry{store: store} }

// Store exposes the durable store for callers that need lower-level APIs.
func (r *Registry) Store() *Store {
	if r == nil {
		return nil
	}
	return r.store
}

// List returns every readable catalog entry and reports skipped records through
// the Store's configured diagnostic sink.
func (r *Registry) List() ([]*Entry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("catalog registry has no store")
	}
	return r.store.List()
}

// Get loads one entry by stable catalog ID.
func (r *Registry) Get(id string) (*Entry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("catalog registry has no store")
	}
	return r.store.Get(id)
}

// Update applies an atomic mutation to one entry.
func (r *Registry) Update(id string, mutate func(*Entry) error) (*Entry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("catalog registry has no store")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.Update(id, mutate)
}

// Match finds an observation without changing the catalog. Exact canonical path
// or common-directory identity on the same host always wins. A remote identity
// is considered only as a conservative relocation hint.
func (r *Registry) Match(observation Observation) (*Entry, error) {
	normalized, err := normalizeObservation(observation, false)
	if err != nil {
		return nil, err
	}
	entry, _, err := r.match(normalized)
	return entry, err
}

// EnsureRepository returns the matching repository, attaching the observed host
// location when safe. If nothing matches—or a remote is ambiguous because it
// may describe another live clone—it creates a distinct repository record.
func (r *Registry) EnsureRepository(observation Observation) (*Entry, error) {
	normalized, err := normalizeObservation(observation, true)
	if err != nil {
		return nil, err
	}
	if r == nil || r.store == nil {
		return nil, errors.New("catalog registry has no store")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	matched, method, matchErr := r.match(normalized)
	if matchErr == nil {
		return r.attach(matched.ID, normalized)
	}
	if !errors.Is(matchErr, ErrNotFound) && !(errors.Is(matchErr, ErrAmbiguous) && method == matchRemote) {
		return nil, matchErr
	}

	entry := &Entry{
		Kind:           KindRepository,
		Name:           normalized.Name,
		RemoteIdentity: normalized.RemoteIdentity,
		Locations: map[string]Location{
			normalized.Host: locationFromObservation(normalized),
		},
	}
	if err := r.store.Create(entry); err != nil {
		return nil, err
	}
	return entry.Clone(), nil
}

// Patch applies an atomic catalog mutation.
func (r *Registry) Patch(id string, mutate func(*Entry) error) (*Entry, error) {
	return r.Update(id, mutate)
}

// Attach explicitly records observation as id's present location. Explicit
// attachment may update a changed remote because the caller supplied the stable
// catalog identity rather than asking the remote hint to choose one.
func (r *Registry) Attach(id string, observation Observation) (*Entry, error) {
	normalized, err := normalizeObservation(observation, true)
	if err != nil {
		return nil, err
	}
	if r == nil || r.store == nil {
		return nil, errors.New("catalog registry has no store")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attach(id, normalized)
}

func (r *Registry) attach(id string, observation Observation) (*Entry, error) {
	return r.store.Update(id, func(entry *Entry) error {
		if observation.RemoteIdentity != "" {
			entry.RemoteIdentity = observation.RemoteIdentity
		}
		location := locationFromObservation(observation)
		if previous, ok := entry.LocationFor(observation.Host); ok && location.GitCommonDir == "" &&
			intersects(canonicalValues(observation.Path, observation.RealPath),
				canonicalValues(previous.CurrentPath, previous.RestorePath, previous.RealPath)) {
			location.GitCommonDir = previous.GitCommonDir
		}
		return entry.SetLocation(observation.Host, location)
	})
}

// FindByPath performs only exact host+canonical-path matching; it never falls
// back to a remote identity.
func (r *Registry) FindByPath(host, path string) (*Entry, error) {
	normalized, err := normalizeObservation(Observation{Host: host, Path: path}, false)
	if err != nil {
		return nil, err
	}
	entries, diagnostics, err := r.entriesWithDiagnostics()
	if err != nil {
		return nil, err
	}
	matches := exactMatches(entries, normalized)
	if len(matches) == 0 && len(diagnostics) > 0 {
		return nil, incompleteCatalogError(diagnostics)
	}
	if len(matches) > 0 {
		r.store.reportDiagnostics(diagnostics)
	}
	return selectExact(normalized, matches)
}

// FindByCommonDir performs exact host+Git-common-directory matching.
func (r *Registry) FindByCommonDir(host, commonDir string) (*Entry, error) {
	normalized, err := normalizeObservation(Observation{Host: host, CommonDir: commonDir}, false)
	if err != nil {
		return nil, err
	}
	entries, diagnostics, err := r.entriesWithDiagnostics()
	if err != nil {
		return nil, err
	}
	matches := exactMatches(entries, normalized)
	if len(matches) == 0 && len(diagnostics) > 0 {
		return nil, incompleteCatalogError(diagnostics)
	}
	if len(matches) > 0 {
		r.store.reportDiagnostics(diagnostics)
	}
	return selectExact(normalized, matches)
}

type matchMethod int

const (
	matchNone matchMethod = iota
	matchExact
	matchRemote
)

func (r *Registry) match(observation Observation) (*Entry, matchMethod, error) {
	entries, diagnostics, err := r.entriesWithDiagnostics()
	if err != nil {
		return nil, matchNone, err
	}
	if matches := exactMatches(entries, observation); len(matches) > 0 {
		r.store.reportDiagnostics(diagnostics)
		entry, err := selectExact(observation, matches)
		return entry, matchExact, err
	}
	if len(diagnostics) > 0 {
		return nil, matchNone, incompleteCatalogError(diagnostics)
	}
	if observation.RemoteIdentity == "" {
		return nil, matchNone, fmt.Errorf("catalog observation %q: %w", observation.Path, ErrNotFound)
	}

	var remoteMatches []*Entry
	for _, entry := range entries {
		if NormalizeRemoteIdentity(entry.RemoteIdentity) == observation.RemoteIdentity {
			remoteMatches = append(remoteMatches, entry)
		}
	}
	Sort(remoteMatches)
	switch len(remoteMatches) {
	case 0:
		return nil, matchNone, fmt.Errorf("remote %q: %w", observation.RemoteIdentity, ErrNotFound)
	case 1:
		if remoteCanRelocate(remoteMatches[0], observation) {
			return remoteMatches[0], matchRemote, nil
		}
		return nil, matchRemote, ambiguous(observation, remoteMatches,
			"remote also belongs to a different live clone on this host")
	default:
		return nil, matchRemote, ambiguous(observation, remoteMatches,
			"remote identity matches multiple catalog records")
	}
}

func (r *Registry) entriesWithDiagnostics() ([]*Entry, []Diagnostic, error) {
	if r == nil || r.store == nil {
		return nil, nil, errors.New("catalog registry has no store")
	}
	return r.store.ListWithDiagnostics()
}

func incompleteCatalogError(diagnostics []Diagnostic) error {
	errorsToJoin := make([]error, 1, len(diagnostics)+1)
	errorsToJoin[0] = ErrIncompleteCatalog
	for _, diagnostic := range diagnostics {
		errorsToJoin = append(errorsToJoin, diagnostic)
	}
	return errors.Join(errorsToJoin...)
}

func exactMatches(entries []*Entry, observation Observation) []*Entry {
	matched := make(map[string]*Entry)
	observationPaths := canonicalValues(observation.Path, observation.RealPath)
	observationCommon := canonicalValue(observation.CommonDir)
	for _, entry := range entries {
		location, ok := entry.LocationFor(observation.Host)
		if !ok {
			continue
		}
		locationPaths := canonicalValues(location.CurrentPath, location.RestorePath, location.RealPath)
		pathMatch := intersects(observationPaths, locationPaths)
		commonMatch := observationCommon != "" && observationCommon == canonicalValue(location.GitCommonDir)
		if pathMatch || commonMatch {
			matched[entry.ID] = entry
		}
	}
	out := make([]*Entry, 0, len(matched))
	for _, entry := range matched {
		out = append(out, entry)
	}
	Sort(out)
	return out
}

func selectExact(observation Observation, matches []*Entry) (*Entry, error) {
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("catalog path %q on host %q: %w", observation.Path, observation.Host, ErrNotFound)
	case 1:
		return matches[0], nil
	default:
		return nil, ambiguous(observation, matches, "canonical path or Git common directory matches multiple records")
	}
}

func ambiguous(observation Observation, entries []*Entry, reason string) error {
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	sort.Strings(ids)
	return &AmbiguousMatchError{Observation: observation, CandidateIDs: ids, Reason: reason}
}

func remoteCanRelocate(entry *Entry, observation Observation) bool {
	location, ok := entry.LocationFor(observation.Host)
	if !ok {
		return true
	}
	// Exact aliases, restore paths, and common directories were already handled.
	// A navigation symlink may disappear while its physical clone or Git common
	// directory remains, so every stored identity path must be absent before the
	// remote can be treated as relocation evidence. Archives also deliberately
	// block relocation while their bytes remain.
	for _, path := range []string{
		location.CurrentPath,
		location.RestorePath,
		location.RealPath,
		location.GitCommonDir,
	} {
		if path == "" {
			continue
		}
		if pathMayStillExist(path) {
			return false
		}
	}
	return true
}

func pathMayStillExist(path string) bool {
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		// Lstat deliberately counts a dangling symlink as existing evidence.
		return true
	}
	canonical, err := pathx.Canonical(path)
	if err != nil {
		return true
	}
	_, err = os.Stat(canonical)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

func locationFromObservation(observation Observation) Location {
	currentPath := observation.Path
	if currentPath == "" {
		currentPath = observation.RealPath
	}
	return Location{
		State:        LocationPresent,
		CurrentPath:  currentPath,
		RealPath:     observation.RealPath,
		GitCommonDir: observation.CommonDir,
	}
}

func normalizeObservation(observation Observation, requireLocation bool) (Observation, error) {
	observation.Host = strings.TrimSpace(observation.Host)
	observation.Name = strings.TrimSpace(observation.Name)
	if observation.Host == "" {
		return Observation{}, errors.New("catalog observation host is required")
	}

	commonDir := observation.CommonDir
	if commonDir == "" {
		commonDir = observation.GitCommonDir
	} else if observation.GitCommonDir != "" {
		left, leftErr := pathx.Canonical(commonDir)
		right, rightErr := pathx.Canonical(observation.GitCommonDir)
		if leftErr != nil || rightErr != nil || left != right {
			return Observation{}, errors.New("catalog observation CommonDir and GitCommonDir disagree")
		}
	}

	var err error
	canonicalPath := ""
	if observation.Path != "" {
		observation.Path, err = absoluteClean(observation.Path)
		if err != nil {
			return Observation{}, fmt.Errorf("catalog observation path: %w", err)
		}
		canonicalPath, err = pathx.Canonical(observation.Path)
		if err != nil {
			return Observation{}, fmt.Errorf("catalog observation path: %w", err)
		}
	}
	if observation.RealPath != "" {
		observation.RealPath, err = pathx.Canonical(observation.RealPath)
		if err != nil {
			return Observation{}, fmt.Errorf("catalog observation real path: %w", err)
		}
		if canonicalPath != "" && canonicalPath != observation.RealPath {
			return Observation{}, fmt.Errorf("catalog observation Path and RealPath disagree: %q != %q",
				canonicalPath, observation.RealPath)
		}
	} else {
		observation.RealPath = canonicalPath
	}
	if commonDir != "" {
		observation.CommonDir, err = pathx.Canonical(commonDir)
		if err != nil {
			return Observation{}, fmt.Errorf("catalog observation Git common directory: %w", err)
		}
	}
	observation.GitCommonDir = observation.CommonDir
	observation.RemoteIdentity = NormalizeRemoteIdentity(observation.RemoteIdentity)

	if requireLocation && observation.Path == "" && observation.RealPath == "" {
		return Observation{}, errors.New("catalog repository observation requires a path")
	}
	if requireLocation {
		path := observation.Path
		if path == "" {
			path = observation.RealPath
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return Observation{}, fmt.Errorf("catalog repository observation path %q: %w", path, statErr)
		}
		if !info.IsDir() {
			return Observation{}, fmt.Errorf("catalog repository observation path %q is not a directory", path)
		}
		if observation.CommonDir != "" {
			commonInfo, commonErr := os.Stat(observation.CommonDir)
			if commonErr != nil {
				return Observation{}, fmt.Errorf("catalog repository observation Git common directory %q: %w",
					observation.CommonDir, commonErr)
			}
			if !commonInfo.IsDir() {
				return Observation{}, fmt.Errorf("catalog repository observation Git common directory %q is not a directory",
					observation.CommonDir)
			}
		}
	}
	if observation.Name == "" {
		path := observation.Path
		if path == "" {
			path = observation.RealPath
		}
		if path != "" {
			observation.Name = filepath.Base(path)
		}
	}
	if requireLocation && (observation.Name == "" || observation.Name == ".") {
		return Observation{}, errors.New("catalog repository observation requires a name")
	}
	return observation, nil
}

func absoluteClean(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func canonicalValues(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if canonical := canonicalValue(value); canonical != "" {
			out[canonical] = struct{}{}
		}
	}
	return out
}

func canonicalValue(value string) string {
	if value == "" {
		return ""
	}
	canonical, err := pathx.Canonical(value)
	if err != nil {
		return ""
	}
	return canonical
}

func intersects(left, right map[string]struct{}) bool {
	for value := range left {
		if _, ok := right[value]; ok {
			return true
		}
	}
	return false
}

// NormalizeRemoteIdentity converts common HTTPS and SCP-style Git URLs into a
// stable host/path key. Hosts are case-insensitive, but paths are preserved
// because arbitrary Git servers may use a case-sensitive repository namespace.
// The result remains only a hint for matching.
func NormalizeRemoteIdentity(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	if azure := normalizeAzureDevOpsRemoteIdentity(identity); azure != "" {
		return azure
	}
	if parsed, err := url.Parse(identity); err == nil && parsed.Scheme != "" {
		if strings.EqualFold(parsed.Scheme, "file") {
			return ""
		}
		if parsed.Host != "" {
			return remoteIdentityKey(parsed.Host, parsed.EscapedPath())
		}
	}
	if looksLikeLocalRemote(identity) {
		return ""
	}

	afterUser := identity
	user := ""
	if at := strings.LastIndexByte(afterUser, '@'); at >= 0 {
		user, afterUser = afterUser[:at], afterUser[at+1:]
	}
	if host, remotePath, ok := splitSCPLike(afterUser); ok {
		if user != "" && !strings.EqualFold(user, "git") && !strings.HasPrefix(remotePath, "/") {
			remotePath = "~" + user + "/" + remotePath
		}
		return remoteIdentityKey(host, remotePath)
	}

	identity = strings.Trim(identity, "/")
	if host, remotePath, ok := strings.Cut(identity, "/"); ok {
		return remoteIdentityKey(host, remotePath)
	}
	return ""
}

func normalizeAzureDevOpsRemoteIdentity(identity string) string {
	if kind, name := forge.IdentityFromURL(identity); kind == forge.AzureDevOps && name != "" {
		return string(forge.AzureDevOps) + "/" + name
	}

	// Records written before Azure support already contain the generic
	// host/path form. Recognize it so existing catalog entries migrate on read.
	trimmed := strings.Trim(strings.TrimSuffix(strings.TrimSpace(identity), ".git"), "/")
	parts := strings.Split(trimmed, "/")
	decode := func(values []string) ([]string, bool) {
		out := make([]string, len(values))
		for i, value := range values {
			decoded, err := url.PathUnescape(value)
			if err != nil || decoded == "" || strings.Contains(decoded, "/") {
				return nil, false
			}
			out[i] = decoded
		}
		return out, true
	}
	if len(parts) == 5 && strings.EqualFold(parts[0], "dev.azure.com") && strings.EqualFold(parts[3], "_git") {
		if decoded, ok := decode([]string{parts[1], parts[2], parts[4]}); ok {
			return string(forge.AzureDevOps) + "/" + strings.Join(decoded, "/")
		}
	}
	if len(parts) == 5 && strings.EqualFold(parts[0], "ssh.dev.azure.com") && strings.EqualFold(parts[1], "v3") {
		if decoded, ok := decode(parts[2:]); ok {
			return string(forge.AzureDevOps) + "/" + strings.Join(decoded, "/")
		}
	}
	if len(parts) >= 4 && strings.HasSuffix(strings.ToLower(parts[0]), ".visualstudio.com") &&
		!strings.EqualFold(parts[0], "vs-ssh.visualstudio.com") && strings.EqualFold(parts[len(parts)-2], "_git") {
		organization := strings.TrimSuffix(strings.ToLower(parts[0]), ".visualstudio.com")
		if decoded, ok := decode([]string{organization, parts[len(parts)-3], parts[len(parts)-1]}); ok {
			return string(forge.AzureDevOps) + "/" + strings.Join(decoded, "/")
		}
	}
	if len(parts) == 5 && strings.EqualFold(parts[0], "vs-ssh.visualstudio.com") &&
		strings.HasPrefix(parts[1], "~") && strings.EqualFold(parts[3], "_ssh") {
		if decoded, ok := decode([]string{strings.TrimPrefix(parts[1], "~"), parts[2], parts[4]}); ok {
			return string(forge.AzureDevOps) + "/" + strings.Join(decoded, "/")
		}
	}
	return ""
}

func looksLikeLocalRemote(identity string) bool {
	if filepath.IsAbs(identity) || strings.HasPrefix(identity, `\\`) {
		return true
	}
	for _, prefix := range []string{"./", "../", "~/", `.\`, `..\`, `~\`} {
		if strings.HasPrefix(identity, prefix) {
			return true
		}
	}
	if len(identity) >= 2 && identity[1] == ':' {
		letter := identity[0]
		if letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z' {
			return true
		}
	}
	return !strings.ContainsAny(identity, `/\:@`)
}

func splitSCPLike(identity string) (string, string, bool) {
	if strings.HasPrefix(identity, "[") {
		if end := strings.Index(identity, "]:"); end >= 0 {
			return identity[:end+1], identity[end+2:], true
		}
	}
	colon := strings.IndexByte(identity, ':')
	if colon <= 0 || strings.ContainsAny(identity[:colon], `/\\`) {
		return "", "", false
	}
	return identity[:colon], identity[colon+1:], true
}

func remoteIdentityKey(host, remotePath string) string {
	host = strings.ToLower(strings.Trim(host, "/"))
	remotePath = trimGitSuffix(strings.Trim(remotePath, "/"))
	if host == "" || remotePath == "" {
		return ""
	}
	return host + "/" + remotePath
}

func trimGitSuffix(value string) string {
	return strings.TrimSuffix(value, ".git")
}
