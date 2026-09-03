package localfiles

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	devconfig "github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/lease"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/repo"
)

type TargetCode string

const (
	TargetAbsent       TargetCode = "target-clone-absent"
	TargetAmbiguous    TargetCode = "target-clone-ambiguous"
	TargetFetchedOnly  TargetCode = "target-branch-not-checked-out"
	TargetStale        TargetCode = "target-checkout-stale"
	TargetIncompatible TargetCode = "target-incompatible"
)

func (code TargetCode) valid() bool {
	switch code {
	case TargetAbsent, TargetAmbiguous, TargetFetchedOnly, TargetStale, TargetIncompatible:
		return true
	default:
		return false
	}
}

type TargetError struct{ Code TargetCode }

func (e *TargetError) Error() string { return string(e.Code) }

type Service struct {
	Config        devconfig.Config
	StoreRoot     string
	Authority     *lease.Authority
	Platform      string
	LoadMachineID func(context.Context) (string, error)
	Fault         func(point, path string) error
}

func NewService(cfg devconfig.Config) *Service {
	return &Service{Config: cfg, Authority: lease.New(""), Platform: runtime.GOOS, LoadMachineID: machineid.LoadOrCreate}
}

func (s *Service) platform() string {
	if s != nil && s.Platform != "" {
		return s.Platform
	}
	return runtime.GOOS
}

func (s *Service) machineID(ctx context.Context) (string, error) {
	if s == nil {
		return "", errors.New("nil local-files service")
	}
	load := s.LoadMachineID
	if load == nil {
		load = machineid.LoadOrCreate
	}
	return load(ctx)
}

func (s *Service) Capability(ctx context.Context, request fleet.CapabilityRequest) (fleet.CapabilityResponse, error) {
	if err := request.Validate(); err != nil {
		return fleet.CapabilityResponse{}, err
	}
	id, err := s.machineID(ctx)
	if err != nil {
		return fleet.CapabilityResponse{}, err
	}
	limits := s.Config.LocalFiles.Limits()
	if err := limits.Validate(); err != nil {
		return fleet.CapabilityResponse{}, fmt.Errorf("local-files host limits: %w", err)
	}
	response := fleet.CapabilityResponse{
		SchemaVersion: request.SchemaVersion, Feature: request.Feature,
		ProtocolVersion: request.ProtocolVersion, MachineID: id,
		Platform: s.platform(), Limits: limitsToWire(limits), Supported: true,
	}
	if response.Platform == "windows" {
		response.Supported = false
		response.Reason = "native-windows-acl-transport-disabled"
	}
	if err := response.Validate(request); err != nil {
		return fleet.CapabilityResponse{}, err
	}
	return response, nil
}

type targetCheckout struct {
	root      string
	commonDir string
	identity  string
	branch    string
	head      string
	id        string
}

func (s *Service) resolveTarget(ctx context.Context, binding Binding) (targetCheckout, error) {
	if err := binding.Validate(); err != nil {
		return targetCheckout{}, err
	}
	options := repo.DefaultOptions()
	if s.Config.Bootstrap.MaxDepth > 0 {
		options.MaxDepth = s.Config.Bootstrap.MaxDepth
	}
	repositories, err := repo.Discover(ctx, s.Config.DiscoveryRoots(), options)
	if err != nil {
		return targetCheckout{}, err
	}
	type candidate struct {
		path      string
		commonDir string
	}
	var candidates []candidate
	for _, repository := range repositories {
		if repository.Bare || !repository.HasGit {
			continue
		}
		gitRepo, err := gitx.Discover(ctx, repository.Path)
		if err != nil {
			return targetCheckout{}, fmt.Errorf("inspect discovered target repository: %w", err)
		}
		matched, err := checkoutHasIdentity(ctx, gitRepo.Root, binding.RemoteIdentity)
		if err != nil {
			return targetCheckout{}, fmt.Errorf("inspect target repository fetch identities: %w", err)
		}
		if !matched {
			continue
		}
		candidates = append(candidates, candidate{path: gitRepo.Root, commonDir: gitRepo.GitCommonDir})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].commonDir < candidates[j].commonDir })
	switch len(candidates) {
	case 0:
		return targetCheckout{}, &TargetError{Code: TargetAbsent}
	case 1:
	default:
		return targetCheckout{}, &TargetError{Code: TargetAmbiguous}
	}
	worktree, checkedOut, err := gitx.WorktreeFor(ctx, candidates[0].path, binding.Branch)
	if err != nil {
		return targetCheckout{}, err
	}
	if !checkedOut || worktree.Bare || worktree.Prunable {
		return targetCheckout{}, &TargetError{Code: TargetFetchedOnly}
	}
	head, err := gitx.Run(ctx, worktree.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return targetCheckout{}, err
	}
	status, err := gitx.StatusOf(ctx, worktree.Path)
	if err != nil {
		return targetCheckout{}, err
	}
	if status.Detached || status.Branch != binding.Branch || head != binding.HeadOID || worktree.Head != binding.HeadOID {
		return targetCheckout{}, &TargetError{Code: TargetStale}
	}
	checkout := targetCheckout{
		root: worktree.Path, commonDir: candidates[0].commonDir,
		identity: binding.RemoteIdentity, branch: binding.Branch, head: head,
	}
	checkout.id = digestJSON(struct {
		Domain    string `json:"domain"`
		CommonDir string `json:"common_dir"`
		Root      string `json:"root"`
		Branch    string `json:"branch"`
		Head      string `json:"head"`
	}{"dev-local-files-checkout-v1", checkout.commonDir, checkout.root, checkout.branch, checkout.head})
	return checkout, nil
}

func checkoutHasIdentity(ctx context.Context, checkout, wanted string) (bool, error) {
	names, err := gitx.Run(ctx, checkout, "remote")
	if err != nil {
		return false, err
	}
	for _, name := range strings.Fields(names) {
		// Repository identity is established from fetch URLs. A push-only match
		// can name a different mirror or publication target and must not authorize
		// writing local files into an otherwise unrelated clone.
		urls, err := gitx.Run(ctx, checkout, "remote", "get-url", "--all", name)
		if err != nil {
			continue
		}
		for _, raw := range strings.Split(urls, "\n") {
			if catalog.NormalizeRemoteIdentity(raw) == wanted {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) Plan(ctx context.Context, request PlanRequest) (PlanResponse, error) {
	if s == nil {
		return PlanResponse{}, errors.New("nil local-files service")
	}
	if s.platform() == "windows" {
		return PlanResponse{}, &TargetError{Code: TargetIncompatible}
	}
	if err := request.Validate(); err != nil {
		return PlanResponse{}, err
	}
	id, err := s.machineID(ctx)
	if err != nil {
		return PlanResponse{}, err
	}
	if id != request.Binding.TargetMachine {
		return PlanResponse{}, errors.New("target machine identity changed after capability probe")
	}
	hostLimits := s.Config.LocalFiles.Limits()
	if err := hostLimits.Validate(); err != nil {
		return PlanResponse{}, err
	}
	negotiated, err := NegotiateLimits(hostLimits, request.Limits)
	if err != nil || negotiated != request.SafeLimits() {
		return PlanResponse{}, errors.New("plan limits do not match target policy")
	}
	checkout, err := s.resolveTarget(ctx, request.Binding)
	if err != nil {
		return PlanResponse{}, err
	}
	response := PlanResponse{
		SchemaVersion: SchemaVersion, ProtocolVersion: fleet.LocalFilesProtocolVersion,
		RequestID: request.RequestID, ManifestDigest: request.ManifestDigest,
		TargetMachine: id, TargetCheckout: checkout.id,
	}
	for _, spec := range request.Files {
		planned := PlanFile{PublicFile: spec.public(StateFailed)}
		if err := proveGitEligibility(ctx, checkout.root, spec.Path); err != nil {
			planned.State = stateForError(err)
			response.Files = append(response.Files, planned)
			continue
		}
		observed, err := observePath(ctx, checkout.root, spec.Path, request.SafeLimits(), true)
		if err != nil {
			planned.State = stateForError(err)
			response.Files = append(response.Files, planned)
			continue
		}
		targetMode := ""
		if observed.exists {
			targetMode = fmt.Sprintf("%04o", observed.info.Mode().Perm())
		}
		switch {
		case !observed.exists:
			planned.State, planned.Action = StateReady, actionCreate
		case observed.digest == spec.SHA256 && targetMode == spec.Mode:
			planned.State, planned.Action, planned.TargetSHA256, planned.TargetMode = StateCurrent, actionCurrent, observed.digest, targetMode
		case request.Replace:
			planned.State, planned.Action, planned.TargetSHA256, planned.TargetMode = StateReady, actionReplace, observed.digest, targetMode
		default:
			planned.State, planned.TargetSHA256, planned.TargetMode = StateBlockedConflict, observed.digest, targetMode
		}
		response.Files = append(response.Files, planned)
	}
	response.PlanDigest = planDigest(request, response)
	if err := response.Validate(request); err != nil {
		return PlanResponse{}, err
	}
	return response, nil
}
