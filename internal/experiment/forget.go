package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

// Presence is an observation, not a persisted lifecycle state. Missing is
// established only below an accessible, real parent under the expected root.
func (s *Service) Presence(path string) string {
	if _, err := s.missingParent(path); err == nil {
		return "missing"
	}
	root, err := s.openRemovalRoot(path)
	if err == nil {
		root.Close()
		return "present"
	}
	return "unavailable"
}

func (s *Service) missingParent(path string) (string, error) {
	root, _, err := safefile.OpenRoot(s.triesRoot)
	if err != nil {
		return "", err
	}
	defer root.Close()
	canonical, err := filepath.EvalSymlinks(s.triesRoot)
	if err != nil {
		return "", err
	}
	if filepath.Dir(path) != canonical && filepath.Dir(path) != s.triesRoot {
		return "", errors.New("forget requires an immediate local Try under tries_root")
	}
	if strings.HasPrefix(filepath.Base(path), ".") {
		return "", errors.New("hidden Try storage requires recovery")
	}
	if _, err := root.Lstat(filepath.Base(path)); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("Try directory is present or its absence cannot be verified")
	}
	identity, _, err := removalIdentity(s.triesRoot)
	return identity, err
}

type ForgetPlan struct {
	ID                  string `json:"id"`
	Source              string `json:"source"`
	Method              string `json:"method"`
	entry               *catalog.Entry
	parent, guard, seal string
}

func (p ForgetPlan) fingerprint() string {
	return removalDigest([]any{p.ID, p.Source, p.Method, p.entry, p.parent, p.guard})
}

func (s *Service) PlanForget(ctx context.Context, ref string, expected *catalog.Entry) (ForgetPlan, error) {
	p := ForgetPlan{Method: "forget"}
	e, err := s.resolveRemovalEntry(ref)
	if err != nil {
		return p, err
	}
	if expected != nil && !sameRemovalEntry(e, expected) {
		return p, errors.New("Try selection changed")
	}
	l, _ := e.LocationFor(s.host)
	p.ID, p.Source, p.entry = e.ID, l.CurrentPath, e.Clone()
	if err := s.inspectForget(ctx, &p); err != nil {
		return p, err
	}
	p.seal = p.fingerprint()
	return p, nil
}

func (s *Service) inspectForget(ctx context.Context, p *ForgetPlan) error {
	e := p.entry
	if e == nil || e.Kind != catalog.KindTry || e.Experiment == nil || e.Experiment.Phase == catalog.PhaseGraduated || e.MoveIntent != nil || e.RecoveryReceipt != nil {
		return errors.New("only an unreferenced, missing Try can be forgotten")
	}
	l, ok := e.LocationFor(s.host)
	if !ok || len(e.Locations) != 1 || l.State != catalog.LocationPresent || l.CurrentPath != p.Source || l.RemovalID != "" || l.RestorePath != "" {
		return errors.New("Try has another host location or lifecycle/recovery history")
	}
	if strings.TrimSpace(e.Note) != "" || len(e.Tags) > 0 {
		return errors.New("Try has personal notes or tags; review and clear them before forgetting")
	}
	if l.GitCommonDir != "" {
		within, err := pathx.Contains(p.Source, l.GitCommonDir)
		if err != nil || !within {
			return errors.New("Try references external/shared Git storage")
		}
	}
	if l.RealPath != "" {
		real, err := pathx.Canonical(l.RealPath)
		source, sourceErr := pathx.Canonical(p.Source)
		if err != nil || sourceErr != nil || real != source {
			return errors.New("Try has a different recorded real path")
		}
	}
	var err error
	p.parent, err = s.missingParent(p.Source)
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
	for _, other := range entries {
		if other.ID == e.ID {
			continue
		}
		if intent := other.MoveIntent; intent != nil && intent.Host == s.host && (pathsRelated(p.Source, intent.SourcePath) || pathsRelated(p.Source, intent.DestinationPath)) {
			return errors.New("another catalog move references this Try")
		}
		if receipt := other.RecoveryReceipt; receipt != nil && receipt.Host == s.host && (pathsRelated(p.Source, receipt.SourcePath) || pathsRelated(p.Source, receipt.RestorePath) || pathsRelated(p.Source, receipt.ArchivePath)) {
			return errors.New("another recovery receipt references this Try")
		}
		ol, exists := other.LocationFor(s.host)
		if !exists {
			continue
		}
		for _, path := range []string{ol.CurrentPath, ol.RealPath, ol.GitCommonDir, ol.RestorePath} {
			if pathsRelated(p.Source, path) || (l.GitCommonDir != "" && path == l.GitCommonDir) {
				return fmt.Errorf("catalog asset %s references this Try", other.ID)
			}
		}
	}
	// Recovery records remain authoritative even when their catalog move intent
	// is absent. Unknown or malformed records cannot prove independence.
	files, err := os.ReadDir(filepath.Dir(s.removalJournalPath("unused")))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(s.removalJournalPath("unused")), f.Name()))
		if err != nil {
			return err
		}
		var journal RemovalJournal
		if err := json.Unmarshal(data, &journal); err != nil {
			return errors.New("Try recovery inventory is unreadable")
		}
		// A completed forget audit has no bytes to recover and does not own a
		// later, independently enrolled Try which happens to reuse that path.
		if journal.Method == "forget" && journal.Outcome == "forgotten" && journal.ID != e.ID {
			continue
		}
		if journal.ID == e.ID || (journal.Host == s.host && pathsRelated(p.Source, journal.Source)) {
			return errors.New("Try has a recovery record; inspect it individually")
		}
	}
	if s.forgetGuard == nil {
		return errors.New("Try reference inspection is unavailable")
	}
	p.guard, err = s.forgetGuard(ctx, e.Clone())
	return err
}

func pathsRelated(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	var err error
	a, err = pathx.Canonical(a)
	if err != nil {
		return true
	}
	b, err = pathx.Canonical(b)
	if err != nil {
		return true
	}
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

// ApplyForget runs inside the caller's task/note guards, then locks and rereads
// the catalog. It never removes a project directory or note source.
func (s *Service) ApplyForget(ctx context.Context, p ForgetPlan) (RemovalResult, error) {
	r := RemovalResult{ID: p.ID, Method: "forget", Outcome: "not-applied"}
	if p.seal == "" || p.seal != p.fingerprint() {
		return r, errors.New("invalid or modified forget plan")
	}
	err := s.store.WithLock(ctx, func() error {
		e, err := s.store.Get(p.ID)
		if err != nil {
			return err
		}
		if !sameRemovalEntry(e, p.entry) {
			return errors.New("Try changed after forget preview")
		}
		fresh := p
		if err := s.inspectForget(ctx, &fresh); err != nil {
			return err
		}
		if fresh.fingerprint() != p.seal {
			return errors.New("Try authority changed after forget preview")
		}
		journal := RemovalJournal{Version: 1, OperationID: uuid.NewString(), ID: p.ID, Host: s.host, Method: "forget", Source: p.Source, Outcome: "forget-pending", Started: s.now()}
		r.Journal = s.removalJournalPath(journal.OperationID)
		if err := s.writeRemovalJournal(ctx, journal); err != nil {
			return err
		}
		parent, err := s.missingParent(p.Source)
		if err != nil || parent != p.parent {
			return errors.Join(errors.New("Try absence changed; forget not applied"), err)
		}
		if err := s.store.DeleteExactUnderLock(p.entry); err != nil {
			return err
		}
		r.Outcome, journal.Outcome = "forgotten", "forgotten"
		return s.writeRemovalJournal(ctx, journal)
	})
	return r, err
}
