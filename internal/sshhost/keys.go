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
	"sort"
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
	identity    selectedKeyIdentity
	publicPath  string
	fingerprint string
}

type keyMaterialState struct {
	serviceID        uint64
	safe             KeyCandidate
	publicLine       []byte
	pairVerification *keyPairVerification
	publicSource     *secureFileIdentity
	identitySource   *selectedKeyIdentity
	agent            *keyAgentContext
}

// The selected agent is an execution reference, never serialized key metadata.
// Capturing even an inherited socket prevents a later environment change from
// silently substituting a different agent.
type keyAgentContext struct{ socket string }

func (a *keyAgentContext) environment() []string {
	return []string{"LC_ALL=C", "SSH_AUTH_SOCK=" + a.socket}
}

type keyPlanState struct {
	serviceID       uint64
	public          KeyPlan
	request         KeyRequest
	material        *keyMaterialState
	identity        selectedKeyIdentity
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
	if request.LocalOnly {
		if request.Alias != "" || request.Effective != nil {
			return KeyCatalog{}, errors.New("local-only key catalog cannot use an alias or effective configuration")
		}
	} else if request.Effective == nil {
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
			previousPath := entries[index].safe.PublicPath
			mergeCatalogEntry(&entries[index], candidate)
			if entries[index].safe.PublicPath != previousPath {
				entries[index].publicLine = append([]byte(nil), record.normalized...)
				entries[index].safe.Comment = record.metadata.Comment
			}
			return
		}
		byFingerprint[candidate.Fingerprint] = len(entries)
		entries = append(entries, catalogEntry{safe: candidate, publicLine: append([]byte(nil), record.normalized...)})
	}
	diagnosed := make(map[string]bool)
	addDiagnostic := func(code, path string, incomplete bool, message ...string) {
		key := code + "\x00" + path
		if diagnosed[key] {
			return
		}
		diagnosed[key] = true
		diagnostic := Diagnostic{Code: code, Path: path, Incomplete: incomplete}
		if len(message) > 0 {
			diagnostic.Message = message[0]
		}
		catalog.Diagnostics = append(catalog.Diagnostics, diagnostic)
		if incomplete {
			catalog.Complete = false
		}
	}
	publicRecords := make(map[string]publicKeyRecord)
	publicFailures := make(map[string]bool)
	addPath := func(path string, source KeySource, effectiveIdentity string) {
		path = filepath.Clean(path)
		if publicFailures[path] {
			return
		}
		record, cached := publicRecords[path]
		if !cached {
			var err error
			record, err = s.readPublicKeyFile(path)
			if err != nil {
				publicFailures[path] = true
				inferredCompanion := effectiveIdentity != "" && !strings.HasSuffix(strings.ToLower(effectiveIdentity), ".pub")
				if inferredCompanion && errors.Is(err, fs.ErrNotExist) && s.validateSSHPath(path, true) == nil {
					// ssh -G includes unused default identities. Absence of their
					// inferred public file is known, not an unreadable source or
					// evidence that a private companion exists.
					if _, statErr := os.Lstat(path); errors.Is(statErr, fs.ErrNotExist) {
						addDiagnostic("public_key_companion_missing", path, false,
							"No .pub companion found for this configured identity; it may be an unused OpenSSH default. Private-key availability has not been checked. If this identity is needed, manually provide or recreate its public companion.")
						return
					}
				}
				addDiagnostic("public_key_unreadable", path, true, publicKeyReadDiagnosticMessage(err))
				return
			}
			publicRecords[path] = record
		}
		identity := strings.TrimSuffix(path, ".pub")
		if effectiveIdentity != "" {
			identity = strings.TrimSuffix(effectiveIdentity, ".pub")
		}
		private, stub, identityErr := s.inspectIdentityProvenanceWithError(identity, record.metadata.Algorithm)
		repair := !private && !stub && s.unprotectedPrivateCompanion(identity, path)
		if repair {
			addDiagnostic("private_key_permissions", identity, false, privateKeyPermissionDiagnosticMessage(identityErr))
		}
		if !private && !stub && !repair {
			identity = path
		}
		candidate := KeyCandidate{
			Source: source, PublicPath: path, IdentityFile: identity,
			NeedsPermissionRepair: repair,
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
		visited := 0
		walkErr := filepath.WalkDir(s.paths.SSHDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				addDiagnostic("public_key_tree_unreadable", path, true)
				return nil
			}
			visited++
			if visited > maxCatalogFiles*8 {
				addDiagnostic("public_key_tree_limit_exceeded", "", true)
				return fs.SkipAll
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
	} else if err == nil || !errors.Is(err, fs.ErrNotExist) {
		addDiagnostic("public_key_tree_unreadable", s.paths.SSHDir, true)
	}

	agentEnabled := !request.NoAgent
	agentSocket := os.Getenv("SSH_AUTH_SOCK")
	if values := effective.Values["identityagent"]; agentEnabled && len(values) > 0 {
		configured, enabled, resolveErr := s.resolveIdentityAgent(values[0])
		if resolveErr != nil {
			addDiagnostic("identity_agent_unsupported", "", true)
			agentEnabled = false
		} else {
			agentEnabled = enabled
			if configured != "" {
				agentSocket = configured
			}
		}
	}
	var agent *keyAgentContext
	if agentEnabled && agentSocket != "" && !filepath.IsAbs(agentSocket) {
		absolute, err := filepath.Abs(agentSocket)
		if err != nil {
			addDiagnostic("identity_agent_unsupported", "", true)
			agentEnabled = false
		} else {
			agentSocket = absolute
		}
	}
	if agentEnabled {
		if !validUTF8NoControl(agentSocket) {
			addDiagnostic("identity_agent_unsupported", "", true)
		} else {
			agent = &keyAgentContext{socket: agentSocket}
			records, diagnostics, err := s.readAgentKeys(ctx, agent, "ssh-add -L")
			if err != nil {
				return KeyCatalog{}, err
			}
			for _, code := range diagnostics {
				addDiagnostic(code, "", true)
			}
			for _, record := range records {
				add(record, KeyCandidate{Source: KeySourceAgent, Provenance: KeyProvenance{Agent: true}})
			}
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].safe.Fingerprint < entries[j].safe.Fingerprint })
	for _, entry := range entries {
		candidate := s.bindKeyMaterial(entry.safe, entry.publicLine)
		if entry.safe.Provenance.Agent && agent != nil {
			copy := *agent
			candidate.state.agent = &copy
		}
		catalog.Candidates = append(catalog.Candidates, candidate)
	}
	return catalog, nil
}

func (s *Service) readAgentKeys(ctx context.Context, agent *keyAgentContext, display string) ([]publicKeyRecord, []string, error) {
	result, err := s.runner.Run(ctx, RunRequest{Name: "ssh-add", Args: []string{"-L"}, Env: agent.environment(), Display: display})
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, []string{"agent_unavailable"}, nil
	}
	if result.ExitCode == 1 {
		return nil, nil, nil
	}
	if result.ExitCode != 0 {
		return nil, []string{"agent_unavailable"}, nil
	}
	if result.StdoutTruncated || len(result.Stdout) > maxAgentCatalogBytes {
		return nil, []string{"agent_output_limit_exceeded"}, nil
	}
	lines := bytes.Split(result.Stdout, []byte{'\n'})
	var diagnostics []string
	if len(lines) > maxCatalogFiles+1 {
		diagnostics = append(diagnostics, "agent_key_limit_exceeded")
		lines = lines[:maxCatalogFiles]
	}
	var records []publicKeyRecord
	invalid := false
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, err := parsePublicKeyRecord(line)
		if err != nil {
			invalid = true
			continue
		}
		records = append(records, record)
	}
	if invalid {
		diagnostics = append(diagnostics, "agent_key_invalid")
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].metadata.Fingerprint == records[j].metadata.Fingerprint {
			return records[i].metadata.Comment < records[j].metadata.Comment
		}
		return records[i].metadata.Fingerprint < records[j].metadata.Fingerprint
	})
	return records, diagnostics, nil
}

// These messages accept only errors from bounded local key/metadata readers,
// never subprocess output. The parser's unsupported-algorithm errors are the
// only ones that interpolate a field from the public record; redact that field.
func publicKeyReadDiagnosticMessage(err error) string {
	reason := safeKeyDiagnosticReason(err)
	switch {
	case errors.Is(err, ErrSourceChanged):
		return "Public-key source changed during inspection. Retry the key listing."
	case errors.Is(err, fs.ErrNotExist):
		return "Public-key path was not found: " + reason + ". Check the configured path or select another key."
	case errors.Is(err, fs.ErrPermission):
		return "Access denied while inspecting public key: " + reason + ". Run dev ssh key doctor to inspect permissions."
	case errors.Is(err, ErrUnsafePath):
		return "Public-key source failed safety checks: " + reason + ". Run dev ssh key doctor to inspect permissions and paths."
	case strings.HasPrefix(reason, "OpenSSH public "), strings.HasPrefix(reason, "validate OpenSSH public blob:"), strings.HasPrefix(reason, "unsupported public-key "):
		return "Invalid OpenSSH public key: " + reason + ". Check the .pub file format or manually recreate its public companion from the matching private key."
	default:
		return "Cannot inspect public key: " + reason + ". Check file availability and retry the key listing."
	}
}

func privateKeyPermissionDiagnosticMessage(err error) string {
	if err == nil {
		return "Private identity needs a fresh permission check and key selection. Run dev ssh key doctor."
	}
	return "Private identity failed safety checks: " + safeKeyDiagnosticReason(err) + ". Run dev ssh key doctor to inspect permissions, then select the key again."
}

func safeKeyDiagnosticReason(err error) string {
	if err == nil {
		return "inspection failed"
	}
	reason := err.Error()
	if strings.Contains(reason, "unsupported public-key algorithm ") || strings.Contains(reason, "unsupported certificate algorithm ") {
		return "unsupported public-key or certificate algorithm"
	}
	return reason
}

// A direct regular companion that could not pass the private-file checks is
// retained for explicit permission review. The permission service independently
// proves ownership/link safety before repair; this observation grants no write.
func (s *Service) unprotectedPrivateCompanion(identity, publicPath string) bool {
	if identity == "" || identity == publicPath || !s.pathWithinSSH(identity) || s.validateSSHPath(identity, false) != nil {
		return false
	}
	info, err := os.Lstat(identity)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func (s *Service) readPublicKeySource(path string) (publicKeyRecord, *secureFileIdentity, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return publicKeyRecord{}, nil, err
	}
	record, err := s.readPublicKeyFile(path)
	if err != nil {
		return publicKeyRecord{}, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !sameSelectedKeyFileInfo(before, after) {
		return publicKeyRecord{}, nil, ErrSourceChanged
	}
	return record, &secureFileIdentity{path: path, info: after}, nil
}

func (s *Service) revalidateSelectedKeySources(ctx context.Context, material *keyMaterialState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.revalidateSelectedLocalSources(material); err != nil {
		return err
	}
	if material.agent != nil && !material.safe.Provenance.Private && !material.safe.Provenance.SecurityKeyStub {
		return s.revalidateAgentKey(ctx, material.agent, material.safe.Fingerprint)
	}
	return nil
}

func (s *Service) revalidateSelectedLocalSources(material *keyMaterialState) error {
	if err := s.revalidateSelectedPublicSource(material); err != nil {
		return err
	}
	if material.safe.Provenance.Private || material.safe.Provenance.SecurityKeyStub {
		if material.identitySource == nil {
			return fmt.Errorf("selected private identity metadata was not captured: %w", ErrSourceChanged)
		}
		if err := s.revalidateSelectedKeyIdentity(*material.identitySource); err != nil {
			return fmt.Errorf("selected private identity changed: %w", ErrSourceChanged)
		}
	}
	return nil
}

func (s *Service) revalidateSelectedPublicSource(material *keyMaterialState) error {
	if material.safe.NeedsPermissionRepair {
		return fmt.Errorf("selected identity needs permission review and a fresh key selection: %w", ErrBlocked)
	}
	if material.safe.PublicPath != "" {
		if material.publicSource == nil {
			return fmt.Errorf("selected public source was not captured: %w", ErrSourceChanged)
		}
		current, identity, err := s.readPublicKeySource(material.safe.PublicPath)
		if err != nil || !sameSelectedKeyFileInfo(material.publicSource.info, identityInfo(identity)) || current.metadata.Fingerprint != material.safe.Fingerprint || !publicLinesEqual(current.normalized, material.publicLine) {
			return fmt.Errorf("selected public key source changed: %w", ErrSourceChanged)
		}
	}
	return nil
}

func (s *Service) revalidateAgentKey(ctx context.Context, agent *keyAgentContext, fingerprint string) error {
	// An empty socket selects the platform's native default agent. There is no
	// portable target-only directive for that context once an ambient socket has
	// appeared; clearing the process environment would also alter ProxyJump.
	if agent.socket == "" && os.Getenv("SSH_AUTH_SOCK") != "" {
		return fmt.Errorf("default SSH agent context changed; select the key again: %w", ErrSourceChanged)
	}
	records, diagnostics, err := s.readAgentKeys(ctx, agent, "ssh-add selected-key availability")
	if err != nil {
		return err
	}
	if len(diagnostics) > 0 {
		return fmt.Errorf("selected SSH agent is unavailable or incompletely observed: %w", ErrSourceChanged)
	}
	for _, record := range records {
		if record.metadata.Fingerprint == fingerprint {
			return nil
		}
	}
	return fmt.Errorf("selected key is no longer present in its SSH agent: %w", ErrSourceChanged)
}

func identityInfo(identity *secureFileIdentity) fs.FileInfo {
	if identity == nil {
		return nil
	}
	return identity.info
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
	hadPrivate := entry.safe.Provenance.Private || entry.safe.Provenance.SecurityKeyStub
	newPrivate := candidate.Provenance.Private || candidate.Provenance.SecurityKeyStub
	if !hadPrivate && (newPrivate || candidate.NeedsPermissionRepair && !entry.safe.NeedsPermissionRepair) {
		entry.safe.PublicPath = candidate.PublicPath
		entry.safe.IdentityFile = candidate.IdentityFile
		entry.safe.NeedsPermissionRepair = candidate.NeedsPermissionRepair
		entry.safe.Source = candidate.Source
	}
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
	if safe.PublicPath != "" {
		current, identity, err := s.readPublicKeySource(safe.PublicPath)
		if err == nil && current.metadata.Fingerprint == safe.Fingerprint && publicLinesEqual(current.normalized, publicLine) {
			state.publicSource = identity
		}
	}
	if safe.Provenance.Private || safe.Provenance.SecurityKeyStub {
		if identity, err := s.inspectSelectedKeyIdentity(safe.IdentityFile); err == nil {
			state.identitySource = &identity
		}
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
		if material.safe.NeedsPermissionRepair {
			return KeyPlan{Action: ActionBlocked, Operation: KeyUse, IdentityFile: material.safe.IdentityFile, Fingerprint: material.safe.Fingerprint, Diagnostics: []Diagnostic{{Code: "private_key_permissions", Message: privateKeyPermissionDiagnosticMessage(nil), Path: material.safe.IdentityFile, BlocksMutation: true}}}, nil
		}
		if err := s.revalidateSelectedLocalSources(material); err != nil {
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
		repair := !private && !stub && s.unprotectedPrivateCompanion(identity, selected)
		if !private && !stub && !repair {
			identity = selected
		}
		candidate := KeyCandidate{
			Source: KeySourceExplicit, Sources: []KeySource{KeySourceExplicit},
			Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
			Fingerprint: record.metadata.Fingerprint, PublicPath: selected, IdentityFile: identity,
			Provenance:            KeyProvenance{Private: private, SecurityKeyStub: stub},
			NeedsPermissionRepair: repair,
		}
		bound := s.bindKeyMaterial(candidate, record.normalized)
		material, _ := s.validateKeyCandidate(bound)
		plan := keyPlanForMaterial(ActionNoop, KeyUse, candidate)
		state := &keyPlanState{serviceID: s.id, request: request, material: material}
		state.public = plan
		plan.state = state
		return plan, nil
	}

	identity, err := s.inspectSelectedKeyIdentity(selected)
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
	plan := KeyPlan{
		Action: action, Operation: operation, Source: candidate.Source,
		Algorithm: candidate.Algorithm, Comment: candidate.Comment, Fingerprint: candidate.Fingerprint,
		PublicPath: candidate.PublicPath, IdentityFile: candidate.IdentityFile,
	}
	if candidate.NeedsPermissionRepair {
		plan.Action = ActionBlocked
		plan.Diagnostics = []Diagnostic{{Code: "private_key_permissions", Message: privateKeyPermissionDiagnosticMessage(nil), Path: candidate.IdentityFile, BlocksMutation: true}}
	}
	return plan
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
		// Onboarding can review generation before its separate managed Include
		// initialization creates ~/.ssh. Only that exact canonical parent may
		// be absent; arbitrary nested key directories still require preparation.
		if filepath.Dir(resolved) != s.paths.SSHDir || !errors.Is(err, fs.ErrNotExist) {
			return KeyPlan{}, err
		}
		if err := validateHomeDirectory(s.paths.Home); err != nil {
			return KeyPlan{}, err
		}
		if _, err := os.Lstat(s.paths.SSHDir); !errors.Is(err, fs.ErrNotExist) {
			return KeyPlan{}, ErrUnsafePath
		}
		plan := KeyPlan{Action: ActionCreate, Operation: KeyGenerate, Source: KeySourceGenerated, Algorithm: "ssh-ed25519", Comment: request.Comment, PublicPath: resolved + ".pub", IdentityFile: resolved}
		request.DestinationIdentity = resolved
		state := &keyPlanState{serviceID: s.id, request: request, expectedPrivate: fileSnapshot{path: resolved}, expectedPublic: fileSnapshot{path: resolved + ".pub"}}
		state.public = plan
		plan.state = state
		return plan, nil
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
		if state.material.agent != nil {
			copy := *state.material.agent
			candidate.state.agent = &copy
		}
		return KeyResult{Action: ActionNoop, Operation: KeyUse, Candidate: candidate}, nil
	case KeyDerive:
		return s.applyDerivedKey(ctx, plan)
	case KeyGenerate:
		return s.applyGeneratedKey(ctx, plan)
	default:
		return KeyResult{}, fmt.Errorf("unsupported key plan operation %q", plan.Operation)
	}
}

// RevalidateKeySelection checks a reviewed selection before any configuration
// effect. It does not decrypt, derive, generate, write, or repair key material.
// Agent selections query only their captured agent's public inventory.
func (s *Service) RevalidateKeySelection(ctx context.Context, plan KeyPlan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.Action == ActionBlocked || plan.state == nil {
		return ErrBlocked
	}
	state := plan.state
	if state.serviceID != s.id || !equalKeyPlans(plan, state.public) {
		return errors.New("key plan was not produced by this service or its public fields were modified")
	}
	switch plan.Operation {
	case KeyUse:
		if state.material == nil {
			return errors.New("selected key plan has no material state")
		}
		return s.revalidateSelectedKeySources(ctx, state.material)
	case KeyDerive:
		if err := s.revalidateSelectedKeyIdentity(state.identity); err != nil {
			return err
		}
		current, err := s.inspectPublicDestination(plan.PublicPath)
		if err != nil {
			return err
		}
		if current.exists != state.expectedPublic.exists {
			return ErrKeyCollision
		}
		return nil
	case KeyGenerate:
		parent := filepath.Dir(plan.IdentityFile)
		if err := s.validateKeyParent(parent); err != nil {
			_, statErr := os.Lstat(parent)
			if !samePath(parent, s.paths.SSHDir) || !errors.Is(statErr, fs.ErrNotExist) {
				return err
			}
			if err := validateHomeDirectory(s.paths.Home); err != nil {
				return err
			}
		}
		for _, path := range []string{plan.IdentityFile, plan.PublicPath} {
			if _, err := os.Lstat(path); err == nil {
				return ErrKeyCollision
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported key selection operation")
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
	if err := s.revalidateSelectedKeySources(ctx, material); err != nil {
		return nil, err
	}
	safe := material.safe
	if !safe.Provenance.Private && !safe.Provenance.SecurityKeyStub {
		return nil, nil
	}
	if safe.IdentityFile == "" || safe.PublicPath == "" {
		return nil, errors.New("private-backed selected key has no public companion")
	}
	identity, err := s.inspectSelectedKeyIdentity(safe.IdentityFile)
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
			previous.identity.path != identity.path || !sameSelectedKeyIdentity(previous.identity, identity) {
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
	if err := s.revalidateSelectedKeyIdentity(identity); err != nil {
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
	if err := s.revalidateSelectedKeyIdentity(state.identity); err != nil {
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
	if err := s.revalidateSelectedKeyIdentity(state.identity); err != nil {
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
	if err := s.revalidateSelectedKeyIdentity(state.identity); err != nil {
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
	if parent == s.paths.SSHDir {
		if err := validateHomeDirectory(s.paths.Home); err != nil {
			return KeyResult{}, err
		}
		if err := ensurePrivateChild(s.paths.Home, ".ssh", false); err != nil {
			return KeyResult{}, err
		}
	}
	if err := s.validateKeyParent(parent); err != nil {
		return KeyResult{}, err
	}
	privateNow, err := s.inspectPrivateDestination(plan.IdentityFile)
	if err != nil {
		return KeyResult{}, err
	}
	publicNow, err := s.inspectPublicDestination(plan.PublicPath)
	if err != nil {
		return KeyResult{}, err
	}
	if privateNow.exists || publicNow.exists {
		return KeyResult{}, ErrKeyCollision
	}
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
	publishedIdentity, err := s.inspectSelectedKeyIdentity(plan.IdentityFile)
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
	private, stub, _ = s.inspectIdentityProvenanceWithError(identity, algorithm)
	return private, stub
}

func (s *Service) inspectIdentityProvenanceWithError(identity, algorithm string) (private, stub bool, err error) {
	if identity == "" || !s.pathWithinSSH(identity) {
		return false, false, ErrUnsafePath
	}
	if _, err := s.inspectSelectedKeyIdentity(identity); err != nil {
		return false, false, err
	}
	if strings.HasPrefix(algorithm, "sk-") {
		return false, true, nil
	}
	return true, false, nil
}
