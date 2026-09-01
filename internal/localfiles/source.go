package localfiles

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

var ErrPlanBlocked = errors.New("portable-file plan is blocked")

type SourceOptions struct {
	Checkout string
	Binding  Binding
	Patterns []Pattern
	Limits   safefile.Limits
	Replace  bool
	// RequestID is a focused-test seam. Production leaves it empty.
	RequestID string
}

// Source is an exact, content-addressed local manifest. It keeps no file bytes;
// BuildEnvelope reopens and rehashes each non-current source after target plan.
type Source struct {
	checkout string
	request  PlanRequest
}

func (s *Source) Request() PlanRequest { return s.request }

func PrepareSource(ctx context.Context, options SourceOptions) (*Source, Report, error) {
	if err := options.Limits.Validate(); err != nil {
		return nil, Report{}, err
	}
	if err := options.Binding.Validate(); err != nil {
		return nil, Report{}, err
	}
	if err := verifySourceBinding(ctx, options.Checkout, options.Binding); err != nil {
		return nil, Report{}, fmt.Errorf("verify source checkout binding: %w", err)
	}
	paths, err := Expand(options.Checkout, options.Patterns, options.Limits)
	if err != nil {
		return nil, Report{}, err
	}
	if err := pathx.ValidatePortablePathSet(paths, options.Limits.PathLimits()); err != nil {
		return nil, Report{}, err
	}
	requestID := options.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}
	request := PlanRequest{
		SchemaVersion: SchemaVersion, ProtocolVersion: fleet.LocalFilesProtocolVersion,
		RequestID: requestID, Binding: options.Binding, Limits: limitsToWire(options.Limits),
		Replace: options.Replace,
	}
	report := Report{SchemaVersion: SchemaVersion}
	blocked := false
	for _, path := range paths {
		public := PublicFile{Path: path, Mode: "0600", State: StateFailed}
		observed, observeErr := observePath(ctx, options.Checkout, path, options.Limits, true)
		if observeErr != nil {
			public.State = stateForError(observeErr)
			report.Files = append(report.Files, public)
			blocked = true
			continue
		}
		if !observed.exists {
			public.State = StateMissing
			report.Files = append(report.Files, public)
			blocked = true
			continue
		}
		public.Size = observed.info.Size()
		if observed.info.Mode().Perm()&0o111 != 0 {
			public.Mode = "0700"
		}
		if eligibilityErr := proveGitEligibility(ctx, options.Checkout, path); eligibilityErr != nil {
			public.State = stateForError(eligibilityErr)
			report.Files = append(report.Files, public)
			blocked = true
			continue
		}
		public.State = StateReady
		report.Files = append(report.Files, public)
		request.Files = append(request.Files, FileSpec{
			Path: path, Size: observed.info.Size(), Mode: public.Mode, SHA256: observed.digest,
		})
	}
	report.Files = sortedPublic(report.Files)
	if blocked {
		return nil, report, ErrPlanBlocked
	}
	sort.Slice(request.Files, func(i, j int) bool { return request.Files[i].Path < request.Files[j].Path })
	request.ManifestDigest = manifestDigest(request)
	if err := request.Validate(); err != nil {
		return nil, report, err
	}
	return &Source{checkout: options.Checkout, request: request}, report, nil
}

// BuildEnvelope performs the source-side apply revalidation and includes bytes
// only for target actions that create or explicitly replace a file.
func (s *Source) BuildEnvelope(ctx context.Context, plan PlanResponse, retainForEvict bool) (ApplyEnvelope, error) {
	if s == nil {
		return ApplyEnvelope{}, errors.New("build local-files envelope from nil source")
	}
	if err := verifySourceBinding(ctx, s.checkout, s.request.Binding); err != nil {
		return ApplyEnvelope{}, fmt.Errorf("source checkout changed after planning: %w", err)
	}
	if err := plan.Validate(s.request); err != nil {
		return ApplyEnvelope{}, err
	}
	envelope := ApplyEnvelope{
		SchemaVersion: SchemaVersion, ProtocolVersion: fleet.LocalFilesProtocolVersion,
		Request: s.request, Plan: plan, RetainForEvict: retainForEvict,
	}
	for index, planned := range plan.Files {
		if planned.Action == actionCurrent {
			continue
		}
		if planned.Action != actionCreate && planned.Action != actionReplace {
			return ApplyEnvelope{}, ErrPlanBlocked
		}
		spec := s.request.Files[index]
		if err := proveGitEligibility(ctx, s.checkout, spec.Path); err != nil {
			return ApplyEnvelope{}, fmt.Errorf("source path %q changed eligibility: %w", spec.Path, ErrDrift)
		}
		observed, err := observePath(ctx, s.checkout, spec.Path, s.request.SafeLimits(), true)
		if err != nil || !observed.exists || observed.info.Size() != spec.Size || observed.digest != spec.SHA256 ||
			(observed.info.Mode().Perm()&0o111 != 0) != spec.executable() {
			return ApplyEnvelope{}, fmt.Errorf("source path %q changed after planning: %w", spec.Path, ErrDrift)
		}
		envelope.Payloads = append(envelope.Payloads, Payload{
			Path: spec.Path, Content: base64.StdEncoding.EncodeToString(observed.data),
		})
	}
	if err := verifySourceBinding(ctx, s.checkout, s.request.Binding); err != nil {
		return ApplyEnvelope{}, fmt.Errorf("source checkout changed while building payload: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return ApplyEnvelope{}, err
	}
	return envelope, nil
}

func (e ApplyEnvelope) Validate() error {
	if e.SchemaVersion != SchemaVersion || e.ProtocolVersion != fleet.LocalFilesProtocolVersion {
		return errors.New("unsupported local-files apply protocol")
	}
	if err := e.Request.Validate(); err != nil {
		return err
	}
	if err := e.Plan.Validate(e.Request); err != nil {
		return err
	}
	expected := map[string]FileSpec{}
	for index, planned := range e.Plan.Files {
		if planned.Action == actionCreate || planned.Action == actionReplace {
			expected[e.Request.Files[index].Path] = e.Request.Files[index]
		}
	}
	if len(e.Payloads) != len(expected) {
		return errors.New("apply payload count does not match target actions")
	}
	seen := map[string]bool{}
	var total int64
	for index, payload := range e.Payloads {
		if index > 0 && e.Payloads[index-1].Path >= payload.Path {
			return errors.New("apply payload paths must be unique and sorted")
		}
		spec, ok := expected[payload.Path]
		if !ok || seen[payload.Path] {
			return fmt.Errorf("unexpected apply payload for %q", payload.Path)
		}
		seen[payload.Path] = true
		decoded, err := base64.StdEncoding.Strict().DecodeString(payload.Content)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != payload.Content {
			return fmt.Errorf("payload for %q is not canonical base64", payload.Path)
		}
		if int64(len(decoded)) != spec.Size || digestBytes(decoded) != spec.SHA256 {
			return fmt.Errorf("payload for %q does not match its manifest", payload.Path)
		}
		if int64(len(decoded)) > e.Request.SafeLimits().MaxFileBytes || int64(len(decoded)) > e.Request.SafeLimits().MaxTotalBytes-total {
			return fmt.Errorf("payload for %q exceeds negotiated limits", payload.Path)
		}
		total += int64(len(decoded))
	}
	return nil
}

func stateForError(err error) State {
	switch {
	case errors.Is(err, ErrUnsafe), errors.Is(err, safefile.ErrUnsafeType), errors.Is(err, pathx.ErrNotPortable), errors.Is(err, pathx.ErrPathCollision):
		return StateBlockedUnsafe
	case errors.Is(err, ErrIneligible):
		return StateBlockedIneligible
	case errors.Is(err, fs.ErrNotExist):
		return StateMissing
	default:
		return StateFailed
	}
}

func limitsToWire(limits safefile.Limits) fleet.FileLimits {
	return fleet.FileLimits{
		MaxFiles: limits.MaxFiles, MaxFileBytes: limits.MaxFileBytes,
		MaxTotalBytes: limits.MaxTotalBytes, MaxPathBytes: limits.MaxPathBytes,
		MaxComponentBytes: limits.MaxComponentBytes, MaxPathDepth: limits.MaxPathDepth,
	}
}

// NegotiateLimits takes the lower value for every policy field and validates
// both sides against this binary's compiled ceilings.
func NegotiateLimits(local safefile.Limits, remote fleet.FileLimits) (safefile.Limits, error) {
	if err := local.Validate(); err != nil {
		return safefile.Limits{}, err
	}
	remoteLimits := safefile.Limits{
		MaxFiles: remote.MaxFiles, MaxFileBytes: remote.MaxFileBytes,
		MaxTotalBytes: remote.MaxTotalBytes, MaxPathBytes: remote.MaxPathBytes,
		MaxComponentBytes: remote.MaxComponentBytes, MaxPathDepth: remote.MaxPathDepth,
	}
	if err := remoteLimits.Validate(); err != nil {
		return safefile.Limits{}, err
	}
	result := safefile.Limits{
		MaxFiles:          min(local.MaxFiles, remoteLimits.MaxFiles),
		MaxFileBytes:      min(local.MaxFileBytes, remoteLimits.MaxFileBytes),
		MaxTotalBytes:     min(local.MaxTotalBytes, remoteLimits.MaxTotalBytes),
		MaxPathBytes:      min(local.MaxPathBytes, remoteLimits.MaxPathBytes),
		MaxComponentBytes: min(local.MaxComponentBytes, remoteLimits.MaxComponentBytes),
		MaxPathDepth:      min(local.MaxPathDepth, remoteLimits.MaxPathDepth),
	}
	return result, result.Validate()
}
