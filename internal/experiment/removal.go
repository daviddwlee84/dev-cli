package experiment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

// RemovalRequest describes explicit disposal, never a verified backup/reclaim.
type RemovalRequest struct {
	Ref       string
	Permanent bool
	Expected  *catalog.Entry
}

// RemovalPlan seals its authority privately. Callers can display it, but may
// not replace its path, mode or observations between confirmation and apply.
type RemovalPlan struct {
	ID                          string   `json:"id"`
	Source                      string   `json:"source"`
	Method                      string   `json:"method"`
	Bytes                       int64    `json:"logical_bytes"`
	Files                       int      `json:"files"`
	Warnings                    []string `json:"warnings"`
	entry                       *catalog.Entry
	identity, tree, guard, seal string
}

type RemovalJournal struct {
	Version     int       `json:"version"`
	OperationID string    `json:"operation_id"`
	ID          string    `json:"id"`
	Host        string    `json:"host"`
	Method      string    `json:"method"`
	Source      string    `json:"source"`
	Identity    string    `json:"identity"`
	Tree        string    `json:"tree"`
	Outcome     string    `json:"outcome"`
	Started     time.Time `json:"started"`
}

type RemovalResult struct {
	ID      string `json:"id"`
	Method  string `json:"method"`
	Outcome string `json:"outcome"`
	Journal string `json:"journal,omitempty"`
}

func removalDigest(value any) string {
	body, _ := json.Marshal(value)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func (p RemovalPlan) fingerprint() string {
	return removalDigest([]any{p.ID, p.Source, p.Method, p.Bytes, p.Files, p.Warnings, p.entry, p.identity, p.tree, p.guard})
}

func (s *Service) PlanRemoval(ctx context.Context, request RemovalRequest) (RemovalPlan, error) {
	var plan RemovalPlan
	entry, err := s.resolveRemovalEntry(request.Ref)
	if err != nil {
		return plan, err
	}
	if request.Expected != nil && !sameRemovalEntry(request.Expected, entry) {
		return plan, errors.New("selected Try changed; reload and choose again")
	}
	location, ok := entry.LocationFor(s.host)
	if !ok || (location.State != catalog.LocationPresent && location.State != catalog.LocationArchived) {
		return plan, errors.New("Try is not present or archived on this host")
	}
	plan = RemovalPlan{ID: entry.ID, Source: location.CurrentPath, Method: "trash", entry: entry.Clone()}
	if request.Permanent {
		plan.Method = "permanent"
	} else if err := s.trashAvailable(); err != nil {
		return plan, err
	}
	if err := s.inspectRemoval(ctx, &plan); err != nil {
		return plan, err
	}
	plan.seal = plan.fingerprint()
	return plan, nil
}

func (s *Service) inspectRemoval(ctx context.Context, plan *RemovalPlan) error {
	entry := plan.entry
	if entry == nil || !eligibleTryRecord(entry) || entry.MoveIntent != nil {
		return errors.New("Try has changed or has a pending filesystem operation")
	}
	if entry.Experiment.Phase == catalog.PhaseGraduated {
		return errors.New("graduated repositories cannot be deleted through Try lifecycle")
	}
	if err := s.validateVisibleTryPath(plan.Source); err != nil {
		if archiveErr := s.validateArchivedPath(entry.ID, plan.Source); archiveErr != nil {
			return errors.Join(err, archiveErr)
		}
	}
	// Every path component below tries_root must be a real directory. Checking
	// the canonical path alone would hide symlink or reparse traversal.
	root, err := s.openRemovalRoot(plan.Source)
	if err != nil {
		return err
	}
	defer root.Close()
	wd, err := s.getwd()
	if err != nil {
		return err
	}
	if within, err := pathx.Contains(plan.Source, wd); err != nil || within {
		return errors.New("leave the Try directory before removing it")
	}
	probe, err := s.inspectMovableSource(ctx, plan.Source)
	if err != nil {
		return err
	}
	if probe.live.Repo != nil && probe.live.Repo.IsLinkedWorktree {
		return errors.New("linked/shared Git worktrees cannot be removed through Try deletion")
	}
	if probe.live.Repo != nil {
		if !samePath(probe.live.Repo.GitCommonDir, filepath.Join(plan.Source, ".git")) {
			return errors.New("external/shared Git storage blocks deletion")
		}
		if body, err := os.ReadFile(filepath.Join(plan.Source, ".git", "objects", "info", "alternates")); err == nil && strings.TrimSpace(string(body)) != "" {
			return errors.New("shared Git object alternates block deletion")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if inside, err := pathx.Contains(plan.Source, s.store.Dir); err != nil || inside {
		return errors.New("Try contains durable dev state")
	}
	if probe.live.StatusError != nil {
		return probe.live.StatusError
	}
	plan.Warnings = []string{"Trash is recoverable only until emptied; it does not release disk space. No backup has been verified."}
	if plan.Method == "permanent" {
		plan.Warnings = []string{"Permanent deletion discards all contents, including untracked/ignored files, local refs and stash. No backup has been verified."}
	}
	if probe.live.Repo == nil {
		plan.Warnings = append(plan.Warnings, "Non-Git Try: files have no Git recovery.")
	} else {
		plan.Warnings = append(plan.Warnings, "The entire independent Git repository, including every local branch/tag and stash, is included.")
		if probe.live.Status != nil && probe.live.Status.Dirty() {
			plan.Warnings = append(plan.Warnings, "Uncommitted or untracked work is present.")
		}
		if probe.originURL == "" {
			plan.Warnings = append(plan.Warnings, "No origin remote is configured.")
		}
	}
	if s.removalGuard == nil {
		return errors.New("Try removal requires a task/runtime observation guard")
	}
	plan.guard, err = s.removalGuard(ctx, plan.Source)
	if err != nil {
		return err
	}
	plan.identity, _, err = removalIdentity(plan.Source)
	if err != nil {
		return err
	}
	plan.tree, plan.Bytes, plan.Files, err = removalTree(ctx, plan.Source, root)
	return err
}

// removalTree includes ignored files and Git storage. It never follows links,
// accepts mount boundaries, or descends into independently owned repositories.
func removalTree(ctx context.Context, path string, root *os.Root) (string, int64, int, error) {
	_, volume, err := removalIdentity(path)
	if err != nil {
		return "", 0, 0, err
	}
	hash := sha256.New()
	var bytes int64
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 1000000 {
			return errors.New("Try contains too many entries to verify safely")
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !strings.EqualFold(name, ".git") && strings.EqualFold(filepath.Base(name), ".git") {
			return fmt.Errorf("nested Git repository at %s blocks deletion", name)
		}
		if strings.EqualFold(name, ".git") && !info.IsDir() {
			return errors.New("external/shared Git metadata blocks deletion")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%q link %q\n", name, target)
			return nil
		}
		if name != "." && !strings.EqualFold(name, ".git") && info.IsDir() {
			head, headErr := root.Stat(name + "/HEAD")
			objects, objectsErr := root.Stat(name + "/objects")
			configuration, configErr := root.Stat(name + "/config")
			if headErr == nil && head.Mode().IsRegular() && objectsErr == nil && objects.IsDir() && configErr == nil && configuration.Mode().IsRegular() {
				return fmt.Errorf("nested bare Git repository at %s blocks deletion", name)
			}
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("special file %s blocks deletion", name)
		}
		identity, device, err := removalIdentity(filepath.Join(path, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if device != volume {
			return fmt.Errorf("mount boundary at %s blocks deletion", name)
		}
		fmt.Fprintf(hash, "%q %s %d %d %d\n", name, identity, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)), bytes, count, err
}

func (s *Service) ApplyRemoval(ctx context.Context, plan RemovalPlan) (RemovalResult, error) {
	result := RemovalResult{ID: plan.ID, Method: plan.Method, Outcome: "not-applied"}
	if plan.seal == "" || plan.seal != plan.fingerprint() {
		return result, errors.New("invalid or modified removal plan")
	}
	err := s.store.WithLock(ctx, func() error {
		fresh, err := s.store.Get(plan.ID)
		if err != nil {
			return err
		}
		if !sameRemovalEntry(fresh, plan.entry) {
			return errors.New("Try changed after removal preview")
		}
		observed := plan
		if err := s.inspectRemoval(ctx, &observed); err != nil {
			return err
		}
		if observed.fingerprint() != plan.seal {
			return errors.New("Try contents or authority changed after removal preview")
		}
		if plan.Method == "trash" {
			if err := s.trashAvailable(); err != nil {
				return err
			}
		}
		journal := RemovalJournal{Version: 1, OperationID: uuid.NewString(), ID: plan.ID, Host: s.host, Method: plan.Method, Source: plan.Source, Identity: plan.identity, Tree: plan.tree, Outcome: "pending", Started: s.clock().UTC()}
		result.Journal = s.removalJournalPath(journal.OperationID)
		if err := s.writeRemovalJournal(ctx, journal); err != nil {
			return err
		}
		_, err = s.catalogUpdate(plan.ID, func(current *catalog.Entry) error {
			if !sameRemovalEntry(current, plan.entry) {
				return errors.New("Try changed before removal intent")
			}
			current.MoveIntent = &catalog.MoveIntent{Host: s.host, Operation: "remove-" + plan.Method, SourcePath: plan.Source, DestinationPath: result.Journal, Started: journal.Started}
			return nil
		})
		if err != nil {
			return err
		}
		// Revalidate at the final boundary after intent publication. No automatic
		// retry follows any call into the destructive backend.
		root, held, err := safefile.OpenRoot(plan.Source)
		if err != nil {
			return err
		}
		defer root.Close()
		tree, _, _, err := removalTree(ctx, plan.Source, root)
		if err != nil {
			return err
		}
		if tree != plan.tree {
			return errors.New("Try contents changed before removal; intent retained")
		}
		if guard, err := s.removalGuard(ctx, plan.Source); err != nil || guard != plan.guard {
			return errors.Join(errors.New("removal occupancy changed; intent retained"), err)
		}
		if err := safefile.VerifyRoot(plan.Source, held); err != nil {
			return err
		}
		result.Outcome = "unknown"
		if plan.Method == "trash" {
			err = s.trash(ctx, plan.Source)
		} else {
			parent, _, openErr := safefile.OpenRoot(filepath.Dir(plan.Source))
			if openErr != nil {
				return openErr
			}
			defer parent.Close()
			if err = safefile.VerifyChildRoot(parent, filepath.Base(plan.Source), held); err == nil {
				err = parent.RemoveAll(filepath.Base(plan.Source))
			}
		}
		if err != nil {
			return fmt.Errorf("removal outcome unknown; inspect %s before recovery: %w", result.Journal, err)
		}
		if _, err := os.Lstat(plan.Source); !errors.Is(err, os.ErrNotExist) {
			return errors.New("removal source is still present or indeterminate; intent retained")
		}
		journal.Outcome = "removed"
		if err := s.writeRemovalJournal(ctx, journal); err != nil {
			return err
		}
		if err := s.finalizeRemoval(journal); err != nil {
			return err
		}
		result.Outcome = "removed"
		return nil
	})
	return result, err
}

func (s *Service) removalJournalPath(id string) string {
	return filepath.Join(filepath.Dir(s.store.Dir), "try-removals", id+".json")
}

func (s *Service) writeRemovalJournal(ctx context.Context, journal RemovalJournal) error {
	path := s.removalJournalPath(journal.OperationID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	root, _, err := safefile.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		_, err = safefile.CreatePrivateNoClobber(ctx, root, name, body, false)
		return err
	}
	if err != nil {
		return err
	}
	_, err = safefile.AtomicReplace(ctx, root, name, info, body, 0600)
	return err
}

func (s *Service) readRemovalJournal(ctx context.Context, id string) (RemovalJournal, error) {
	var journal RemovalJournal
	if _, err := uuid.Parse(id); err != nil {
		return journal, err
	}
	path := s.removalJournalPath(id)
	root, _, err := safefile.OpenRoot(filepath.Dir(path))
	if err != nil {
		return journal, err
	}
	defer root.Close()
	body, _, err := safefile.ReadStableRegular(ctx, root, filepath.Base(path), nil, 64<<10)
	if err != nil {
		return journal, err
	}
	if err = json.Unmarshal(body, &journal); err != nil {
		return journal, err
	}
	if journal.Version != 1 || journal.OperationID != id || journal.Host != s.host || (journal.Method != "trash" && journal.Method != "permanent") {
		return journal, errors.New("invalid removal journal authority")
	}
	return journal, nil
}

func (s *Service) finalizeRemoval(journal RemovalJournal) error {
	_, err := s.catalogUpdate(journal.ID, func(entry *catalog.Entry) error {
		intent := entry.MoveIntent
		if intent == nil || intent.Host != s.host || intent.Operation != "remove-"+journal.Method || intent.SourcePath != journal.Source || intent.DestinationPath != s.removalJournalPath(journal.OperationID) {
			return errors.New("removal intent changed")
		}
		location, ok := entry.LocationFor(s.host)
		if !ok {
			return errors.New("removal location disappeared")
		}
		if location.RestorePath == "" {
			location.RestorePath = journal.Source
		}
		location.State, location.CurrentPath, location.RealPath = catalog.LocationEvicted, "", ""
		location.RemovalID, location.RemovalMethod = journal.OperationID, journal.Method
		entry.MoveIntent = nil
		return entry.SetLocation(s.host, location)
	})
	return err
}

// RestoreRemoved reassociates bytes already restored by the OS. It neither
// searches the Trash nor accepts an arbitrary replacement directory as proof.
func (s *Service) RestoreRemoved(ctx context.Context, ref, path string) (Item, error) {
	var result Item
	selected, err := s.resolveRemovalEntry(ref)
	if err != nil {
		return result, err
	}
	err = s.store.WithLock(ctx, func() error {
		entry, err := s.store.Get(selected.ID)
		if err != nil {
			return err
		}
		location, ok := entry.LocationFor(s.host)
		if !ok {
			return errors.New("Try has no location on this host")
		}
		operationID := location.RemovalID
		pending := entry.MoveIntent != nil && entry.MoveIntent.Host == s.host && entry.MoveIntent.Operation == "remove-trash"
		if pending {
			operationID = strings.TrimSuffix(filepath.Base(entry.MoveIntent.DestinationPath), ".json")
		} else if location.State != catalog.LocationEvicted || entry.MoveIntent != nil {
			return errors.New("only a retained Trash operation can be reassociated")
		}
		journal, err := s.readRemovalJournal(ctx, operationID)
		if err != nil {
			return err
		}
		if journal.ID != entry.ID || journal.Method != "trash" || (!pending && journal.Outcome != "removed") {
			return errors.New("no matching Trash operation")
		}
		if pending && (entry.MoveIntent.SourcePath != journal.Source || entry.MoveIntent.DestinationPath != s.removalJournalPath(operationID)) {
			return errors.New("pending Trash authority does not match its journal")
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
		restoredState := catalog.LocationPresent
		if err := s.validateVisibleTryPath(path); err != nil {
			if archiveErr := s.validateArchivedPath(entry.ID, path); archiveErr != nil {
				return errors.Join(err, archiveErr)
			}
			restoredState = catalog.LocationArchived
		}
		root, err := s.openRemovalRoot(path)
		if err != nil {
			return err
		}
		defer root.Close()
		tree, _, _, err := removalTree(ctx, path, root)
		if err != nil {
			return err
		}
		if journal.Tree == "" || tree != journal.Tree {
			return errors.New("restored folder contents changed; original Trash identity must be recovered before editing")
		}
		identity, _, err := removalIdentity(path)
		if err != nil {
			return err
		}
		if identity != journal.Identity {
			return errors.New("restored folder does not match the removed filesystem identity")
		}
		probe, err := s.inspectMovableSource(ctx, path)
		if err != nil {
			return err
		}
		entries, diagnostics, err := s.store.ListWithDiagnostics()
		if err != nil {
			return err
		}
		if len(diagnostics) > 0 {
			return incompleteCatalogError(diagnostics)
		}
		for _, other := range matchingEntries(entries, s.host, probe) {
			if other.ID != entry.ID {
				return errors.New("restored folder is claimed by another catalog asset")
			}
		}
		updated, err := s.catalogUpdate(entry.ID, func(current *catalog.Entry) error {
			if !sameRemovalEntry(current, entry) {
				return errors.New("Try changed during recovery")
			}
			location.State, location.CurrentPath, location.RealPath = restoredState, path, probe.live.RealPath
			if restoredState == catalog.LocationPresent {
				location.RestorePath = ""
			}
			location.RemovalID, location.RemovalMethod = operationID, "trash"
			current.MoveIntent = nil
			return current.SetLocation(s.host, location)
		})
		if err == nil {
			result = itemFromEntry(updated, probe.live)
			journal.Outcome = "reassociated"
			err = s.writeRemovalJournal(ctx, journal)
		}
		return err
	})
	return result, err
}

// Resolve from durable catalog records only. Planning and recovery must not
// discover, register or implicitly revive a restored directory.
func (s *Service) resolveRemovalEntry(ref string) (*catalog.Entry, error) {
	entries, diagnostics, err := s.store.ListWithDiagnostics()
	if err != nil {
		return nil, err
	}
	if len(diagnostics) > 0 {
		return nil, incompleteCatalogError(diagnostics)
	}
	var matches []*catalog.Entry
	for _, entry := range entries {
		if !eligibleTryRecord(entry) {
			continue
		}
		if entry.ID == ref {
			return entry, nil
		}
		location, _ := entry.LocationFor(s.host)
		if ref == entry.Name || ref == entry.Title() || ref == entry.Experiment.Slug || (location.CurrentPath != "" && (ref == location.CurrentPath || ref == filepath.Base(location.CurrentPath))) || (location.RestorePath != "" && (ref == location.RestorePath || ref == filepath.Base(location.RestorePath))) {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("Try reference %q is ambiguous; use its catalog ID", ref)
	}
	return nil, fmt.Errorf("no cataloged Try matches %q; run dev tries list to discover it first", ref)
}

func (s *Service) openRemovalRoot(source string) (*os.Root, error) {
	root, _, err := safefile.OpenRoot(s.triesRoot)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(s.triesRoot)
	if err != nil {
		root.Close()
		return nil, err
	}
	rel, err := filepath.Rel(canonical, source)
	if err != nil || !filepath.IsLocal(rel) {
		rel, err = filepath.Rel(s.triesRoot, source)
	}
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		root.Close()
		return nil, errors.New("removal target escapes tries_root")
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		next, _, openErr := safefile.OpenChildRoot(root, part)
		root.Close()
		if openErr != nil {
			return nil, openErr
		}
		root = next
	}
	return root, nil
}

func sameRemovalEntry(left, right *catalog.Entry) bool {
	return left != nil && right != nil && removalDigest(left.Clone()) == removalDigest(right.Clone())
}
