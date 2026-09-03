package assessment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testSource(id string) Source {
	return Source{
		ID:           id,
		Authority:    AuthorityLocalLive,
		Freshness:    FreshnessFresh,
		Completeness: CompletenessComplete,
		ObservedAt:   time.Date(2026, 8, 31, 4, 5, 6, 7, time.FixedZone("test", 8*60*60)),
		Fingerprint:  FingerprintBytes([]byte(id)),
	}
}

func TestReportFingerprintIgnoresCollectionOrder(t *testing.T) {
	generated := time.Date(2026, 8, 31, 1, 2, 3, 4, time.UTC)
	firstGate := Gate{
		Code:    "transfer-target",
		Outcome: OutcomeBlocked,
		Sources: []Source{testSource("runtime.state"), testSource("git.status")},
		Reasons: []Reason{
			{Code: "runtime-active", Subject: "checkout:one", Detail: "an agent occupies the checkout", Remediation: "stop the agent"},
			{Code: "checkout-dirty", Subject: "checkout:one", Detail: "the checkout has local changes", Remediation: "commit or preserve the changes"},
		},
	}
	secondGate := Gate{Code: "portable-files", Outcome: OutcomeEligible, Sources: []Source{testSource("files.snapshot")}}

	left, err := NewReport(ProfileDeep, generated, []Gate{firstGate, secondGate})
	if err != nil {
		t.Fatal(err)
	}
	firstGate.Sources[0], firstGate.Sources[1] = firstGate.Sources[1], firstGate.Sources[0]
	firstGate.Reasons[0], firstGate.Reasons[1] = firstGate.Reasons[1], firstGate.Reasons[0]
	right, err := NewReport(ProfileDeep, generated.In(time.FixedZone("elsewhere", -5*60*60)), []Gate{secondGate, firstGate})
	if err != nil {
		t.Fatal(err)
	}
	if left.Fingerprint != right.Fingerprint {
		t.Fatalf("fingerprints differ by collection order: %s != %s", left.Fingerprint, right.Fingerprint)
	}
	if err := left.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if left.Gates[0].Code != "portable-files" || left.Gates[1].Sources[0].ID != "git.status" {
		t.Fatalf("report was not canonicalized: %+v", left.Gates)
	}
}

func TestReportValidationRejectsTamperAndNonCanonicalWireOrder(t *testing.T) {
	report, err := NewReport(ProfileDeep, time.Now(), []Gate{
		{Code: "transfer-source", Outcome: OutcomeEligible, Sources: []Source{testSource("git.status")}},
		{Code: "transfer-target", Outcome: OutcomeEligible, Sources: []Source{testSource("fleet.probe")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	tampered := report
	tampered.Gates = append([]Gate(nil), report.Gates...)
	tampered.Gates[0].Code = "portable-files"
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("tampered Validate = %v", err)
	}

	reordered := report
	reordered.Gates = append([]Gate(nil), report.Gates...)
	reordered.Gates[0], reordered.Gates[1] = reordered.Gates[1], reordered.Gates[0]
	if err := reordered.Validate(); err == nil || !strings.Contains(err.Error(), "canonical order") {
		t.Fatalf("reordered Validate = %v", err)
	}
}

func TestDeepReportRejectsConclusiveOutcomeFromStaleEvidence(t *testing.T) {
	source := testSource("fleet.snapshot")
	source.Freshness = FreshnessStale
	_, err := NewReport(ProfileDeep, time.Now(), []Gate{{Code: "transfer-target", Outcome: OutcomeEligible, Sources: []Source{source}}})
	if err == nil || !strings.Contains(err.Error(), "non-conclusive") {
		t.Fatalf("NewReport error = %v", err)
	}
	if _, err := NewReport(ProfileCheap, time.Now(), []Gate{{Code: "transfer-target", Outcome: OutcomeEligible, Sources: []Source{source}}}); err != nil {
		t.Fatalf("cheap report rejected display evidence: %v", err)
	}
}

func TestDecodeIsStrictAndVerifiesSeal(t *testing.T) {
	report, err := NewReport(ProfileDeep, time.Now(), []Gate{{Code: "restore-verified", Outcome: OutcomeEligible, Sources: []Source{testSource("restore.drill")}}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(encoded)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	unknown := bytes.Replace(encoded, []byte(`{"schema_version":`), []byte(`{"unexpected":true,"schema_version":`), 1)
	if _, err := Decode(bytes.NewReader(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field Decode = %v", err)
	}
	withExtra := append(append([]byte(nil), encoded...), []byte(` {}`)...)
	if _, err := Decode(bytes.NewReader(withExtra)); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("trailing Decode = %v", err)
	}
}

func TestReducePolicyPrecedenceAndEvidenceQuality(t *testing.T) {
	fresh := testSource("git.status")
	stale := fresh
	stale.Freshness = FreshnessStale
	cached := fresh
	cached.Authority = AuthorityCache

	cases := []struct {
		name     string
		profile  Profile
		sources  []Source
		outcomes []Outcome
		want     Outcome
	}{
		{name: "blocked wins", profile: ProfileDeep, sources: []Source{fresh}, outcomes: []Outcome{OutcomeEligible, OutcomeBlocked, OutcomeIndeterminate}, want: OutcomeBlocked},
		{name: "unknown wins over eligible", profile: ProfileDeep, sources: []Source{fresh}, outcomes: []Outcome{OutcomeEligible, OutcomeIndeterminate}, want: OutcomeIndeterminate},
		{name: "eligible ignores not applicable", profile: ProfileDeep, sources: []Source{fresh}, outcomes: []Outcome{OutcomeNotApplicable, OutcomeEligible}, want: OutcomeEligible},
		{name: "all not applicable", profile: ProfileDeep, outcomes: []Outcome{OutcomeNotApplicable, OutcomeNotApplicable}, want: OutcomeNotApplicable},
		{name: "missing observations", profile: ProfileDeep, want: OutcomeIndeterminate},
		{name: "missing source", profile: ProfileDeep, outcomes: []Outcome{OutcomeEligible}, want: OutcomeIndeterminate},
		{name: "deep rejects stale", profile: ProfileDeep, sources: []Source{stale}, outcomes: []Outcome{OutcomeEligible}, want: OutcomeIndeterminate},
		{name: "deep rejects cache", profile: ProfileDeep, sources: []Source{cached}, outcomes: []Outcome{OutcomeBlocked}, want: OutcomeIndeterminate},
		{name: "cheap displays stale", profile: ProfileCheap, sources: []Source{stale}, outcomes: []Outcome{OutcomeEligible}, want: OutcomeEligible},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Reduce(test.profile, test.sources, test.outcomes...)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Reduce = %q, want %q", got, test.want)
			}
		})
	}
}

func TestIndeterminateGateCanRepresentMissingEvidence(t *testing.T) {
	outcome, err := Reduce(ProfileDeep, nil, OutcomeEligible)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeIndeterminate {
		t.Fatalf("Reduce without evidence = %q, want %q", outcome, OutcomeIndeterminate)
	}
	gate := Gate{
		Code:    "transfer-target",
		Outcome: outcome,
		Reasons: []Reason{{Code: "probe-unavailable", Subject: "host:lab", Detail: "the probe produced no evidence", Remediation: "retry the live probe"}},
	}
	if _, err := NewReport(ProfileDeep, time.Now(), []Gate{gate}); err != nil {
		t.Fatalf("source-less indeterminate report: %v", err)
	}
}

func TestReasonRejectsHumanFacingControlCharacters(t *testing.T) {
	cases := []Reason{
		{Code: "probe-failed", Subject: "host:\tlab", Detail: "failed"},
		{Code: "probe-failed", Subject: "host:lab", Detail: "first line\nsecond line"},
		{Code: "probe-failed", Subject: "host:lab", Detail: "failed", Remediation: "retry\x1b[2J"},
	}
	for _, reason := range cases {
		if err := reason.Validate(); err == nil || !strings.Contains(err.Error(), "control character") {
			t.Errorf("Reason.Validate(%q, %q, %q) = %v", reason.Subject, reason.Detail, reason.Remediation, err)
		}
	}
}

func TestReasonAndOutcomeValidation(t *testing.T) {
	gate := Gate{
		Code:    "whole-clone-reclaim",
		Outcome: OutcomeBlocked,
		Sources: []Source{testSource("git.status")},
	}
	if err := gate.Validate(ProfileDeep); err == nil || !strings.Contains(err.Error(), "requires a reason") {
		t.Fatalf("reasonless blocked gate Validate = %v", err)
	}
	gate.Reasons = []Reason{{Code: "Not Stable", Subject: "repo:one", Detail: "blocked"}}
	if err := gate.Validate(ProfileDeep); err == nil || !strings.Contains(err.Error(), "invalid reason code") {
		t.Fatalf("invalid reason Validate = %v", err)
	}
}
