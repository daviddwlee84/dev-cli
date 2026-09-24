package prflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/submodule"
	"github.com/daviddwlee84/dev-cli/internal/task"
	"github.com/daviddwlee84/dev-cli/internal/wt"
)

var ErrNoLocalRepository = errors.New("no matching local repository; select a project clone destination or Try")

type DetailResolver interface {
	Detail(context.Context, Reference) (Detail, error)
}
type CheckoutGitRun func(context.Context, string, ...string) (string, error)

type checkoutSafeError struct{ cause error }

func (e *checkoutSafeError) Error() string { return gitx.SafeDiagnosticText(e.cause.Error()) }
func (e *checkoutSafeError) Unwrap() error { return e.cause }

type CheckoutConfig struct {
	Config         config.Config
	Tasks          *task.Store
	Experiments    *experiment.Service
	Resolver       DetailResolver
	Repositories   []string
	Run            CheckoutGitRun
	Log            io.Writer
	ProvisionGuard func(context.Context, string) error
	Acquire        func(context.Context, repo.AcquireRequest) (repo.AcquireResult, error)
}

type CheckoutService struct{ cfg CheckoutConfig }

func NewCheckoutService(cfg CheckoutConfig) *CheckoutService {
	if cfg.Run == nil {
		cfg.Run = gitx.RunUnattended
	}
	if cfg.Acquire == nil {
		cfg.Acquire = repo.Acquire
	}
	cfg.Repositories = append([]string(nil), cfg.Repositories...)
	return &CheckoutService{cfg: cfg}
}

type CheckoutRequest struct {
	Reference        Reference `json:"reference"`
	RepoPath         string    `json:"repo_path,omitempty"`
	SourceRemote     string    `json:"source_remote,omitempty"`
	CloneDestination string    `json:"clone_destination,omitempty"`
	Try              bool      `json:"try"`
	Provision        bool      `json:"provision"`
}

type LocalCheckout struct {
	RepoPath     string `json:"repo_path"`
	GitCommonDir string `json:"git_common_dir"`
	Path         string `json:"path,omitempty"`
	Branch       string `json:"branch,omitempty"`
	HeadOID      string `json:"head_oid,omitempty"`
	Dirty        bool   `json:"dirty"`
	Differs      bool   `json:"differs"`
	CatalogID    string `json:"catalog_id,omitempty"`
	Bound        bool   `json:"bound"`
}

type AmbiguousCheckoutError struct{ Candidates []LocalCheckout }

func (e *AmbiguousCheckoutError) Error() string {
	return "multiple matching local repositories or checkouts; select an exact --repo path"
}

type CheckoutPlan struct {
	Request              CheckoutRequest `json:"request"`
	Detail               Detail          `json:"detail"`
	RepoPath             string          `json:"repo_path"`
	Path                 string          `json:"path"`
	Branch               string          `json:"branch"`
	HeadOID              string          `json:"head_oid"`
	Reused               bool            `json:"reused"`
	Differs              bool            `json:"differs"`
	CatalogID            string          `json:"catalog_id,omitempty"`
	Clone                bool            `json:"clone"`
	Try                  bool            `json:"try"`
	Label                string          `json:"label"`
	common               string
	authority            string
	fetchURL             string
	fetchRef             string
	parent               string
	parentIdentity       string
	targetParent         string
	targetParentIdentity string
	seal                 string
	tryPlan              *experiment.CreatePlan
}

type CheckoutEffect struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}
type CheckoutResult struct {
	Path            string           `json:"path"`
	RepoPath        string           `json:"repo_path"`
	Label           string           `json:"label"`
	Branch          string           `json:"branch"`
	HeadOID         string           `json:"head_oid,omitempty"`
	ExpectedHeadOID string           `json:"expected_head_oid"`
	Reused          bool             `json:"reused"`
	Created         bool             `json:"created"`
	CatalogID       string           `json:"catalog_id,omitempty"`
	Differs         bool             `json:"differs"`
	PartialSuccess  bool             `json:"partial_success"`
	Effects         []CheckoutEffect `json:"effects"`
	Warnings        []string         `json:"warnings,omitempty"`
}

func checkoutDigest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (p CheckoutPlan) digest() string {
	// Observation timestamps and display state cannot redirect checkout. All
	// mutable provider and local inputs that authorize it are sealed here.
	return checkoutDigest([]any{p.Request, checkoutDetailIdentity(p.Detail), p.RepoPath, p.Path, p.Branch, p.HeadOID, p.Reused, p.Differs, p.CatalogID, p.Clone, p.Try, p.Label, p.common, p.authority, p.fetchURL, p.fetchRef, p.parent, p.parentIdentity, p.targetParent, p.targetParentIdentity})
}
func checkoutDetailIdentity(d Detail) any {
	return []any{d.Reference, d.AccountID, d.RepositoryID, d.HeadOID, d.BaseOID, d.HeadRepo, d.HeadBranch, d.BaseBranch, d.HeadURL, d.BaseURL}
}

func validateCheckoutDetail(d Detail, ref Reference) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Number <= 0 || d.Reference != ref || d.AccountID == "" || d.RepositoryID == "" {
		return errors.New("provider did not establish exact request/account/repository identity")
	}
	for _, oid := range []string{d.HeadOID, d.BaseOID} {
		if len(oid) != 40 && len(oid) != 64 {
			return errors.New("provider did not report an exact commit identity")
		}
		if _, err := hex.DecodeString(oid); err != nil {
			return errors.New("invalid provider commit identity")
		}
	}
	base := forge.ParseRemoteIdentity(d.BaseURL)
	if !samePRRepository(base, ref.Forge, ref.Host, ref.Repo) {
		return errors.New("provider base URL does not identify the requested repository")
	}
	if d.HeadBranch == "" || d.BaseBranch == "" {
		return errors.New("provider did not report exact head/base branches")
	}
	return nil
}

func samePRRepository(identity forge.RemoteIdentity, kind forge.Kind, host, name string) bool {
	return identity.Kind == kind && strings.EqualFold(identity.Host, host) && strings.EqualFold(identity.Name, name)
}

func (s *CheckoutService) Plan(ctx context.Context, req CheckoutRequest) (CheckoutPlan, error) {
	var p CheckoutPlan
	if s.cfg.Resolver == nil {
		return p, errors.New("PR checkout requires a detail resolver")
	}
	if s.cfg.Tasks == nil {
		return p, errors.New("PR checkout requires complete task inventory")
	}
	if req.Provision && s.cfg.ProvisionGuard == nil {
		return p, errors.New("PR provisioning requires a checkout occupancy guard")
	}
	if req.SourceRemote != "" && (req.Try || req.CloneDestination != "") {
		return p, errors.New("source-remote selects an existing local repository remote")
	}
	if req.Try && req.CloneDestination != "" || req.RepoPath != "" && (req.Try || req.CloneDestination != "") {
		return p, errors.New("select one local repository, project clone, or Try")
	}
	d, err := s.cfg.Resolver.Detail(ctx, req.Reference)
	if err != nil {
		return p, err
	}
	if err = validateCheckoutDetail(d, req.Reference); err != nil {
		return p, err
	}
	branch := "pr/" + string(req.Reference.Forge) + "/" + config.Slug(req.Reference.Repo) + "-" + strconv.Itoa(req.Reference.Number)
	p = CheckoutPlan{Request: req, Detail: d, Branch: branch, HeadOID: d.HeadOID, Clone: req.CloneDestination != "" || req.Try, Try: req.Try, fetchURL: d.BaseURL}
	p.fetchRef = prHeadRef(req.Reference)
	if p.Clone {
		if req.Try {
			if s.cfg.Experiments == nil {
				return p, errors.New("Try checkout requires the experiment service")
			}
			tp, err := s.cfg.Experiments.PlanCreate(ctx, experiment.CreateRequest{Name: config.Slug(req.Reference.Repo) + "-pr-" + strconv.Itoa(req.Reference.Number), Clone: d.BaseURL})
			if err != nil {
				return p, err
			}
			p.tryPlan = &tp
			p.RepoPath = tp.Path
			p.Path = tp.Path
		} else {
			p.RepoPath, err = pathx.Canonical(config.Expand(req.CloneDestination))
			if err != nil {
				return p, err
			}
			p.parent, p.parentIdentity, err = checkoutDestination(ctx, p.RepoPath)
			if err != nil {
				return p, err
			}
			p.Path, err = s.cfg.Config.WorktreePathFor(filepath.Base(p.RepoPath), p.RepoPath, branch, "")
			if err != nil {
				return p, err
			}
			if err = wt.ValidateTarget(p.Path, p.RepoPath); err != nil {
				return p, err
			}
		}
	} else {
		candidates, err := s.local(ctx, d, req.RepoPath)
		if err != nil {
			return p, err
		}
		var checkouts []LocalCheckout
		for _, c := range candidates {
			if c.Path != "" {
				checkouts = append(checkouts, c)
			}
		}
		if len(checkouts) > 1 {
			return p, &AmbiguousCheckoutError{Candidates: checkouts}
		}
		if len(checkouts) == 1 {
			c := checkouts[0]
			p.RepoPath, p.Path, p.Branch, p.common, p.HeadOID, p.CatalogID = c.RepoPath, c.Path, c.Branch, c.GitCommonDir, c.HeadOID, c.CatalogID
			p.Reused, p.Differs = true, c.Differs
		} else {
			if len(candidates) == 0 {
				return p, ErrNoLocalRepository
			}
			if len(candidates) > 1 {
				return p, &AmbiguousCheckoutError{Candidates: candidates}
			}
			c := candidates[0]
			p.RepoPath, p.common, p.CatalogID = c.RepoPath, c.GitCommonDir, c.CatalogID
			binding, _ := gitx.Run(ctx, p.RepoPath, "config", "--local", "--get-all", "branch."+branch+".dev-pr-url")
			if binding != "" && binding != req.Reference.URL() {
				return p, errors.New("PR branch binding conflicts with another source")
			}
			// A branch created by an earlier interrupted PR checkout can be
			// surfaced again, but never reset to the now-current remote head.
			if gitx.BranchExists(ctx, p.RepoPath, branch) {
				binding, _ := gitx.Run(ctx, p.RepoPath, "config", "--local", "--get-all", "branch."+branch+".dev-pr-url")
				if binding != req.Reference.URL() {
					return p, errors.New("PR branch name is occupied without this request's binding")
				}
				p.HeadOID, err = gitx.Run(ctx, p.RepoPath, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
				if err != nil {
					return p, err
				}
				p.Differs = p.HeadOID != d.HeadOID
			}
			p.Path, err = s.cfg.Config.WorktreePathFor(filepath.Base(p.RepoPath), p.RepoPath, branch, "")
			if err != nil {
				return p, err
			}
			if err = wt.ValidateTarget(p.Path, p.RepoPath); err != nil {
				return p, err
			}
		}
		p.fetchURL, err = selectCheckoutFetchURL(ctx, p.RepoPath, d, req.SourceRemote)
		if err != nil {
			return p, err
		}
		p.authority, err = s.checkoutAuthority(ctx, p, nil)
		if err != nil {
			return p, err
		}
	}
	if !p.Reused && !p.Try {
		p.targetParent, p.targetParentIdentity, err = checkoutDestination(ctx, p.Path)
		if err != nil {
			return p, err
		}
	}
	p.Label = filepath.Base(p.RepoPath) + "/" + p.Branch
	p.seal = p.digest()
	return p, nil
}

func prHeadRef(ref Reference) string {
	if ref.Forge == forge.GitLab {
		return "refs/merge-requests/" + strconv.Itoa(ref.Number) + "/head"
	}
	return "refs/pull/" + strconv.Itoa(ref.Number) + "/head"
}

func checkoutDestination(ctx context.Context, path string) (string, string, error) {
	if _, err := os.Lstat(path); err == nil {
		return "", "", fmt.Errorf("clone destination already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	parent := filepath.Dir(path)
	for {
		if _, err := os.Stat(parent); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", "", errors.New("no existing clone parent")
		}
		parent = next
	}
	parent, err := pathx.Canonical(parent)
	if err != nil {
		return "", "", err
	}
	if _, err := gitx.Discover(ctx, parent); err == nil {
		return "", "", errors.New("clone destination must be outside an existing repository")
	}
	id, err := gitx.DirectoryIdentity(parent)
	return parent, id, err
}

func (s *CheckoutService) checkoutAuthority(ctx context.Context, p CheckoutPlan, tx *task.Tx) (string, error) {
	g, err := gitx.Discover(ctx, p.RepoPath)
	if err != nil {
		return "", err
	}
	common, err := pathx.Canonical(g.GitCommonDir)
	if err != nil {
		return "", err
	}
	if common != p.common {
		return "", errors.New("repository identity changed; preview again")
	}
	id, err := gitx.DirectoryIdentity(common)
	if err != nil {
		return "", err
	}
	configuration, err := gitx.Run(ctx, p.RepoPath, "config", "--null", "--list")
	if err != nil {
		return "", err
	}
	worktrees, err := gitx.Worktrees(ctx, p.RepoPath)
	if err != nil {
		return "", err
	}
	var claims []task.Record
	if s.cfg.Tasks == nil {
		return "", errors.New("PR checkout requires complete task inventory")
	}
	var records []task.Record
	var diagnostics []task.Diagnostic
	if tx != nil {
		records, diagnostics, err = tx.ListRecords()
	} else {
		records, diagnostics, err = s.cfg.Tasks.ListRecords()
	}
	if err != nil {
		return "", err
	}
	if len(diagnostics) != 0 {
		return "", errors.New("task inventory is incomplete")
	}
	for _, record := range records {
		if record.Task.Branch != p.Branch && record.Task.WorktreePath != p.Path {
			continue
		}
		r, err := gitx.Discover(ctx, record.Task.RepoPath)
		if err != nil {
			return "", errors.New("task repository ownership could not be verified")
		}
		c, err := pathx.Canonical(r.GitCommonDir)
		if err != nil {
			return "", err
		}
		if c == common {
			claims = append(claims, record)
			if !p.Reused {
				return "", fmt.Errorf("branch is claimed by task %s; use its existing lifecycle", record.Task.ID)
			}
		}
	}
	if p.Reused {
		r, err := gitx.ResolveRegisteredWorktree(ctx, p.RepoPath, p.Path)
		if err != nil {
			return "", err
		}
		if r.Worktree.Prunable || r.Worktree.Branch != p.Branch || r.Worktree.Head != p.HeadOID {
			return "", errors.New("selected checkout changed; preview again")
		}
		if _, err := os.Stat(p.Path); err != nil {
			return "", err
		}
	}
	branchOID, _ := gitx.Run(ctx, p.RepoPath, "rev-parse", "--verify", "refs/heads/"+p.Branch+"^{commit}")
	contents := []string{}
	if p.Request.Provision {
		for _, path := range []string{p.RepoPath, p.Path} {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return "", err
			}
			snapshot, err := gitx.InspectTriageContents(ctx, path, nil)
			if err != nil {
				return "", err
			}
			contents = append(contents, path, snapshot.Fingerprint)
		}
	}
	return checkoutDigest([]any{common, id, configuration, worktrees, claims, branchOID, contents}), nil
}

func (s *CheckoutService) Apply(ctx context.Context, p CheckoutPlan) (result CheckoutResult, err error) {
	result = CheckoutResult{Path: p.Path, RepoPath: p.RepoPath, Label: p.Label, Branch: p.Branch, ExpectedHeadOID: p.HeadOID, Reused: p.Reused, Differs: p.Differs, CatalogID: p.CatalogID, Effects: []CheckoutEffect{}}
	verifiedPlan := false
	defer func() {
		if verifiedPlan {
			if head, probeErr := gitx.Run(ctx, result.Path, "rev-parse", "--verify", "HEAD^{commit}"); probeErr == nil {
				result.HeadOID = head
				result.Differs = head != p.Detail.HeadOID
			}
		}
		if err != nil {
			for _, effect := range result.Effects {
				if effect.Stage != "reuse" {
					result.PartialSuccess = true
					break
				}
			}
		}
		if err != nil {
			err = &checkoutSafeError{cause: err}
		}
	}()
	if p.seal == "" || p.seal != p.digest() {
		return result, errors.New("checkout plan changed; preview again")
	}
	verifiedPlan = true
	d, err := s.cfg.Resolver.Detail(ctx, p.Request.Reference)
	if err != nil {
		return result, err
	}
	if checkoutDigest(checkoutDetailIdentity(d)) != checkoutDigest(checkoutDetailIdentity(p.Detail)) {
		return result, errors.New("PR identity or head changed; preview again")
	}
	if p.Clone {
		if p.Try {
			if p.tryPlan == nil {
				return result, errors.New("missing sealed Try acquisition")
			}
			created, createErr := s.cfg.Experiments.ApplyCreate(ctx, *p.tryPlan)
			result.Created, result.CatalogID = created.Created, created.Item.ID
			if created.Created {
				result.Effects = append(result.Effects, CheckoutEffect{Stage: "clone", Status: "retained", Path: created.Path})
			}
			if created.Tracked {
				result.Effects = append(result.Effects, CheckoutEffect{Stage: "catalog", Status: "completed", Path: created.Path, Detail: created.Item.ID})
			}
			if createErr != nil {
				return result, createErr
			}
		} else {
			parent, id, e := checkoutDestination(ctx, p.RepoPath)
			if e != nil {
				return result, e
			}
			if parent != p.parent || id != p.parentIdentity {
				return result, errors.New("clone destination changed; preview again")
			}
			acquired, e := s.cfg.Acquire(ctx, repo.AcquireRequest{Kind: repo.AcquireClone, CloneRef: p.Detail.BaseURL, CloneRemote: "origin", Destination: p.RepoPath, Submodules: "none", Config: s.cfg.Config})
			if acquired.Created {
				result.Created = true
				result.Effects = append(result.Effects, CheckoutEffect{Stage: "clone", Status: "completed", Path: acquired.Path})
			}
			if e != nil {
				if _, statErr := os.Lstat(p.RepoPath); statErr == nil && !acquired.Created {
					result.Effects = append(result.Effects, CheckoutEffect{Stage: "clone", Status: "retained", Path: p.RepoPath})
				}
				return result, e
			}
		}
		g, e := gitx.Discover(ctx, p.RepoPath)
		if e != nil {
			return result, e
		}
		p.common, e = pathx.Canonical(g.GitCommonDir)
		if e != nil {
			return result, e
		}
	}
	err = gitx.WithLifecycleLock(ctx, p.common, func() error {
		if s.cfg.Tasks == nil {
			return errors.New("PR checkout requires task inventory")
		}
		return s.cfg.Tasks.WithLock(ctx, func(tx *task.Tx) error {
			if !p.Clone {
				authority, e := s.checkoutAuthority(ctx, p, tx)
				if e != nil {
					return e
				}
				if authority != p.authority {
					return errors.New("local checkout authority changed; preview again")
				}
			}
			if p.Clone {
				if _, e := s.checkoutAuthority(ctx, p, tx); e != nil {
					return e
				}
				if gitx.BranchExists(ctx, p.RepoPath, p.Branch) {
					return errors.New("new clone already has the reserved PR branch; retained for inspection")
				}
			}
			if !p.Try && !p.Reused {
				parent, id, e := checkoutDestination(ctx, p.Path)
				if e != nil {
					return e
				}
				if parent != p.targetParent || id != p.targetParentIdentity {
					return errors.New("worktree destination changed; preview again")
				}
			}
			if p.Reused {
				result.Effects = append(result.Effects, CheckoutEffect{Stage: "reuse", Status: "completed", Path: p.Path})
				if p.Request.Provision {
					return s.provisionCheckout(ctx, p, &result, tx)
				}
				return nil
			}
			resolvedURL, e := gitx.Run(ctx, p.RepoPath, "ls-remote", "--get-url", p.fetchURL)
			if e != nil {
				return e
			}
			if !samePRRepository(forge.ParseRemoteIdentity(resolvedURL), p.Request.Reference.Forge, p.Request.Reference.Host, p.Request.Reference.Repo) {
				return errors.New("Git URL rewriting redirects the PR repository; inspect configuration")
			}
			// Ref publication is isolated from user branches and every fetched
			// object is checked against the provider's reviewed immutable head.
			ref := "refs/dev/pr/" + string(p.Request.Reference.Forge) + "/" + config.Slug(p.Request.Reference.Repo) + "/" + strconv.Itoa(p.Request.Reference.Number)
			_, e = s.cfg.Run(ctx, p.RepoPath, "fetch", "--no-tags", "--no-recurse-submodules", "--", p.fetchURL, "+"+p.fetchRef+":"+ref)
			if e != nil {
				return e
			}
			result.Effects = append(result.Effects, CheckoutEffect{Stage: "fetch", Status: "completed", Path: p.RepoPath})
			head, e := gitx.Run(ctx, p.RepoPath, "rev-parse", "--verify", ref+"^{commit}")
			if e != nil {
				return e
			}
			if head != p.Detail.HeadOID {
				return errors.New("fetched PR head differs from the reviewed commit; fetch retained, preview again")
			}
			binding, _ := gitx.Run(ctx, p.RepoPath, "config", "--local", "--get-all", "branch."+p.Branch+".dev-pr-url")
			if binding != "" && binding != p.Request.Reference.URL() {
				return errors.New("PR branch binding conflicts with another source")
			}
			if _, e = s.cfg.Run(ctx, p.RepoPath, "config", "--local", "--replace-all", "branch."+p.Branch+".dev-pr-url", p.Request.Reference.URL()); e != nil {
				return e
			}
			result.Effects = append(result.Effects, CheckoutEffect{Stage: "binding", Status: "completed", Path: p.Path})
			if p.Try {
				if _, e = s.cfg.Run(ctx, p.RepoPath, "switch", "-c", p.Branch, p.Detail.HeadOID); e != nil {
					return e
				}
				result.Created = true
				result.Effects = append(result.Effects, CheckoutEffect{Stage: "checkout", Status: "completed", Path: p.Path})
			} else {
				m := wt.Manager{Cfg: s.cfg.Config, Log: s.cfg.Log}
				created, e := m.Create(ctx, wt.CreateRequest{LockHeld: true, RepoPath: p.RepoPath, RepoName: filepath.Base(p.RepoPath), Branch: p.Branch, Base: p.HeadOID, Path: p.Path, Label: p.Label, NoProvision: true, NoRuntime: true, Submodules: "none"})
				if created != nil {
					result.Created = true
					result.Path = created.Path
					result.Effects = append(result.Effects, CheckoutEffect{Stage: "checkout", Status: "completed", Path: created.Path})
				}
				if e != nil {
					return e
				}
			}
			if p.Request.Provision {
				return s.provisionCheckout(ctx, p, &result, tx)
			}
			return nil
		})
	})
	if result.Differs {
		result.Warnings = append(result.Warnings, "local checkout differs from the current PR head; existing commits and edits were retained")
	}
	return result, err
}

func (s *CheckoutService) provisionCheckout(ctx context.Context, p CheckoutPlan, result *CheckoutResult, tx *task.Tx) error {
	if s.cfg.ProvisionGuard == nil {
		return errors.New("PR provisioning requires a checkout occupancy guard")
	}
	selected := p
	selected.Reused = true
	selected.Path = result.Path
	before, err := s.checkoutAuthority(ctx, selected, tx)
	if err != nil {
		return err
	}
	if err = s.cfg.ProvisionGuard(ctx, result.Path); err != nil {
		return err
	}
	after, err := s.checkoutAuthority(ctx, selected, tx)
	if err != nil {
		return err
	}
	if after != before {
		return errors.New("checkout changed during provisioning guard; preview again")
	}
	settings, err := wt.SettingsForTrusted(ctx, s.cfg.Config, result.Path)
	if err != nil {
		return err
	}
	effective, err := submodule.Settings(s.cfg.Config, result.Path, "")
	if err != nil {
		return err
	}
	result.Effects = append(result.Effects, CheckoutEffect{Stage: "submodules", Status: "attempted", Path: result.Path, Detail: effective.Init})
	if _, err = submodule.Prepare(ctx, s.cfg.Config, result.Path, effective.Init, nil, nil, false); err != nil {
		result.Effects[len(result.Effects)-1].Status = "failed"
		return err
	}
	result.Effects[len(result.Effects)-1].Status = "completed"
	settings.Submodules = config.Submodules{Init: "none"}
	if p.Try || result.Path == p.RepoPath {
		settings.Include = nil
		settings.Link = nil
		settings.Strategy = wt.Reinstall
		settings.Strategies = nil
	}
	provisioner := wt.Provisioner{Settings: settings, Timeout: settings.ProvisionTimeout, Log: s.cfg.Log}
	result.Effects = append(result.Effects, CheckoutEffect{Stage: "provision", Status: "attempted", Path: result.Path})
	provision, err := provisioner.Apply(ctx, buildPRProvisionPlan(ctx, settings, p.RepoPath, result.Path), p.RepoPath, result.Path)
	if err != nil || len(provision.Failures) > 0 {
		result.Effects[len(result.Effects)-1].Status = "failed"
		return errors.Join(append([]error{err}, provision.Failures...)...)
	}
	result.Effects[len(result.Effects)-1].Status = "completed"
	return nil
}

// A PR can introduce an ecosystem absent from the canonical branch. Discover
// its install commands in the selected checkout while copying ignored inputs
// only from the canonical source that the user configured.
func buildPRProvisionPlan(ctx context.Context, settings wt.Settings, source, target string) wt.Plan {
	targetPlan := wt.BuildPlan(ctx, settings, target)
	sourcePlan := wt.BuildPlan(ctx, settings, source)
	steps := []wt.Step{}
	if source != target {
		for _, step := range sourcePlan.Steps {
			if step.Kind == wt.StepCopyFile || step.Kind == wt.StepLinkDir && step.Ecosystem == "" {
				steps = append(steps, step)
			}
		}
	}
	for _, step := range targetPlan.Steps {
		if step.Kind == wt.StepRun || step.Ecosystem != "" {
			if step.Kind == wt.StepCopyDir || step.Kind == wt.StepLinkDir {
				_, err := os.Stat(filepath.Join(source, step.What))
				step.Skipped = err != nil
				if err != nil {
					step.Why = "dependency directory is not available in the canonical source"
				}
			}
			steps = append(steps, step)
		}
	}
	targetPlan.Steps = steps
	return targetPlan
}
