package localfiles

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/lease"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

type preflightFile struct {
	spec    FileSpec
	planned PlanFile
	before  observation
	content []byte
}

func (s *Service) Apply(ctx context.Context, envelope ApplyEnvelope) (ApplyResponse, error) {
	if s == nil {
		return ApplyResponse{}, errors.New("nil local-files service")
	}
	if s.platform() == "windows" {
		return ApplyResponse{}, &TargetError{Code: TargetIncompatible}
	}
	id, err := s.machineID(ctx)
	if err != nil {
		return ApplyResponse{}, err
	}
	// This check intentionally precedes envelope validation. The hidden command
	// performs the same capability check before reading stdin at all.
	if id != envelope.Request.Binding.TargetMachine {
		return ApplyResponse{}, errors.New("target machine identity changed after capability probe")
	}
	if err := envelope.Validate(); err != nil {
		return ApplyResponse{}, err
	}
	checkout, err := s.resolveTarget(ctx, envelope.Request.Binding)
	if err != nil {
		return ApplyResponse{}, err
	}
	authority := s.Authority
	if authority == nil {
		authority = lease.New("")
	}
	keys := []lease.Key{
		lease.BranchKey(envelope.Request.Binding.RemoteIdentity, envelope.Request.Binding.Branch),
		lease.BranchKey(lease.GitCommonDirIdentity(checkout.commonDir), envelope.Request.Binding.Branch),
	}
	var response ApplyResponse
	err = authority.WithMutation(ctx, keys, func() error {
		var applyErr error
		response, applyErr = s.applyUnderLease(ctx, envelope, checkout)
		return applyErr
	})
	return response, err
}

func (s *Service) applyUnderLease(ctx context.Context, envelope ApplyEnvelope, expected targetCheckout) (ApplyResponse, error) {
	checkout, err := s.resolveTarget(ctx, envelope.Request.Binding)
	if err != nil {
		return ApplyResponse{}, err
	}
	if checkout.commonDir != expected.commonDir || checkout.id != expected.id {
		return ApplyResponse{}, errors.New("target checkout changed while acquiring operation lease")
	}
	if checkout.id != envelope.Plan.TargetCheckout {
		return ApplyResponse{}, errors.New("target checkout changed after planning")
	}
	if err := s.validateNegotiatedLimits(envelope.Request); err != nil {
		return ApplyResponse{}, err
	}

	store, exists, err := openOperationStore(s.StoreRoot, envelope.Request.RequestID)
	if err != nil {
		return ApplyResponse{}, err
	}
	var prior journal
	if exists {
		prior, err = store.loadJournal()
		if err != nil {
			return ApplyResponse{}, err
		}
		if prior.PlanDigest != envelope.Plan.PlanDigest || prior.ManifestDigest != envelope.Request.ManifestDigest ||
			prior.RetainForEvict != envelope.RetainForEvict {
			return ApplyResponse{}, errors.New("local-files request ID is already bound to another apply")
		}
		if prior.Phase == phaseCompleted {
			return s.verifyCompleted(ctx, checkout, envelope, store, prior)
		}
		if prior.Phase == phaseApplying || prior.Phase == phaseReconcile {
			if err := s.rollback(ctx, checkout, envelope, store, &prior); err != nil {
				prior.Phase = phaseReconcile
				_ = store.writeJournal(prior)
				return ApplyResponse{}, errors.New("prior local-files apply requires reconciliation")
			}
			prior.Phase = phaseRolledBack
			if err := store.writeJournal(prior); err != nil {
				return ApplyResponse{}, err
			}
		}
		if prior.Phase == phaseRolledBack {
			if err := store.cleanupPayloads(false); err != nil {
				return ApplyResponse{}, errors.New("prior rolled-back local-files staging cleanup is incomplete")
			}
		}
	}

	files, err := preflightApply(ctx, checkout.root, envelope)
	if err != nil {
		return ApplyResponse{}, err
	}
	value := journal{
		SchemaVersion: SchemaVersion, RequestID: envelope.Request.RequestID,
		PlanDigest: envelope.Plan.PlanDigest, ManifestDigest: envelope.Request.ManifestDigest,
		RetainForEvict: envelope.RetainForEvict, Phase: phaseStaged,
	}
	for _, file := range files {
		entry := journalFile{Path: file.spec.Path, Size: file.spec.Size, Mode: file.spec.Mode, Action: file.planned.Action, State: file.planned.State}
		if entry.Action == actionReplace {
			entry.OldSHA256 = file.before.digest
			entry.OldMode = file.planned.TargetMode
		}
		value.Files = append(value.Files, entry)
	}
	if store == nil || !exists {
		store, err = newOperationStore(s.StoreRoot, envelope.Request.RequestID)
		if err != nil {
			return ApplyResponse{}, err
		}
		if err := store.ensureOperation(); err != nil {
			return ApplyResponse{}, err
		}
		if err := s.fault("after-store-create", ""); err != nil {
			return ApplyResponse{}, err
		}
	}
	// The operation binding is durable before any manifest or payload bytes.
	// A crash from this point is a resumable staged journal, never an opaque
	// request-ID directory that permanently poisons retries.
	if err := store.writeJournal(value); err != nil {
		return ApplyResponse{}, err
	}
	if err := s.fault("after-journal", ""); err != nil {
		return ApplyResponse{}, err
	}
	if err := store.ensurePayloadLayout(); err != nil {
		return ApplyResponse{}, err
	}
	if err := store.writeManifest(envelope); err != nil {
		return ApplyResponse{}, err
	}
	for index, file := range files {
		if file.planned.Action != actionCurrent || envelope.RetainForEvict {
			if err := store.writeBlob(store.blobPath(index), file.content, file.spec.executable()); err != nil {
				return ApplyResponse{}, err
			}
		}
		if file.planned.Action == actionReplace {
			if err := store.writeBlob(store.rollbackPath(index), file.before.data, file.before.info.Mode().Perm()&0o111 != 0); err != nil {
				return ApplyResponse{}, err
			}
		}
	}
	if err := s.fault("after-stage", ""); err != nil {
		return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
	}
	value.Phase = phaseApplying
	if err := store.writeJournal(value); err != nil {
		return ApplyResponse{}, err
	}
	if err := s.fault("after-applying-journal", ""); err != nil {
		return ApplyResponse{}, err
	}

	for index, file := range files {
		if file.planned.Action == actionCurrent {
			value.Files[index].State = StateCurrent
			continue
		}
		if err := s.fault("before-publish", file.spec.Path); err != nil {
			return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
		}
		parent, err := openHeldParent(checkout.root, file.spec.Path, true)
		if err != nil {
			return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
		}
		name := pathBase(file.spec.Path)
		switch file.planned.Action {
		case actionCreate:
			_, err = safefile.CreatePrivateNoClobber(ctx, parent.parent, name, file.content, file.spec.executable())
		case actionReplace:
			_, err = safefile.AtomicReplacePrivate(ctx, parent.parent, name, file.before.info, file.content, file.spec.executable())
		default:
			err = errors.New("invalid local-files publication action")
		}
		closeErr := parent.close()
		err = errors.Join(err, closeErr)
		if err != nil {
			return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
		}
		// In-process rollback may use this provenance immediately. The durable
		// journal is intentionally written after the fault seam; a real crash in
		// that window leaves Published=false and later recovery must preserve any
		// path whose ownership cannot be proven.
		value.Files[index].Published = true
		if file.planned.Action == actionCreate {
			value.Files[index].State = StateCreated
		} else {
			value.Files[index].State = StateReplaced
		}
		if err := s.fault("after-publish", file.spec.Path); err != nil {
			return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
		}
		if err := store.writeJournal(value); err != nil {
			return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
		}
	}
	if err := s.fault("before-final-verify", ""); err != nil {
		return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
	}
	if err := verifyAllCurrent(ctx, checkout.root, envelope.Request.Files, envelope.Request.SafeLimits()); err != nil {
		return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
	}
	if err := s.fault("before-complete", ""); err != nil {
		return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
	}
	value.Phase = phaseCompleted
	if err := store.writeJournal(value); err != nil {
		return ApplyResponse{}, s.failAndRollback(ctx, checkout, envelope, store, &value, err)
	}
	if err := store.cleanupPayloads(envelope.RetainForEvict); err != nil {
		return ApplyResponse{}, errors.New("local-files apply completed but private staging cleanup is incomplete")
	}
	return responseFromJournal(value), nil
}

func (s *Service) validateNegotiatedLimits(request PlanRequest) error {
	host := s.Config.LocalFiles.Limits()
	negotiated, err := NegotiateLimits(host, request.Limits)
	if err != nil || negotiated != request.SafeLimits() {
		return errors.New("apply limits do not match target policy")
	}
	return nil
}

func preflightApply(ctx context.Context, checkout string, envelope ApplyEnvelope) ([]preflightFile, error) {
	payloads := map[string][]byte{}
	for _, payload := range envelope.Payloads {
		decoded, err := base64.StdEncoding.Strict().DecodeString(payload.Content)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != payload.Content {
			return nil, fmt.Errorf("decode canonical payload for %q", payload.Path)
		}
		payloads[payload.Path] = decoded
	}
	files := make([]preflightFile, len(envelope.Request.Files))
	for index, spec := range envelope.Request.Files {
		planned := envelope.Plan.Files[index]
		if planned.Action == "" {
			return nil, ErrPlanBlocked
		}
		if err := proveGitEligibility(ctx, checkout, spec.Path); err != nil {
			return nil, fmt.Errorf("target path %q changed eligibility: %w", spec.Path, ErrDrift)
		}
		observed, err := observePath(ctx, checkout, spec.Path, envelope.Request.SafeLimits(), true)
		if err != nil {
			return nil, fmt.Errorf("target path %q changed after planning: %w", spec.Path, ErrDrift)
		}
		file := preflightFile{spec: spec, planned: planned, before: observed}
		switch planned.Action {
		case actionCurrent:
			if !observed.exists || observed.digest != spec.SHA256 || fmt.Sprintf("%04o", observed.info.Mode().Perm()) != planned.TargetMode {
				return nil, fmt.Errorf("target path %q is no longer current: %w", spec.Path, ErrDrift)
			}
			file.content = observed.data
		case actionCreate:
			if observed.exists {
				return nil, fmt.Errorf("target path %q appeared after planning: %w", spec.Path, ErrDrift)
			}
			file.content = payloads[spec.Path]
		case actionReplace:
			if !observed.exists || observed.digest != planned.TargetSHA256 || fmt.Sprintf("%04o", observed.info.Mode().Perm()) != planned.TargetMode {
				return nil, fmt.Errorf("target path %q changed before replacement: %w", spec.Path, ErrDrift)
			}
			file.content = payloads[spec.Path]
		default:
			return nil, errors.New("invalid local-files target action")
		}
		if int64(len(file.content)) != spec.Size || digestBytes(file.content) != spec.SHA256 {
			return nil, fmt.Errorf("content for %q no longer matches the plan", spec.Path)
		}
		files[index] = file
	}
	return files, nil
}

func (s *Service) failAndRollback(ctx context.Context, checkout targetCheckout, envelope ApplyEnvelope, store *operationStore, value *journal, cause error) error {
	value.Phase = phaseReconcile
	_ = store.writeJournal(*value)
	if err := s.rollback(ctx, checkout, envelope, store, value); err != nil {
		value.Phase = phaseReconcile
		_ = store.writeJournal(*value)
		return errors.New("local-files apply failed and requires reconciliation")
	}
	value.Phase = phaseRolledBack
	for index := range value.Files {
		if value.Files[index].Action != actionCurrent {
			value.Files[index].State = StateRolledBack
			value.Files[index].Published = false
		}
	}
	if err := store.writeJournal(*value); err != nil {
		return errors.New("local-files apply rolled back but journal finalization failed")
	}
	if err := store.cleanupPayloads(false); err != nil {
		return errors.New("local-files apply rolled back but private staging cleanup is incomplete")
	}
	return fmt.Errorf("local-files apply failed and was rolled back: %w", cause)
}

func (s *Service) rollback(ctx context.Context, checkout targetCheckout, envelope ApplyEnvelope, store *operationStore, value *journal) error {
	for index := len(value.Files) - 1; index >= 0; index-- {
		entry := &value.Files[index]
		if entry.Action == actionCurrent {
			continue
		}
		spec := envelope.Request.Files[index]
		if err := proveGitEligibility(ctx, checkout.root, spec.Path); err != nil {
			return err
		}
		observed, err := observePath(ctx, checkout.root, spec.Path, envelope.Request.SafeLimits(), true)
		if err != nil {
			return err
		}
		switch entry.Action {
		case actionCreate:
			if !entry.Published {
				if observed.exists {
					return errors.New("unproven target appeared before create rollback")
				}
				continue
			}
			if !observed.exists {
				continue
			}
			if observed.digest != spec.SHA256 || observedFileMode(observed) != spec.Mode {
				return errors.New("created target changed before rollback")
			}
			parent, err := openHeldParent(checkout.root, spec.Path, false)
			if err != nil {
				return err
			}
			err = parent.parent.Remove(pathBase(spec.Path))
			err = errors.Join(err, syncHeldRoot(parent.parent), parent.close())
			if err != nil {
				return err
			}
		case actionReplace:
			oldState := observed.exists && observed.digest == entry.OldSHA256 && observedFileMode(observed) == entry.OldMode
			if !entry.Published {
				if oldState {
					continue
				}
				return errors.New("replacement publication cannot be proven during rollback")
			}
			if oldState {
				continue
			}
			if !observed.exists || observed.digest != spec.SHA256 || observedFileMode(observed) != spec.Mode {
				return errors.New("replacement target changed before rollback")
			}
			oldContent, err := store.readBlob(index, true, envelope.Request.SafeLimits().MaxFileBytes)
			if err != nil || digestBytes(oldContent) != entry.OldSHA256 {
				return errors.New("replacement rollback blob is unavailable")
			}
			parent, err := openHeldParent(checkout.root, spec.Path, false)
			if err != nil {
				return err
			}
			current, err := parent.parent.Lstat(pathBase(spec.Path))
			oldMode, modeErr := strconv.ParseUint(entry.OldMode, 8, 12)
			err = errors.Join(err, modeErr)
			if err == nil {
				_, err = safefile.AtomicReplace(ctx, parent.parent, pathBase(spec.Path), current, oldContent, fs.FileMode(oldMode))
			}
			err = errors.Join(err, parent.close())
			if err != nil {
				return err
			}
		}
	}
	// Empty parent directories are intentionally retained. A path alone cannot
	// prove that a directory still has the identity created by this operation;
	// deleting it after a crash could remove another process's replacement.
	return verifyRollback(ctx, checkout.root, envelope, *value)
}

func verifyRollback(ctx context.Context, checkout string, envelope ApplyEnvelope, value journal) error {
	for index, entry := range value.Files {
		spec := envelope.Request.Files[index]
		observed, err := observePath(ctx, checkout, spec.Path, envelope.Request.SafeLimits(), true)
		if err != nil {
			return err
		}
		switch entry.Action {
		case actionCurrent:
			if !observed.exists || observed.digest != spec.SHA256 || fmt.Sprintf("%04o", observed.info.Mode().Perm()) != envelope.Plan.Files[index].TargetMode {
				return errors.New("current target changed during rollback")
			}
		case actionCreate:
			if observed.exists {
				return errors.New("created target remains after rollback")
			}
		case actionReplace:
			if !observed.exists || observed.digest != entry.OldSHA256 || fmt.Sprintf("%04o", observed.info.Mode().Perm()) != entry.OldMode {
				return errors.New("replacement target was not restored")
			}
		}
	}
	return nil
}

func (s *Service) verifyCompleted(ctx context.Context, checkout targetCheckout, envelope ApplyEnvelope, store *operationStore, value journal) (ApplyResponse, error) {
	if err := verifyAllCurrent(ctx, checkout.root, envelope.Request.Files, envelope.Request.SafeLimits()); err != nil {
		return ApplyResponse{}, errors.New("completed local-files journal no longer matches the checkout")
	}
	if value.RetainForEvict {
		for index, spec := range envelope.Request.Files {
			content, err := store.readBlob(index, false, envelope.Request.SafeLimits().MaxFileBytes)
			if err != nil || int64(len(content)) != spec.Size || digestBytes(content) != spec.SHA256 {
				return ApplyResponse{}, errors.New("retained local-files recovery blob is unavailable")
			}
		}
	} else if err := store.cleanupPayloads(false); err != nil {
		return ApplyResponse{}, errors.New("completed local-files staging cleanup is incomplete")
	}
	return responseFromJournal(value), nil
}

func observedFileMode(observed observation) string {
	if !observed.exists || observed.info == nil {
		return ""
	}
	return fmt.Sprintf("%04o", observed.info.Mode().Perm())
}

func verifyAllCurrent(ctx context.Context, checkout string, specs []FileSpec, limits safefile.Limits) error {
	for _, spec := range specs {
		if err := proveGitEligibility(ctx, checkout, spec.Path); err != nil {
			return err
		}
		observed, err := observePath(ctx, checkout, spec.Path, limits, true)
		if err != nil || !observed.exists || observed.digest != spec.SHA256 || observed.info.Size() != spec.Size {
			return fmt.Errorf("target path %q failed post-write verification", spec.Path)
		}
		if fmt.Sprintf("%04o", observed.info.Mode().Perm()) != spec.Mode {
			return fmt.Errorf("target path %q permissions do not match the manifest", spec.Path)
		}
	}
	return nil
}

func responseFromJournal(value journal) ApplyResponse {
	files := make([]PublicFile, len(value.Files))
	for index, entry := range value.Files {
		files[index] = PublicFile{Path: entry.Path, Size: entry.Size, Mode: entry.Mode, State: entry.State}
	}
	return ApplyResponse{
		SchemaVersion: SchemaVersion, ProtocolVersion: fleet.LocalFilesProtocolVersion,
		RequestID: value.RequestID, PlanDigest: value.PlanDigest, Files: files,
	}
}

func (s *Service) fault(point, path string) error {
	if s.Fault == nil {
		return nil
	}
	return s.Fault(point, path)
}

type heldParent struct {
	checkout string
	rootInfo fs.FileInfo
	root     *os.Root
	parent   *os.Root
	roots    []*os.Root
	edges    []heldEdge
}

type heldEdge struct {
	parent *os.Root
	name   string
	info   fs.FileInfo
}

func openHeldParent(checkout, slashPath string, create bool) (*heldParent, error) {
	if err := pathx.ValidatePortableSlashPath(slashPath, safefile.DefaultLimits().PathLimits()); err != nil {
		return nil, err
	}
	root, rootInfo, err := safefile.OpenRoot(checkout)
	if err != nil {
		return nil, err
	}
	held := &heldParent{checkout: checkout, rootInfo: rootInfo, root: root, parent: root, roots: []*os.Root{root}}
	components := strings.Split(slashPath, "/")
	prefix := make([]string, 0, len(components)-1)
	for _, component := range components[:len(components)-1] {
		prefix = append(prefix, component)
		child, info, err := safefile.OpenChildRoot(held.parent, component)
		if errors.Is(err, fs.ErrNotExist) && create {
			if err := held.parent.Mkdir(component, 0o700); err != nil {
				_ = held.close()
				return nil, err
			}
			child, info, err = safefile.OpenChildRoot(held.parent, component)
		}
		if err != nil {
			_ = held.close()
			return nil, err
		}
		if _, err := child.Lstat(".git"); err == nil {
			_ = child.Close()
			_ = held.close()
			return nil, ErrUnsafe
		} else if !errors.Is(err, fs.ErrNotExist) {
			_ = child.Close()
			_ = held.close()
			return nil, err
		}
		held.edges = append(held.edges, heldEdge{parent: held.parent, name: component, info: info})
		held.roots = append(held.roots, child)
		held.parent = child
	}
	return held, nil
}

func (h *heldParent) close() error {
	if h == nil {
		return nil
	}
	var result error
	for index := len(h.edges) - 1; index >= 0; index-- {
		result = errors.Join(result, safefile.VerifyChildRoot(h.edges[index].parent, h.edges[index].name, h.edges[index].info))
	}
	result = errors.Join(result, safefile.VerifyRoot(h.checkout, h.rootInfo))
	for index := len(h.roots) - 1; index >= 0; index-- {
		result = errors.Join(result, h.roots[index].Close())
	}
	h.roots = nil
	return result
}

func syncHeldRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func pathBase(slashPath string) string {
	parts := strings.Split(slashPath, "/")
	return parts[len(parts)-1]
}
