package retire_test

import (
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/retire"
)

func TestAuditEligibleFacts(t *testing.T) {
	got := retire.Audit(eligibleAuditInput())
	if got.Status != retire.AuditEligible {
		t.Fatalf("Audit status = %q, checks=%+v", got.Status, got.Checks)
	}
	wantIDs := []string{
		retire.CheckTargetKind,
		retire.CheckRegistered,
		retire.CheckWorktreeUnlocked,
		retire.CheckBranchNamed,
		retire.CheckTaskIdentity,
		retire.CheckPathExists,
		retire.CheckStatusAvailable,
		retire.CheckClean,
		retire.CheckGitOperation,
		retire.CheckBaseKnown,
		retire.CheckBranchContained,
		retire.CheckTaskState,
		retire.CheckArtifactKnown,
		retire.CheckArtifactFinalized,
		retire.CheckRuntimeKnown,
		retire.CheckRuntimeReady,
	}
	if len(got.Checks) != len(wantIDs) {
		t.Fatalf("check count = %d, want %d", len(got.Checks), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got.Checks[i].ID != want {
			t.Errorf("check %d ID = %q, want %q", i, got.Checks[i].ID, want)
		}
	}
}

func TestAuditCanonicalAndEphemeralAreNotApplicable(t *testing.T) {
	for name, mutate := range map[string]func(*retire.AuditInput){
		"canonical": func(in *retire.AuditInput) { in.TargetKind = retire.AuditTargetCanonical },
		"ephemeral": func(in *retire.AuditInput) { in.TargetKind = retire.AuditTargetEphemeral },
	} {
		t.Run(name, func(t *testing.T) {
			input := retire.AuditInput{}
			mutate(&input)
			got := retire.Audit(input)
			if got.Status != retire.AuditNotApplicable {
				t.Fatalf("Audit status = %q, checks=%+v", got.Status, got.Checks)
			}
			for _, check := range got.Checks {
				if check.Status != retire.AuditNotApplicable {
					t.Errorf("check %s = %q", check.ID, check.Status)
				}
			}
		})
	}
}

func TestAuditFailedGatesBlock(t *testing.T) {
	tests := map[string]struct {
		checkID string
		mutate  func(*retire.AuditInput)
	}{
		"unregistered existing path": {
			checkID: retire.CheckRegistered,
			mutate:  func(in *retire.AuditInput) { in.Registered = retire.KnownFact(false) },
		},
		"locked": {
			checkID: retire.CheckWorktreeUnlocked,
			mutate:  func(in *retire.AuditInput) { in.Unlocked = retire.KnownFact(false) },
		},
		"detached": {
			checkID: retire.CheckBranchNamed,
			mutate:  func(in *retire.AuditInput) { in.BranchNamed = retire.KnownFact(false) },
		},
		"task identity mismatch": {
			checkID: retire.CheckTaskIdentity,
			mutate:  func(in *retire.AuditInput) { in.IdentityMatches = retire.KnownFact(false) },
		},
		"dirty": {
			checkID: retire.CheckClean,
			mutate:  func(in *retire.AuditInput) { in.Dirty = retire.KnownFact(true) },
		},
		"in progress": {
			checkID: retire.CheckGitOperation,
			mutate:  func(in *retire.AuditInput) { in.InProgress = retire.KnownFact(true) },
		},
		"not contained": {
			checkID: retire.CheckBranchContained,
			mutate:  func(in *retire.AuditInput) { in.Contained = retire.KnownFact(false) },
		},
		"task not done": {
			checkID: retire.CheckTaskState,
			mutate: func(in *retire.AuditInput) {
				in.TaskPresent = retire.KnownFact(true)
				in.TaskState = "warm"
			},
		},
		"artifacts not finalized": {
			checkID: retire.CheckArtifactFinalized,
			mutate:  func(in *retire.AuditInput) { in.Finalized = false },
		},
		"runtime not ready": {
			checkID: retire.CheckRuntimeReady,
			mutate:  func(in *retire.AuditInput) { in.RuntimeReady = false },
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			input := eligibleAuditInput()
			tc.mutate(&input)
			got := retire.Audit(input)
			if got.Status != retire.AuditBlocked {
				t.Fatalf("Audit status = %q, checks=%+v", got.Status, got.Checks)
			}
			if statusOf(got, tc.checkID) != retire.AuditBlocked {
				t.Errorf("check %s = %q", tc.checkID, statusOf(got, tc.checkID))
			}
		})
	}
}

func TestAuditUnknownFactsNeverBecomeEligible(t *testing.T) {
	tests := map[string]func(*retire.AuditInput){
		"target kind":    func(in *retire.AuditInput) { in.TargetKind = "" },
		"registration":   func(in *retire.AuditInput) { in.Registered = retire.Fact{} },
		"lock":           func(in *retire.AuditInput) { in.Unlocked = retire.Fact{} },
		"branch name":    func(in *retire.AuditInput) { in.BranchNamed = retire.Fact{} },
		"task identity":  func(in *retire.AuditInput) { in.IdentityMatches = retire.Fact{} },
		"path existence": func(in *retire.AuditInput) { in.PathExists = retire.Fact{} },
		"status error":   func(in *retire.AuditInput) { in.StatusError = "unreadable" },
		"dirty":          func(in *retire.AuditInput) { in.Dirty = retire.Fact{} },
		"git operation":  func(in *retire.AuditInput) { in.InProgress = retire.Fact{} },
		"base":           func(in *retire.AuditInput) { in.BaseKnown = false },
		"containment":    func(in *retire.AuditInput) { in.Contained = retire.Fact{} },
		"task presence":  func(in *retire.AuditInput) { in.TaskPresent = retire.Fact{} },
		"artifacts":      func(in *retire.AuditInput) { in.ArtifactKnown = false },
		"runtime":        func(in *retire.AuditInput) { in.RuntimeKnown = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := eligibleAuditInput()
			mutate(&input)
			got := retire.Audit(input)
			if got.Status != retire.AuditUnknown {
				t.Fatalf("Audit status = %q, want unknown; checks=%+v", got.Status, got.Checks)
			}
		})
	}

	if got := retire.Audit(retire.AuditInput{}); got.Status != retire.AuditUnknown {
		t.Fatalf("zero-value AuditInput status = %q, want unknown", got.Status)
	}
}

func TestAuditBlockerOutranksUnknownEvidence(t *testing.T) {
	input := eligibleAuditInput()
	input.Dirty = retire.KnownFact(true)
	input.RuntimeKnown = false
	if got := retire.Audit(input); got.Status != retire.AuditBlocked {
		t.Fatalf("Audit status = %q, want blocked; checks=%+v", got.Status, got.Checks)
	}
}

func TestAuditAlreadyAbsentCheckoutAndTaskVariants(t *testing.T) {
	input := eligibleAuditInput()
	input.Registered = retire.KnownFact(false)
	input.PathExists = retire.KnownFact(false)
	input.Dirty = retire.Fact{}
	input.InProgress = retire.Fact{}
	if got := retire.Audit(input); got.Status != retire.AuditEligible {
		t.Fatalf("absent checkout status = %q, checks=%+v", got.Status, got.Checks)
	}

	input = eligibleAuditInput()
	input.TaskPresent = retire.KnownFact(true)
	input.TaskState = "done"
	if got := retire.Audit(input); got.Status != retire.AuditEligible {
		t.Fatalf("done task status = %q, checks=%+v", got.Status, got.Checks)
	}

	input.TaskState = ""
	if got := retire.Audit(input); got.Status != retire.AuditUnknown {
		t.Fatalf("unknown persisted task state = %q, checks=%+v", got.Status, got.Checks)
	}

	input = eligibleAuditInput()
	input.TaskState = "done"
	if got := retire.Audit(input); got.Status != retire.AuditUnknown {
		t.Fatalf("conflicting absent task and state = %q, checks=%+v", got.Status, got.Checks)
	}
}

func eligibleAuditInput() retire.AuditInput {
	return retire.AuditInput{
		TargetKind:      retire.AuditTargetDev,
		Registered:      retire.KnownFact(true),
		Unlocked:        retire.KnownFact(true),
		BranchNamed:     retire.KnownFact(true),
		IdentityMatches: retire.KnownFact(true),
		PathExists:      retire.KnownFact(true),
		Dirty:           retire.KnownFact(false),
		InProgress:      retire.KnownFact(false),
		BaseKnown:       true,
		Contained:       retire.KnownFact(true),
		TaskPresent:     retire.KnownFact(false),
		ArtifactKnown:   true,
		Finalized:       true,
		RuntimeKnown:    true,
		RuntimeReady:    true,
	}
}

func statusOf(result retire.AuditResult, id string) retire.AuditStatus {
	for _, check := range result.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}
