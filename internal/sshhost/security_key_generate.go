package sshhost

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) planGeneratedSecurityKey(ctx context.Context, request KeyRequest) (KeyPlan, error) {
	options, err := normalizeSecurityKeyOptions(request.SecurityKey)
	if err != nil {
		return KeyPlan{}, err
	}
	observation, hardware, err := s.observeSecurityKeyCapability(ctx, options.Provider)
	if err != nil {
		return KeyPlan{}, err
	}
	plan := KeyPlan{Action: ActionBlocked, Operation: KeyGenerate, Source: KeySourceGenerated,
		KeyType: request.Type, Algorithm: request.Type.algorithm(), SecurityKey: options,
		SecurityKeyProvider: observation.Provider, KeygenPath: observation.KeygenPath,
		SSHClientPath: observation.SSHClientPath, Diagnostics: observation.Diagnostics}
	if !observation.CanAttempt {
		return plan, nil
	}
	if !request.Type.securityKey() {
		return KeyPlan{}, errors.New("security-key generation requires ed25519-sk or ecdsa-sk")
	}
	if !request.Interactive {
		plan.Diagnostics = append(plan.Diagnostics, Diagnostic{Code: "interaction_required", Message: "Security-key generation requires explicit native PIN/touch interaction.", BlocksMutation: true})
		return plan, nil
	}
	options.Provider = observation.Provider
	request.SecurityKey = options
	if request.DestinationIdentity == "" {
		request.DestinationIdentity = filepath.Join(s.paths.SSHDir, "id_"+strings.ReplaceAll(string(request.Type), "-", "_")+"_dev")
	}
	base, err := s.planGeneratedKey(request)
	if err != nil {
		return KeyPlan{}, err
	}
	base.KeyType, base.Algorithm = request.Type, request.Type.algorithm()
	base.SecurityKey, base.SecurityKeyProvider = options, options.Provider
	base.KeygenPath, base.SSHClientPath = observation.KeygenPath, observation.SSHClientPath
	base.Diagnostics = append(base.Diagnostics, observation.Diagnostics...)
	if base.state != nil {
		hardware.options = options
		base.state.hardware = hardware
		base.state.public = cloneKeyPlan(base)
	}
	return base, nil
}

func cloneHardwareReceipt(result KeyResult) KeyResult {
	result.Candidate = cloneKeyCandidate(result.Candidate)
	result.LocalFiles = append([]KeyLocalFileObservation(nil), result.LocalFiles...)
	if result.Hardware != nil {
		copy := *result.Hardware
		result.Hardware = &copy
	}
	return result
}

func (s *Service) applyGeneratedSecurityKey(ctx context.Context, plan KeyPlan) (result KeyResult, resultErr error) {
	state, hardware := plan.state, plan.state.hardware
	hardware.mu.Lock()
	defer hardware.mu.Unlock()
	if hardware.attempted {
		return cloneHardwareReceipt(hardware.receipt), fmt.Errorf("this hardware enrollment plan was already attempted; review a fresh plan before another enrollment: %w", ErrBlocked)
	}
	result = KeyResult{Action: ActionNoop, Operation: KeyGenerate}
	defer func() {
		if hardware.attempted {
			hardware.receipt = cloneHardwareReceipt(result)
		}
	}()
	if err := s.RevalidateKeySelection(ctx, plan); err != nil {
		return result, err
	}
	parent := filepath.Dir(plan.IdentityFile)
	if parent == s.paths.SSHDir || plan.CreateParent != "" {
		if err := ensurePrivateChild(s.paths.Home, ".ssh", false); err != nil {
			return result, err
		}
	}
	if plan.CreateParent != "" {
		if filepath.Dir(plan.CreateParent) != s.paths.SSHDir || !samePath(parent, plan.CreateParent) {
			return result, ErrUnsafePath
		}
		if err := ensurePrivateChild(s.paths.SSHDir, filepath.Base(parent), true); err != nil {
			return result, err
		}
	}
	if err := s.validateKeyParent(parent); err != nil {
		return result, err
	}
	base, err := allocateSecurityKeyStagingBase(parent)
	if err != nil {
		return result, err
	}
	var privateStage, publicStage *stagedFile
	var captured [2]fs.FileInfo
	var localPublication [2]bool
	validated := false
	defer func() {
		retain := false
		if resultErr != nil && validated {
			identityPath := securityKeyRecoveryPath(privateStage, base, plan.IdentityFile)
			publicPath := securityKeyRecoveryPath(publicStage, base+".pub", plan.PublicPath)
			if identityPath != "" && publicPath != "" {
				result.Hardware.RecoveryIdentityPath, result.Hardware.RecoveryPublicPath = identityPath, publicPath
				result.Candidate.IdentityFile, result.Candidate.PublicPath = identityPath, publicPath
				result.Retained = true
				retain = true
			}
		}
		for index, stage := range []*stagedFile{privateStage, publicStage} {
			path, kind := base, "staging_identity"
			if index == 1 {
				path, kind = base+".pub", "staging_public"
			}
			var cleanupErr error
			if stage != nil {
				if retain {
					_ = stage.root.Close()
					stage.root = nil
				} else {
					cleanupErr = discardSecurityKeyStage(stage)
				}
			} else if captured[index] != nil {
				// Never acquire fresh deletion authority after a stale-source error.
				snapshot, err := readSecureFileIfExists(path, false)
				if err != nil {
					cleanupErr = err
				} else if snapshot.exists {
					if !sameSelectedKeyFileInfo(captured[index], snapshot.info) {
						cleanupErr = ErrSourceChanged
					} else {
						cleanupErr = removeSecureFile(snapshot)
					}
				}
			}
			if cleanupErr != nil {
				result.LocalFiles = append(result.LocalFiles, KeyLocalFileObservation{Path: path, Kind: kind, Status: "unknown"})
				resultErr = errors.Join(resultErr, fmt.Errorf("security-key staging path requires manual inspection: %w", cleanupErr))
			}
		}
		if resultErr != nil {
			for index, stage := range []*stagedFile{privateStage, publicStage} {
				if !localPublication[index] {
					continue
				}
				path, kind := plan.IdentityFile, "identity"
				if index == 1 {
					path, kind = plan.PublicPath, "public"
				}
				status := "unknown"
				if securityKeyRecoveryPath(stage, path) != "" {
					status = "retained"
					result.Retained = true
				}
				result.LocalFiles = append(result.LocalFiles, KeyLocalFileObservation{Path: path, Kind: kind, Status: status})
			}
			result.Candidate = cloneKeyCandidate(result.Candidate)
		}
	}()
	args := []string{"-q", "-t", string(plan.KeyType), "-f", base, "-w", hardware.options.Provider, "-O", "application=" + hardware.options.Application}
	if hardware.options.Resident {
		args = append(args, "-O", "resident")
	}
	if hardware.options.VerifyRequired {
		args = append(args, "-O", "verify-required")
	}
	if state.request.Comment != "" {
		args = append(args, "-C", state.request.Comment)
	}
	if state.request.NoPassphrase {
		args = append(args, "-N", "")
	}
	if err := s.revalidateSecurityKeyState(hardware); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	hardware.attempted = true
	started := func(time.Time) {
		result.Hardware = &HardwareKeyEffect{Kind: "fido", Status: "unknown", Resident: hardware.options.Resident}
	}
	run, runErr := s.runner.Run(ctx, RunRequest{Name: hardware.keygen.resolved, Args: args,
		Interactive: true, UnsetEnv: []string{"SSH_SK_PROVIDER"}, Display: "ssh-keygen enroll security key (native PIN/touch)", OnStarted: started})
	if runErr == nil && result.Hardware == nil {
		started(time.Time{})
	}
	if result.Hardware != nil {
		captured[0], _ = os.Lstat(base)
		captured[1], _ = os.Lstat(base + ".pub")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if runErr != nil || run.ExitCode != 0 {
		return result, errors.New("native security-key enrollment did not complete; inspect hardware state before any new attempt")
	}
	if err := s.revalidateSecurityKeyState(hardware); err != nil {
		return result, err
	}
	privateStage, err = s.hardenCapturedSecurityKeyFile(base, captured[0])
	if err != nil {
		return result, err
	}
	publicStage, err = s.hardenCapturedSecurityKeyFile(base+".pub", captured[1])
	if err != nil {
		return result, err
	}
	record, publicIdentity, err := s.readPublicKeySource(base + ".pub")
	if err != nil || record.metadata.Algorithm != plan.KeyType.algorithm() {
		return result, errors.New("native generation did not produce the requested security-key algorithm")
	}
	identity, err := s.inspectSelectedKeyIdentity(base)
	if err != nil {
		return result, err
	}
	if !sameSelectedKeyFileInfo(identity.info, privateStage.snapshot.info) || !sameSelectedKeyFileInfo(publicIdentity.info, publicStage.snapshot.info) {
		return result, ErrSourceChanged
	}
	derived, err := s.runner.Run(ctx, RunRequest{Name: hardware.keygen.resolved,
		Args: []string{"-y", "-f", base, "-w", hardware.options.Provider}, Interactive: true, CaptureStdout: true,
		UnsetEnv: []string{"SSH_SK_PROVIDER"}, Display: "ssh-keygen validate generated security-key stub"})
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err != nil || derived.ExitCode != 0 || derived.StdoutTruncated {
		return result, errors.New("native security-key stub validation did not complete")
	}
	derivedRecord, err := parsePublicKeyRecord(derived.Stdout)
	if err != nil || derivedRecord.metadata.Algorithm != plan.KeyType.algorithm() || derivedRecord.metadata.Fingerprint != record.metadata.Fingerprint {
		return result, errors.New("generated security-key stub and public key do not match")
	}
	if err := s.revalidateSecurityKeyState(hardware); err != nil {
		return result, err
	}
	if err := s.revalidateSelectedKeyIdentity(identity); err != nil {
		return result, err
	}
	if err := privateStage.revalidate(); err != nil {
		return result, err
	}
	if err := publicStage.revalidate(); err != nil {
		return result, err
	}
	validated = true
	result.Hardware.Status = "created"
	result.Candidate = KeyCandidate{Source: KeySourceGenerated, Sources: []KeySource{KeySourceGenerated},
		Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment, Fingerprint: record.metadata.Fingerprint,
		Provenance: KeyProvenance{SecurityKeyStub: true}}
	if s.beforeKeyCommit != nil {
		s.beforeKeyCommit()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.revalidateSecurityKeyState(hardware); err != nil {
		return result, err
	}
	if err := s.revalidateSelectedKeyIdentity(identity); err != nil {
		return result, err
	}
	if err := publicStage.revalidate(); err != nil {
		return result, err
	}
	published, err := commitNoReplaceObserved(privateStage, plan.IdentityFile, state.expectedPrivate)
	localPublication[0] = published
	if err != nil {
		result.PublicationUnknown = published
		if published {
			if info, statErr := os.Lstat(plan.IdentityFile); statErr == nil && os.SameFile(privateStage.snapshot.info, info) {
				result.Action, result.Created, result.Retained = ActionCreate, true, true
			}
		}
		return result, fmt.Errorf("publish security-key identity: %w", err)
	}
	result.Action, result.Created, result.Retained = ActionCreate, true, true
	if s.afterSecurityKeyIdentityCommit != nil {
		s.afterSecurityKeyIdentityCommit()
	}
	published, err = commitNoReplaceObserved(publicStage, plan.PublicPath, state.expectedPublic)
	localPublication[1] = published
	if err != nil {
		result.PublicationUnknown = published
		return result, fmt.Errorf("publish security-key public file: %w", err)
	}
	publishedIdentity, err := s.inspectSelectedKeyIdentity(plan.IdentityFile)
	if err != nil {
		result.PublicationUnknown = true
		return result, err
	}
	candidate := s.bindVerifiedKeyMaterial(KeyCandidate{Source: KeySourceGenerated, Sources: []KeySource{KeySourceGenerated},
		Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment, Fingerprint: record.metadata.Fingerprint,
		IdentityFile: plan.IdentityFile, PublicPath: plan.PublicPath, Provenance: KeyProvenance{SecurityKeyStub: true}}, record.normalized,
		&keyPairVerification{identity: publishedIdentity, publicPath: plan.PublicPath, fingerprint: record.metadata.Fingerprint})
	candidate.state.hardware = hardware
	if err := s.revalidateSelectedKeySources(ctx, candidate.state); err != nil {
		result.PublicationUnknown = true
		return result, err
	}
	result.Candidate = candidate
	return result, nil
}

func allocateSecurityKeyStagingBase(parent string) (string, error) {
	for range 32 {
		path, err := allocateKeyStagingBase(parent)
		if err != nil {
			return "", err
		}
		path = filepath.Join(parent, strings.Replace(filepath.Base(path), ".dev-key-", ".dev-sk-recovery-", 1))
		_, privateErr := os.Lstat(path)
		_, publicErr := os.Lstat(path + ".pub")
		if errors.Is(privateErr, fs.ErrNotExist) && errors.Is(publicErr, fs.ErrNotExist) {
			return path, nil
		}
	}
	return "", errors.New("could not allocate a security-key staging path")
}

func securityKeyRecoveryPath(stage *stagedFile, paths ...string) string {
	if stage == nil || verifyHeldDirectory(stage.dir, stage.held, true) != nil {
		return ""
	}
	for _, path := range paths {
		current, err := readSecureFile(path, false)
		if err == nil && os.SameFile(stage.snapshot.info, current.info) && current.digest == stage.snapshot.digest {
			return path
		}
	}
	return ""
}

func discardSecurityKeyStage(stage *stagedFile) error {
	if stage == nil || stage.root == nil {
		return nil
	}
	defer func() { _ = stage.root.Close(); stage.root = nil }()
	if _, err := stage.root.Lstat(stage.name); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := snapshotStillCurrentAt(stage.root, stage.name, stage.snapshot); err != nil {
		return err
	}
	return stage.root.Remove(stage.name)
}
