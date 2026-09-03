package sshhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxCatalogFiles      = 512
	maxCatalogDepth      = 16
	maxAgentCatalogBytes = 4 << 20
	maxKeyCommentBytes   = 4 << 10
)

type publicKeyRecord struct {
	metadata   KeyMetadata
	normalized []byte
	blob       []byte
}

type keyPairVerification struct {
	identity    secureFileIdentity
	publicPath  string
	fingerprint string
}

type keyMaterialState struct {
	serviceID        uint64
	safe             KeyCandidate
	publicLine       []byte
	pairVerification *keyPairVerification
}

type keyPlanState struct {
	serviceID       uint64
	public          KeyPlan
	request         KeyRequest
	material        *keyMaterialState
	identity        secureFileIdentity
	expectedPublic  fileSnapshot
	expectedPrivate fileSnapshot
}

type catalogEntry struct {
	safe       KeyCandidate
	publicLine []byte
}

// ParsePublicKey parses and fingerprints one bounded OpenSSH public-key record
// without returning the record or decoded wire bytes.
func ParsePublicKey(data []byte) (KeyMetadata, error) {
	record, err := parsePublicKeyRecord(data)
	if err != nil {
		return KeyMetadata{}, err
	}
	return record.metadata, nil
}

func parsePublicKeyRecord(data []byte) (publicKeyRecord, error) {
	if len(data) == 0 || len(data) > MaxPublicKeyLineBytes {
		return publicKeyRecord{}, fmt.Errorf("OpenSSH public record must be 1-%d bytes", MaxPublicKeyLineBytes)
	}
	line := append([]byte(nil), data...)
	if line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
	}
	if len(line) == 0 || bytes.IndexAny(line, "\r\n\x00") >= 0 || !utf8.Valid(line) {
		return publicKeyRecord{}, errors.New("OpenSSH public record is not one valid UTF-8 line")
	}
	text := strings.TrimSpace(string(line))
	algorithm, rest, ok := cutPublicField(text)
	if !ok || !validPublicAlgorithm(algorithm) {
		return publicKeyRecord{}, errors.New("OpenSSH public record has an invalid algorithm")
	}
	encoded, comment, ok := cutPublicField(rest)
	if !ok || encoded == "" {
		return publicKeyRecord{}, errors.New("OpenSSH public record has no key blob")
	}
	comment = strings.TrimSpace(comment)
	if len(comment) > maxKeyCommentBytes || !validUTF8NoControl(comment) {
		return publicKeyRecord{}, errors.New("OpenSSH public record has an invalid comment")
	}
	blob, err := decodePublicBlob(encoded)
	if err != nil {
		return publicKeyRecord{}, errors.New("OpenSSH public record has an invalid base64 blob")
	}
	if len(blob) == 0 || len(blob) > MaxPublicKeyBlobBytes {
		return publicKeyRecord{}, fmt.Errorf("OpenSSH public blob must be 1-%d bytes", MaxPublicKeyBlobBytes)
	}
	if err := validatePublicWireBlob(algorithm, blob); err != nil {
		return publicKeyRecord{}, fmt.Errorf("validate OpenSSH public blob: %w", err)
	}
	sum := sha256.Sum256(blob)
	metadata := KeyMetadata{
		Algorithm:   algorithm,
		Comment:     comment,
		Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
	}
	normalized := algorithm + " " + base64.StdEncoding.EncodeToString(blob)
	if comment != "" {
		normalized += " " + comment
	}
	return publicKeyRecord{metadata: metadata, normalized: []byte(normalized), blob: append([]byte(nil), blob...)}, nil
}

func cutPublicField(value string) (field, rest string, ok bool) {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if value == "" {
		return "", "", false
	}
	index := strings.IndexFunc(value, unicode.IsSpace)
	if index < 0 {
		return value, "", true
	}
	return value[:index], strings.TrimLeftFunc(value[index:], unicode.IsSpace), true
}

func validPublicAlgorithm(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func decodePublicBlob(encoded string) ([]byte, error) {
	blob, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err == nil {
		return blob, nil
	}
	return base64.RawStdEncoding.Strict().DecodeString(encoded)
}

func validatePublicWireBlob(algorithm string, blob []byte) error {
	wireAlgorithm, rest, err := consumeSSHString(blob)
	if err != nil || string(wireAlgorithm) != algorithm {
		return errors.New("wire algorithm does not match the record algorithm")
	}
	if strings.HasSuffix(algorithm, "-cert-v01@openssh.com") {
		return validateCertificateWire(algorithm, rest)
	}
	switch {
	case algorithm == "ssh-ed25519":
		key, tail, err := consumeSSHString(rest)
		if err != nil || len(key) != 32 || len(tail) != 0 {
			return errors.New("invalid Ed25519 wire key")
		}
		return nil
	case algorithm == "sk-ssh-ed25519@openssh.com":
		key, tail, err := consumeSSHString(rest)
		if err != nil || len(key) != 32 {
			return errors.New("invalid security-key Ed25519 wire key")
		}
		application, tail, err := consumeSSHString(tail)
		if err != nil || len(application) == 0 || len(tail) != 0 {
			return errors.New("invalid security-key application")
		}
		return nil
	case algorithm == "ssh-rsa":
		return validateStringFields(rest, 2, "RSA")
	case algorithm == "ssh-dss":
		return validateStringFields(rest, 4, "DSA")
	case strings.HasPrefix(algorithm, "ecdsa-sha2-"):
		curve, tail, err := consumeSSHString(rest)
		if err != nil || string(curve) != strings.TrimPrefix(algorithm, "ecdsa-sha2-") {
			return errors.New("ECDSA curve does not match the algorithm")
		}
		point, tail, err := consumeSSHString(tail)
		if err != nil || len(point) == 0 || len(tail) != 0 {
			return errors.New("invalid ECDSA point")
		}
		return nil
	case algorithm == "sk-ecdsa-sha2-nistp256@openssh.com":
		curve, tail, err := consumeSSHString(rest)
		if err != nil || string(curve) != "nistp256" {
			return errors.New("invalid security-key ECDSA curve")
		}
		point, tail, err := consumeSSHString(tail)
		if err != nil || len(point) == 0 {
			return errors.New("invalid security-key ECDSA point")
		}
		application, tail, err := consumeSSHString(tail)
		if err != nil || len(application) == 0 || len(tail) != 0 {
			return errors.New("invalid security-key application")
		}
		return nil
	default:
		return fmt.Errorf("unsupported public-key algorithm %q", algorithm)
	}
}

func validateStringFields(rest []byte, count int, label string) error {
	for index := 0; index < count; index++ {
		field, tail, err := consumeSSHString(rest)
		if err != nil || len(field) == 0 {
			return fmt.Errorf("invalid %s wire field", label)
		}
		rest = tail
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected data after %s wire key", label)
	}
	return nil
}

func certificateBaseAlgorithm(algorithm string) string {
	switch algorithm {
	case "sk-ssh-ed25519-cert-v01@openssh.com":
		return "sk-ssh-ed25519@openssh.com"
	case "sk-ecdsa-sha2-nistp256-cert-v01@openssh.com":
		return "sk-ecdsa-sha2-nistp256@openssh.com"
	default:
		return strings.TrimSuffix(algorithm, "-cert-v01@openssh.com")
	}
}

func validateCertificateWire(algorithm string, rest []byte) error {
	nonce, rest, err := consumeSSHString(rest)
	if err != nil || len(nonce) == 0 {
		return errors.New("certificate has no nonce")
	}
	base := certificateBaseAlgorithm(algorithm)
	switch {
	case base == "ssh-ed25519":
		key, tail, err := consumeSSHString(rest)
		if err != nil || len(key) != 32 {
			return errors.New("invalid Ed25519 certificate key")
		}
		rest = tail
	case base == "ssh-rsa":
		for range 2 {
			field, tail, err := consumeSSHString(rest)
			if err != nil || len(field) == 0 {
				return errors.New("invalid RSA certificate key")
			}
			rest = tail
		}
	case strings.HasPrefix(base, "ecdsa-sha2-"):
		curve, tail, err := consumeSSHString(rest)
		if err != nil || string(curve) != strings.TrimPrefix(base, "ecdsa-sha2-") {
			return errors.New("invalid ECDSA certificate curve")
		}
		point, tail, err := consumeSSHString(tail)
		if err != nil || len(point) == 0 {
			return errors.New("invalid ECDSA certificate point")
		}
		rest = tail
	case base == "sk-ssh-ed25519@openssh.com":
		key, tail, err := consumeSSHString(rest)
		if err != nil || len(key) != 32 {
			return errors.New("invalid security-key Ed25519 certificate key")
		}
		application, tail, err := consumeSSHString(tail)
		if err != nil || len(application) == 0 {
			return errors.New("invalid security-key certificate application")
		}
		rest = tail
	case base == "sk-ecdsa-sha2-nistp256@openssh.com":
		curve, tail, err := consumeSSHString(rest)
		if err != nil || string(curve) != "nistp256" {
			return errors.New("invalid security-key ECDSA certificate curve")
		}
		point, tail, err := consumeSSHString(tail)
		if err != nil || len(point) == 0 {
			return errors.New("invalid security-key ECDSA certificate point")
		}
		application, tail, err := consumeSSHString(tail)
		if err != nil || len(application) == 0 {
			return errors.New("invalid security-key certificate application")
		}
		rest = tail
	default:
		return fmt.Errorf("unsupported certificate algorithm %q", algorithm)
	}
	if len(rest) < 12 {
		return errors.New("truncated certificate serial or type")
	}
	rest = rest[12:]
	for range 2 { // key ID and valid principals
		_, tail, err := consumeSSHString(rest)
		if err != nil {
			return errors.New("invalid certificate identity fields")
		}
		rest = tail
	}
	if len(rest) < 16 {
		return errors.New("truncated certificate validity")
	}
	rest = rest[16:]
	for index := range 5 { // critical, extensions, reserved, signature key, signature
		field, tail, err := consumeSSHString(rest)
		if err != nil {
			return errors.New("invalid certificate trailing fields")
		}
		if index >= 3 && len(field) == 0 {
			return errors.New("certificate has an empty signature field")
		}
		rest = tail
	}
	if len(rest) != 0 {
		return errors.New("unexpected certificate trailing data")
	}
	return nil
}

func basePublicKeyRecord(record publicKeyRecord) (publicKeyRecord, error) {
	algorithm := record.metadata.Algorithm
	if !strings.HasSuffix(algorithm, "-cert-v01@openssh.com") {
		return record, nil
	}
	base := certificateBaseAlgorithm(algorithm)
	_, rest, err := consumeSSHString(record.blob)
	if err != nil {
		return publicKeyRecord{}, err
	}
	_, rest, err = consumeSSHString(rest) // certificate nonce
	if err != nil {
		return publicKeyRecord{}, err
	}
	fieldCount := 0
	switch {
	case base == "ssh-ed25519", base == "sk-ssh-ed25519@openssh.com":
		fieldCount = 1
		if strings.HasPrefix(base, "sk-") {
			fieldCount = 2
		}
	case base == "ssh-rsa", strings.HasPrefix(base, "ecdsa-sha2-"):
		fieldCount = 2
	case base == "sk-ecdsa-sha2-nistp256@openssh.com":
		fieldCount = 3
	default:
		return publicKeyRecord{}, fmt.Errorf("unsupported certificate algorithm %q", algorithm)
	}
	keyFields := rest
	for range fieldCount {
		_, tail, consumeErr := consumeSSHString(rest)
		if consumeErr != nil {
			return publicKeyRecord{}, consumeErr
		}
		rest = tail
	}
	keyFields = keyFields[:len(keyFields)-len(rest)]
	baseBlob := make([]byte, 4+len(base)+len(keyFields))
	binary.BigEndian.PutUint32(baseBlob, uint32(len(base)))
	copy(baseBlob[4:], base)
	copy(baseBlob[4+len(base):], keyFields)
	line := []byte(base + " " + base64.StdEncoding.EncodeToString(baseBlob))
	return parsePublicKeyRecord(line)
}

func consumeSSHString(data []byte) ([]byte, []byte, error) {
	if len(data) < 4 {
		return nil, nil, io.ErrUnexpectedEOF
	}
	length := uint64(binary.BigEndian.Uint32(data[:4]))
	if length > uint64(len(data)-4) {
		return nil, nil, io.ErrUnexpectedEOF
	}
	end := 4 + int(length)
	return data[4:end], data[end:], nil
}

// Catalog returns de-duplicated public candidates keyed by SHA256 fingerprint.
func (s *Service) Catalog(ctx context.Context, request KeyCatalogRequest) (KeyCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return KeyCatalog{}, err
	}
	var effective EffectiveConfig
	if request.Effective == nil {
		if err := ValidateLookupAlias(request.Alias); err != nil {
			return KeyCatalog{}, err
		}
		resolved, err := s.Effective(ctx, request.Alias)
		if err != nil {
			return KeyCatalog{}, err
		}
		effective = resolved
	} else {
		effective = cloneEffective(*request.Effective)
		if err := ValidateLookupAlias(effective.Alias); err != nil {
			return KeyCatalog{}, err
		}
		if request.Alias != "" && !equalAlias(request.Alias, effective.Alias) {
			return KeyCatalog{}, errors.New("catalog alias does not match effective config")
		}
	}

	catalog := KeyCatalog{Complete: true}
	entries := make([]catalogEntry, 0)
	byFingerprint := make(map[string]int)
	add := func(record publicKeyRecord, candidate KeyCandidate) {
		candidate.Algorithm = record.metadata.Algorithm
		candidate.Comment = record.metadata.Comment
		candidate.Fingerprint = record.metadata.Fingerprint
		if len(candidate.Sources) == 0 {
			candidate.Sources = []KeySource{candidate.Source}
		}
		if index, ok := byFingerprint[candidate.Fingerprint]; ok {
			mergeCatalogEntry(&entries[index], candidate)
			return
		}
		byFingerprint[candidate.Fingerprint] = len(entries)
		entries = append(entries, catalogEntry{safe: candidate, publicLine: append([]byte(nil), record.normalized...)})
	}
	addDiagnostic := func(code, path string, incomplete bool) {
		catalog.Diagnostics = append(catalog.Diagnostics, Diagnostic{Code: code, Path: path, Incomplete: incomplete})
		if incomplete {
			catalog.Complete = false
		}
	}
	publicRecords := make(map[string]publicKeyRecord)
	addPath := func(path string, source KeySource, effectiveIdentity string) {
		path = filepath.Clean(path)
		record, cached := publicRecords[path]
		if !cached {
			var err error
			record, err = s.readPublicKeyFile(path)
			if err != nil {
				addDiagnostic("public_key_unreadable", path, true)
				return
			}
			publicRecords[path] = record
		}
		identity := strings.TrimSuffix(path, ".pub")
		if effectiveIdentity != "" {
			identity = effectiveIdentity
		}
		private, stub := s.inspectIdentityProvenance(identity, record.metadata.Algorithm)
		if !private && !stub && effectiveIdentity == "" {
			identity = path
		}
		candidate := KeyCandidate{
			Source: source, PublicPath: path, IdentityFile: identity,
			Provenance: KeyProvenance{
				Effective: source == KeySourceEffectiveIdentity,
				Private:   private, SecurityKeyStub: stub,
			},
		}
		add(record, candidate)
	}

	for _, raw := range effective.IdentityFiles {
		identity, err := s.resolveSSHKeyPath(raw)
		if err != nil || strings.EqualFold(raw, "none") {
			addDiagnostic("identity_file_unsupported", "", false)
			continue
		}
		publicPath := identity
		if !strings.HasSuffix(strings.ToLower(publicPath), ".pub") {
			publicPath += ".pub"
		}
		if !s.pathWithinSSH(publicPath) {
			addDiagnostic("identity_file_outside_ssh_tree", "", false)
			continue
		}
		addPath(publicPath, KeySourceEffectiveIdentity, identity)
	}

	if info, err := os.Lstat(s.paths.SSHDir); err == nil && info.IsDir() {
		files := 0
		walkErr := filepath.WalkDir(s.paths.SSHDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				addDiagnostic("public_key_tree_unreadable", path, true)
				return nil
			}
			relative, err := filepath.Rel(s.paths.SSHDir, path)
			if err != nil {
				addDiagnostic("public_key_tree_unreadable", path, true)
				return nil
			}
			if relative != "." && strings.Count(filepath.ToSlash(relative), "/") >= maxCatalogDepth && entry.IsDir() {
				addDiagnostic("public_key_depth_exceeded", path, true)
				return filepath.SkipDir
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".pub") {
				return nil
			}
			files++
			if files > maxCatalogFiles {
				addDiagnostic("public_key_file_limit_exceeded", "", true)
				return fs.SkipAll
			}
			addPath(filepath.Clean(path), KeySourcePublicFile, "")
			return nil
		})
		if walkErr != nil {
			if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
				return KeyCatalog{}, walkErr
			}
			addDiagnostic("public_key_tree_unreadable", s.paths.SSHDir, true)
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		addDiagnostic("public_key_tree_unreadable", s.paths.SSHDir, true)
	}

	agentEnabled := !request.NoAgent
	agentEnv := []string{"LC_ALL=C"}
	if values := effective.Values["identityagent"]; agentEnabled && len(values) > 0 {
		configured, enabled, resolveErr := s.resolveIdentityAgent(values[0])
		if resolveErr != nil {
			addDiagnostic("identity_agent_unsupported", "", true)
			agentEnabled = false
		} else {
			agentEnabled = enabled
			if configured != "" {
				agentEnv = append(agentEnv, "SSH_AUTH_SOCK="+configured)
			}
		}
	}
	if agentEnabled {
		result, err := s.runner.Run(ctx, RunRequest{
			Name: "ssh-add", Args: []string{"-L"}, Env: agentEnv, Display: "ssh-add -L",
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return KeyCatalog{}, ctxErr
			}
			addDiagnostic("agent_unavailable", "", true)
		} else if result.ExitCode == 0 {
			if result.StdoutTruncated || len(result.Stdout) > maxAgentCatalogBytes {
				addDiagnostic("agent_output_limit_exceeded", "", true)
			} else {
				lines := bytes.Split(result.Stdout, []byte{'\n'})
				if len(lines) > maxCatalogFiles+1 {
					addDiagnostic("agent_key_limit_exceeded", "", true)
					lines = lines[:maxCatalogFiles]
				}
				for _, line := range lines {
					if len(bytes.TrimSpace(line)) == 0 {
						continue
					}
					record, parseErr := parsePublicKeyRecord(line)
					if parseErr != nil {
						addDiagnostic("agent_key_invalid", "", true)
						continue
					}
					add(record, KeyCandidate{Source: KeySourceAgent, Provenance: KeyProvenance{Agent: true}})
				}
			}
		} else if result.ExitCode != 1 {
			addDiagnostic("agent_unavailable", "", true)
		}
	}

	for _, entry := range entries {
		catalog.Candidates = append(catalog.Candidates, s.bindKeyMaterial(entry.safe, entry.publicLine))
	}
	return catalog, nil
}

func (s *Service) resolveIdentityAgent(value string) (path string, enabled bool, err error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "", strings.EqualFold(value, "SSH_AUTH_SOCK"):
		return "", true, nil
	case strings.EqualFold(value, "none"):
		return "", false, nil
	}
	resolved := value
	switch {
	case strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\\`):
		resolved = filepath.Join(s.paths.Home, filepath.FromSlash(value[2:]))
	case strings.HasPrefix(value, "%d/") || strings.HasPrefix(value, `%d\\`):
		resolved = filepath.Join(s.paths.Home, filepath.FromSlash(value[3:]))
	case strings.HasPrefix(value, "${HOME}/") || strings.HasPrefix(value, `${HOME}\\`):
		resolved = filepath.Join(s.paths.Home, filepath.FromSlash(value[len("${HOME}/"):]))
	case strings.HasPrefix(value, "~") || strings.ContainsAny(value, "%$"):
		return "", false, ErrUnsupportedRoute
	}
	if !filepath.IsAbs(resolved) || !validUTF8NoControl(resolved) {
		return "", false, ErrUnsafePath
	}
	return filepath.Clean(resolved), true, nil
}

func cloneEffective(effective EffectiveConfig) EffectiveConfig {
	copy := effective
	copy.IdentityFiles = append([]string(nil), effective.IdentityFiles...)
	copy.Values = make(map[string][]string, len(effective.Values))
	for key, values := range effective.Values {
		copy.Values[key] = append([]string(nil), values...)
	}
	if effective.IdentitiesOnly != nil {
		value := *effective.IdentitiesOnly
		copy.IdentitiesOnly = &value
	}
	return copy
}

func mergeCatalogEntry(entry *catalogEntry, candidate KeyCandidate) {
	wasEffective := entry.safe.Provenance.Effective
	for _, source := range candidate.Sources {
		if !containsKeySource(entry.safe.Sources, source) {
			entry.safe.Sources = append(entry.safe.Sources, source)
		}
	}
	if !containsKeySource(entry.safe.Sources, candidate.Source) {
		entry.safe.Sources = append(entry.safe.Sources, candidate.Source)
	}
	entry.safe.Provenance.Effective = entry.safe.Provenance.Effective || candidate.Provenance.Effective
	entry.safe.Provenance.Private = entry.safe.Provenance.Private || candidate.Provenance.Private
	entry.safe.Provenance.SecurityKeyStub = entry.safe.Provenance.SecurityKeyStub || candidate.Provenance.SecurityKeyStub
	entry.safe.Provenance.Agent = entry.safe.Provenance.Agent || candidate.Provenance.Agent
	if entry.safe.PublicPath == "" && candidate.PublicPath != "" {
		entry.safe.PublicPath = candidate.PublicPath
	}
	if entry.safe.IdentityFile == "" && candidate.IdentityFile != "" {
		entry.safe.IdentityFile = candidate.IdentityFile
	}
	if candidate.Provenance.Effective && !wasEffective {
		entry.safe.Source = candidate.Source
	}
}

func containsKeySource(values []KeySource, value KeySource) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (s *Service) bindKeyMaterial(safe KeyCandidate, publicLine []byte) KeyCandidate {
	return s.bindVerifiedKeyMaterial(safe, publicLine, nil)
}

func (s *Service) bindVerifiedKeyMaterial(safe KeyCandidate, publicLine []byte, verification *keyPairVerification) KeyCandidate {
	safe.state = nil
	safe.Sources = append([]KeySource(nil), safe.Sources...)
	state := &keyMaterialState{
		serviceID: s.id, safe: safe, publicLine: append([]byte(nil), publicLine...),
		pairVerification: cloneKeyPairVerification(verification),
	}
	bound := safe
	bound.state = state
	return bound
}

func cloneKeyPairVerification(verification *keyPairVerification) *keyPairVerification {
	if verification == nil {
		return nil
	}
	copy := *verification
	return &copy
}

func (s *Service) validateKeyCandidate(candidate KeyCandidate) (*keyMaterialState, error) {
	if candidate.state == nil || candidate.state.serviceID != s.id {
		return nil, errors.New("key candidate was not produced by this service")
	}
	public := candidate
	public.state = nil
	if !reflect.DeepEqual(public, candidate.state.safe) {
		return nil, errors.New("key candidate public fields were modified")
	}
	if len(candidate.state.publicLine) == 0 {
		return nil, errors.New("key candidate has no private material state")
	}
	return candidate.state, nil
}

// PlanKey performs only local bounded reads. It never invokes ssh, ssh-add, or
// ssh-keygen and never creates a file.
func (s *Service) PlanKey(ctx context.Context, request KeyRequest) (KeyPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return KeyPlan{}, err
	}
	operation := request.Operation
	if operation == "" {
		operation = KeyUse
	}
	if operation != KeyUse && operation != KeyGenerate {
		return KeyPlan{}, fmt.Errorf("unsupported key operation %q", operation)
	}
	request.Operation = operation
	if operation == KeyGenerate {
		return s.planGeneratedKey(request)
	}
	if request.Candidate.state != nil {
		if request.Path != "" {
			return KeyPlan{}, errors.New("key request cannot select both candidate and path")
		}
		material, err := s.validateKeyCandidate(request.Candidate)
		if err != nil {
			return KeyPlan{}, err
		}
		plan := keyPlanForMaterial(ActionNoop, KeyUse, material.safe)
		state := &keyPlanState{serviceID: s.id, request: request, material: material}
		state.public = plan
		plan.state = state
		return plan, nil
	}
	if request.Path == "" {
		return KeyPlan{}, errors.New("key path or catalog candidate is required")
	}
	selected, err := s.resolveSSHKeyPath(request.Path)
	if err != nil {
		return KeyPlan{}, err
	}
	if strings.HasSuffix(strings.ToLower(selected), ".pub") {
		record, err := s.readPublicKeyFile(selected)
		if err != nil {
			return KeyPlan{}, err
		}
		identity := strings.TrimSuffix(selected, ".pub")
		private, stub := s.inspectIdentityProvenance(identity, record.metadata.Algorithm)
		if !private && !stub {
			identity = selected
		}
		candidate := KeyCandidate{
			Source: KeySourceExplicit, Sources: []KeySource{KeySourceExplicit},
			Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
			Fingerprint: record.metadata.Fingerprint, PublicPath: selected, IdentityFile: identity,
			Provenance: KeyProvenance{Private: private, SecurityKeyStub: stub},
		}
		bound := s.bindKeyMaterial(candidate, record.normalized)
		material, _ := s.validateKeyCandidate(bound)
		plan := keyPlanForMaterial(ActionNoop, KeyUse, candidate)
		state := &keyPlanState{serviceID: s.id, request: request, material: material}
		state.public = plan
		plan.state = state
		return plan, nil
	}

	identity, err := s.inspectSecureIdentity(selected)
	if err != nil {
		return KeyPlan{}, fmt.Errorf("inspect selected identity: %w", err)
	}
	publicPath := selected + ".pub"
	expectedPublic, err := s.inspectPublicDestination(publicPath)
	if err != nil {
		return KeyPlan{}, err
	}
	if expectedPublic.exists {
		record, err := s.readPublicKeyFile(publicPath)
		if err != nil {
			return KeyPlan{}, err
		}
		candidate := KeyCandidate{
			Source: KeySourceExplicit, Sources: []KeySource{KeySourceExplicit},
			Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
			Fingerprint: record.metadata.Fingerprint, PublicPath: publicPath, IdentityFile: selected,
			Provenance: KeyProvenance{
				Private:         !strings.HasPrefix(record.metadata.Algorithm, "sk-"),
				SecurityKeyStub: strings.HasPrefix(record.metadata.Algorithm, "sk-"),
			},
		}
		bound := s.bindKeyMaterial(candidate, record.normalized)
		material, _ := s.validateKeyCandidate(bound)
		plan := keyPlanForMaterial(ActionNoop, KeyUse, candidate)
		state := &keyPlanState{serviceID: s.id, request: request, material: material}
		state.public = plan
		plan.state = state
		return plan, nil
	}

	plan := KeyPlan{Action: ActionBlocked, Operation: KeyDerive, Source: KeySourceDerived, PublicPath: publicPath, IdentityFile: selected}
	if !request.AllowDerive {
		plan.Diagnostics = []Diagnostic{{Code: "derive_confirmation_required", Path: publicPath, BlocksMutation: true}}
		return plan, nil
	}
	plan.Action = ActionCreate
	state := &keyPlanState{
		serviceID: s.id, request: request, identity: identity, expectedPublic: expectedPublic,
	}
	state.request.Operation = KeyDerive
	state.public = plan
	plan.state = state
	return plan, nil
}

func keyPlanForMaterial(action PlanAction, operation KeyOperation, candidate KeyCandidate) KeyPlan {
	return KeyPlan{
		Action: action, Operation: operation, Source: candidate.Source,
		Algorithm: candidate.Algorithm, Comment: candidate.Comment, Fingerprint: candidate.Fingerprint,
		PublicPath: candidate.PublicPath, IdentityFile: candidate.IdentityFile,
	}
}

func (s *Service) planGeneratedKey(request KeyRequest) (KeyPlan, error) {
	if !request.Interactive && !request.NoPassphrase {
		return KeyPlan{
			Action: ActionBlocked, Operation: KeyGenerate, Source: KeySourceGenerated,
			Diagnostics: []Diagnostic{{Code: "interaction_required", BlocksMutation: true}},
		}, nil
	}
	if !validUTF8NoControl(request.Comment) || len(request.Comment) > maxKeyCommentBytes {
		return KeyPlan{}, errors.New("generated key comment is invalid")
	}
	destination := request.DestinationIdentity
	if destination == "" {
		destination = filepath.Join(s.paths.SSHDir, "id_ed25519_dev")
	}
	resolved, err := s.resolveSSHKeyPath(destination)
	if err != nil {
		return KeyPlan{}, err
	}
	if strings.HasSuffix(strings.ToLower(resolved), ".pub") {
		return KeyPlan{}, errors.New("generated identity destination must not end in .pub")
	}
	if err := s.validateKeyParent(filepath.Dir(resolved)); err != nil {
		return KeyPlan{}, err
	}
	expectedPrivate, err := s.inspectPrivateDestination(resolved)
	if err != nil {
		return KeyPlan{}, err
	}
	expectedPublic, err := s.inspectPublicDestination(resolved + ".pub")
	if err != nil {
		return KeyPlan{}, err
	}
	plan := KeyPlan{
		Action: ActionCreate, Operation: KeyGenerate, Source: KeySourceGenerated,
		Algorithm: "ssh-ed25519", Comment: request.Comment,
		PublicPath: resolved + ".pub", IdentityFile: resolved,
	}
	if expectedPrivate.exists || expectedPublic.exists {
		plan.Action = ActionBlocked
		plan.Diagnostics = []Diagnostic{{Code: "key_collision", Path: resolved, BlocksMutation: true}}
		return plan, nil
	}
	request.DestinationIdentity = resolved
	state := &keyPlanState{
		serviceID: s.id, request: request, expectedPrivate: expectedPrivate, expectedPublic: expectedPublic,
	}
	state.public = plan
	plan.state = state
	return plan, nil
}

// ApplyKey consumes a source-bound plan. Native ssh-keygen receives no
// passphrase in argv or environment except the explicit empty -N value required
// by a noninteractive NoPassphrase generation request.
func (s *Service) ApplyKey(ctx context.Context, plan KeyPlan) (KeyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if plan.Action == ActionBlocked || plan.state == nil {
		return KeyResult{Action: plan.Action, Operation: plan.Operation}, ErrBlocked
	}
	state := plan.state
	if state.serviceID != s.id || !equalKeyPlans(plan, state.public) {
		return KeyResult{}, errors.New("key plan was not produced by this service or its public fields were modified")
	}
	switch plan.Operation {
	case KeyUse:
		if state.material == nil {
			return KeyResult{}, errors.New("selected key plan has no material state")
		}
		verification, err := s.verifyKeyPair(ctx, state.material, state.request.Interactive)
		if err != nil {
			return KeyResult{}, err
		}
		candidate := s.bindVerifiedKeyMaterial(state.material.safe, state.material.publicLine, verification)
		return KeyResult{Action: ActionNoop, Operation: KeyUse, Candidate: candidate}, nil
	case KeyDerive:
		return s.applyDerivedKey(ctx, plan)
	case KeyGenerate:
		return s.applyGeneratedKey(ctx, plan)
	default:
		return KeyResult{}, fmt.Errorf("unsupported key plan operation %q", plan.Operation)
	}
}

func equalKeyPlans(left, right KeyPlan) bool {
	left.state = nil
	right.state = nil
	return reflect.DeepEqual(left, right)
}

func noninteractiveKeygenEnv() []string {
	return []string{"SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=never", "DISPLAY="}
}

// verifyKeyPair proves that a private identity or security-key stub matches the
// public companion whose bytes will be installed. It never reads private bytes:
// native ssh-keygen derives the public half and owns any interactive prompt.
func (s *Service) verifyKeyPair(ctx context.Context, material *keyMaterialState, interactive bool) (*keyPairVerification, error) {
	if material == nil {
		return nil, errors.New("selected key has no material state")
	}
	safe := material.safe
	if !safe.Provenance.Private && !safe.Provenance.SecurityKeyStub {
		return nil, nil
	}
	if safe.IdentityFile == "" || safe.PublicPath == "" {
		return nil, errors.New("private-backed selected key has no public companion")
	}
	identity, err := s.inspectSecureIdentity(safe.IdentityFile)
	if err != nil {
		return nil, fmt.Errorf("validate selected identity: %w", err)
	}
	companion, err := s.readPublicKeyFile(safe.PublicPath)
	if err != nil {
		return nil, fmt.Errorf("validate selected public companion: %w", err)
	}
	if companion.metadata.Fingerprint != safe.Fingerprint || !publicLinesEqual(companion.normalized, material.publicLine) {
		return nil, errors.New("selected public companion changed")
	}
	if previous := material.pairVerification; previous != nil {
		if previous.publicPath != safe.PublicPath || previous.fingerprint != companion.metadata.Fingerprint ||
			previous.identity.path != identity.path || !stableFileInfo(previous.identity.info, identity.info) {
			return nil, fmt.Errorf("selected key pair changed after verification: %w", ErrSourceChanged)
		}
		return cloneKeyPairVerification(previous), nil
	}

	request := RunRequest{
		Name: "ssh-keygen", Args: []string{"-y", "-f", identity.path},
		Interactive: interactive, CaptureStdout: true, Display: "ssh-keygen verify public companion",
	}
	if !interactive {
		request.Env = noninteractiveKeygenEnv()
	}
	result, err := s.runner.Run(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("derive selected SSH public key: %w", err)
	}
	if result.ExitCode != 0 {
		if !interactive {
			return nil, ErrInteractionRequired
		}
		return nil, fmt.Errorf("derive selected SSH public key: ssh-keygen exited with status %d", result.ExitCode)
	}
	derived, err := parsePublicKeyRecord(result.Stdout)
	if err != nil {
		return nil, errors.New("derive selected SSH public key: invalid ssh-keygen output")
	}
	comparison, err := basePublicKeyRecord(companion)
	if err != nil {
		return nil, fmt.Errorf("validate selected public companion: %w", err)
	}
	if derived.metadata.Fingerprint != comparison.metadata.Fingerprint {
		return nil, errors.New("selected SSH private key and public companion do not match")
	}
	if err := s.revalidateSecureIdentity(identity); err != nil {
		return nil, fmt.Errorf("selected identity changed during verification: %w", ErrSourceChanged)
	}
	current, err := s.readPublicKeyFile(safe.PublicPath)
	if err != nil || current.metadata.Fingerprint != companion.metadata.Fingerprint || !publicLinesEqual(current.normalized, companion.normalized) {
		return nil, fmt.Errorf("selected public companion changed during verification: %w", ErrSourceChanged)
	}
	return &keyPairVerification{
		identity: identity, publicPath: safe.PublicPath, fingerprint: companion.metadata.Fingerprint,
	}, nil
}

func (s *Service) applyDerivedKey(ctx context.Context, plan KeyPlan) (KeyResult, error) {
	state := plan.state
	if err := s.revalidateSecureIdentity(state.identity); err != nil {
		return KeyResult{}, fmt.Errorf("selected identity changed before derivation: %w", ErrSourceChanged)
	}
	request := RunRequest{
		Name: "ssh-keygen", Args: []string{"-y", "-f", state.identity.path},
		Interactive: state.request.Interactive, CaptureStdout: true, Display: "ssh-keygen derive public key",
	}
	if !state.request.Interactive {
		request.Env = noninteractiveKeygenEnv()
	}
	result, err := s.runner.Run(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return KeyResult{}, ctxErr
		}
		if !state.request.Interactive {
			return KeyResult{}, ErrInteractionRequired
		}
		return KeyResult{}, fmt.Errorf("derive SSH public key: %w", err)
	}
	if result.ExitCode != 0 {
		if !state.request.Interactive {
			return KeyResult{}, ErrInteractionRequired
		}
		return KeyResult{}, fmt.Errorf("derive SSH public key: ssh-keygen exited with status %d", result.ExitCode)
	}
	record, err := parsePublicKeyRecord(result.Stdout)
	if err != nil {
		return KeyResult{}, fmt.Errorf("derive SSH public key: invalid ssh-keygen output")
	}
	if err := s.revalidateSecureIdentity(state.identity); err != nil {
		return KeyResult{}, fmt.Errorf("selected identity changed during derivation: %w", ErrSourceChanged)
	}
	staged, err := createStagedFile(filepath.Dir(plan.PublicPath), append(append([]byte(nil), record.normalized...), '\n'), nil)
	if err != nil {
		return KeyResult{}, err
	}
	defer staged.discard()
	if s.beforeKeyCommit != nil {
		s.beforeKeyCommit()
	}
	if err := s.revalidateSecureIdentity(state.identity); err != nil {
		return KeyResult{}, fmt.Errorf("selected identity changed before publication: %w", ErrSourceChanged)
	}
	if err := commitNoReplace(staged, plan.PublicPath, state.expectedPublic); err != nil {
		return KeyResult{}, fmt.Errorf("publish derived public key: %w", ErrKeyCollision)
	}
	candidate := KeyCandidate{
		Source: KeySourceDerived, Sources: []KeySource{KeySourceDerived},
		Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
		Fingerprint: record.metadata.Fingerprint, PublicPath: plan.PublicPath, IdentityFile: plan.IdentityFile,
		Provenance: KeyProvenance{
			Private:         !strings.HasPrefix(record.metadata.Algorithm, "sk-"),
			SecurityKeyStub: strings.HasPrefix(record.metadata.Algorithm, "sk-"),
		},
	}
	verification := &keyPairVerification{
		identity: state.identity, publicPath: plan.PublicPath, fingerprint: record.metadata.Fingerprint,
	}
	bound := s.bindVerifiedKeyMaterial(candidate, record.normalized, verification)
	return KeyResult{
		Action: ActionCreate, Operation: KeyDerive, Candidate: bound, Created: true,
	}, nil
}

func (s *Service) applyGeneratedKey(ctx context.Context, plan KeyPlan) (KeyResult, error) {
	state := plan.state
	parent := filepath.Dir(plan.IdentityFile)
	stagingBase, err := allocateKeyStagingBase(parent)
	if err != nil {
		return KeyResult{}, err
	}
	defer os.Remove(stagingBase)
	defer os.Remove(stagingBase + ".pub")
	args := []string{"-q", "-t", "ed25519", "-f", stagingBase}
	if state.request.Comment != "" {
		args = append(args, "-C", state.request.Comment)
	}
	if !state.request.Interactive {
		args = append(args, "-N", "")
	}
	runResult, err := s.runner.Run(ctx, RunRequest{
		Name: "ssh-keygen", Args: args, Interactive: state.request.Interactive,
		Display: "ssh-keygen generate Ed25519 key",
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return KeyResult{}, ctxErr
		}
		return KeyResult{}, fmt.Errorf("generate SSH key: %w", err)
	}
	if runResult.ExitCode != 0 {
		return KeyResult{}, fmt.Errorf("generate SSH key: ssh-keygen exited with status %d", runResult.ExitCode)
	}
	if err := s.hardenAndSyncGeneratedFile(stagingBase); err != nil {
		return KeyResult{}, fmt.Errorf("secure generated identity: %w", err)
	}
	if err := s.hardenAndSyncGeneratedFile(stagingBase + ".pub"); err != nil {
		return KeyResult{}, fmt.Errorf("secure generated public key: %w", err)
	}
	record, err := s.readPublicKeyFile(stagingBase + ".pub")
	if err != nil || record.metadata.Algorithm != "ssh-ed25519" {
		return KeyResult{}, errors.New("generated SSH public key is not a valid Ed25519 record")
	}
	deriveRequest := RunRequest{
		Name: "ssh-keygen", Args: []string{"-y", "-f", stagingBase},
		Interactive: state.request.Interactive, CaptureStdout: true, Display: "ssh-keygen validate generated key pair",
	}
	if !state.request.Interactive {
		deriveRequest.Env = noninteractiveKeygenEnv()
	}
	derivedResult, err := s.runner.Run(ctx, deriveRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return KeyResult{}, ctxErr
		}
		return KeyResult{}, fmt.Errorf("validate generated SSH key pair: %w", err)
	}
	if derivedResult.ExitCode != 0 {
		return KeyResult{}, fmt.Errorf("validate generated SSH key pair: ssh-keygen exited with status %d", derivedResult.ExitCode)
	}
	derived, err := parsePublicKeyRecord(derivedResult.Stdout)
	if err != nil || derived.metadata.Fingerprint != record.metadata.Fingerprint {
		return KeyResult{}, errors.New("generated SSH private and public keys do not match")
	}
	privateStaged, err := adoptGeneratedStagedFile(stagingBase)
	if err != nil {
		return KeyResult{}, fmt.Errorf("validate generated identity: %w", err)
	}
	defer privateStaged.discard()
	publicStaged, err := adoptGeneratedStagedFile(stagingBase + ".pub")
	if err != nil {
		return KeyResult{}, fmt.Errorf("validate generated public key: %w", err)
	}
	defer publicStaged.discard()
	if s.beforeKeyCommit != nil {
		s.beforeKeyCommit()
	}
	if err := commitNoReplace(privateStaged, plan.IdentityFile, state.expectedPrivate); err != nil {
		return KeyResult{}, fmt.Errorf("publish generated identity: %w", ErrKeyCollision)
	}
	publishedPrivate, readErr := readSecureFile(plan.IdentityFile, false)
	if readErr != nil {
		return KeyResult{}, fmt.Errorf("verify generated identity publication: %w", readErr)
	}
	if err := commitNoReplace(publicStaged, plan.PublicPath, state.expectedPublic); err != nil {
		rollbackErr := removeSecureFile(publishedPrivate)
		return KeyResult{}, errors.Join(
			fmt.Errorf("publish generated public key: %w", ErrKeyCollision),
			wrapRollbackError(rollbackErr),
		)
	}
	if err := platformSyncDirectory(parent); err != nil {
		return KeyResult{}, fmt.Errorf("sync generated key directory: %w", err)
	}
	candidate := KeyCandidate{
		Source: KeySourceGenerated, Sources: []KeySource{KeySourceGenerated},
		Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
		Fingerprint: record.metadata.Fingerprint, PublicPath: plan.PublicPath, IdentityFile: plan.IdentityFile,
		Provenance: KeyProvenance{Private: true},
	}
	publishedIdentity, err := s.inspectSecureIdentity(plan.IdentityFile)
	if err != nil {
		return KeyResult{}, fmt.Errorf("verify generated identity publication: %w", err)
	}
	verification := &keyPairVerification{
		identity: publishedIdentity, publicPath: plan.PublicPath, fingerprint: record.metadata.Fingerprint,
	}
	bound := s.bindVerifiedKeyMaterial(candidate, record.normalized, verification)
	return KeyResult{
		Action: ActionCreate, Operation: KeyGenerate, Candidate: bound,
		Created: true, Retained: true,
	}, nil
}

func wrapRollbackError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("rollback generated identity after pair collision: %w", err)
}

func allocateKeyStagingBase(parent string) (string, error) {
	for range 32 {
		var random [18]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := ".dev-key-" + base64.RawURLEncoding.EncodeToString(random[:]) + ".tmp"
		base := filepath.Join(parent, name)
		if _, err := os.Lstat(base); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				continue
			}
			return "", err
		}
		if _, err := os.Lstat(base + ".pub"); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				continue
			}
			return "", err
		}
		return base, nil
	}
	return "", errors.New("could not allocate SSH key staging basename")
}

func adoptGeneratedStagedFile(path string) (*stagedFile, error) {
	dir := filepath.Dir(path)
	root, held, err := openHeldDirectory(dir, true)
	if err != nil {
		return nil, err
	}
	snapshot, err := readSecureFileAt(root, filepath.Base(path), path, false)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &stagedFile{dir: dir, name: filepath.Base(path), root: root, held: held, snapshot: snapshot}, nil
}

func (s *Service) inspectIdentityProvenance(identity, algorithm string) (private, stub bool) {
	if identity == "" || !s.pathWithinSSH(identity) {
		return false, false
	}
	if _, err := s.inspectSecureIdentity(identity); err != nil {
		return false, false
	}
	if strings.HasPrefix(algorithm, "sk-") {
		return false, true
	}
	return true, false
}
