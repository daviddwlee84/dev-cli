package ephemeral

import "testing"

func TestAuditEligibleFacts(t *testing.T) {
	got := Audit(eligibleAuditInput())
	if got.Classification != Eligible {
		t.Fatalf("classification = %q, checks=%+v", got.Classification, got.Checks)
	}
	want := []string{
		CheckProviderOwned,
		CheckProviderUnique,
		CheckProviderMapping,
		CheckProviderIdentity,
		CheckWorkflowTerminal,
		CheckAgentDone,
		CheckJournalStarted,
		CheckJournalResult,
		CheckNotResumed,
		CheckProviderInactive,
		CheckRegistered,
		CheckPathPresent,
		CheckLinkedWorktree,
		CheckNonMain,
		CheckBranchNamed,
		CheckBranchMatches,
		CheckCommonDir,
		CheckHeadMatches,
		CheckUnlocked,
		CheckNotPrunable,
		CheckClean,
		CheckIgnoredEmpty,
		CheckSubmodulesClean,
		CheckNoGitOperation,
		CheckTaskUnclaimed,
		CheckArtifactsKnown,
		CheckArtifactsSafe,
		CheckCallerOutside,
		CheckRuntimeKnown,
		CheckRuntimeClear,
	}
	if len(got.Checks) != len(want) {
		t.Fatalf("check count = %d, want %d: %+v", len(got.Checks), len(want), got.Checks)
	}
	for i, id := range want {
		if got.Checks[i].ID != id {
			t.Errorf("check %d = %q, want %q", i, got.Checks[i].ID, id)
		}
	}
}

func TestAuditZeroFactsAreUnknown(t *testing.T) {
	got := Audit(AuditInput{})
	if got.Classification != Unknown {
		t.Fatalf("zero-value classification = %q, want unknown; checks=%+v", got.Classification, got.Checks)
	}
	for _, check := range got.Checks {
		if check.Classification == Eligible {
			t.Errorf("zero-value check %s became eligible", check.ID)
		}
	}
}

func TestAuditBlockedOutranksUnknown(t *testing.T) {
	input := eligibleAuditInput()
	input.Clean = KnownFact(false)
	input.RuntimeKnown = Fact{}
	got := Audit(input)
	if got.Classification != Blocked {
		t.Fatalf("classification = %q, want blocked; checks=%+v", got.Classification, got.Checks)
	}
}

func TestAuditEverySafetyFailureBlocks(t *testing.T) {
	tests := map[string]struct {
		id     string
		mutate func(*AuditInput)
	}{
		"duplicate provider claim": {CheckProviderUnique, func(in *AuditInput) { in.ProviderUnique = KnownFact(false) }},
		"mapping mismatch":         {CheckProviderMapping, func(in *AuditInput) { in.ProviderMapping = KnownFact(false) }},
		"identity mismatch":        {CheckProviderIdentity, func(in *AuditInput) { in.ProviderIdentity = KnownFact(false) }},
		"too recent":               {CheckProviderInactive, func(in *AuditInput) { in.ProviderInactive = KnownFact(false) }},
		"detached":                 {CheckBranchNamed, func(in *AuditInput) { in.BranchNamed = KnownFact(false) }},
		"branch mismatch":          {CheckBranchMatches, func(in *AuditInput) { in.BranchMatches = KnownFact(false) }},
		"common dir mismatch":      {CheckCommonDir, func(in *AuditInput) { in.CommonDirMatches = KnownFact(false) }},
		"head mismatch":            {CheckHeadMatches, func(in *AuditInput) { in.HeadMatches = KnownFact(false) }},
		"locked":                   {CheckUnlocked, func(in *AuditInput) { in.Unlocked = KnownFact(false) }},
		"dirty":                    {CheckClean, func(in *AuditInput) { in.Clean = KnownFact(false) }},
		"ignored":                  {CheckIgnoredEmpty, func(in *AuditInput) { in.IgnoredEmpty = KnownFact(false) }},
		"dirty submodule":          {CheckSubmodulesClean, func(in *AuditInput) { in.SubmodulesClean = KnownFact(false) }},
		"git operation":            {CheckNoGitOperation, func(in *AuditInput) { in.NoGitOperation = KnownFact(false) }},
		"task claim":               {CheckTaskUnclaimed, func(in *AuditInput) { in.TaskUnclaimed = KnownFact(false) }},
		"unsafe artifacts":         {CheckArtifactsSafe, func(in *AuditInput) { in.ArtifactsSafe = KnownFact(false) }},
		"caller inside":            {CheckCallerOutside, func(in *AuditInput) { in.CallerOutside = KnownFact(false) }},
		"runtime coverage":         {CheckRuntimeClear, func(in *AuditInput) { in.RuntimeClear = KnownFact(false) }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			input := eligibleAuditInput()
			tc.mutate(&input)
			got := Audit(input)
			if got.Classification != Blocked {
				t.Fatalf("classification = %q, checks=%+v", got.Classification, got.Checks)
			}
			if statusOf(got, tc.id) != Blocked {
				t.Errorf("check %s = %q", tc.id, statusOf(got, tc.id))
			}
		})
	}
}

func TestAuditStrictV1AmbiguityIsUnknown(t *testing.T) {
	tests := map[string]func(*AuditInput){
		"provider Git identity": func(in *AuditInput) { in.ProviderIdentity = Fact{} },
		"killed without result": func(in *AuditInput) { in.JournalResult = KnownFact(false) },
		"progress workflow":     func(in *AuditInput) { in.WorkflowTerminal = KnownFact(false) },
		"agent not done":        func(in *AuditInput) { in.AgentDone = KnownFact(false) },
		"resumed transcript":    func(in *AuditInput) { in.NotResumed = KnownFact(false) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := eligibleAuditInput()
			mutate(&input)
			got := Audit(input)
			if got.Classification != Unknown {
				t.Fatalf("classification = %q, want unknown; checks=%+v", got.Classification, got.Checks)
			}
		})
	}
}

func TestAuditReportOnlyStructuresAreNotApplicable(t *testing.T) {
	tests := map[string]func(*AuditInput){
		"not provider owned": func(in *AuditInput) { in.ProviderOwned = KnownFact(false) },
		"unregistered":       func(in *AuditInput) { in.Registered = KnownFact(false) },
		"missing":            func(in *AuditInput) { in.PathPresent = KnownFact(false) },
		"main":               func(in *AuditInput) { in.NonMain = KnownFact(false) },
		"prunable":           func(in *AuditInput) { in.NotPrunable = KnownFact(false) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := eligibleAuditInput()
			mutate(&input)
			got := Audit(input)
			if got.Classification != NotApplicable {
				t.Fatalf("classification = %q, want not-applicable; checks=%+v", got.Classification, got.Checks)
			}
		})
	}
}

func TestAuditKilledCanBeTerminalOnlyWithAllChildProofs(t *testing.T) {
	input := eligibleAuditInput()
	input.WorkflowState = "killed"
	if got := Audit(input); got.Classification != Eligible {
		t.Fatalf("fully linked killed workflow = %q, checks=%+v", got.Classification, got.Checks)
	}
	input.JournalResult = KnownFact(false)
	if got := Audit(input); got.Classification != Unknown {
		t.Fatalf("killed workflow without result = %q, checks=%+v", got.Classification, got.Checks)
	}
}

func eligibleAuditInput() AuditInput {
	return AuditInput{
		ProviderOwned:    KnownFact(true),
		ProviderUnique:   KnownFact(true),
		ProviderMapping:  KnownFact(true),
		ProviderIdentity: KnownFact(true),
		WorkflowState:    "completed",
		WorkflowTerminal: KnownFact(true),
		AgentState:       "done",
		AgentDone:        KnownFact(true),
		JournalStarted:   KnownFact(true),
		JournalResult:    KnownFact(true),
		NotResumed:       KnownFact(true),
		ProviderInactive: KnownFact(true),
		Registered:       KnownFact(true),
		PathPresent:      KnownFact(true),
		LinkedWorktree:   KnownFact(true),
		NonMain:          KnownFact(true),
		BranchNamed:      KnownFact(true),
		BranchMatches:    KnownFact(true),
		CommonDirMatches: KnownFact(true),
		HeadMatches:      KnownFact(true),
		Unlocked:         KnownFact(true),
		NotPrunable:      KnownFact(true),
		Clean:            KnownFact(true),
		IgnoredEmpty:     KnownFact(true),
		SubmodulesClean:  KnownFact(true),
		NoGitOperation:   KnownFact(true),
		TaskUnclaimed:    KnownFact(true),
		ArtifactsKnown:   KnownFact(true),
		ArtifactsSafe:    KnownFact(true),
		CallerOutside:    KnownFact(true),
		RuntimeKnown:     KnownFact(true),
		RuntimeClear:     KnownFact(true),
	}
}

func statusOf(result AuditResult, id string) Classification {
	for _, check := range result.Checks {
		if check.ID == id {
			return check.Classification
		}
	}
	return ""
}
