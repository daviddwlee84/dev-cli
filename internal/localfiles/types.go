// Package localfiles plans and applies one-way transfers of explicitly selected,
// ignored local files between exact repository checkouts.
package localfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/google/uuid"
)

const (
	SchemaVersion              = 1
	MaxPlanEnvelopeBytes int64 = 2 << 20
	// A maximum raw payload is 32 MiB; JSON base64 plus bounded metadata fits in
	// 48 MiB. Both client transport and target decoder enforce this ceiling.
	MaxApplyEnvelopeBytes int64 = 48 << 20
	MaxApplyResponseBytes int64 = 2 << 20
)

type State string

const (
	StateReady             State = "ready"
	StateCurrent           State = "current"
	StateBlockedConflict   State = "blocked-conflict"
	StateBlockedIneligible State = "blocked-ineligible"
	StateBlockedUnsafe     State = "blocked-unsafe"
	StateMissing           State = "missing"
	StateFailed            State = "failed"
	StateCreated           State = "created"
	StateReplaced          State = "replaced"
	StateRolledBack        State = "rolled-back"
	StateReconcile         State = "reconcile"
)

func (s State) validPlan() bool {
	switch s {
	case StateReady, StateCurrent, StateBlockedConflict, StateBlockedIneligible,
		StateBlockedUnsafe, StateMissing, StateFailed:
		return true
	default:
		return false
	}
}

func (s State) validResult() bool {
	return s.validPlan() || s == StateCreated || s == StateReplaced || s == StateRolledBack || s == StateReconcile
}

// PublicFile is the complete public per-file surface. Digests, content,
// environment values, absolute roots, and base64 never enter this type.
type PublicFile struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
	State State  `json:"state"`
}

// Report is used for both report-only plans and apply results.
type Report struct {
	SchemaVersion int          `json:"schema_version"`
	Files         []PublicFile `json:"files"`
}

func (r Report) Successful() bool {
	for _, file := range r.Files {
		switch file.State {
		case StateReady, StateCurrent, StateCreated, StateReplaced:
		default:
			return false
		}
	}
	return len(r.Files) > 0
}

// Pattern records local provenance only. It is never serialized to a target.
type Pattern struct {
	Value  string
	Source string
}

// Binding proves both peers are discussing one exact checkout state.
type Binding struct {
	RemoteIdentity string `json:"remote_identity"`
	Branch         string `json:"branch"`
	HeadOID        string `json:"head_oid"`
	SourceMachine  string `json:"source_machine_id"`
	TargetMachine  string `json:"target_machine_id"`
}

func (b Binding) Validate() error {
	if b.RemoteIdentity == "" || b.RemoteIdentity != catalog.NormalizeRemoteIdentity(b.RemoteIdentity) {
		return errors.New("binding has no canonical remote identity")
	}
	if err := validateText("branch", b.Branch); err != nil {
		return err
	}
	if !validOID(b.HeadOID) {
		return errors.New("binding has an invalid HEAD OID")
	}
	if err := machineid.Validate(b.SourceMachine); err != nil {
		return fmt.Errorf("source machine: %w", err)
	}
	if err := machineid.Validate(b.TargetMachine); err != nil {
		return fmt.Errorf("target machine: %w", err)
	}
	if b.SourceMachine == b.TargetMachine {
		return errors.New("source and target machine IDs must differ")
	}
	return nil
}

// FileSpec is private protocol metadata. SHA256 is never copied to Report.
type FileSpec struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

func (f FileSpec) executable() bool { return f.Mode == "0700" }

func (f FileSpec) public(state State) PublicFile {
	return PublicFile{Path: f.Path, Size: f.Size, Mode: f.Mode, State: state}
}

func validateFileSpecs(files []FileSpec, limits safefile.Limits) error {
	metadata := make([]safefile.Metadata, len(files))
	for index, file := range files {
		if index > 0 && files[index-1].Path >= file.Path {
			return errors.New("file manifest paths must be unique and sorted")
		}
		if file.Mode != "0600" && file.Mode != "0700" {
			return fmt.Errorf("file %q has unsupported mode", file.Path)
		}
		if !validDigest(file.SHA256) {
			return fmt.Errorf("file %q has an invalid SHA-256 digest", file.Path)
		}
		metadata[index] = safefile.Metadata{Path: file.Path, Size: file.Size}
	}
	return safefile.ValidateManifest(metadata, limits)
}

// PlanRequest contains exact expanded files only; no pattern can cross the wire.
type PlanRequest struct {
	SchemaVersion   int              `json:"schema_version"`
	ProtocolVersion int              `json:"protocol_version"`
	RequestID       string           `json:"request_id"`
	Binding         Binding          `json:"binding"`
	Limits          fleet.FileLimits `json:"limits"`
	Replace         bool             `json:"replace"`
	ManifestDigest  string           `json:"manifest_digest"`
	Files           []FileSpec       `json:"files"`
}

func (r PlanRequest) SafeLimits() safefile.Limits {
	return safefile.Limits{
		MaxFiles: r.Limits.MaxFiles, MaxFileBytes: r.Limits.MaxFileBytes,
		MaxTotalBytes: r.Limits.MaxTotalBytes, MaxPathBytes: r.Limits.MaxPathBytes,
		MaxComponentBytes: r.Limits.MaxComponentBytes, MaxPathDepth: r.Limits.MaxPathDepth,
	}
}

func (r PlanRequest) Validate() error {
	if r.SchemaVersion != SchemaVersion || r.ProtocolVersion != fleet.LocalFilesProtocolVersion {
		return errors.New("unsupported local-files plan protocol")
	}
	parsed, err := uuid.Parse(r.RequestID)
	if err != nil || parsed == uuid.Nil || parsed.String() != r.RequestID {
		return errors.New("plan request has an invalid request ID")
	}
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if err := r.Limits.Validate(); err != nil {
		return fmt.Errorf("wire limits: %w", err)
	}
	if err := r.SafeLimits().Validate(); err != nil {
		return fmt.Errorf("safe-file limits: %w", err)
	}
	if len(r.Files) == 0 {
		return errors.New("plan request has no exact files")
	}
	if err := validateFileSpecs(r.Files, r.SafeLimits()); err != nil {
		return err
	}
	if !validDigest(r.ManifestDigest) || r.ManifestDigest != manifestDigest(r) {
		return errors.New("plan request manifest digest does not match")
	}
	return nil
}

type Action string

const (
	actionCurrent Action = "current"
	actionCreate  Action = "create"
	actionReplace Action = "replace"
)

// PlanFile carries target observations privately between plan and apply.
type PlanFile struct {
	PublicFile
	Action       Action `json:"action"`
	TargetSHA256 string `json:"target_sha256,omitempty"`
	TargetMode   string `json:"target_mode,omitempty"`
}

// PlanResponse is private protocol state. Call PublicReport before rendering.
type PlanResponse struct {
	SchemaVersion   int        `json:"schema_version"`
	ProtocolVersion int        `json:"protocol_version"`
	RequestID       string     `json:"request_id"`
	ManifestDigest  string     `json:"manifest_digest"`
	TargetMachine   string     `json:"target_machine_id"`
	TargetCheckout  string     `json:"target_checkout_id"`
	PlanDigest      string     `json:"plan_digest"`
	Files           []PlanFile `json:"files"`
}

// PlanWireResponse carries either one validated plan or one stable target-state
// code. Expected target blockers therefore survive SSH without reflecting
// arbitrary remote stderr into local diagnostics.
type PlanWireResponse struct {
	SchemaVersion   int           `json:"schema_version"`
	ProtocolVersion int           `json:"protocol_version"`
	Plan            *PlanResponse `json:"plan,omitempty"`
	ErrorCode       TargetCode    `json:"error_code,omitempty"`
}

func (r PlanWireResponse) Validate(request PlanRequest) error {
	if r.SchemaVersion != SchemaVersion || r.ProtocolVersion != fleet.LocalFilesProtocolVersion {
		return errors.New("unsupported local-files plan response protocol")
	}
	if r.Plan != nil {
		if r.ErrorCode != "" {
			return errors.New("local-files plan response contains both plan and error")
		}
		return r.Plan.Validate(request)
	}
	if !r.ErrorCode.valid() {
		return errors.New("local-files plan response has no valid result")
	}
	return nil
}

func (r PlanResponse) PublicReport() Report {
	files := make([]PublicFile, len(r.Files))
	for index := range r.Files {
		files[index] = r.Files[index].PublicFile
	}
	return Report{SchemaVersion: SchemaVersion, Files: files}
}

func (r PlanResponse) Validate(request PlanRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if r.SchemaVersion != SchemaVersion || r.ProtocolVersion != fleet.LocalFilesProtocolVersion ||
		r.RequestID != request.RequestID || r.ManifestDigest != request.ManifestDigest ||
		r.TargetMachine != request.Binding.TargetMachine {
		return errors.New("plan response binding does not match request")
	}
	if !validDigest(r.TargetCheckout) {
		return errors.New("plan response has an invalid checkout identity")
	}
	if len(r.Files) != len(request.Files) {
		return errors.New("plan response file count does not match request")
	}
	for index, file := range r.Files {
		spec := request.Files[index]
		if file.Path != spec.Path || file.Size != spec.Size || file.Mode != spec.Mode || !file.State.validPlan() {
			return fmt.Errorf("plan response metadata for %q does not match request", spec.Path)
		}
		switch file.Action {
		case actionCurrent:
			if file.State != StateCurrent || file.TargetSHA256 != spec.SHA256 || file.TargetMode != spec.Mode {
				return fmt.Errorf("plan response current observation for %q is invalid", spec.Path)
			}
		case actionCreate:
			if file.State != StateReady || file.TargetSHA256 != "" || file.TargetMode != "" {
				return fmt.Errorf("plan response create observation for %q is invalid", spec.Path)
			}
		case actionReplace:
			if !request.Replace || file.State != StateReady || !validDigest(file.TargetSHA256) || !validFileMode(file.TargetMode) {
				return fmt.Errorf("plan response replace observation for %q is invalid", spec.Path)
			}
		case "":
			if file.State == StateReady || file.State == StateCurrent {
				return fmt.Errorf("plan response for %q omits a required action", spec.Path)
			}
			if file.TargetSHA256 != "" && !validDigest(file.TargetSHA256) {
				return fmt.Errorf("plan response for %q has an invalid target digest", spec.Path)
			}
			if file.TargetMode != "" && !validFileMode(file.TargetMode) {
				return fmt.Errorf("plan response for %q has an invalid target mode", spec.Path)
			}
		default:
			return fmt.Errorf("plan response for %q has an unknown action", spec.Path)
		}
	}
	if !validDigest(r.PlanDigest) || r.PlanDigest != planDigest(request, r) {
		return errors.New("plan response digest does not match")
	}
	return nil
}

type Payload struct {
	Path    string `json:"path"`
	Content string `json:"content_base64"`
}

type ApplyEnvelope struct {
	SchemaVersion   int          `json:"schema_version"`
	ProtocolVersion int          `json:"protocol_version"`
	Request         PlanRequest  `json:"request"`
	Plan            PlanResponse `json:"plan"`
	RetainForEvict  bool         `json:"retain_for_evict"`
	Payloads        []Payload    `json:"payloads"`
}

type ApplyResponse struct {
	SchemaVersion   int          `json:"schema_version"`
	ProtocolVersion int          `json:"protocol_version"`
	RequestID       string       `json:"request_id"`
	PlanDigest      string       `json:"plan_digest"`
	Files           []PublicFile `json:"files"`
}

func (r ApplyResponse) PublicReport() Report {
	return Report{SchemaVersion: SchemaVersion, Files: append([]PublicFile(nil), r.Files...)}
}

func (r ApplyResponse) Validate(request PlanRequest, plan PlanResponse) error {
	if r.SchemaVersion != SchemaVersion || r.ProtocolVersion != fleet.LocalFilesProtocolVersion ||
		r.RequestID != request.RequestID || r.PlanDigest != plan.PlanDigest || len(r.Files) != len(request.Files) {
		return errors.New("apply response binding does not match plan")
	}
	for index, file := range r.Files {
		spec := request.Files[index]
		if file.Path != spec.Path || file.Size != spec.Size || file.Mode != spec.Mode || !file.State.validResult() {
			return fmt.Errorf("apply response metadata for %q does not match plan", spec.Path)
		}
		expected := StateCurrent
		switch plan.Files[index].Action {
		case actionCreate:
			expected = StateCreated
		case actionReplace:
			expected = StateReplaced
		case actionCurrent:
		default:
			return fmt.Errorf("apply response action for %q is invalid", spec.Path)
		}
		if file.State != expected {
			return fmt.Errorf("apply response state for %q does not match plan", spec.Path)
		}
	}
	return nil
}

func manifestDigest(request PlanRequest) string {
	payload := struct {
		Domain    string           `json:"domain"`
		RequestID string           `json:"request_id"`
		Binding   Binding          `json:"binding"`
		Limits    fleet.FileLimits `json:"limits"`
		Replace   bool             `json:"replace"`
		Files     []FileSpec       `json:"files"`
	}{"dev-local-files-manifest-v1", request.RequestID, request.Binding, request.Limits, request.Replace, request.Files}
	return digestJSON(payload)
}

func planDigest(request PlanRequest, response PlanResponse) string {
	files := append([]PlanFile(nil), response.Files...)
	payload := struct {
		Domain         string     `json:"domain"`
		ManifestDigest string     `json:"manifest_digest"`
		TargetMachine  string     `json:"target_machine_id"`
		TargetCheckout string     `json:"target_checkout_id"`
		Files          []PlanFile `json:"files"`
	}{"dev-local-files-plan-v1", request.ManifestDigest, response.TargetMachine, response.TargetCheckout, files}
	return digestJSON(payload)
}

func digestJSON(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validFileMode(value string) bool {
	if len(value) != 4 {
		return false
	}
	mode, err := strconv.ParseUint(value, 8, 12)
	return err == nil && mode <= 0o777
}

func validateText(name, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s is required and must be trimmed", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func sortedPublic(files []PublicFile) []PublicFile {
	result := append([]PublicFile(nil), files...)
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}
