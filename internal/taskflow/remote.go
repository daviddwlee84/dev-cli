package taskflow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

var fullGitOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type remoteRefreshObservation struct {
	record *task.Record

	repo         gitx.Repo
	repoPath     string
	gitCommonDir string
	checkout     string
	worktree     gitx.RegisteredWorktree

	remoteName string
	remoteURL  string
	head       string
	base       string

	providerKind       forge.Kind
	providerRepository string
	provider           forge.Forge
	providerResolveErr error
	providerCapable    bool
	providerAvailable  bool

	refs RemoteRefsObservation
}

func (s *lifecycleService) refreshRemoteHandler() Handler {
	return Handler{Plan: s.planRefreshRemote, Apply: s.applyRefreshRemote}
}

func validateRefreshRemoteLocator(locator Locator) error {
	switch {
	case locator.RepoPath == "":
		return fmt.Errorf("%w: exact repository path is required", ErrInvalidRequest)
	case locator.GitCommonDir == "":
		return fmt.Errorf("%w: exact Git common directory is required", ErrInvalidRequest)
	case !normalizedRemoteBranch(locator.Branch):
		return fmt.Errorf("%w: exact head branch %q is not normalized", ErrInvalidRequest, locator.Branch)
	case locator.Base != "" && !normalizedRemoteBranch(locator.Base):
		return fmt.Errorf("%w: review base %q is not normalized", ErrInvalidRequest, locator.Base)
	case locator.HeadOID != "" && !fullGitOID.MatchString(locator.HeadOID):
		return fmt.Errorf("%w: exact HEAD OID %q is not normalized", ErrInvalidRequest, locator.HeadOID)
	case locator.BaseOID != "" && !fullGitOID.MatchString(locator.BaseOID):
		return fmt.Errorf("%w: exact base OID %q is not normalized", ErrInvalidRequest, locator.BaseOID)
	}

	if locator.TaskID == "" {
		switch {
		case locator.TaskRevision != "":
			return fmt.Errorf("%w: task revision requires a TaskID", ErrInvalidRequest)
		case locator.HeadOID == "":
			return fmt.Errorf("%w: unmanaged remote refresh requires an exact HEAD OID", ErrInvalidRequest)
		case locator.Mode != "" && !validMode(locator.Mode):
			return fmt.Errorf("%w: unknown checkout mode %q", ErrInvalidRequest, locator.Mode)
		case locator.State != "" && !validState(locator.State):
			return fmt.Errorf("%w: unknown task state %q", ErrInvalidRequest, locator.State)
		}
		return nil
	}

	switch {
	case !exactTaskID.MatchString(locator.TaskID) || filepath.Base(locator.TaskID) != locator.TaskID || locator.TaskID == "." || locator.TaskID == "..":
		return fmt.Errorf("%w: invalid exact TaskID %q", ErrInvalidRequest, locator.TaskID)
	case locator.TaskRevision == "":
		return fmt.Errorf("%w: exact task revision is required", ErrInvalidRequest)
	case !validMode(locator.Mode):
		return fmt.Errorf("%w: exact checkout mode is required", ErrInvalidRequest)
	case !validState(locator.State):
		return fmt.Errorf("%w: exact task state is required", ErrInvalidRequest)
	}
	return nil
}

func normalizedRemoteBranch(branch string) bool {
	return branch != "" && branch == strings.TrimSpace(branch) &&
		!strings.ContainsRune(branch, '\x00') &&
		!strings.HasPrefix(branch, "refs/heads/") &&
		!strings.HasPrefix(branch, "refs/remotes/")
}

func normalizeRemoteName(name string) (string, error) {
	if name == "" {
		return "origin", nil
	}
	if name != strings.TrimSpace(name) || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("%w: remote name %q is not normalized", ErrInvalidRequest, name)
	}
	return name, nil
}

func (s *lifecycleService) planRefreshRemote(ctx context.Context, request Request) (PlanSpec, error) {
	if err := contextError(ctx); err != nil {
		return PlanSpec{}, err
	}
	if err := validateRefreshRemoteLocator(request.Locator); err != nil {
		return PlanSpec{}, err
	}

	if request.Locator.TaskID == "" {
		spec, _, err := s.observeRefreshRemote(ctx, request, nil)
		return spec, err
	}

	record, err := s.tasks.GetRecord(request.Locator.TaskID)
	if err != nil {
		return PlanSpec{}, fmt.Errorf("%w: load exact task %q: %v", ErrInvalidRequest, request.Locator.TaskID, err)
	}
	spec, _, err := s.observeRefreshRemote(ctx, request, record)
	if err != nil {
		return PlanSpec{}, err
	}
	current, err := s.tasks.GetRecord(request.Locator.TaskID)
	if err != nil {
		return PlanSpec{}, &StalePlanError{Reason: "task record disappeared while planning remote refresh"}
	}
	if current.Revision != record.Revision {
		return PlanSpec{}, staleTaskRevision(record.Revision, current.Revision, "task record changed while planning remote refresh")
	}
	return spec, nil
}

func (s *lifecycleService) observeRefreshRemote(ctx context.Context, request Request, record *task.Record) (PlanSpec, remoteRefreshObservation, error) {
	observed := remoteRefreshObservation{record: record, head: request.Locator.Branch, base: request.Locator.Base}
	if err := validateRefreshRemoteLocator(request.Locator); err != nil {
		return PlanSpec{}, observed, err
	}
	if record != nil {
		if err := validateRecordIdentity(request.Locator, *record); err != nil {
			return PlanSpec{}, observed, err
		}
	}

	remoteName, err := normalizeRemoteName(request.Locator.Remote)
	if err != nil {
		return PlanSpec{}, observed, err
	}
	observed.remoteName = remoteName

	repository, err := s.gitDiscover(ctx, request.Locator.RepoPath)
	if err != nil {
		return PlanSpec{}, observed, fmt.Errorf("%w: discover selected repository: %v", ErrInvalidRequest, err)
	}
	observed.repo = repository
	observed.repoPath, err = s.canonicalPath(repository.MainRoot)
	if err != nil {
		return PlanSpec{}, observed, fmt.Errorf("%w: canonicalize Git main checkout: %v", ErrInvalidRequest, err)
	}
	observed.gitCommonDir, err = s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		return PlanSpec{}, observed, fmt.Errorf("%w: canonicalize Git common directory: %v", ErrInvalidRequest, err)
	}
	requestedRepo, err := s.canonicalPath(request.Locator.RepoPath)
	if err != nil {
		return PlanSpec{}, observed, fmt.Errorf("%w: canonicalize requested repository: %v", ErrInvalidRequest, err)
	}
	requestedCommon, err := s.canonicalPath(request.Locator.GitCommonDir)
	if err != nil {
		return PlanSpec{}, observed, fmt.Errorf("%w: canonicalize requested Git common directory: %v", ErrInvalidRequest, err)
	}
	if requestedRepo != observed.repoPath || requestedCommon != observed.gitCommonDir {
		return PlanSpec{}, observed, &StalePlanError{Reason: fmt.Sprintf(
			"selected repository identity changed: main %q -> %q, common directory %q -> %q",
			requestedRepo, observed.repoPath, requestedCommon, observed.gitCommonDir,
		)}
	}
	if filepath.IsAbs(request.Locator.RepoKey) {
		requestedKey, keyErr := s.canonicalPath(request.Locator.RepoKey)
		if keyErr != nil {
			return PlanSpec{}, observed, fmt.Errorf("%w: canonicalize requested repository key: %v", ErrInvalidRequest, keyErr)
		}
		if requestedKey != observed.gitCommonDir {
			return PlanSpec{}, observed, &StalePlanError{Reason: "selected repository key no longer names the Git common directory"}
		}
	}

	if err := s.validateRemoteRefNames(ctx, observed.repoPath, remoteName, observed.head, observed.base); err != nil {
		return PlanSpec{}, observed, err
	}
	if record == nil {
		if err := s.validateRefreshCheckout(ctx, request.Locator, &observed); err != nil {
			return PlanSpec{}, observed, err
		}
	}

	observed.remoteURL = strings.TrimSpace(s.gitRemote(ctx, observed.repoPath, observed.remoteName))
	observed.providerKind, observed.providerRepository = forge.IdentityFromURL(observed.remoteURL)
	options := request.Options.(RefreshRemoteOptions)
	if options.QueryReview && observed.remoteURL != "" && observed.providerKind != forge.Unknown && observed.providerRepository != "" {
		observed.provider, observed.providerResolveErr = s.resolveForge(observed.providerKind)
		if observed.providerResolveErr == nil {
			switch {
			case observed.provider == nil:
				observed.providerResolveErr = errors.New("forge resolver returned no provider")
			case observed.provider.Kind() != observed.providerKind:
				observed.providerResolveErr = fmt.Errorf("forge resolver returned %s for %s remote", observed.provider.Kind(), observed.providerKind)
			default:
				_, observed.providerCapable = observed.provider.(forge.ReviewQuerier)
				observed.providerAvailable = observed.provider.Available()
			}
		}
	}

	observed.refs = s.observeRemoteRefs(ctx, observed.repoPath, observed.remoteName, observed.head, observed.base)
	if request.Locator.HeadOID != "" {
		if observed.refs.LocalHead.Failure != "" || !observed.refs.LocalHead.Exists || observed.refs.LocalHead.OID != request.Locator.HeadOID {
			return PlanSpec{}, observed, &StalePlanError{Reason: fmt.Sprintf(
				"selected head changed from %q to %q", request.Locator.HeadOID, observed.refs.LocalHead.OID,
			)}
		}
	}
	if request.Locator.BaseOID != "" {
		if observed.refs.LocalBase.Failure != "" || !observed.refs.LocalBase.Exists || observed.refs.LocalBase.OID != request.Locator.BaseOID {
			return PlanSpec{}, observed, &StalePlanError{Reason: fmt.Sprintf(
				"selected base changed from %q to %q", request.Locator.BaseOID, observed.refs.LocalBase.OID,
			)}
		}
	}
	return s.refreshRemoteSpec(request, observed), observed, nil
}

func (s *lifecycleService) validateRemoteRefNames(ctx context.Context, repoPath, remote, head, base string) error {
	for label, ref := range map[string]string{
		"head":        "refs/heads/" + head,
		"remote name": "refs/remotes/" + remote + "/__dev_refresh_probe__",
	} {
		if _, err := s.gitRun(ctx, repoPath, "check-ref-format", ref); err != nil {
			return fmt.Errorf("%w: %s is not a valid normalized Git name: %v", ErrInvalidRequest, label, err)
		}
	}
	if base != "" {
		if _, err := s.gitRun(ctx, repoPath, "check-ref-format", "refs/heads/"+base); err != nil {
			return fmt.Errorf("%w: review base is not a valid normalized Git branch: %v", ErrInvalidRequest, err)
		}
	}
	return nil
}

func (s *lifecycleService) validateRefreshCheckout(ctx context.Context, locator Locator, observed *remoteRefreshObservation) error {
	checkout := locator.CheckoutPath
	if checkout == "" {
		checkout = locator.RepoPath
	}
	canonical, err := s.canonicalPath(checkout)
	if err != nil {
		return fmt.Errorf("%w: canonicalize selected checkout: %v", ErrInvalidRequest, err)
	}
	registered, err := s.resolveWorktree(ctx, observed.repoPath, canonical)
	if err != nil {
		return &StalePlanError{Reason: "selected checkout registration changed: " + err.Error()}
	}
	if registered.Path != canonical || registered.RepositoryPath != observed.repoPath || registered.GitCommonDir != observed.gitCommonDir {
		return &StalePlanError{Reason: "selected checkout no longer belongs to the exact repository identity"}
	}
	if registered.Worktree.Detached || registered.Worktree.Branch != locator.Branch {
		return &StalePlanError{Reason: fmt.Sprintf(
			"selected checkout branch changed from %q to %q", locator.Branch, registered.Worktree.Branch,
		)}
	}
	liveHead, err := s.gitRun(ctx, canonical, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return &StalePlanError{Reason: "selected checkout HEAD is no longer observable: " + err.Error()}
	}
	liveHead = strings.TrimSpace(liveHead)
	if registered.Worktree.Head != liveHead || locator.HeadOID != liveHead {
		return &StalePlanError{Reason: fmt.Sprintf(
			"selected checkout HEAD changed from %q to registry=%q live=%q", locator.HeadOID, registered.Worktree.Head, liveHead,
		)}
	}
	observed.checkout = canonical
	observed.worktree = registered
	return nil
}

func (s *lifecycleService) observeRemoteRefs(ctx context.Context, repoPath, remote, head, base string) RemoteRefsObservation {
	result := RemoteRefsObservation{ObservedAt: s.now()}
	result.LocalHead = s.observeNamedRef(ctx, repoPath, "refs/heads/"+head)
	result.RemoteHead = s.observeNamedRef(ctx, repoPath, "refs/remotes/"+remote+"/"+head)
	if base != "" {
		result.LocalBase = s.observeNamedRef(ctx, repoPath, "refs/heads/"+base)
		result.RemoteBase = s.observeNamedRef(ctx, repoPath, "refs/remotes/"+remote+"/"+base)
	}
	return result
}

func (s *lifecycleService) observeNamedRef(ctx context.Context, repoPath, ref string) NamedRefObservation {
	observed := NamedRefObservation{Ref: ref}
	exists, err := s.gitRefState(ctx, repoPath, ref)
	if err != nil {
		observed.Failure = err.Error()
		return observed
	}
	if !exists {
		return observed
	}
	observed.Exists = true
	oid, err := s.resolveRefOID(ctx, repoPath, ref)
	if err != nil {
		observed.Failure = err.Error()
		return observed
	}
	observed.OID = oid
	return observed
}

func (s *lifecycleService) refreshRemoteSpec(request Request, observed remoteRefreshObservation) PlanSpec {
	options := request.Options.(RefreshRemoteOptions)
	conditions := []Condition{
		refreshTaskCondition(observed),
		condition(ConditionRepoIdentity, VerdictMet, RequirementRequired,
			"Git main "+observed.repoPath+" with common directory "+observed.gitCommonDir, ""),
		refreshHeadCondition(observed),
		refreshRemoteURLCondition(observed),
		refreshRefsCondition(observed),
		refreshReviewBaseCondition(observed, options),
		refreshReviewProviderCondition(observed, options),
		refreshReviewCapabilityCondition(observed, options),
		refreshReviewCLICondition(observed, options),
	}

	var effects []Effect
	if options.FetchRefs {
		effects = append(effects, NewEffect(
			EffectFetchRefs, "fetch and prune the exact configured remote", observed.repoPath, false, true,
			map[string]string{
				"remote": observed.remoteName, "remote-url": observed.remoteURL,
				"pre-refs": remoteRefsAuthority(observed.refs),
			},
		))
	}
	if options.QueryReview {
		effects = append(effects, NewEffect(
			EffectQueryReview, "query the exact provider repository and head/base relationship", observed.providerRepository, false, true,
			map[string]string{
				"remote": observed.remoteName, "remote-url": observed.remoteURL,
				"provider": string(observed.providerKind), "repository": observed.providerRepository,
				"head": observed.head, "base": observed.base,
			},
		))
	}

	retained := []string{"branch:" + observed.head + "@" + observed.refs.LocalHead.OID}
	if observed.checkout != "" {
		retained = append(retained, "checkout:"+observed.checkout)
	}
	return PlanSpec{
		Authority:         refreshRemoteAuthority(observed),
		Conditions:        conditions,
		Effects:           effects,
		RetainedResources: retained,
		Confirmation: Confirmation{
			Kind: ConfirmationApproval, Prompt: "Run the selected explicit remote network operations?",
		},
		Summary:     refreshRemoteSummary(observed, options),
		DisplayedAt: s.now(),
	}
}

func refreshTaskCondition(observed remoteRefreshObservation) Condition {
	if observed.record == nil {
		return condition(ConditionTaskCurrent, VerdictMet, RequirementAdvisory,
			"repository/checkout context has no persisted task record", "")
	}
	return condition(ConditionTaskCurrent, VerdictMet, RequirementRequired,
		"task "+observed.record.Task.ID+" revision "+observed.record.Revision+" is validated without a state write", "")
}

func refreshHeadCondition(observed remoteRefreshObservation) Condition {
	ref := observed.refs.LocalHead
	switch {
	case ref.Failure != "":
		return condition(ConditionBranchRef, VerdictError, RequirementRequired, ref.Failure, "repair local head-ref observation")
	case !ref.Exists:
		return condition(ConditionBranchRef, VerdictBlocked, RequirementRequired,
			"local head ref "+ref.Ref+" does not exist", "restore the exact named local branch")
	case ref.OID == "":
		return condition(ConditionBranchRef, VerdictError, RequirementRequired,
			"local head ref resolved without an OID", "repair local head-ref observation")
	default:
		return condition(ConditionBranchRef, VerdictMet, RequirementRequired,
			ref.Ref+" is exactly "+ref.OID, "")
	}
}

func refreshRemoteURLCondition(observed remoteRefreshObservation) Condition {
	if observed.remoteURL == "" {
		return condition(ConditionRemoteURL, VerdictBlocked, RequirementRequired,
			"remote "+observed.remoteName+" has no configured URL", "configure the exact remote before network refresh")
	}
	return condition(ConditionRemoteURL, VerdictMet, RequirementRequired,
		"remote "+observed.remoteName+" is configured as "+observed.remoteURL, "")
}

func refreshRefsCondition(observed remoteRefreshObservation) Condition {
	verdict := VerdictMet
	for _, ref := range []NamedRefObservation{
		observed.refs.LocalHead, observed.refs.LocalBase, observed.refs.RemoteHead, observed.refs.RemoteBase,
	} {
		if ref.Failure != "" {
			verdict = VerdictError
			break
		}
	}
	return condition(ConditionRemoteRefs, verdict, RequirementAdvisory,
		remoteRefsEvidence(observed.refs), "repair local Git ref observation before relying on freshness")
}

func refreshReviewBaseCondition(observed remoteRefreshObservation, options RefreshRemoteOptions) Condition {
	if !options.QueryReview {
		return condition(ConditionReviewBase, VerdictMet, RequirementAdvisory,
			"review query is not selected; an explicit base is not required", "")
	}
	if observed.base == "" {
		return condition(ConditionReviewBase, VerdictNeedsInput, RequirementRequired,
			"review query requires an exact base branch", "select the base from the current repository context")
	}
	return condition(ConditionReviewBase, VerdictMet, RequirementRequired,
		"review base is "+observed.base, "")
}

func refreshReviewProviderCondition(observed remoteRefreshObservation, options RefreshRemoteOptions) Condition {
	requirement := RequirementAdvisory
	if options.QueryReview {
		requirement = RequirementRequired
	}
	if observed.providerKind == forge.Unknown || observed.providerRepository == "" {
		return condition(ConditionReviewProvider, VerdictUnsupported, requirement,
			"remote URL does not identify a supported GitHub, GitLab, or Azure DevOps repository", "select fetch-only or configure a supported provider remote")
	}
	return condition(ConditionReviewProvider, VerdictMet, requirement,
		fmt.Sprintf("%s repository identity %s", observed.providerKind, observed.providerRepository), "")
}

func refreshReviewCapabilityCondition(observed remoteRefreshObservation, options RefreshRemoteOptions) Condition {
	if !options.QueryReview {
		return condition(ConditionReviewCapability, VerdictMet, RequirementAdvisory,
			"review query is not selected; forge capability is not required", "")
	}
	if observed.providerResolveErr != nil {
		return condition(ConditionReviewCapability, VerdictUnsupported, RequirementRequired,
			observed.providerResolveErr.Error(), "use fetch-only or install support for this provider")
	}
	if observed.provider == nil || !observed.providerCapable {
		return condition(ConditionReviewCapability, VerdictUnsupported, RequirementRequired,
			fmt.Sprintf("%s adapter does not implement review queries", observed.providerKind), "use fetch-only or a review-capable provider")
	}
	return condition(ConditionReviewCapability, VerdictMet, RequirementRequired,
		fmt.Sprintf("%s adapter implements review queries", observed.providerKind), "")
}

func refreshReviewCLICondition(observed remoteRefreshObservation, options RefreshRemoteOptions) Condition {
	if !options.QueryReview {
		return condition(ConditionReviewCLI, VerdictMet, RequirementAdvisory,
			"review query is not selected; no provider CLI is required", "")
	}
	if observed.provider == nil || observed.providerResolveErr != nil || !observed.providerCapable {
		return condition(ConditionReviewCLI, VerdictUnsupported, RequirementRequired,
			"review-capable provider CLI could not be selected", "resolve provider capability first")
	}
	if !observed.providerAvailable {
		return condition(ConditionReviewCLI, VerdictUnsupported, RequirementRequired,
			fmt.Sprintf("%s provider CLI %s is not installed", observed.providerKind, observed.provider.Bin()), "install the provider CLI and retry")
	}
	return condition(ConditionReviewCLI, VerdictMet, RequirementRequired,
		fmt.Sprintf("%s provider CLI %s is locally available", observed.providerKind, observed.provider.Bin()), "")
}

func refreshRemoteAuthority(observed remoteRefreshObservation) map[string]string {
	authority := map[string]string{
		"remote.repo-main":          observed.repoPath,
		"remote.git-common-dir":     observed.gitCommonDir,
		"remote.checkout":           observed.checkout,
		"remote.worktree":           worktreeAuthority(observed.worktree, observed.checkout != "", nil, 0),
		"remote.name":               observed.remoteName,
		"remote.url":                observed.remoteURL,
		"remote.head":               observed.head,
		"remote.base":               observed.base,
		"remote.provider":           string(observed.providerKind),
		"remote.repository":         observed.providerRepository,
		"remote.provider-bin":       reviewProviderBin(observed.provider),
		"remote.provider-error":     errorString(observed.providerResolveErr),
		"remote.provider-capable":   boolString(observed.providerCapable),
		"remote.provider-available": boolString(observed.providerAvailable),
		"remote.refs":               remoteRefsAuthority(observed.refs),
	}
	if observed.record != nil {
		authority["remote.task-id"] = observed.record.Task.ID
		authority["remote.task-revision"] = observed.record.Revision
		authority["remote.task-mode"] = string(observed.record.Task.EffectiveMode())
		authority["remote.task-state"] = string(observed.record.Task.State)
	} else {
		authority["remote.task-id"] = ""
		authority["remote.task-revision"] = ""
		authority["remote.task-mode"] = ""
		authority["remote.task-state"] = ""
	}
	return authority
}

func remoteRefsAuthority(refs RemoteRefsObservation) string {
	values := make([]string, 0, 16)
	for _, ref := range []NamedRefObservation{refs.LocalHead, refs.LocalBase, refs.RemoteHead, refs.RemoteBase} {
		values = append(values, ref.Ref, boolString(ref.Exists), ref.OID, ref.Failure)
	}
	return authorityHash("taskflow-remote-refs-v1", values...)
}

func localRemoteRefsAuthority(refs RemoteRefsObservation) string {
	values := make([]string, 0, 8)
	for _, ref := range []NamedRefObservation{refs.LocalHead, refs.LocalBase} {
		values = append(values, ref.Ref, boolString(ref.Exists), ref.OID, ref.Failure)
	}
	return authorityHash("taskflow-remote-local-refs-v1", values...)
}

func remoteRefsEvidence(refs RemoteRefsObservation) string {
	parts := make([]string, 0, 4)
	for _, ref := range []NamedRefObservation{refs.LocalHead, refs.LocalBase, refs.RemoteHead, refs.RemoteBase} {
		if ref.Ref == "" {
			continue
		}
		switch {
		case ref.Failure != "":
			parts = append(parts, ref.Ref+" error: "+ref.Failure)
		case ref.Exists:
			parts = append(parts, ref.Ref+"="+ref.OID)
		default:
			parts = append(parts, ref.Ref+" is absent")
		}
	}
	return strings.Join(parts, "; ")
}

func refreshRemoteSummary(observed remoteRefreshObservation, options RefreshRemoteOptions) string {
	operation := "fetch refs and query review"
	switch {
	case options.FetchRefs && !options.QueryReview:
		operation = "fetch refs"
	case !options.FetchRefs && options.QueryReview:
		operation = "query review"
	}
	return operation + " for " + observed.head + " via " + observed.remoteName
}

func (s *lifecycleService) applyRefreshRemote(ctx context.Context, approved Plan) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if approved.Action != RefreshRemote {
		return Result{}, &InvalidPlanError{PlanID: approved.PlanID, Reason: "refresh-remote handler received " + string(approved.Action)}
	}
	if err := validateRefreshRemoteLocator(approved.Locator); err != nil {
		return Result{}, err
	}
	commonDir, err := s.canonicalPath(approved.Locator.GitCommonDir)
	if err != nil {
		return Result{}, fmt.Errorf("%w: canonicalize approved repository lock identity: %v", ErrInvalidPlan, err)
	}

	var result Result
	applyLocked := func(tx *task.Tx) error {
		var record *task.Record
		if approved.Locator.TaskID != "" {
			if tx == nil {
				return &InvalidPlanError{PlanID: approved.PlanID, Reason: "managed remote refresh has no task transaction"}
			}
			current, loadErr := tx.GetRecord(approved.Locator.TaskID)
			if loadErr != nil {
				return &StalePlanError{ExpectedPlanID: approved.PlanID, Reason: "task record disappeared before remote refresh"}
			}
			if identityErr := validateRecordIdentity(approved.Locator, *current); identityErr != nil {
				return identityErr
			}
			record = current
		}

		spec, observed, observeErr := s.observeRefreshRemote(ctx, approved.Request, record)
		if observeErr != nil {
			return observeErr
		}
		fresh, buildErr := BuildPlan(approved.Request, spec)
		if buildErr != nil {
			return buildErr
		}
		if record != nil {
			current, reloadErr := tx.GetRecord(record.Task.ID)
			if reloadErr != nil || current.Revision != record.Revision {
				actual := ""
				if current != nil {
					actual = current.Revision
				}
				return staleTaskRevision(record.Revision, actual, "task record changed during locked remote refresh replan")
			}
		}
		if fresh.PlanID != approved.PlanID || fresh.AuthorityFingerprint != approved.AuthorityFingerprint {
			return &StalePlanError{
				ExpectedPlanID: approved.PlanID, ActualPlanID: fresh.PlanID,
				ExpectedAuthorityFingerprint: approved.AuthorityFingerprint,
				ActualAuthorityFingerprint:   fresh.AuthorityFingerprint,
				Reason:                       "fresh task, repository, remote URL, provider, or ref authority differs",
			}
		}
		if fresh.Availability != AvailabilityReady {
			notReady := &PlanNotReadyError{PlanID: fresh.PlanID, Availability: fresh.Availability, conditions: fresh.Conditions()}
			return &InvalidPlanError{PlanID: fresh.PlanID, Reason: "fresh remote refresh conditions are not ready", Cause: notReady}
		}

		execution := &executionState{service: s, plan: fresh}
		result, observeErr = execution.executeRefreshRemote(ctx, observed)
		return observeErr
	}

	err = s.repoLock(ctx, commonDir, func() error {
		if approved.Locator.TaskID == "" {
			return applyLocked(nil)
		}
		return s.tasks.WithLock(ctx, applyLocked)
	})
	return result, err
}

func (e *executionState) executeRefreshRemote(ctx context.Context, observed remoteRefreshObservation) (Result, error) {
	data := RemoteObservation{
		RemoteName: observed.remoteName, RemoteURL: observed.remoteURL,
		Provider: observed.providerKind, Repository: observed.providerRepository,
		Head: observed.head, Base: observed.base, BeforeRefs: observed.refs,
	}
	e.remote = &data
	fetched := false

	for _, effect := range e.plan.Effects() {
		switch effect.Code {
		case EffectFetchRefs:
			err := e.run(effect, func() (string, error) {
				_, fetchErr := e.service.gitRun(ctx, observed.repoPath, "fetch", "--prune", "--", observed.remoteName)
				if fetchErr != nil {
					return "", fmt.Errorf("fetch --prune %s: %w", observed.remoteName, fetchErr)
				}
				return "fetched and pruned " + observed.remoteName, nil
			})
			if err != nil {
				recovery := []string{"remote refs were not proven refreshed; repair the exact remote fetch and create a fresh plan"}
				if e.plan.Request.Options.(RefreshRemoteOptions).QueryReview {
					recovery = append(recovery, "review query was not attempted after the failed fetch")
				}
				return e.fail(err, recovery...)
			}
			fetched = true
			data.AfterRefs = e.service.observeRemoteRefs(ctx, observed.repoPath, observed.remoteName, observed.head, observed.base)
			data.HasAfterRefs = true

		case EffectQueryReview:
			err := e.run(effect, func() (string, error) {
				if err := e.service.revalidateRemoteQueryContext(ctx, observed, fetched); err != nil {
					e.setRemoteReviewFailure(&data, observed, err)
					return "", err
				}
				query := forge.ReviewQuery{Repository: observed.providerRepository, Head: observed.head, Base: observed.base}
				review, queryErr := e.service.queryReview(ctx, observed.provider, observed.repoPath, query)
				if queryErr != nil {
					e.setRemoteReviewFailure(&data, observed, queryErr)
					return "", queryErr
				}
				if review == nil {
					data.Review = RemoteReviewObservation{
						State: ObservationKnown, Exists: false,
						Provider: observed.providerKind, ObservedAt: e.service.now(),
					}
					data.HasReview = true
					return "provider reports no exact review", nil
				}
				if err := validateRemoteReview(observed, *review); err != nil {
					e.setRemoteReviewFailure(&data, observed, err)
					return "", err
				}
				observedAt := review.ObservedAt.UTC()
				if review.ObservedAt.IsZero() {
					observedAt = e.service.now()
				}
				data.Review = RemoteReviewObservation{
					State: ObservationKnown, Exists: true,
					Provider: review.Provider, ReviewState: review.State,
					Draft: review.Draft, URL: review.URL, ObservedAt: observedAt,
				}
				data.HasReview = true
				return fmt.Sprintf("observed %s review (%s)", review.Provider, review.State), nil
			})
			if err != nil {
				if fetched {
					return e.fail(err, "fetch completed; review evidence remains an explicit error and may be retried from a fresh query-only plan")
				}
				return e.fail(err, "local refs and task state were unchanged; repair provider access or response handling and retry")
			}

		default:
			return e.fail(&InvalidPlanError{PlanID: e.plan.PlanID, Reason: "unexpected RefreshRemote effect " + string(effect.Code)})
		}
	}
	return e.result(), nil
}

func (s *lifecycleService) revalidateRemoteQueryContext(ctx context.Context, observed remoteRefreshObservation, allowFetchedRefs bool) error {
	repository, err := s.gitDiscover(ctx, observed.repoPath)
	if err != nil {
		return staleBoundary("repository became unavailable before review query: " + err.Error())
	}
	mainPath, err := s.canonicalPath(repository.MainRoot)
	if err != nil {
		return staleBoundary("repository main path became unavailable before review query: " + err.Error())
	}
	commonDir, err := s.canonicalPath(repository.GitCommonDir)
	if err != nil {
		return staleBoundary("Git common directory became unavailable before review query: " + err.Error())
	}
	if mainPath != observed.repoPath || commonDir != observed.gitCommonDir {
		return staleBoundary("repository identity changed before review query")
	}
	remoteURL := strings.TrimSpace(s.gitRemote(ctx, observed.repoPath, observed.remoteName))
	kind, repositoryIdentity := forge.IdentityFromURL(remoteURL)
	if remoteURL != observed.remoteURL || kind != observed.providerKind || repositoryIdentity != observed.providerRepository {
		return staleBoundary("remote URL or provider repository identity changed before review query")
	}
	if observed.provider == nil || observed.provider.Kind() != observed.providerKind {
		return staleBoundary("review provider identity changed before query")
	}
	if _, ok := observed.provider.(forge.ReviewQuerier); !ok {
		return staleBoundary("review provider capability changed before query")
	}
	if !observed.provider.Available() {
		return staleBoundary("review provider CLI became unavailable before query")
	}
	freshRefs := s.observeRemoteRefs(ctx, observed.repoPath, observed.remoteName, observed.head, observed.base)
	if allowFetchedRefs {
		if localRemoteRefsAuthority(freshRefs) != localRemoteRefsAuthority(observed.refs) {
			return staleBoundary("local head or base ref authority changed during fetch")
		}
	} else if remoteRefsAuthority(freshRefs) != remoteRefsAuthority(observed.refs) {
		return staleBoundary("local or remote-tracking ref authority changed before query")
	}
	return nil
}

func (e *executionState) setRemoteReviewFailure(data *RemoteObservation, observed remoteRefreshObservation, err error) {
	data.Review = RemoteReviewObservation{
		State: ObservationError, Exists: false,
		Provider: observed.providerKind, ObservedAt: e.service.now(),
		FailureKind: classifyRemoteReviewFailure(err), Failure: err.Error(),
	}
	data.HasReview = true
}

func classifyRemoteReviewFailure(err error) RemoteReviewFailureKind {
	if errors.Is(err, forge.ErrAmbiguousReview) {
		return RemoteReviewFailureAmbiguous
	}
	if errors.Is(err, forge.ErrMalformedReviewResponse) {
		return RemoteReviewFailureMalformed
	}
	var unsupported *forge.ErrUnsupported
	if errors.As(err, &unsupported) {
		return RemoteReviewFailureUnsupported
	}
	var missingCLI *forge.ErrNoCLI
	if errors.As(err, &missingCLI) {
		return RemoteReviewFailureMissingCLI
	}
	var missingExtension *forge.ErrNoExtension
	if errors.As(err, &missingExtension) {
		return RemoteReviewFailureMissingExtension
	}
	return RemoteReviewFailureProvider
}

func validateRemoteReview(observed remoteRefreshObservation, review forge.Review) error {
	switch {
	case review.Provider != observed.providerKind:
		return malformedRemoteReview(observed.providerKind, "provider")
	case review.Repository != observed.providerRepository:
		return malformedRemoteReview(observed.providerKind, "repository identity")
	case review.Head != observed.head:
		return malformedRemoteReview(observed.providerKind, "head branch")
	case review.Base != observed.base:
		return malformedRemoteReview(observed.providerKind, "base branch")
	case strings.TrimSpace(review.ID) == "" || review.ID != strings.TrimSpace(review.ID):
		return malformedRemoteReview(observed.providerKind, "identifier")
	case review.Number <= 0:
		return malformedRemoteReview(observed.providerKind, "number")
	case !review.State.Valid():
		return malformedRemoteReview(observed.providerKind, "state")
	case review.State == forge.ReviewDraft && !review.Draft:
		return malformedRemoteReview(observed.providerKind, "draft flag")
	case review.State == forge.ReviewOpen && review.Draft:
		return malformedRemoteReview(observed.providerKind, "draft state")
	case !validRemoteReviewURL(review.URL):
		return malformedRemoteReview(observed.providerKind, "URL")
	}
	return nil
}

func malformedRemoteReview(provider forge.Kind, field string) error {
	return &forge.MalformedReviewResponseError{Provider: provider, Index: 0, Field: field}
}

func validRemoteReviewURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}

func remoteRefCount(refs RemoteRefsObservation) int {
	count := 0
	for _, ref := range []NamedRefObservation{refs.LocalHead, refs.LocalBase, refs.RemoteHead, refs.RemoteBase} {
		if ref.Exists && ref.Failure == "" && ref.OID != "" {
			count++
		}
	}
	return count
}

func remoteRefSummary(refs RemoteRefsObservation) string {
	return strconv.Itoa(remoteRefCount(refs)) + " named ref OID(s) observed"
}
