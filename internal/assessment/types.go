// Package assessment defines presentation-neutral, versioned readiness reports.
package assessment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const SchemaVersion = 1

const fingerprintPrefix = "sha256:"

type Outcome string

const (
	OutcomeEligible      Outcome = "eligible"
	OutcomeBlocked       Outcome = "blocked"
	OutcomeIndeterminate Outcome = "indeterminate"
	OutcomeNotApplicable Outcome = "not-applicable"
)

type Profile string

const (
	// ProfileCheap permits cached, stale, or incomplete observations for display.
	// It must not be used to authorize a mutation.
	ProfileCheap Profile = "cheap"
	// ProfileDeep requires complete, fresh, live-authority evidence for any
	// conclusive outcome.
	ProfileDeep Profile = "deep"
)

type Freshness string

const (
	FreshnessFresh   Freshness = "fresh"
	FreshnessStale   Freshness = "stale"
	FreshnessUnknown Freshness = "unknown"
)

type Completeness string

const (
	CompletenessComplete Completeness = "complete"
	CompletenessPartial  Completeness = "partial"
	CompletenessUnknown  Completeness = "unknown"
)

type Authority string

const (
	AuthorityLocalLive   Authority = "local-live"
	AuthorityRemoteLive  Authority = "remote-live"
	AuthorityDerivedLive Authority = "derived-live"
	AuthorityCache       Authority = "cache"
)

// Source describes where one gate's evidence came from. Fingerprint identifies
// the observed facts, not the entire Report.
type Source struct {
	ID           string       `json:"id"`
	Authority    Authority    `json:"authority"`
	Freshness    Freshness    `json:"freshness"`
	Completeness Completeness `json:"completeness"`
	ObservedAt   time.Time    `json:"observed_at"`
	Fingerprint  string       `json:"fingerprint"`
}

// Reason is a stable machine-readable explanation with human-facing context.
// Code and Subject are safe identifiers; Detail and Remediation may evolve.
type Reason struct {
	Code        string `json:"code"`
	Subject     string `json:"subject"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

// Gate assesses one independently actionable scope, such as transfer-source or
// whole-clone-reclaim.
type Gate struct {
	Code    string   `json:"code"`
	Outcome Outcome  `json:"outcome"`
	Sources []Source `json:"sources,omitempty"`
	Reasons []Reason `json:"reasons,omitempty"`
}

// Report is the schema-v1 assessment envelope. Seal must be called before a
// report is persisted or sent over a protocol boundary.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Profile       Profile   `json:"profile"`
	GeneratedAt   time.Time `json:"generated_at"`
	Fingerprint   string    `json:"fingerprint"`
	Gates         []Gate    `json:"gates"`
}

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._:/-][a-z0-9]+)*$`)

func (o Outcome) Validate() error {
	switch o {
	case OutcomeEligible, OutcomeBlocked, OutcomeIndeterminate, OutcomeNotApplicable:
		return nil
	default:
		return fmt.Errorf("unknown assessment outcome %q", o)
	}
}

func (p Profile) Validate() error {
	switch p {
	case ProfileCheap, ProfileDeep:
		return nil
	default:
		return fmt.Errorf("unknown assessment profile %q", p)
	}
}

func (s Source) Validate() error {
	if !identifierPattern.MatchString(s.ID) {
		return fmt.Errorf("invalid evidence source id %q", s.ID)
	}
	switch s.Authority {
	case AuthorityLocalLive, AuthorityRemoteLive, AuthorityDerivedLive, AuthorityCache:
	default:
		return fmt.Errorf("source %s has unknown authority %q", s.ID, s.Authority)
	}
	switch s.Freshness {
	case FreshnessFresh, FreshnessStale, FreshnessUnknown:
	default:
		return fmt.Errorf("source %s has unknown freshness %q", s.ID, s.Freshness)
	}
	switch s.Completeness {
	case CompletenessComplete, CompletenessPartial, CompletenessUnknown:
	default:
		return fmt.Errorf("source %s has unknown completeness %q", s.ID, s.Completeness)
	}
	if s.ObservedAt.IsZero() {
		return fmt.Errorf("source %s observed_at is required", s.ID)
	}
	if !ValidFingerprint(s.Fingerprint) {
		return fmt.Errorf("source %s has invalid fingerprint %q", s.ID, s.Fingerprint)
	}
	return nil
}

func (s Source) conclusive() bool {
	if s.Freshness != FreshnessFresh || s.Completeness != CompletenessComplete {
		return false
	}
	switch s.Authority {
	case AuthorityLocalLive, AuthorityRemoteLive, AuthorityDerivedLive:
		return true
	default:
		return false
	}
}

func (r Reason) Validate() error {
	if !identifierPattern.MatchString(r.Code) {
		return fmt.Errorf("invalid reason code %q", r.Code)
	}
	if err := validateText("reason subject", r.Subject, true); err != nil {
		return fmt.Errorf("reason %s: %w", r.Code, err)
	}
	if err := validateText("reason detail", r.Detail, true); err != nil {
		return fmt.Errorf("reason %s: %w", r.Code, err)
	}
	if err := validateText("reason remediation", r.Remediation, false); err != nil {
		return fmt.Errorf("reason %s: %w", r.Code, err)
	}
	return nil
}

func (g Gate) Validate(profile Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if !identifierPattern.MatchString(g.Code) {
		return fmt.Errorf("invalid gate code %q", g.Code)
	}
	if err := g.Outcome.Validate(); err != nil {
		return fmt.Errorf("gate %s: %w", g.Code, err)
	}
	seenSources := make(map[string]struct{}, len(g.Sources))
	for _, source := range g.Sources {
		if err := source.Validate(); err != nil {
			return fmt.Errorf("gate %s: %w", g.Code, err)
		}
		if _, exists := seenSources[source.ID]; exists {
			return fmt.Errorf("gate %s repeats source %q", g.Code, source.ID)
		}
		seenSources[source.ID] = struct{}{}
	}
	for _, reason := range g.Reasons {
		if err := reason.Validate(); err != nil {
			return fmt.Errorf("gate %s: %w", g.Code, err)
		}
	}
	if (g.Outcome == OutcomeEligible || g.Outcome == OutcomeBlocked) && len(g.Sources) == 0 {
		return fmt.Errorf("gate %s outcome %s requires evidence", g.Code, g.Outcome)
	}
	if (g.Outcome == OutcomeBlocked || g.Outcome == OutcomeIndeterminate) && len(g.Reasons) == 0 {
		return fmt.Errorf("gate %s outcome %s requires a reason", g.Code, g.Outcome)
	}
	if profile == ProfileDeep && (g.Outcome == OutcomeEligible || g.Outcome == OutcomeBlocked) {
		for _, source := range g.Sources {
			if !source.conclusive() {
				return fmt.Errorf("gate %s has conclusive outcome %s from non-conclusive source %s", g.Code, g.Outcome, source.ID)
			}
		}
	}
	return nil
}

// NewReport constructs, canonicalizes, and seals a report.
func NewReport(profile Profile, generatedAt time.Time, gates []Gate) (Report, error) {
	report := Report{SchemaVersion: SchemaVersion, Profile: profile, GeneratedAt: generatedAt, Gates: gates}
	if err := report.Seal(); err != nil {
		return Report{}, err
	}
	return report, nil
}

// Seal canonicalizes the report and records a fingerprint over every field
// except Fingerprint itself.
func (r *Report) Seal() error {
	if r == nil {
		return errors.New("seal nil assessment report")
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = SchemaVersion
	}
	canonical := r.Canonical()
	canonical.Fingerprint = ""
	if err := canonical.validateContent(); err != nil {
		return err
	}
	fingerprint, err := canonical.computeFingerprint()
	if err != nil {
		return err
	}
	canonical.Fingerprint = fingerprint
	*r = canonical
	return nil
}

// Validate verifies the schema, policy invariants, canonical fingerprint, and
// deterministic ordering of a sealed report.
func (r Report) Validate() error {
	if err := r.validateContent(); err != nil {
		return err
	}
	if !ValidFingerprint(r.Fingerprint) {
		return fmt.Errorf("invalid report fingerprint %q", r.Fingerprint)
	}
	canonical := r.Canonical()
	canonical.Fingerprint = ""
	want, err := canonical.computeFingerprint()
	if err != nil {
		return err
	}
	if r.Fingerprint != want {
		return fmt.Errorf("assessment fingerprint mismatch: got %s, want %s", r.Fingerprint, want)
	}
	if !reportsEqualOrdering(r, r.Canonical()) {
		return errors.New("assessment report is not in canonical order")
	}
	return nil
}

func (r Report) validateContent() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("assessment schema_version %d: want %d", r.SchemaVersion, SchemaVersion)
	}
	if err := r.Profile.Validate(); err != nil {
		return err
	}
	if r.GeneratedAt.IsZero() {
		return errors.New("assessment generated_at is required")
	}
	seenGates := make(map[string]struct{}, len(r.Gates))
	for _, gate := range r.Gates {
		if err := gate.Validate(r.Profile); err != nil {
			return err
		}
		if _, exists := seenGates[gate.Code]; exists {
			return fmt.Errorf("assessment repeats gate %q", gate.Code)
		}
		seenGates[gate.Code] = struct{}{}
	}
	return nil
}

// Canonical returns a deep copy with UTC timestamps and order-insensitive
// collections sorted by their complete stable representation.
func (r Report) Canonical() Report {
	result := r
	result.GeneratedAt = canonicalTime(result.GeneratedAt)
	result.Gates = make([]Gate, len(r.Gates))
	copy(result.Gates, r.Gates)
	for gateIndex := range result.Gates {
		gate := &result.Gates[gateIndex]
		gate.Sources = append([]Source(nil), gate.Sources...)
		for sourceIndex := range gate.Sources {
			gate.Sources[sourceIndex].ObservedAt = canonicalTime(gate.Sources[sourceIndex].ObservedAt)
		}
		sort.Slice(gate.Sources, func(i, j int) bool {
			left, right := gate.Sources[i], gate.Sources[j]
			return strings.Join([]string{left.ID, string(left.Authority), string(left.Freshness), string(left.Completeness), left.ObservedAt.Format(time.RFC3339Nano), left.Fingerprint}, "\x00") <
				strings.Join([]string{right.ID, string(right.Authority), string(right.Freshness), string(right.Completeness), right.ObservedAt.Format(time.RFC3339Nano), right.Fingerprint}, "\x00")
		})
		gate.Reasons = append([]Reason(nil), gate.Reasons...)
		sort.Slice(gate.Reasons, func(i, j int) bool {
			left, right := gate.Reasons[i], gate.Reasons[j]
			return strings.Join([]string{left.Code, left.Subject, left.Detail, left.Remediation}, "\x00") <
				strings.Join([]string{right.Code, right.Subject, right.Detail, right.Remediation}, "\x00")
		})
	}
	sort.Slice(result.Gates, func(i, j int) bool { return result.Gates[i].Code < result.Gates[j].Code })
	return result
}

func (r Report) computeFingerprint() (string, error) {
	payload := struct {
		SchemaVersion int       `json:"schema_version"`
		Profile       Profile   `json:"profile"`
		GeneratedAt   time.Time `json:"generated_at"`
		Gates         []Gate    `json:"gates"`
	}{r.SchemaVersion, r.Profile, r.GeneratedAt, r.Gates}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode assessment fingerprint: %w", err)
	}
	return FingerprintBytes(encoded), nil
}

// Decode reads exactly one strict schema-v1 report and verifies its seal.
func Decode(reader io.Reader) (Report, error) {
	if reader == nil {
		return Report{}, errors.New("decode assessment from nil reader")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode assessment: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Report{}, errors.New("decode assessment: multiple JSON values")
		}
		return Report{}, fmt.Errorf("decode assessment trailing data: %w", err)
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func FingerprintBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return fingerprintPrefix + hex.EncodeToString(digest[:])
}

func ValidFingerprint(value string) bool {
	if !strings.HasPrefix(value, fingerprintPrefix) || len(value) != len(fingerprintPrefix)+sha256.Size*2 {
		return false
	}
	digest := strings.TrimPrefix(value, fingerprintPrefix)
	if digest != strings.ToLower(digest) {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func validateText(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be trimmed", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.Round(0).UTC()
}

func reportsEqualOrdering(left, right Report) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
