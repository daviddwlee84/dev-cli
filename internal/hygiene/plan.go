package hygiene

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lockx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type FileChange struct {
	File         string `json:"file"`
	Replacements int    `json:"replacements"`
	BeforeDigest string `json:"before_digest"`
	AfterDigest  string `json:"after_digest"`
}
type Plan struct {
	SchemaVersion         int          `json:"schema_version"`
	Kind                  string       `json:"kind"`
	ID                    string       `json:"id"`
	Revision              string       `json:"revision"`
	Status                string       `json:"status"`
	Files                 []FileChange `json:"files"`
	RequiresWriterStopped bool         `json:"requires_writer_stopped"`
	HookAction            string       `json:"hook_action,omitempty"`
	Policy                *Policy      `json:"policy,omitempty"`
	Completed             []string     `json:"completed,omitempty"`
	Recovery              []string     `json:"recovery,omitempty"`
	ReviewFile            string       `json:"review_file"`
}
type change struct {
	Path         string
	Token        string
	Desired      []byte
	BeforeDigest string
}
type planRecord struct {
	Plan                                  Plan
	Root, RepoID, RepoToken, PolicyDigest string
	Changes                               []change
	HooksToken                            string
	RecoveryID                            string
	ScannerDigest                         string
	Audit                                 bool
	ReviewDigest                          string
}
type ApplyOptions struct {
	WriterStopped bool
	Guard         func(context.Context, string, []string) error
}

func (s *Service) repoToken(ctx context.Context) (string, error) {
	id, err := gitx.DirectoryIdentity(s.Root)
	if err != nil {
		return "", err
	}
	head, headErr := gitBytes(ctx, s.Root, nil, "rev-parse", "--verify", "HEAD")
	branch, branchErr := gitBytes(ctx, s.Root, nil, "symbolic-ref", "-q", "HEAD")
	if headErr != nil {
		if branchErr != nil {
			return "", headErr
		}
		present, e := gitBytes(ctx, s.Root, nil, "for-each-ref", "--format=%(refname)", strings.TrimSpace(string(branch)))
		if e != nil || len(present) != 0 {
			return "", headErr
		}
		head = []byte("unborn")
	}
	return digest([]byte(id + string(head) + string(branch))), nil
}
func (s *Service) newPlan(ctx context.Context, kind string) (planRecord, error) {
	if err := s.prepare(ctx); err != nil {
		return planRecord{}, err
	}
	token, err := s.repoToken(ctx)
	if err != nil {
		return planRecord{}, err
	}
	return planRecord{Plan: Plan{SchemaVersion: 1, Kind: kind, ID: newID(), Status: "prepared", Files: []FileChange{}}, Root: s.Root, RepoID: s.RepoID, RepoToken: token, PolicyDigest: policyDigest(s.Policy)}, nil
}
func (s *Service) addChange(ctx context.Context, p *planRecord, file, path string, desired []byte, replacements int) error {
	editor, err := configedit.NewTextFile(ctx, path, desired)
	if err != nil {
		return errors.New("selected file cannot be safely edited; inspect its ownership, links or permissions")
	}
	if editor.Empty() {
		return nil
	}
	token, err := editor.SourceToken(path)
	if err != nil {
		return err
	}
	c := editor.Preview()[0]
	p.Changes = append(p.Changes, change{path, token, desired, c.BeforeDigest})
	p.Plan.Files = append(p.Plan.Files, FileChange{s.displayPath(file), replacements, keyedID(s.key, c.BeforeDigest), keyedID(s.key, c.AfterDigest)})
	return nil
}
func (s *Service) PreviewRedact(ctx context.Context, reportID string, files, ids []string) (Plan, error) {
	if err := s.prepare(ctx); err != nil {
		return Plan{}, err
	}
	var scan scanRecord
	if err := s.load(ctx, reportID, &scan); err != nil {
		return Plan{}, err
	}
	if scan.Report.Scope != "worktree" || scan.Report.RepoID != s.RepoID || scan.Root != s.Root || scan.Report.PolicyDigest != keyedID(s.key, policyDigest(s.Policy)) {
		return Plan{}, ErrStale
	}
	if len(files) == 0 && len(ids) == 0 {
		return Plan{}, errors.New("select at least one file or finding to redact")
	}
	selectedFiles := map[string]bool{}
	for _, f := range files {
		if !validRelative(f) {
			return Plan{}, errors.New("invalid selected path")
		}
		selectedFiles[f] = true
	}
	selectedIDs := map[string]bool{}
	for _, id := range ids {
		selectedIDs[id] = true
	}
	findings := map[string]Finding{}
	for _, f := range scan.Report.Findings {
		findings[f.ID] = f
	}
	sources := map[string]source{}
	for _, x := range scan.Sources {
		sources[x.Path] = x
	}
	grouped := map[string][]edit{}
	matchedIDs := map[string]bool{}
	matchedFiles := map[string]bool{}
	for _, e := range scan.Edits {
		if !selectedFiles[e.Path] && !selectedIDs[e.Finding] {
			continue
		}
		f, ok := findings[e.Finding]
		if !ok || !f.CanRedact || f.Disposition == "accepted" {
			continue
		}
		grouped[e.Path] = append(grouped[e.Path], e)
		matchedIDs[e.Finding] = true
		matchedFiles[e.Path] = true
	}
	for id := range selectedIDs {
		if !matchedIDs[id] {
			return Plan{}, errors.New("selected finding is not eligible for redaction")
		}
	}
	for path := range selectedFiles {
		if !matchedFiles[path] {
			return Plan{}, errors.New("selected file has no eligible replacements")
		}
	}
	p, err := s.newPlan(ctx, "hygiene_redact")
	if err != nil {
		return Plan{}, err
	}
	cfg, ig, e := s.scannerInputs(ctx, scan.Report.Audit)
	if e != nil || scannerDigest(cfg, ig) != scan.ScannerDigest {
		return Plan{}, ErrStale
	}
	p.ScannerDigest = scan.ScannerDigest
	p.Audit = scan.Report.Audit
	paths := []string{}
	for path := range grouped {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	priority := func(e edit) int {
		switch findings[e.Finding].Category {
		case "secret":
			return 0
		case "known":
			return 1
		}
		return 2
	}
	for _, file := range paths {
		src, ok := sources[file]
		if !ok || src.Token == "" {
			return Plan{}, ErrStale
		}
		data, err := safefile.ReadStablePath(ctx, filepath.Join(s.Root, filepath.FromSlash(file)), MaxFileBytes)
		if err != nil || digest(data) != src.Digest {
			return Plan{}, ErrStale
		}
		edits := grouped[file]
		sort.SliceStable(edits, func(i, j int) bool {
			if edits[i].Start != edits[j].Start {
				return edits[i].Start < edits[j].Start
			}
			return edits[i].End > edits[j].End
		})
		chosen := []edit{}
		for first := 0; first < len(edits); {
			end := edits[first].End
			last := first + 1
			for last < len(edits) && edits[last].Start < end {
				if edits[last].End > end {
					end = edits[last].End
				}
				last++
			}
			cluster := append([]edit(nil), edits[first:last]...)
			sort.SliceStable(cluster, func(i, j int) bool {
				if priority(cluster[i]) != priority(cluster[j]) {
					return priority(cluster[i]) < priority(cluster[j])
				}
				return cluster[i].End-cluster[i].Start > cluster[j].End-cluster[j].Start
			})
			accepted := []edit{}
			for _, e := range cluster {
				if e.Start < 0 || e.End > len(data) || e.Start >= e.End {
					return Plan{}, errors.New("invalid recorded replacement")
				}
				overlap := false
				for _, x := range accepted {
					if e.Start < x.End && x.Start < e.End {
						overlap = true
						break
					}
				}
				if !overlap {
					accepted = append(accepted, e)
				}
			}
			chosen = append(chosen, accepted...)
			first = last
		}
		sort.Slice(chosen, func(i, j int) bool { return chosen[i].Start < chosen[j].Start })
		size := len(data)
		for _, e := range chosen {
			size += len(e.Replacement) - (e.End - e.Start)
			if size > MaxFileBytes {
				return Plan{}, errors.New("replacement exceeds file limit")
			}
		}
		after := make([]byte, 0, size)
		offset := 0
		for _, e := range chosen {
			after = append(after, data[offset:e.Start]...)
			after = append(after, []byte(e.Replacement)...)
			offset = e.End
		}
		after = append(after, data[offset:]...)
		path := filepath.Join(s.Root, filepath.FromSlash(file))
		if err = s.addChange(ctx, &p, file, path, after, len(chosen)); err != nil {
			return Plan{}, err
		}
		if len(p.Changes) > 0 && p.Changes[len(p.Changes)-1].Path == path && p.Changes[len(p.Changes)-1].Token != src.Token {
			return Plan{}, ErrStale
		}
		if isArtifact(file) {
			p.Plan.RequiresWriterStopped = true
		}
	}
	if err = s.savePlan(ctx, &p); err != nil {
		return Plan{}, err
	}
	return p.Plan, nil
}
func isArtifact(file string) bool {
	for _, p := range []string{".specstory/", ".claude/", ".codex/", ".cursor/", ".opencode/", ".specify/"} {
		if strings.HasPrefix(file, p) {
			return true
		}
	}
	return false
}
func (s *Service) Apply(ctx context.Context, id string, o ApplyOptions) (Plan, error) {
	if err := s.prepare(ctx); err != nil {
		return Plan{}, err
	}
	var p planRecord
	if err := s.load(ctx, id, &p); err != nil {
		return Plan{}, err
	}
	if p.Plan.ReviewFile != id+".review.txt" {
		return p.Plan, ErrStale
	}
	review, e := safefile.ReadStablePath(ctx, filepath.Join(s.Dir, p.Plan.ReviewFile), MaxRecordBytes)
	if e != nil || keyedID(s.key, string(review)) != p.ReviewDigest {
		return p.Plan, ErrStale
	}
	if p.Plan.ID != id || !hmac.Equal([]byte(p.Plan.Revision), []byte(s.planRevision(p))) {
		return p.Plan, ErrStale
	}
	if p.Plan.Status == "applied" {
		return p.Plan, nil
	}
	if p.Plan.Status != "prepared" {
		return p.Plan, errors.New("partial operation requires recovery review; do not repeat blindly")
	}
	if p.Root != s.Root || p.RepoID != s.RepoID || p.PolicyDigest != policyDigest(s.Policy) {
		return p.Plan, ErrStale
	}
	if p.Plan.RequiresWriterStopped && !o.WriterStopped {
		return p.Plan, errors.New("artifact redaction requires explicit post-writer proof (--writer-stopped)")
	}
	err := lockx.WithDir(ctx, s.Dir, "hygiene", func() error {
		return gitx.WithLifecycleLock(ctx, s.Common, func() error {
			if err := s.checkInputs(ctx); err != nil {
				return err
			}
			if p.ScannerDigest != "" {
				cfg, ig, e := s.scannerInputs(ctx, p.Audit)
				if e != nil || scannerDigest(cfg, ig) != p.ScannerDigest {
					return ErrStale
				}
			}
			token, err := s.repoToken(ctx)
			if err != nil || token != p.RepoToken {
				return ErrStale
			}
			paths := []string{}
			for _, c := range p.Changes {
				paths = append(paths, c.Path)
			}
			if o.Guard != nil {
				if err := o.Guard(ctx, s.Root, paths); err != nil {
					return err
				}
			}
			editors := []configedit.Plan{}
			for _, c := range p.Changes {
				if !s.allowedTarget(p.Plan.Kind, c.Path) {
					return errors.New("plan target is outside its allowed scope")
				}
				editor, err := configedit.NewTextFile(ctx, c.Path, c.Desired)
				if err != nil {
					return ErrStale
				}
				token, err := editor.SourceToken(c.Path)
				if err != nil || token != c.Token {
					return ErrStale
				}
				editors = append(editors, editor)
			}
			if p.Plan.HookAction != "" {
				if err = s.checkHooksToken(ctx, p.HooksToken); err != nil {
					return err
				}
			}
			p.Plan.Status = "applying"
			if err = s.save(ctx, id, p); err != nil {
				return err
			}
			for i, editor := range editors {
				token, e := s.repoToken(ctx)
				if e != nil || token != p.RepoToken {
					return ErrStale
				}
				if e = s.checkInputs(ctx); e != nil {
					return e
				}
				if o.Guard != nil {
					if err = o.Guard(ctx, s.Root, paths[i:]); err != nil {
						return err
					}
				}
				for _, remaining := range editors[i:] {
					if err = remaining.Check(ctx); err != nil {
						return ErrStale
					}
				}
				result, e := configedit.Apply(ctx, editor, filepath.Join(s.Dir, "recovery"))
				if result.Receipt != "" {
					p.Plan.Recovery = append(p.Plan.Recovery, result.Receipt)
				}
				if e != nil {
					p.Plan.Status = "partial"
					_ = s.save(context.Background(), id, p)
					return errors.New("file apply interrupted; inspect private recovery receipt")
				}
				p.Plan.Completed = append(p.Plan.Completed, p.Plan.Files[i].File)
				if err = s.save(ctx, id, p); err != nil {
					return errors.New("file changed but ledger save failed; inspect recovery")
				}
			}
			if p.Plan.HookAction == "install-local" {
				if err = installHook(ctx, s.Root); err != nil {
					p.Plan.Status = "partial"
					_ = s.save(context.Background(), id, p)
					return err
				}
			}
			p.Plan.Status = "applied"
			return s.save(ctx, id, p)
		})
	})
	return p.Plan, err
}
func (s *Service) allowedTarget(kind, path string) bool {
	switch kind {
	case "hygiene_rules":
		return path == filepath.Join(s.Dir, "policy.toml")
	case "hygiene_setup":
		for _, rel := range []string{".pre-commit-config.yaml", ".gitleaks.toml", ".dev-cli/hygiene.toml"} {
			if path == filepath.Join(s.Root, filepath.FromSlash(rel)) {
				return true
			}
		}
	case "hygiene_redact":
		rel, err := filepath.Rel(s.Root, path)
		return err == nil && validRelative(filepath.ToSlash(rel))
	}
	return false
}
func (s *Service) Restore(ctx context.Context, receipt string, apply bool, o ApplyOptions) ([]configedit.Change, error) {
	plan, err := configedit.RestorePlan(ctx, filepath.Join(s.Dir, "recovery"), receipt)
	if err != nil {
		return nil, errors.New("recovery unavailable or source changed")
	}
	for _, c := range plan.Preview() {
		if !s.allowedTarget("hygiene_redact", c.Path) && !s.allowedTarget("hygiene_rules", c.Path) {
			return nil, errors.New("recovery path belongs to another checkout")
		}
	}
	if !apply {
		return plan.Preview(), nil
	}
	paths := []string{}
	for _, c := range plan.Preview() {
		paths = append(paths, c.Path)
		rel, _ := filepath.Rel(s.Root, c.Path)
		if isArtifact(filepath.ToSlash(rel)) && !o.WriterStopped {
			return nil, errors.New("artifact recovery requires explicit post-writer proof")
		}
	}
	err = gitx.WithLifecycleLock(ctx, s.Common, func() error {
		if o.Guard != nil {
			if e := o.Guard(ctx, s.Root, paths); e != nil {
				return e
			}
		}
		_, e := configedit.Apply(ctx, plan, filepath.Join(s.Dir, "recovery"))
		return e
	})
	return plan.Preview(), err
}
func (s *Service) ReadPlan(ctx context.Context, id string) (Plan, error) {
	var p planRecord
	err := s.load(ctx, id, &p)
	return p.Plan, err
}

// Settle bounds the second observation used by external manual dogfood.
func Settle(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}

func (s *Service) planRevision(p planRecord) string {
	b, _ := json.Marshal(struct {
		ID, Kind, Root, RepoID, RepoToken, PolicyDigest, HookAction, HooksToken, ScannerDigest, ReviewFile, ReviewDigest string
		Writer, Audit                                                                                                    bool
		Files                                                                                                            []FileChange
		Changes                                                                                                          []change
	}{p.Plan.ID, p.Plan.Kind, p.Root, p.RepoID, p.RepoToken, p.PolicyDigest, p.Plan.HookAction, p.HooksToken, p.ScannerDigest, p.Plan.ReviewFile, p.ReviewDigest, p.Plan.RequiresWriterStopped, p.Audit, p.Plan.Files, p.Changes})
	return keyedID(s.key, string(b))
}
func (s *Service) savePlan(ctx context.Context, p *planRecord) error {
	var review bytes.Buffer
	fmt.Fprintf(&review, "PRIVATE REVIEW: %s\nPlan: %s\nHook action: %s\nNever paste this file into chat, Git or CI logs. It may contain private values.\n", p.Plan.Kind, p.Plan.ID, p.Plan.HookAction)
	for _, c := range p.Changes {
		fmt.Fprintf(&review, "\n=== Proposed full content: %s ===\n", c.Path)
		review.Write(c.Desired)
		review.WriteByte('\n')
	}
	if review.Len() > MaxRecordBytes {
		return errors.New("review exceeds byte limit; select fewer files")
	}
	p.Plan.ReviewFile = p.Plan.ID + ".review.txt"
	p.ReviewDigest = keyedID(s.key, review.String())
	if err := fleet.WritePrivateConfigFile(filepath.Join(s.Dir, p.Plan.ReviewFile), review.Bytes(), false); err != nil {
		return err
	}
	p.Plan.Revision = s.planRevision(*p)
	return s.save(ctx, p.Plan.ID, *p)
}

// ReviewPath exposes only the explicitly requested private review location.
func (s *Service) ReviewPath(ctx context.Context, id string) (string, error) {
	if err := s.prepare(ctx); err != nil {
		return "", err
	}
	var p planRecord
	if err := s.load(ctx, id, &p); err != nil {
		return "", err
	}
	if p.Plan.ID != id || p.Plan.ReviewFile != id+".review.txt" || !hmac.Equal([]byte(p.Plan.Revision), []byte(s.planRevision(p))) {
		return "", ErrStale
	}
	path := filepath.Join(s.Dir, p.Plan.ReviewFile)
	body, err := safefile.ReadStablePath(ctx, path, MaxRecordBytes)
	if err != nil || keyedID(s.key, string(body)) != p.ReviewDigest {
		return "", ErrStale
	}
	return path, nil
}
