package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

const (
	recoveryDigestPrefix        = "sha256:"
	recoveryMaxCaptureSize      = 64 << 20
	recoveryMaxAggregateCapture = 256 << 20
)

// RecoveryCompleteness says whether every fact in a recovery snapshot scope was
// conclusively observed. A detected blocker does not make evidence partial; a
// failed, malformed, or cancelled probe does.
type RecoveryCompleteness string

const (
	RecoveryCompletenessComplete RecoveryCompleteness = "complete"
	RecoveryCompletenessPartial  RecoveryCompleteness = "partial"
	RecoveryCompletenessUnknown  RecoveryCompleteness = "unknown"
)

// RecoveryFindingKind distinguishes a conclusively observed recovery blocker
// from a collection failure that makes the snapshot incomplete.
type RecoveryFindingKind string

const (
	RecoveryFindingBlocker      RecoveryFindingKind = "blocker"
	RecoveryFindingProbeFailure RecoveryFindingKind = "probe-failure"
)

// RecoveryFinding is a stable, content-free explanation of evidence that needs
// policy or operator attention. Code, Kind, Scope, and Subject are suitable for
// machine decisions; Message is diagnostic only.
type RecoveryFinding struct {
	Code     string              `json:"code"`
	Kind     RecoveryFindingKind `json:"kind"`
	Scope    string              `json:"scope"`
	Subject  string              `json:"subject"`
	Blocking bool                `json:"blocking"`
	Message  string              `json:"message,omitempty"`
}

// RecoveryRefKind classifies every ref below refs without discarding custom
// namespaces.
type RecoveryRefKind string

const (
	RecoveryRefHead   RecoveryRefKind = "head"
	RecoveryRefTag    RecoveryRefKind = "tag"
	RecoveryRefNote   RecoveryRefKind = "note"
	RecoveryRefStash  RecoveryRefKind = "stash"
	RecoveryRefRemote RecoveryRefKind = "remote"
	RecoveryRefOther  RecoveryRefKind = "other"
)

// RecoveryRef is one exact local ref and the type of the object it directly
// names. Annotated tags therefore report object type "tag", not their peeled
// target.
type RecoveryRef struct {
	Name       string          `json:"name"`
	OID        string          `json:"oid"`
	ObjectType string          `json:"object_type"`
	Kind       RecoveryRefKind `json:"kind"`
}

// RecoveryPresence avoids treating an unprobed worktree as a missing one.
type RecoveryPresence string

const (
	RecoveryPresencePresent RecoveryPresence = "present"
	RecoveryPresenceMissing RecoveryPresence = "missing"
	RecoveryPresenceUnknown RecoveryPresence = "unknown"
)

// RecoveryEntryType is the lstat-observed filesystem type. Links are never
// followed by recovery collection.
type RecoveryEntryType string

const (
	RecoveryEntryRegular    RecoveryEntryType = "regular"
	RecoveryEntryDirectory  RecoveryEntryType = "directory"
	RecoveryEntrySymlink    RecoveryEntryType = "symlink"
	RecoveryEntrySpecial    RecoveryEntryType = "special"
	RecoveryEntryMissing    RecoveryEntryType = "missing"
	RecoveryEntryUnreadable RecoveryEntryType = "unreadable"
)

// RecoveryOperation is an in-progress Git operation marker in one worktree's
// private Git directory.
type RecoveryOperation struct {
	Name string            `json:"name"`
	Type RecoveryEntryType `json:"type"`
}

// RecoveryIndexEntry preserves one exact index stage. Mode is Git's canonical
// octal mode string. Tag is the documented ls-files -t/-v classification.
type RecoveryIndexEntry struct {
	Path            string `json:"path"`
	Mode            string `json:"mode"`
	OID             string `json:"oid"`
	Stage           int    `json:"stage"`
	Tag             string `json:"tag"`
	AssumeUnchanged bool   `json:"assume_unchanged"`
	SkipWorktree    bool   `json:"skip_worktree"`
}

// RecoveryTrackedFile is held-root evidence for one materialized tracked path.
// Digest covers raw working-tree bytes and is deliberately independent of Git
// clean/smudge filters and porcelain status. Raw bytes are not public facts.
type RecoveryTrackedFile struct {
	Path         string            `json:"path"`
	Materialized bool              `json:"materialized"`
	Type         RecoveryEntryType `json:"type"`
	Size         int64             `json:"size"`
	Mode         uint32            `json:"mode"`
	Digest       string            `json:"digest,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// RecoveryDirtyPath is one tracked porcelain-v2 status record. Rename/copy
// source names remain paired rather than being mistaken for another record.
type RecoveryDirtyPath struct {
	Path           string `json:"path"`
	OldPath        string `json:"old_path,omitempty"`
	Kind           string `json:"kind"`
	IndexStatus    string `json:"index_status,omitempty"`
	WorktreeStatus string `json:"worktree_status,omitempty"`
}

// RecoveryLFSPath reports whether index and/or live working-tree attributes
// select the lfs filter for a path.
type RecoveryLFSPath struct {
	Path               string `json:"path"`
	IndexAttributes    bool   `json:"index_attributes"`
	WorktreeAttributes bool   `json:"worktree_attributes"`
}

// RecoverySubmodule is one gitlink index stage. Gitlinks are recorded even when
// the corresponding submodule checkout is absent.
type RecoverySubmodule struct {
	Path  string `json:"path"`
	OID   string `json:"oid"`
	Stage int    `json:"stage"`
}

// RecoveryNestedRepository is a non-gitlink directory containing its own .git
// marker. MarkerType is observed without following links.
type RecoveryNestedRepository struct {
	Path       string            `json:"path"`
	MarkerType RecoveryEntryType `json:"marker_type"`
}

// RecoveryWorktreeSnapshot contains evidence scoped to one registered Git
// worktree. Every collection is read-only and uses the worktree's own index.
type RecoveryWorktreeSnapshot struct {
	Path           string               `json:"path"`
	GitDir         string               `json:"git_dir,omitempty"`
	Head           string               `json:"head,omitempty"`
	Branch         string               `json:"branch,omitempty"`
	Detached       bool                 `json:"detached"`
	Bare           bool                 `json:"bare"`
	Main           bool                 `json:"main"`
	Locked         bool                 `json:"locked"`
	LockedReason   string               `json:"locked_reason,omitempty"`
	Prunable       bool                 `json:"prunable"`
	PrunableReason string               `json:"prunable_reason,omitempty"`
	Presence       RecoveryPresence     `json:"presence"`
	Completeness   RecoveryCompleteness `json:"completeness"`

	SparseCheckout bool `json:"sparse_checkout"`
	SparseCone     bool `json:"sparse_cone"`
	SparseIndex    bool `json:"sparse_index"`

	Operations         []RecoveryOperation        `json:"operations"`
	IndexEntries       []RecoveryIndexEntry       `json:"index_entries"`
	TrackedFiles       []RecoveryTrackedFile      `json:"tracked_files"`
	DirtyPaths         []RecoveryDirtyPath        `json:"dirty_paths"`
	UntrackedPaths     []string                   `json:"untracked_paths"`
	IgnoredPaths       []string                   `json:"ignored_paths"`
	LFSPaths           []RecoveryLFSPath          `json:"lfs_paths"`
	Submodules         []RecoverySubmodule        `json:"submodules"`
	NestedRepositories []RecoveryNestedRepository `json:"nested_repositories"`
}

// RecoveryAlternate identifies an external object directory and how Git learned
// about it. No object-store contents are exposed here.
type RecoveryAlternate struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

// RecoveryRepositoryFacts are repository-wide topology and storage facts.
type RecoveryRepositoryFacts struct {
	Root      string `json:"root,omitempty"`
	GitDir    string `json:"git_dir"`
	CommonDir string `json:"common_dir"`
	Bare      bool   `json:"bare"`

	Shallow           bool `json:"shallow"`
	PartialClone      bool `json:"partial_clone"`
	Promisor          bool `json:"promisor"`
	LFSConfigured     bool `json:"lfs_configured"`
	PromisorPackFiles int  `json:"promisor_pack_files"`

	GitDirOutsideRoot        bool `json:"git_dir_outside_root"`
	CommonDirOutsideMainRoot bool `json:"common_dir_outside_main_root"`
	SharedCommonDir          bool `json:"shared_common_dir"`

	PromisorRemotes []string            `json:"promisor_remotes"`
	Alternates      []RecoveryAlternate `json:"alternates"`
}

// RecoveryAdminRootKind identifies which Git administrative root was walked.
type RecoveryAdminRootKind string

const (
	RecoveryAdminGitDir    RecoveryAdminRootKind = "git-dir"
	RecoveryAdminCommonDir RecoveryAdminRootKind = "common-dir"
)

// RecoveryAdminEntry is held-root metadata for every Git administrative entry.
// Digest exists only for a stably read regular file. Error explains why bytes
// are unavailable for links, special files, or unreadable entries.
type RecoveryAdminEntry struct {
	Path   string            `json:"path"`
	Type   RecoveryEntryType `json:"type"`
	Size   int64             `json:"size"`
	Mode   uint32            `json:"mode"`
	Digest string            `json:"digest,omitempty"`
	Error  string            `json:"error,omitempty"`
}

// RecoveryAdminRoot is a complete recursive view of one private Git
// administrative root.
type RecoveryAdminRoot struct {
	Kind         RecoveryAdminRootKind `json:"kind"`
	Path         string                `json:"path"`
	Completeness RecoveryCompleteness  `json:"completeness"`
	Entries      []RecoveryAdminEntry  `json:"entries"`
}

// RecoverySnapshot is a read-only, content-free public description of ordinary
// Git state. Raw regular-file bodies are retained only in private maps and are
// available as defensive copies to the later recovery artifact builder.
type RecoverySnapshot struct {
	Completeness RecoveryCompleteness       `json:"completeness"`
	ObjectFormat string                     `json:"object_format,omitempty"`
	Repository   RecoveryRepositoryFacts    `json:"repository"`
	Refs         []RecoveryRef              `json:"refs"`
	Worktrees    []RecoveryWorktreeSnapshot `json:"worktrees"`
	AdminRoots   []RecoveryAdminRoot        `json:"admin_roots"`
	Findings     []RecoveryFinding          `json:"findings"`

	trackedBytes map[string][]byte
	adminBytes   map[string][]byte
}

// RawTrackedBytes returns a defensive copy of the raw working-tree bytes for a
// stably read materialized regular file.
func (snapshot RecoverySnapshot) RawTrackedBytes(worktreePath, relativePath string) ([]byte, bool) {
	data, ok := snapshot.trackedBytes[recoveryTrackedKey(worktreePath, relativePath)]
	if !ok {
		return nil, false
	}
	return bytes.Clone(data), true
}

// RawAdminBytes returns a defensive copy of one stably read regular Git
// administrative file.
func (snapshot RecoverySnapshot) RawAdminBytes(root RecoveryAdminRootKind, relativePath string) ([]byte, bool) {
	data, ok := snapshot.adminBytes[recoveryAdminKey(root, relativePath)]
	if !ok {
		return nil, false
	}
	return bytes.Clone(data), true
}

// HasBlockingFindings reports whether collection found either unsupported data
// topology or incomplete evidence. It is descriptive and does not itself grant
// any reclaim or eviction authorization.
func (snapshot RecoverySnapshot) HasBlockingFindings() bool {
	for _, finding := range snapshot.Findings {
		if finding.Blocking {
			return true
		}
	}
	return false
}

// RecoverySnapshotOptions may lower the aggregate private-byte capture ceiling
// for constrained callers and tests. Zero selects the compiled default.
type RecoverySnapshotOptions struct {
	MaxCaptureBytes int64
}

// RecoverySnapshotOf captures refs, registered worktrees, every worktree index
// and materialized tracked file, private Git administration, and unsupported
// storage topologies without modifying Git state. Ordinary probe failures are
// retained in Findings and produce a partial snapshot; cancellation is retained
// and also returned to the caller.
func RecoverySnapshotOf(ctx context.Context, dir string) (RecoverySnapshot, error) {
	return RecoverySnapshotWithOptions(ctx, dir, RecoverySnapshotOptions{})
}

// RecoverySnapshotWithOptions is RecoverySnapshotOf with a downward-only
// aggregate private-byte limit.
func RecoverySnapshotWithOptions(ctx context.Context, dir string, options RecoverySnapshotOptions) (RecoverySnapshot, error) {
	snapshot := RecoverySnapshot{
		Completeness: RecoveryCompletenessComplete,
		Refs:         []RecoveryRef{},
		Worktrees:    []RecoveryWorktreeSnapshot{},
		AdminRoots:   []RecoveryAdminRoot{},
		Findings:     []RecoveryFinding{},
		trackedBytes: make(map[string][]byte),
		adminBytes:   make(map[string][]byte),
	}
	maxCapture := options.MaxCaptureBytes
	if maxCapture == 0 {
		maxCapture = recoveryMaxAggregateCapture
	}
	if maxCapture < 0 || maxCapture > recoveryMaxAggregateCapture {
		snapshot.Completeness = RecoveryCompletenessUnknown
		return snapshot, fmt.Errorf("recovery capture limit must be between 1 and %d bytes", recoveryMaxAggregateCapture)
	}
	collector := recoveryCollector{ctx: ctx, snapshot: &snapshot, maxCaptureBytes: maxCapture}
	if err := collector.contextError("repository", dir); err != nil {
		collector.finalize()
		return snapshot, err
	}

	repository, err := Discover(ctx, dir)
	if err != nil {
		if cancelErr := collector.failure("repository-probe-failed", "repository", dir, err, nil); cancelErr != nil {
			collector.finalize()
			return snapshot, cancelErr
		}
		snapshot.Completeness = RecoveryCompletenessUnknown
		collector.finalize()
		return snapshot, err
	}
	snapshot.Repository = RecoveryRepositoryFacts{
		Root:            repository.Root,
		GitDir:          filepath.Clean(repository.GitDir),
		CommonDir:       filepath.Clean(repository.GitCommonDir),
		Bare:            repository.Bare,
		PromisorRemotes: []string{},
		Alternates:      []RecoveryAlternate{},
	}

	if err := collector.collectObjectFormat(repository); err != nil {
		collector.finalize()
		return snapshot, err
	}
	if err := collector.collectRefs(repository); err != nil {
		collector.finalize()
		return snapshot, err
	}
	if err := collector.collectRepositoryConfig(repository); err != nil {
		collector.finalize()
		return snapshot, err
	}
	if err := collector.collectShallow(repository); err != nil {
		collector.finalize()
		return snapshot, err
	}
	if err := collector.collectWorktreeList(repository); err != nil {
		collector.finalize()
		return snapshot, err
	}
	collector.collectStoragePlacement(repository)

	for index := range snapshot.Worktrees {
		if err := collector.collectWorktree(repository, &snapshot.Worktrees[index]); err != nil {
			collector.finalize()
			return snapshot, err
		}
	}
	if err := collector.collectAdminRoots(repository); err != nil {
		collector.finalize()
		return snapshot, err
	}
	collector.collectObjectStoreFeatures(repository)
	collector.finalize()
	return snapshot, nil
}

type recoveryCollector struct {
	ctx             context.Context
	snapshot        *RecoverySnapshot
	cancelRecorded  bool
	maxCaptureBytes int64
	capturedBytes   int64
}

func (collector *recoveryCollector) contextError(scope, subject string) error {
	if cause := context.Cause(collector.ctx); cause != nil {
		if !collector.cancelRecorded {
			collector.cancelRecorded = true
			collector.addFinding(RecoveryFinding{
				Code: "collection-cancelled", Kind: RecoveryFindingProbeFailure,
				Scope: scope, Subject: subject, Blocking: true, Message: cause.Error(),
			})
		}
		collector.markPartial(nil)
		return cause
	}
	return nil
}

func (collector *recoveryCollector) failure(code, scope, subject string, err error, local *RecoveryCompleteness) error {
	if cause := context.Cause(collector.ctx); cause != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if cause == nil {
			cause = err
		}
		if !collector.cancelRecorded {
			collector.cancelRecorded = true
			collector.addFinding(RecoveryFinding{
				Code: "collection-cancelled", Kind: RecoveryFindingProbeFailure,
				Scope: scope, Subject: subject, Blocking: true, Message: cause.Error(),
			})
		}
		collector.markPartial(local)
		return cause
	}
	collector.markPartial(local)
	collector.addFinding(RecoveryFinding{
		Code: code, Kind: RecoveryFindingProbeFailure, Scope: scope,
		Subject: subject, Blocking: true, Message: recoveryCleanText(err.Error()),
	})
	return nil
}

func (collector *recoveryCollector) blocker(code, scope, subject, message string) {
	collector.addFinding(RecoveryFinding{
		Code: code, Kind: RecoveryFindingBlocker, Scope: scope,
		Subject: subject, Blocking: true, Message: message,
	})
}

func (collector *recoveryCollector) addFinding(finding RecoveryFinding) {
	finding.Code = recoveryIdentifier(finding.Code)
	finding.Scope = recoveryIdentifier(finding.Scope)
	finding.Subject = recoveryCleanSubject(finding.Subject)
	finding.Message = recoveryCleanText(finding.Message)
	collector.snapshot.Findings = append(collector.snapshot.Findings, finding)
}

func (collector *recoveryCollector) markPartial(local *RecoveryCompleteness) {
	if collector.snapshot.Completeness != RecoveryCompletenessUnknown {
		collector.snapshot.Completeness = RecoveryCompletenessPartial
	}
	if local != nil && *local != RecoveryCompletenessUnknown {
		*local = RecoveryCompletenessPartial
	}
}

func (collector *recoveryCollector) reserveCapture(size int64) bool {
	if size < 0 || size > collector.maxCaptureBytes-collector.capturedBytes {
		return false
	}
	collector.capturedBytes += size
	return true
}

func (collector *recoveryCollector) releaseCapture(size int64) {
	if size > 0 && size <= collector.capturedBytes {
		collector.capturedBytes -= size
	}
}

func (collector *recoveryCollector) collectObjectFormat(repository Repo) error {
	output, err := recoveryGit(collector.ctx, repository.GitCommonDir, nil,
		"rev-parse", "--show-object-format=storage")
	if err != nil {
		if cancelErr := collector.failure("object-format-probe-failed", "object-format", "repository", err, nil); cancelErr != nil {
			return cancelErr
		}
	}
	format := strings.TrimSpace(string(output))
	if format == "" {
		if err == nil {
			_ = collector.failure("object-format-parse-failed", "object-format", "repository", errors.New("Git returned an empty object format"), nil)
		}
		return nil
	}
	if strings.ContainsAny(format, "\x00\r\n \t") {
		_ = collector.failure("object-format-parse-failed", "object-format", "repository", fmt.Errorf("unexpected object format %q", format), nil)
		return nil
	}
	collector.snapshot.ObjectFormat = format
	return nil
}

func (collector *recoveryCollector) collectRefs(repository Repo) error {
	output, err := recoveryGit(collector.ctx, repository.GitCommonDir, nil,
		"for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00%(objecttype)%00", "refs")
	if err != nil {
		if cancelErr := collector.failure("refs-probe-failed", "refs", "repository", err, nil); cancelErr != nil {
			return cancelErr
		}
	}
	refs, parseErr := parseRecoveryRefs(output)
	collector.snapshot.Refs = refs
	if parseErr != nil {
		_ = collector.failure("refs-parse-failed", "refs", "repository", parseErr, nil)
	}
	return nil
}

func parseRecoveryRefs(output []byte) ([]RecoveryRef, error) {
	refs := []RecoveryRef{}
	var parseErrors []error
	for recordIndex, record := range bytes.Split(output, []byte{'\n'}) {
		record = bytes.TrimSuffix(record, []byte{'\r'})
		if len(record) == 0 {
			continue
		}
		fields := bytes.Split(record, []byte{0})
		if len(fields) == 4 && len(fields[3]) == 0 {
			fields = fields[:3]
		}
		if len(fields) != 3 {
			parseErrors = append(parseErrors, fmt.Errorf("ref record %d has %d fields", recordIndex, len(fields)))
			continue
		}
		name, oid, objectType := string(fields[0]), string(fields[1]), string(fields[2])
		if !utf8.ValidString(name) || !strings.HasPrefix(name, "refs/") || oid == "" || objectType == "" {
			parseErrors = append(parseErrors, fmt.Errorf("ref record %d is invalid", recordIndex))
			continue
		}
		refs = append(refs, RecoveryRef{Name: name, OID: oid, ObjectType: objectType, Kind: recoveryRefKind(name)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, errors.Join(parseErrors...)
}

func recoveryRefKind(name string) RecoveryRefKind {
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		return RecoveryRefHead
	case strings.HasPrefix(name, "refs/tags/"):
		return RecoveryRefTag
	case strings.HasPrefix(name, "refs/notes/"):
		return RecoveryRefNote
	case name == "refs/stash":
		return RecoveryRefStash
	case strings.HasPrefix(name, "refs/remotes/"):
		return RecoveryRefRemote
	default:
		return RecoveryRefOther
	}
}

func (collector *recoveryCollector) collectRepositoryConfig(repository Repo) error {
	output, err := recoveryGit(collector.ctx, repository.GitCommonDir, nil,
		"config", "--null", "--local", "--list")
	if err != nil {
		if cancelErr := collector.failure("config-probe-failed", "repository-config", "repository", err, nil); cancelErr != nil {
			return cancelErr
		}
	}
	var parseErrors []error
	promisorRemotes := map[string]struct{}{}
	for recordIndex, record := range recoveryNULRecords(output) {
		newline := bytes.IndexByte(record, '\n')
		if newline < 1 {
			parseErrors = append(parseErrors, fmt.Errorf("config record %d has no key/value separator", recordIndex))
			continue
		}
		key := strings.ToLower(string(record[:newline]))
		value := string(record[newline+1:])
		switch {
		case key == "extensions.partialclone":
			if strings.TrimSpace(value) != "" {
				collector.snapshot.Repository.PartialClone = true
				promisorRemotes[strings.TrimSpace(value)] = struct{}{}
			}
		case strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".promisor"):
			remote := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".promisor")
			parsed, boolErr := recoveryGitBool(value)
			if boolErr != nil {
				parseErrors = append(parseErrors, fmt.Errorf("config %s: %w", key, boolErr))
				continue
			}
			if parsed {
				collector.snapshot.Repository.Promisor = true
				collector.snapshot.Repository.PartialClone = true
				promisorRemotes[remote] = struct{}{}
			}
		case strings.HasPrefix(key, "remote.") && strings.HasSuffix(key, ".partialclonefilter"):
			remote := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".partialclonefilter")
			collector.snapshot.Repository.PartialClone = true
			promisorRemotes[remote] = struct{}{}
		case strings.HasPrefix(key, "filter.lfs."):
			collector.snapshot.Repository.LFSConfigured = true
		}
	}
	for remote := range promisorRemotes {
		if remote != "" {
			collector.snapshot.Repository.PromisorRemotes = append(collector.snapshot.Repository.PromisorRemotes, remote)
		}
	}
	sort.Strings(collector.snapshot.Repository.PromisorRemotes)
	if parseErr := errors.Join(parseErrors...); parseErr != nil {
		_ = collector.failure("config-parse-failed", "repository-config", "repository", parseErr, nil)
	}
	return nil
}

func recoveryGitBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid Git boolean %q", value)
	}
}

func (collector *recoveryCollector) collectShallow(repository Repo) error {
	output, err := recoveryGit(collector.ctx, repository.GitCommonDir, nil,
		"rev-parse", "--is-shallow-repository")
	if err != nil {
		if cancelErr := collector.failure("shallow-probe-failed", "object-store", "repository", err, nil); cancelErr != nil {
			return cancelErr
		}
		return nil
	}
	shallow, parseErr := strconv.ParseBool(strings.TrimSpace(string(output)))
	if parseErr != nil {
		_ = collector.failure("shallow-parse-failed", "object-store", "repository", parseErr, nil)
		return nil
	}
	collector.snapshot.Repository.Shallow = shallow
	if shallow {
		collector.blocker("shallow-repository", "object-store", "repository", "repository history is shallow")
	}
	return nil
}

func (collector *recoveryCollector) collectWorktreeList(repository Repo) error {
	output, err := recoveryGit(collector.ctx, repository.GitCommonDir, nil,
		"worktree", "list", "--porcelain", "-z")
	if err != nil {
		if cancelErr := collector.failure("worktrees-probe-failed", "worktrees", "repository", err, nil); cancelErr != nil {
			return cancelErr
		}
	}
	worktrees, parseErr := parseRecoveryWorktrees(output)
	if parseErr != nil {
		_ = collector.failure("worktrees-parse-failed", "worktrees", "repository", parseErr, nil)
	}
	if len(worktrees) == 0 && repository.Root != "" {
		worktrees = append(worktrees, RecoveryWorktreeSnapshot{
			Path: repository.Root, GitDir: repository.GitDir, Bare: repository.Bare,
			Main: true, Presence: RecoveryPresenceUnknown, Completeness: RecoveryCompletenessPartial,
		})
		if err == nil && parseErr == nil {
			_ = collector.failure("worktrees-parse-failed", "worktrees", "repository", errors.New("Git returned no worktrees"), &worktrees[0].Completeness)
		}
	}
	collector.snapshot.Worktrees = worktrees
	return nil
}

func parseRecoveryWorktrees(output []byte) ([]RecoveryWorktreeSnapshot, error) {
	worktrees := []RecoveryWorktreeSnapshot{}
	var current *RecoveryWorktreeSnapshot
	var parseErrors []error
	flush := func() {
		if current == nil {
			return
		}
		if current.Path == "" {
			parseErrors = append(parseErrors, errors.New("worktree record has no path"))
		} else {
			current.Main = len(worktrees) == 0
			worktrees = append(worktrees, *current)
		}
		current = nil
	}
	for fieldIndex, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			flush()
			continue
		}
		keyBytes, valueBytes, hasValue := bytes.Cut(raw, []byte{' '})
		key, value := string(keyBytes), ""
		if hasValue {
			value = string(valueBytes)
		}
		if key == "worktree" {
			flush()
			current = &RecoveryWorktreeSnapshot{
				Path: value, Presence: RecoveryPresenceUnknown, Completeness: RecoveryCompletenessComplete,
				Operations: []RecoveryOperation{}, IndexEntries: []RecoveryIndexEntry{},
				TrackedFiles: []RecoveryTrackedFile{}, DirtyPaths: []RecoveryDirtyPath{},
				UntrackedPaths: []string{}, IgnoredPaths: []string{}, LFSPaths: []RecoveryLFSPath{},
				Submodules: []RecoverySubmodule{}, NestedRepositories: []RecoveryNestedRepository{},
			}
			continue
		}
		if current == nil {
			parseErrors = append(parseErrors, fmt.Errorf("worktree field %d precedes a worktree path", fieldIndex))
			continue
		}
		switch key {
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked, current.LockedReason = true, value
		case "prunable":
			current.Prunable, current.PrunableReason = true, value
		}
	}
	flush()
	return worktrees, errors.Join(parseErrors...)
}

func (collector *recoveryCollector) collectWorktree(repository Repo, worktree *RecoveryWorktreeSnapshot) error {
	if worktree == nil {
		return nil
	}
	if err := collector.contextError("worktree", worktree.Path); err != nil {
		return err
	}
	info, err := os.Lstat(worktree.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		worktree.Presence = RecoveryPresenceMissing
		collector.blocker("worktree-missing", "worktree", worktree.Path, "registered worktree root is missing")
		return nil
	case err != nil:
		worktree.Presence = RecoveryPresenceUnknown
		return collector.failure("worktree-root-probe-failed", "worktree", worktree.Path, err, &worktree.Completeness)
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		worktree.Presence = RecoveryPresencePresent
		collector.markPartial(&worktree.Completeness)
		collector.blocker("unsafe-worktree-root", "worktree", worktree.Path, "registered worktree root is not a stable directory")
		return nil
	default:
		worktree.Presence = RecoveryPresencePresent
	}
	if worktree.Locked {
		collector.blocker("worktree-locked", "worktree", worktree.Path, "registered worktree is locked")
	}
	if worktree.Prunable {
		collector.blocker("worktree-prunable", "worktree", worktree.Path, "registered worktree is prunable")
	}

	gitDirOutput, err := recoveryGit(collector.ctx, worktree.Path, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		if cancelErr := collector.failure("worktree-git-dir-probe-failed", "worktree", worktree.Path, err, &worktree.Completeness); cancelErr != nil {
			return cancelErr
		}
	} else {
		worktree.GitDir = filepath.Clean(strings.TrimSpace(string(gitDirOutput)))
		if worktree.GitDir == "." || !filepath.IsAbs(worktree.GitDir) {
			_ = collector.failure("worktree-git-dir-parse-failed", "worktree", worktree.Path,
				errors.New("Git returned a non-absolute worktree git directory"), &worktree.Completeness)
			worktree.GitDir = ""
		}
	}
	if worktree.GitDir == "" {
		worktree.GitDir = filepath.Clean(repository.GitDir)
	}
	if err := collector.collectOperations(worktree); err != nil {
		return err
	}
	if worktree.Bare || repository.Bare {
		return nil
	}
	if err := collector.collectSparseConfig(worktree); err != nil {
		return err
	}
	if err := collector.collectIndex(worktree); err != nil {
		return err
	}
	submodules := collector.collectSubmodules(worktree)
	if err := collector.collectStatus(worktree); err != nil {
		return err
	}
	if err := collector.collectIgnored(worktree); err != nil {
		return err
	}
	if err := collector.collectTrackedFiles(worktree); err != nil {
		return err
	}
	if err := collector.collectLFS(worktree); err != nil {
		return err
	}
	return collector.collectNestedRepositories(worktree, submodules)
}

func (collector *recoveryCollector) collectOperations(worktree *RecoveryWorktreeSnapshot) error {
	root, rootInfo, err := safefile.OpenRoot(worktree.GitDir)
	if err != nil {
		return collector.failure("worktree-git-dir-open-failed", "git-operation", worktree.Path, err, &worktree.Completeness)
	}
	defer root.Close()
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		info, statErr := root.Lstat(name)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			if cancelErr := collector.failure("git-operation-probe-failed", "git-operation", worktree.Path+":"+name, statErr, &worktree.Completeness); cancelErr != nil {
				return cancelErr
			}
			continue
		}
		worktree.Operations = append(worktree.Operations, RecoveryOperation{Name: name, Type: recoveryEntryType(info)})
		collector.blocker("git-operation-active", "git-operation", worktree.Path+":"+name, "Git operation marker is present")
	}
	if err := safefile.VerifyRoot(worktree.GitDir, rootInfo); err != nil {
		return collector.failure("worktree-git-dir-changed", "git-operation", worktree.Path, err, &worktree.Completeness)
	}
	return nil
}

func (collector *recoveryCollector) collectTrackedFiles(worktree *RecoveryWorktreeSnapshot) error {
	root, rootInfo, err := safefile.OpenRoot(worktree.Path)
	if err != nil {
		return collector.failure("worktree-root-open-failed", "tracked-files", worktree.Path, err, &worktree.Completeness)
	}
	defer root.Close()
	seen := map[string]struct{}{}
	for _, indexEntry := range worktree.IndexEntries {
		if _, exists := seen[indexEntry.Path]; exists {
			continue
		}
		seen[indexEntry.Path] = struct{}{}
		if indexEntry.Mode == "160000" || indexEntry.Mode == "040000" {
			continue
		}
		tracked := RecoveryTrackedFile{Path: indexEntry.Path, Type: RecoveryEntryMissing}
		info, statErr := recoveryLstatRelative(root, indexEntry.Path)
		if errors.Is(statErr, fs.ErrNotExist) {
			worktree.TrackedFiles = append(worktree.TrackedFiles, tracked)
			continue
		}
		if statErr != nil {
			tracked.Type = RecoveryEntryUnreadable
			tracked.Error = recoveryCleanText(statErr.Error())
			worktree.TrackedFiles = append(worktree.TrackedFiles, tracked)
			if cancelErr := collector.failure("tracked-path-probe-failed", "tracked-file", worktree.Path+":"+indexEntry.Path, statErr, &worktree.Completeness); cancelErr != nil {
				return cancelErr
			}
			continue
		}
		tracked.Materialized = true
		tracked.Type = recoveryEntryType(info)
		tracked.Size = info.Size()
		tracked.Mode = uint32(info.Mode())
		if tracked.Type != RecoveryEntryRegular {
			tracked.Error = "materialized tracked path is not a regular file"
			worktree.TrackedFiles = append(worktree.TrackedFiles, tracked)
			collector.blocker("tracked-path-unsupported-type", "tracked-file", worktree.Path+":"+indexEntry.Path, tracked.Error)
			continue
		}
		if !collector.reserveCapture(info.Size()) {
			tracked.Type = RecoveryEntryUnreadable
			tracked.Error = "aggregate recovery capture limit exceeded"
			worktree.TrackedFiles = append(worktree.TrackedFiles, tracked)
			_ = collector.failure("capture-limit-exceeded", "tracked-file", worktree.Path+":"+indexEntry.Path,
				errors.New(tracked.Error), &worktree.Completeness)
			continue
		}
		data, readInfo, readErr := recoveryReadRelative(collector.ctx, root, indexEntry.Path, info, recoveryMaxCaptureSize)
		if readErr != nil {
			collector.releaseCapture(info.Size())
			tracked.Type = RecoveryEntryUnreadable
			tracked.Error = recoveryCleanText(readErr.Error())
			worktree.TrackedFiles = append(worktree.TrackedFiles, tracked)
			if cancelErr := collector.failure("tracked-file-read-failed", "tracked-file", worktree.Path+":"+indexEntry.Path, readErr, &worktree.Completeness); cancelErr != nil {
				return cancelErr
			}
			continue
		}
		tracked.Size = readInfo.Size()
		tracked.Mode = uint32(readInfo.Mode())
		tracked.Digest = recoveryDigest(data)
		collector.snapshot.trackedBytes[recoveryTrackedKey(worktree.Path, indexEntry.Path)] = bytes.Clone(data)
		worktree.TrackedFiles = append(worktree.TrackedFiles, tracked)
	}
	if err := safefile.VerifyRoot(worktree.Path, rootInfo); err != nil {
		return collector.failure("worktree-root-changed", "tracked-files", worktree.Path, err, &worktree.Completeness)
	}
	return nil
}

func (collector *recoveryCollector) collectNestedRepositories(worktree *RecoveryWorktreeSnapshot, submodules map[string]struct{}) error {
	root, rootInfo, err := safefile.OpenRoot(worktree.Path)
	if err != nil {
		return collector.failure("worktree-root-open-failed", "nested-repositories", worktree.Path, err, &worktree.Completeness)
	}
	defer root.Close()
	walkErr := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := collector.contextError("nested-repositories", worktree.Path); err != nil {
			return err
		}
		if walkErr != nil {
			_ = collector.failure("nested-repository-walk-failed", "nested-repositories", worktree.Path+":"+path, walkErr, &worktree.Completeness)
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == "." {
			return nil
		}
		relative := filepath.ToSlash(path)
		if relative == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() != ".git" {
			return nil
		}
		parent := filepath.ToSlash(filepath.Dir(relative))
		if _, isSubmodule := submodules[parent]; isSubmodule {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// WalkDir never follows symlink entries. The already-walked parent chain is
		// therefore confined, while Root.Lstat lets us inspect the reserved .git
		// marker name that portable payload validation intentionally rejects.
		info, err := root.Lstat(relative)
		if err != nil {
			_ = collector.failure("nested-repository-marker-probe-failed", "nested-repositories", worktree.Path+":"+parent, err, &worktree.Completeness)
			return nil
		}
		worktree.NestedRepositories = append(worktree.NestedRepositories, RecoveryNestedRepository{
			Path: parent, MarkerType: recoveryEntryType(info),
		})
		collector.blocker("nested-repository", "nested-repositories", worktree.Path+":"+parent, "nested repository requires independent recovery proof")
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if walkErr != nil {
		if cancelErr := collector.failure("nested-repository-walk-failed", "nested-repositories", worktree.Path, walkErr, &worktree.Completeness); cancelErr != nil {
			return cancelErr
		}
	}
	if err := safefile.VerifyRoot(worktree.Path, rootInfo); err != nil {
		return collector.failure("worktree-root-changed", "nested-repositories", worktree.Path, err, &worktree.Completeness)
	}
	return nil
}

func (collector *recoveryCollector) collectAdminRoots(repository Repo) error {
	roots := []struct {
		kind RecoveryAdminRootKind
		path string
	}{{RecoveryAdminCommonDir, repository.GitCommonDir}}
	if filepath.Clean(repository.GitDir) != filepath.Clean(repository.GitCommonDir) {
		roots = append(roots, struct {
			kind RecoveryAdminRootKind
			path string
		}{RecoveryAdminGitDir, repository.GitDir})
	}
	for _, candidate := range roots {
		if err := collector.collectAdminRoot(candidate.kind, filepath.Clean(candidate.path)); err != nil {
			return err
		}
	}
	return nil
}

func (collector *recoveryCollector) collectAdminRoot(kind RecoveryAdminRootKind, path string) error {
	result := RecoveryAdminRoot{Kind: kind, Path: path, Completeness: RecoveryCompletenessComplete, Entries: []RecoveryAdminEntry{}}
	root, rootInfo, err := safefile.OpenRoot(path)
	if err != nil {
		if cancelErr := collector.failure("admin-root-open-failed", "git-admin", string(kind), err, &result.Completeness); cancelErr != nil {
			collector.snapshot.AdminRoots = append(collector.snapshot.AdminRoots, result)
			return cancelErr
		}
		collector.snapshot.AdminRoots = append(collector.snapshot.AdminRoots, result)
		return nil
	}
	defer root.Close()
	walkErr := fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if err := collector.contextError("git-admin", path); err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		admin := RecoveryAdminEntry{Path: relative, Type: RecoveryEntryUnreadable}
		if walkErr != nil {
			admin.Error = recoveryCleanText(walkErr.Error())
			result.Entries = append(result.Entries, admin)
			_ = collector.failure("admin-entry-walk-failed", "git-admin", string(kind)+":"+relative, walkErr, &result.Completeness)
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, statErr := recoveryLstatRelative(root, relative)
		if statErr != nil {
			admin.Error = recoveryCleanText(statErr.Error())
			result.Entries = append(result.Entries, admin)
			_ = collector.failure("admin-entry-probe-failed", "git-admin", string(kind)+":"+relative, statErr, &result.Completeness)
			return nil
		}
		admin.Type = recoveryEntryType(info)
		admin.Size = info.Size()
		admin.Mode = uint32(info.Mode())
		if admin.Type == RecoveryEntryRegular {
			if !collector.reserveCapture(info.Size()) {
				admin.Type = RecoveryEntryUnreadable
				admin.Error = "aggregate recovery capture limit exceeded"
				_ = collector.failure("capture-limit-exceeded", "git-admin", string(kind)+":"+relative,
					errors.New(admin.Error), &result.Completeness)
			} else {
				data, readInfo, readErr := recoveryReadRelative(collector.ctx, root, relative, info, recoveryMaxCaptureSize)
				if readErr != nil {
					collector.releaseCapture(info.Size())
					admin.Type = RecoveryEntryUnreadable
					admin.Error = recoveryCleanText(readErr.Error())
					_ = collector.failure("admin-entry-read-failed", "git-admin", string(kind)+":"+relative, readErr, &result.Completeness)
				} else {
					admin.Size = readInfo.Size()
					admin.Mode = uint32(readInfo.Mode())
					admin.Digest = recoveryDigest(data)
					collector.snapshot.adminBytes[recoveryAdminKey(kind, relative)] = bytes.Clone(data)
				}
			}
		} else if admin.Type == RecoveryEntrySymlink || admin.Type == RecoveryEntrySpecial {
			admin.Error = "Git administrative entry has an unsupported filesystem type"
			collector.blocker("admin-entry-unsupported-type", "git-admin", string(kind)+":"+relative, admin.Error)
		}
		result.Entries = append(result.Entries, admin)
		return nil
	})
	if walkErr != nil {
		if cancelErr := collector.failure("admin-root-walk-failed", "git-admin", string(kind), walkErr, &result.Completeness); cancelErr != nil {
			collector.snapshot.AdminRoots = append(collector.snapshot.AdminRoots, result)
			return cancelErr
		}
	}
	if err := safefile.VerifyRoot(path, rootInfo); err != nil {
		_ = collector.failure("admin-root-changed", "git-admin", string(kind), err, &result.Completeness)
	}
	collector.snapshot.AdminRoots = append(collector.snapshot.AdminRoots, result)
	return nil
}

type recoveryHeldRoot struct {
	parent *os.Root
	name   string
	info   fs.FileInfo
	root   *os.Root
}

func recoveryOpenParent(root *os.Root, relative string) (*os.Root, []recoveryHeldRoot, string, error) {
	relative = filepath.ToSlash(relative)
	if err := pathx.ValidatePortableSlashPath(relative, safefile.DefaultLimits().PathLimits()); err != nil {
		return nil, nil, "", err
	}
	parts := strings.Split(relative, "/")
	current := root
	held := make([]recoveryHeldRoot, 0, len(parts)-1)
	for _, component := range parts[:len(parts)-1] {
		child, info, err := safefile.OpenChildRoot(current, component)
		if err != nil {
			recoveryCloseHeld(held)
			return nil, nil, "", err
		}
		held = append(held, recoveryHeldRoot{parent: current, name: component, info: info, root: child})
		current = child
	}
	return current, held, parts[len(parts)-1], nil
}

func recoveryCloseHeld(held []recoveryHeldRoot) error {
	var result error
	for index := len(held) - 1; index >= 0; index-- {
		result = errors.Join(result, safefile.VerifyChildRoot(held[index].parent, held[index].name, held[index].info))
		result = errors.Join(result, held[index].root.Close())
	}
	return result
}

func recoveryLstatRelative(root *os.Root, relative string) (fs.FileInfo, error) {
	parent, held, name, err := recoveryOpenParent(root, relative)
	if err != nil {
		return nil, err
	}
	info, statErr := parent.Lstat(name)
	closeErr := recoveryCloseHeld(held)
	return info, errors.Join(statErr, closeErr)
}

func recoveryReadRelative(ctx context.Context, root *os.Root, relative string, expected fs.FileInfo, maxBytes int64) ([]byte, fs.FileInfo, error) {
	parent, held, name, err := recoveryOpenParent(root, relative)
	if err != nil {
		return nil, nil, err
	}
	data, info, readErr := safefile.ReadStableRegular(ctx, parent, name, expected, maxBytes)
	closeErr := recoveryCloseHeld(held)
	return data, info, errors.Join(readErr, closeErr)
}

func recoveryEntryType(info fs.FileInfo) RecoveryEntryType {
	if info == nil {
		return RecoveryEntryUnreadable
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return RecoveryEntrySymlink
	case info.IsDir():
		return RecoveryEntryDirectory
	case info.Mode().IsRegular():
		return RecoveryEntryRegular
	default:
		return RecoveryEntrySpecial
	}
}

func (collector *recoveryCollector) collectStoragePlacement(repository Repo) {
	facts := &collector.snapshot.Repository
	if repository.Root != "" {
		inside, err := pathx.Contains(repository.Root, repository.GitDir)
		if err != nil {
			_ = collector.failure("git-dir-placement-probe-failed", "storage", "git-dir", err, nil)
		} else {
			facts.GitDirOutsideRoot = !inside
		}
	}
	mainRoot := repository.Root
	for _, worktree := range collector.snapshot.Worktrees {
		if worktree.Main && worktree.Path != "" {
			mainRoot = worktree.Path
			break
		}
	}
	if mainRoot != "" {
		inside, err := pathx.Contains(mainRoot, repository.GitCommonDir)
		if err != nil {
			_ = collector.failure("common-dir-placement-probe-failed", "storage", "common-dir", err, nil)
		} else {
			facts.CommonDirOutsideMainRoot = !inside
			if !inside {
				collector.blocker("external-common-directory", "storage", "common-dir", "Git common directory is outside the main worktree root")
			}
		}
	}
	facts.SharedCommonDir = repository.GitDir != repository.GitCommonDir || len(collector.snapshot.Worktrees) > 1
}

func (collector *recoveryCollector) collectIndex(worktree *RecoveryWorktreeSnapshot) error {
	output, err := recoveryGit(collector.ctx, worktree.Path, nil,
		"ls-files", "--stage", "-v", "-z", "--sparse", "--full-name")
	if err != nil {
		if cancelErr := collector.failure("index-probe-failed", "index", worktree.Path, err, &worktree.Completeness); cancelErr != nil {
			return cancelErr
		}
	}
	entries, parseErr := parseRecoveryIndex(output)
	worktree.IndexEntries = entries
	if parseErr != nil {
		_ = collector.failure("index-parse-failed", "index", worktree.Path, parseErr, &worktree.Completeness)
	}
	for _, entry := range entries {
		if entry.Mode == "040000" {
			worktree.SparseIndex = true
		}
	}
	return nil
}

func parseRecoveryIndex(output []byte) ([]RecoveryIndexEntry, error) {
	entries := []RecoveryIndexEntry{}
	var parseErrors []error
	for recordIndex, record := range recoveryNULRecords(output) {
		header, pathBytes, ok := bytes.Cut(record, []byte{'\t'})
		if !ok || len(pathBytes) == 0 || !utf8.Valid(pathBytes) {
			parseErrors = append(parseErrors, fmt.Errorf("index record %d has an invalid path field", recordIndex))
			continue
		}
		fields := strings.Fields(string(header))
		if len(fields) != 4 || len(fields[0]) != 1 {
			parseErrors = append(parseErrors, fmt.Errorf("index record %d has an invalid header", recordIndex))
			continue
		}
		if _, modeErr := strconv.ParseUint(fields[1], 8, 32); modeErr != nil {
			parseErrors = append(parseErrors, fmt.Errorf("index record %d mode %q: %w", recordIndex, fields[1], modeErr))
			continue
		}
		stage, stageErr := strconv.Atoi(fields[3])
		if stageErr != nil || stage < 0 || stage > 3 {
			parseErrors = append(parseErrors, fmt.Errorf("index record %d has invalid stage %q", recordIndex, fields[3]))
			continue
		}
		tag := fields[0][0]
		upper := tag
		if upper >= 'a' && upper <= 'z' {
			upper -= 'a' - 'A'
		}
		entries = append(entries, RecoveryIndexEntry{
			Path: string(pathBytes), Mode: fields[1], OID: fields[2], Stage: stage, Tag: fields[0],
			AssumeUnchanged: tag >= 'a' && tag <= 'z', SkipWorktree: upper == 'S',
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		if entries[i].Stage != entries[j].Stage {
			return entries[i].Stage < entries[j].Stage
		}
		return entries[i].Mode < entries[j].Mode
	})
	return entries, errors.Join(parseErrors...)
}

func (collector *recoveryCollector) collectSparseConfig(worktree *RecoveryWorktreeSnapshot) error {
	probes := []struct {
		key    string
		target *bool
	}{
		{"core.sparseCheckout", &worktree.SparseCheckout},
		{"core.sparseCheckoutCone", &worktree.SparseCone},
		{"index.sparse", &worktree.SparseIndex},
	}
	for _, probe := range probes {
		output, err := recoveryGit(collector.ctx, worktree.Path, nil,
			"config", "--bool", "--default=false", "--get", probe.key)
		if err != nil {
			if cancelErr := collector.failure("sparse-config-probe-failed", "sparse", worktree.Path+":"+probe.key, err, &worktree.Completeness); cancelErr != nil {
				return cancelErr
			}
			continue
		}
		value, boolErr := recoveryGitBool(string(output))
		if boolErr != nil {
			_ = collector.failure("sparse-config-parse-failed", "sparse", worktree.Path+":"+probe.key, boolErr, &worktree.Completeness)
			continue
		}
		*probe.target = *probe.target || value
	}
	if worktree.SparseCheckout {
		collector.blocker("sparse-checkout", "sparse", worktree.Path, "worktree uses sparse checkout")
	}
	if worktree.SparseIndex {
		collector.blocker("sparse-index", "sparse", worktree.Path, "worktree index contains sparse directory state")
	}
	return nil
}

func (collector *recoveryCollector) collectStatus(worktree *RecoveryWorktreeSnapshot) error {
	output, err := recoveryGit(collector.ctx, worktree.Path, nil,
		"status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		if cancelErr := collector.failure("status-probe-failed", "status", worktree.Path, err, &worktree.Completeness); cancelErr != nil {
			return cancelErr
		}
	}
	dirty, untracked, parseErr := parseRecoveryStatus(output)
	worktree.DirtyPaths, worktree.UntrackedPaths = dirty, untracked
	if parseErr != nil {
		_ = collector.failure("status-parse-failed", "status", worktree.Path, parseErr, &worktree.Completeness)
	}
	return nil
}

func parseRecoveryStatus(output []byte) ([]RecoveryDirtyPath, []string, error) {
	records := recoveryNULRecords(output)
	dirty := []RecoveryDirtyPath{}
	untracked := []string{}
	var parseErrors []error
	for index := 0; index < len(records); index++ {
		record := string(records[index])
		if record == "" {
			continue
		}
		switch record[0] {
		case '1', '2':
			fields := strings.Fields(record)
			path := statusPath(record)
			if len(fields) < 2 || len(fields[1]) != 2 || path == "" {
				parseErrors = append(parseErrors, fmt.Errorf("status record %d is malformed", index))
				continue
			}
			change := RecoveryDirtyPath{
				Path: path, Kind: "ordinary", IndexStatus: string(fields[1][0]), WorktreeStatus: string(fields[1][1]),
			}
			if record[0] == '2' {
				change.Kind = "rename-or-copy"
				if index+1 >= len(records) {
					parseErrors = append(parseErrors, fmt.Errorf("rename status record %d has no source path", index))
				} else {
					index++
					change.OldPath = string(records[index])
				}
			}
			dirty = append(dirty, change)
		case 'u':
			fields := strings.Fields(record)
			path := statusPath(record)
			if len(fields) < 2 || len(fields[1]) != 2 || path == "" {
				parseErrors = append(parseErrors, fmt.Errorf("unmerged status record %d is malformed", index))
				continue
			}
			dirty = append(dirty, RecoveryDirtyPath{
				Path: path, Kind: "unmerged", IndexStatus: string(fields[1][0]), WorktreeStatus: string(fields[1][1]),
			})
		case '?':
			path := strings.TrimPrefix(record, "? ")
			if path == record || path == "" {
				parseErrors = append(parseErrors, fmt.Errorf("untracked status record %d is malformed", index))
				continue
			}
			untracked = append(untracked, path)
		case '#':
			// Branch headers are allowed if a future caller adds --branch.
		default:
			parseErrors = append(parseErrors, fmt.Errorf("unknown status record %d type %q", index, record[0]))
		}
	}
	sort.Slice(dirty, func(i, j int) bool {
		return strings.Join([]string{dirty[i].Path, dirty[i].OldPath, dirty[i].Kind}, "\x00") <
			strings.Join([]string{dirty[j].Path, dirty[j].OldPath, dirty[j].Kind}, "\x00")
	})
	sort.Strings(untracked)
	return dirty, untracked, errors.Join(parseErrors...)
}

func (collector *recoveryCollector) collectIgnored(worktree *RecoveryWorktreeSnapshot) error {
	output, err := recoveryGit(collector.ctx, worktree.Path, nil,
		"ls-files", "--others", "--ignored", "--exclude-standard", "--full-name", "-z")
	if err != nil {
		if cancelErr := collector.failure("ignored-probe-failed", "ignored", worktree.Path, err, &worktree.Completeness); cancelErr != nil {
			return cancelErr
		}
	}
	paths := []string{}
	var parseErrors []error
	for recordIndex, record := range recoveryNULRecords(output) {
		if len(record) == 0 || !utf8.Valid(record) {
			parseErrors = append(parseErrors, fmt.Errorf("ignored record %d has an invalid path", recordIndex))
			continue
		}
		paths = append(paths, string(record))
	}
	sort.Strings(paths)
	worktree.IgnoredPaths = paths
	if parseErr := errors.Join(parseErrors...); parseErr != nil {
		_ = collector.failure("ignored-parse-failed", "ignored", worktree.Path, parseErr, &worktree.Completeness)
	}
	return nil
}

func (collector *recoveryCollector) collectLFS(worktree *RecoveryWorktreeSnapshot) error {
	paths := make(map[string]struct{}, len(worktree.IndexEntries)+len(worktree.UntrackedPaths)+len(worktree.IgnoredPaths))
	for _, entry := range worktree.IndexEntries {
		paths[entry.Path] = struct{}{}
	}
	for _, path := range worktree.UntrackedPaths {
		paths[path] = struct{}{}
	}
	for _, path := range worktree.IgnoredPaths {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return nil
	}
	var input bytes.Buffer
	for _, path := range ordered {
		input.WriteString(path)
		input.WriteByte(0)
	}
	byPath := map[string]*RecoveryLFSPath{}
	probes := []struct {
		name   string
		cached bool
	}{
		{"index", true},
		{"worktree", false},
	}
	for _, probe := range probes {
		args := []string{"check-attr"}
		if probe.cached {
			args = append(args, "--cached")
		}
		args = append(args, "-z", "--stdin", "filter")
		output, err := recoveryGit(collector.ctx, worktree.Path, input.Bytes(), args...)
		if err != nil {
			if cancelErr := collector.failure("lfs-attribute-probe-failed", "lfs", worktree.Path+":"+probe.name, err, &worktree.Completeness); cancelErr != nil {
				return cancelErr
			}
		}
		values := recoveryNULRecords(output)
		if len(values)%3 != 0 {
			_ = collector.failure("lfs-attribute-parse-failed", "lfs", worktree.Path+":"+probe.name,
				fmt.Errorf("Git returned %d attribute fields, want a multiple of three", len(values)), &worktree.Completeness)
			continue
		}
		for field := 0; field < len(values); field += 3 {
			path, attribute, value := string(values[field]), string(values[field+1]), string(values[field+2])
			if attribute != "filter" || value != "lfs" {
				continue
			}
			fact := byPath[path]
			if fact == nil {
				fact = &RecoveryLFSPath{Path: path}
				byPath[path] = fact
			}
			if probe.cached {
				fact.IndexAttributes = true
			} else {
				fact.WorktreeAttributes = true
			}
		}
	}
	for _, path := range ordered {
		if fact := byPath[path]; fact != nil {
			worktree.LFSPaths = append(worktree.LFSPaths, *fact)
			collector.blocker("lfs-path", "lfs", worktree.Path+":"+path, "path is governed by the Git LFS filter")
		}
	}
	return nil
}

func (collector *recoveryCollector) collectSubmodules(worktree *RecoveryWorktreeSnapshot) map[string]struct{} {
	paths := map[string]struct{}{}
	for _, entry := range worktree.IndexEntries {
		if entry.Mode != "160000" {
			continue
		}
		worktree.Submodules = append(worktree.Submodules, RecoverySubmodule{Path: entry.Path, OID: entry.OID, Stage: entry.Stage})
		paths[entry.Path] = struct{}{}
	}
	sort.Slice(worktree.Submodules, func(i, j int) bool {
		if worktree.Submodules[i].Path != worktree.Submodules[j].Path {
			return worktree.Submodules[i].Path < worktree.Submodules[j].Path
		}
		return worktree.Submodules[i].Stage < worktree.Submodules[j].Stage
	})
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		collector.blocker("submodule", "submodule", worktree.Path+":"+path, "gitlink requires independent submodule recovery proof")
	}
	return paths
}

func (collector *recoveryCollector) collectObjectStoreFeatures(repository Repo) {
	facts := &collector.snapshot.Repository
	for _, root := range collector.snapshot.AdminRoots {
		if root.Kind != RecoveryAdminCommonDir {
			continue
		}
		for _, entry := range root.Entries {
			if entry.Type == RecoveryEntryRegular && strings.HasPrefix(entry.Path, "objects/pack/") && strings.HasSuffix(entry.Path, ".promisor") {
				facts.PromisorPackFiles++
			}
		}
	}
	if facts.PromisorPackFiles > 0 {
		facts.Promisor = true
		facts.PartialClone = true
	}

	seenAlternates := map[string]struct{}{}
	addAlternate := func(source, path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if !filepath.IsAbs(path) && source == "objects/info/alternates" {
			path = filepath.Join(repository.GitCommonDir, "objects", path)
		}
		if canonical, err := pathx.Canonical(path); err == nil {
			path = canonical
		} else {
			_ = collector.failure("alternate-path-probe-failed", "object-store", path, err, nil)
			path = filepath.Clean(path)
		}
		key := source + "\x00" + path
		if _, exists := seenAlternates[key]; exists {
			return
		}
		seenAlternates[key] = struct{}{}
		facts.Alternates = append(facts.Alternates, RecoveryAlternate{Source: source, Path: path})
		collector.blocker("alternate-object-store", "object-store", path, "repository depends on an alternate object directory")
	}

	if data, ok := collector.snapshot.adminBytes[recoveryAdminKey(RecoveryAdminCommonDir, "objects/info/alternates")]; ok {
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			addAlternate("objects/info/alternates", line)
		}
	}
	for _, path := range filepath.SplitList(os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES")) {
		addAlternate("environment", path)
	}
	sort.Slice(facts.Alternates, func(i, j int) bool {
		return strings.Join([]string{facts.Alternates[i].Source, facts.Alternates[i].Path}, "\x00") <
			strings.Join([]string{facts.Alternates[j].Source, facts.Alternates[j].Path}, "\x00")
	})
	if facts.PartialClone {
		collector.blocker("partial-clone", "object-store", "repository", "repository uses partial-clone storage")
	}
	if facts.Promisor {
		collector.blocker("promisor-object-store", "object-store", "repository", "repository has promisor object storage")
	}
}

func (collector *recoveryCollector) finalize() {
	snapshot := collector.snapshot
	sort.Slice(snapshot.Refs, func(i, j int) bool { return snapshot.Refs[i].Name < snapshot.Refs[j].Name })
	for index := range snapshot.Worktrees {
		worktree := &snapshot.Worktrees[index]
		sort.Slice(worktree.Operations, func(i, j int) bool { return worktree.Operations[i].Name < worktree.Operations[j].Name })
		sort.Slice(worktree.IndexEntries, func(i, j int) bool {
			if worktree.IndexEntries[i].Path != worktree.IndexEntries[j].Path {
				return worktree.IndexEntries[i].Path < worktree.IndexEntries[j].Path
			}
			return worktree.IndexEntries[i].Stage < worktree.IndexEntries[j].Stage
		})
		sort.Slice(worktree.TrackedFiles, func(i, j int) bool { return worktree.TrackedFiles[i].Path < worktree.TrackedFiles[j].Path })
		sort.Strings(worktree.UntrackedPaths)
		sort.Strings(worktree.IgnoredPaths)
		sort.Slice(worktree.LFSPaths, func(i, j int) bool { return worktree.LFSPaths[i].Path < worktree.LFSPaths[j].Path })
		sort.Slice(worktree.Submodules, func(i, j int) bool {
			if worktree.Submodules[i].Path != worktree.Submodules[j].Path {
				return worktree.Submodules[i].Path < worktree.Submodules[j].Path
			}
			return worktree.Submodules[i].Stage < worktree.Submodules[j].Stage
		})
		sort.Slice(worktree.NestedRepositories, func(i, j int) bool {
			return worktree.NestedRepositories[i].Path < worktree.NestedRepositories[j].Path
		})
	}
	sort.Slice(snapshot.AdminRoots, func(i, j int) bool { return snapshot.AdminRoots[i].Kind < snapshot.AdminRoots[j].Kind })
	for index := range snapshot.AdminRoots {
		sort.Slice(snapshot.AdminRoots[index].Entries, func(i, j int) bool {
			return snapshot.AdminRoots[index].Entries[i].Path < snapshot.AdminRoots[index].Entries[j].Path
		})
	}
	sort.SliceStable(snapshot.Findings, func(i, j int) bool {
		left, right := snapshot.Findings[i], snapshot.Findings[j]
		return strings.Join([]string{left.Code, string(left.Kind), left.Scope, left.Subject, left.Message}, "\x00") <
			strings.Join([]string{right.Code, string(right.Kind), right.Scope, right.Subject, right.Message}, "\x00")
	})
}

func recoveryGit(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	command := exec.CommandContext(ctx, "git", full...)
	command.Dir = dir
	command.Env = recoveryEnvironment()
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return stdout.Bytes(), cause
		}
		return stdout.Bytes(), &Error{Args: args, Dir: dir, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

func recoveryEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GIT_OPTIONAL_LOCKS" {
			environment = append(environment, entry)
		}
	}
	return append(environment, "GIT_OPTIONAL_LOCKS=0")
}

func recoveryNULRecords(output []byte) [][]byte {
	if len(output) == 0 {
		return nil
	}
	records := bytes.Split(output, []byte{0})
	if len(records) > 0 && len(records[len(records)-1]) == 0 {
		records = records[:len(records)-1]
	}
	return records
}

func recoveryDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return recoveryDigestPrefix + hex.EncodeToString(digest[:])
}

func recoveryTrackedKey(worktreePath, relativePath string) string {
	return worktreePath + "\x00" + relativePath
}

func recoveryAdminKey(root RecoveryAdminRootKind, relativePath string) string {
	return string(root) + "\x00" + relativePath
}

func recoveryCleanText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return ' '
		}
		return character
	}, value)
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func recoveryCleanSubject(value string) string {
	value = recoveryCleanText(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func recoveryIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	lastDash := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '/' || character == ':' || character == '_' {
			result.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	identifier := strings.Trim(result.String(), "-./:_")
	if identifier == "" {
		return "unknown"
	}
	return identifier
}
