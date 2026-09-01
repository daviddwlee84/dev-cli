// Package repocontext composes the versioned, presentation-neutral report used
// by dev repo context. Collection stays at the CLI/runtime boundaries; this
// package only joins already-observed facts and never performs network I/O.
package repocontext

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/assessment"
	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

const (
	// SchemaVersion is the additive public repository-context JSON contract.
	SchemaVersion = 1
	// FleetCoverageConfiguredHostsOnly prevents a configured fleet snapshot from
	// being mistaken for discovery of every machine which may hold the clone.
	FleetCoverageConfiguredHostsOnly = "configured-hosts-only"
)

// Report is the schema-v1 repository context envelope. Unknown values use nil
// pointers or explicit state/error fields rather than a meaningful zero value.
type Report struct {
	SchemaVersion    int                `json:"schema_version"`
	GeneratedAt      time.Time          `json:"generated_at"`
	Repository       Repository         `json:"repository"`
	SelectedCheckout *CheckoutSelection `json:"selected_checkout"`
	Sources          []Source           `json:"sources"`
	Capabilities     []Capability       `json:"capabilities"`
	Local            LocalFacts         `json:"local"`
	Remotes          []Remote           `json:"remotes"`
	Forge            ForgeFacts         `json:"forge"`
	Fleet            FleetFacts         `json:"fleet"`
	Assessment       assessment.Report  `json:"assessment"`
	Errors           []CollectionError  `json:"errors"`
}

// Source exposes evidence provenance and age. Its assessment fields deliberately
// match assessment.Source so gates can cite the exact same evidence.
type Source struct {
	ID           string                  `json:"id"`
	Authority    assessment.Authority    `json:"authority"`
	Freshness    assessment.Freshness    `json:"freshness"`
	Completeness assessment.Completeness `json:"completeness"`
	ObservedAt   time.Time               `json:"observed_at"`
	AgeSeconds   int64                   `json:"age_seconds"`
	Fingerprint  string                  `json:"fingerprint"`
}

func (s Source) assessmentSource() assessment.Source {
	return assessment.Source{
		ID: s.ID, Authority: s.Authority, Freshness: s.Freshness,
		Completeness: s.Completeness, ObservedAt: s.ObservedAt,
		Fingerprint: s.Fingerprint,
	}
}

type CapabilityState string

const (
	CapabilityAvailable      CapabilityState = "available"
	CapabilityPartial        CapabilityState = "partial"
	CapabilityUnavailable    CapabilityState = "unavailable"
	CapabilityNotImplemented CapabilityState = "not-implemented"
)

// Capability says whether one report section could be collected. It is not an
// action authorization; those live only in scoped assessment gates.
type Capability struct {
	Code      string          `json:"code"`
	State     CapabilityState `json:"state"`
	SourceIDs []string        `json:"source_ids"`
	Detail    string          `json:"detail,omitempty"`
}

// CollectionError preserves a failed or ambiguous observation without making
// the command itself discard otherwise useful facts.
type CollectionError struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

type Repository struct {
	Name      string  `json:"name"`
	Display   string  `json:"display"`
	Path      string  `json:"path"`
	RealPath  *string `json:"real_path"`
	CommonDir *string `json:"common_dir"`
	Bare      bool    `json:"bare"`
	HasGit    bool    `json:"has_git"`
}

type CheckoutSelection struct {
	Index     int                         `json:"index"`
	Path      string                      `json:"path"`
	Branch    *string                     `json:"branch"`
	Canonical bool                        `json:"canonical"`
	Ownership inventory.CheckoutOwnership `json:"ownership"`
}

type LocalFacts struct {
	Checkouts                          []CheckoutFacts `json:"checkouts"`
	OtherTasks                         []TaskFacts     `json:"other_tasks"`
	LinkedWorktreeCount                *int            `json:"linked_worktree_count"`
	ObservedLinkedWorktreeAdminEntries int             `json:"observed_linked_worktree_admin_entries"`
	CheckoutsComplete                  bool            `json:"checkouts_complete"`
	TaskInventoryComplete              bool            `json:"task_inventory_complete"`
	Runtimes                           []RuntimeFacts  `json:"runtimes"`
}

type CheckoutFacts struct {
	Index          int                         `json:"index"`
	Path           string                      `json:"path"`
	Canonical      bool                        `json:"canonical"`
	Ownership      inventory.CheckoutOwnership `json:"ownership"`
	State          string                      `json:"state"`
	Exists         *bool                       `json:"exists"`
	Branch         *string                     `json:"branch"`
	Head           *string                     `json:"head"`
	Detached       *bool                       `json:"detached"`
	Locked         bool                        `json:"locked"`
	LockedReason   *string                     `json:"locked_reason"`
	Prunable       bool                        `json:"prunable"`
	PrunableReason *string                     `json:"prunable_reason"`
	Status         *gitx.Status                `json:"status"`
	PathError      *string                     `json:"path_error"`
	StatusError    *string                     `json:"status_error"`
	LastActivity   *time.Time                  `json:"last_activity"`
	LastCommit     *time.Time                  `json:"last_commit"`
	LastSubject    *string                     `json:"last_subject"`
	Sessions       []RuntimeSession            `json:"sessions"`
	Tasks          []TaskFacts                 `json:"tasks"`
}

type TaskFacts struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Branch        string            `json:"branch"`
	Base          *string           `json:"base"`
	Mode          task.CheckoutMode `json:"mode"`
	State         task.State        `json:"state"`
	Owner         *string           `json:"owner"`
	Next          *string           `json:"next"`
	Tags          []string          `json:"tags"`
	AgentSession  *string           `json:"agent_session"`
	RuntimeName   *string           `json:"runtime_name"`
	RuntimeHandle *string           `json:"runtime_handle"`
	CreatedAt     *time.Time        `json:"created_at"`
	UpdatedAt     *time.Time        `json:"updated_at"`
}

type RuntimeFacts struct {
	Backend   string           `json:"backend"`
	Available bool             `json:"available"`
	SourceID  string           `json:"source_id"`
	Sessions  []RuntimeSession `json:"sessions"`
	Error     *string          `json:"error"`
}

type RuntimeSession struct {
	Backend       string   `json:"backend"`
	Handle        string   `json:"handle"`
	Label         *string  `json:"label"`
	AgentStatus   *string  `json:"agent_status"`
	AgentSessions []string `json:"agent_sessions"`
	Focused       bool     `json:"focused"`
	Checkouts     []int    `json:"checkouts"`
}

type RemoteRole string

const (
	RemoteRoleCurrentUpstream RemoteRole = "current-upstream"
	RemoteRoleOrigin          RemoteRole = "origin"
	RemoteRoleUpstream        RemoteRole = "upstream"
)

type Remote struct {
	Name  string           `json:"name"`
	Roles []RemoteRole     `json:"roles"`
	Fetch []RemoteEndpoint `json:"fetch"`
	Push  []RemoteEndpoint `json:"push"`
}

type RemoteEndpoint struct {
	Transport  string        `json:"transport"`
	Identity   *string       `json:"identity"`
	ForgeMatch string        `json:"forge_match"`
	WebURL     *forge.WebURL `json:"web_url"`
	Error      *string       `json:"error"`
}

type ForgeFacts struct {
	SourceID          *string `json:"source_id"`
	RecordsConsidered *int    `json:"records_considered"`
	MatchingRecords   *int    `json:"matching_records"`
	Complete          *bool   `json:"complete"`
	Error             *string `json:"error"`
}

type FleetFacts struct {
	Coverage        string                 `json:"coverage"`
	ConfiguredHosts *int                   `json:"configured_hosts"`
	Hosts           []FleetHostObservation `json:"hosts"`
	Error           *string                `json:"error"`
}

type FleetMatchState string

const (
	FleetMatchExact       FleetMatchState = "exact"
	FleetMatchNotFound    FleetMatchState = "not-found"
	FleetMatchAmbiguous   FleetMatchState = "ambiguous"
	FleetMatchUnavailable FleetMatchState = "unavailable"
)

type FleetHostObservation struct {
	Name         string            `json:"name"`
	State        fleet.HostState   `json:"state"`
	SourceID     *string           `json:"source_id"`
	CachedAt     *time.Time        `json:"cached_at"`
	Match        FleetMatchState   `json:"match"`
	Repositories []FleetRepository `json:"repositories"`
	Error        *string           `json:"error"`
}

type FleetRepository struct {
	Name          string           `json:"name"`
	Display       string           `json:"display"`
	Path          string           `json:"path"`
	Branch        *string          `json:"branch"`
	Git           *gitx.Status     `json:"git"`
	GitEvidence   string           `json:"git_evidence"`
	Worktrees     int              `json:"worktrees"`
	Tasks         fleet.TaskCounts `json:"tasks"`
	Runtime       *string          `json:"runtime"`
	RuntimeHandle *string          `json:"runtime_handle"`
	AgentStatus   *string          `json:"agent_status"`
	LastActivity  *time.Time       `json:"last_activity"`
}

// RuntimeInput is one live local backend collection. Sessions may include other
// repositories; Build projects only sessions covering this report's checkouts.
type RuntimeInput struct {
	Backend   string
	Available bool
	Sessions  []runtime.Session
	Err       error
}

// ForgeInput is either a cache observation or a live refresh result. Authority
// must be AuthorityCache or AuthorityRemoteLive when ObservedAt is set.
type ForgeInput struct {
	Authority    assessment.Authority
	Freshness    assessment.Freshness
	Completeness assessment.Completeness
	ObservedAt   time.Time
	Records      []forge.RemoteRepo
	Err          error
}

// FleetInput contains observations for exactly ConfiguredHostNames. Build never
// discovers or infers additional machines.
type FleetInput struct {
	Configured          bool
	ConfiguredHostNames []string
	Results             []fleet.HostResult
	CacheTTL            time.Duration
	Err                 error
}

type BuildInput struct {
	GeneratedAt      time.Time
	Context          inventory.RepoContext
	SelectedCheckout int
	SelectionErr     error
	Topology         gitx.RecoveryTopology
	TopologyErr      error
	Runtimes         []RuntimeInput
	Forge            ForgeInput
	Fleet            FleetInput
	Hostname         string
}

// Build joins live local facts with optional external observations. It performs
// no probes and never places raw Git endpoints in the returned report.
func Build(input BuildInput) (Report, error) {
	now := input.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.Round(0).UTC()

	report := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		Repository: Repository{
			Name: input.Context.Repo.Name, Display: input.Context.Repo.Display(),
			Path: input.Context.Repo.Path, RealPath: optionalString(input.Context.Repo.RealPath),
			CommonDir: optionalString(input.Context.Repo.CommonDir),
			Bare:      input.Context.Repo.Bare, HasGit: input.Context.Repo.HasGit,
		},
		Sources:      []Source{},
		Capabilities: []Capability{},
		Remotes:      []Remote{},
		Errors:       []CollectionError{},
	}

	report.Local = buildLocalFacts(input, &report.Errors)
	if input.SelectedCheckout >= 0 && input.SelectedCheckout < len(report.Local.Checkouts) {
		checkout := report.Local.Checkouts[input.SelectedCheckout]
		report.SelectedCheckout = &CheckoutSelection{
			Index: checkout.Index, Path: checkout.Path, Branch: checkout.Branch,
			Canonical: checkout.Canonical, Ownership: checkout.Ownership,
		}
	} else {
		message := "selected checkout is not present in the local checkout inventory"
		if input.SelectionErr != nil {
			message = cleanError(input.SelectionErr)
		}
		report.Errors = append(report.Errors, CollectionError{
			Code: "selection-unavailable", Scope: "local", Subject: "selected-checkout", Message: message,
		})
	}

	localIdentities := privateRemoteIdentities(input.Topology)
	report.Remotes, report.Forge, report.Errors = buildRemoteFacts(now, input, report.Errors)
	report.Fleet, report.Errors = buildFleetFacts(now, input.Fleet, localIdentities, report.Errors)

	sources := buildSources(now, input, report)
	report.Sources = sources
	report.Capabilities = buildCapabilities(input, report)

	readiness := AssessLocal(input.Context, input.SelectedCheckout, input.Hostname)
	sourceMap := make(map[string]assessment.Source, len(sources))
	for _, source := range sources {
		sourceMap[source.ID] = source.assessmentSource()
	}
	gates := []assessment.Gate{
		readinessGate("checkout-readiness", readiness.Checkout, sourcesNamed(sourceMap, "local.checkout")),
		readinessGate("task-readiness", readiness.Task, sourcesNamed(sourceMap, "local.tasks", "local.runtime")),
		readinessGate("worktree-readiness", readiness.Worktree, sourcesNamed(sourceMap, "local.worktrees", "local.runtime")),
		{
			Code: "whole-clone-eviction", Outcome: assessment.OutcomeIndeterminate,
			Reasons: []assessment.Reason{{
				Code: "recovery-proof-unavailable", Subject: "repository",
				Detail:      "whole-clone eviction is not assessed by the context profile",
				Remediation: "create and restore-verify a recovery receipt before eviction",
			}},
		},
	}
	assessmentReport, err := assessment.NewReport(assessment.ProfileCheap, now, gates)
	if err != nil {
		return Report{}, fmt.Errorf("seal repository context assessment: %w", err)
	}
	report.Assessment = assessmentReport

	sort.Slice(report.Errors, func(i, j int) bool {
		left, right := report.Errors[i], report.Errors[j]
		return strings.Join([]string{left.Scope, left.Code, left.Subject, left.Message}, "\x00") <
			strings.Join([]string{right.Scope, right.Code, right.Subject, right.Message}, "\x00")
	})
	return report, nil
}

func buildLocalFacts(input BuildInput, reportErrors *[]CollectionError) LocalFacts {
	context := input.Context
	local := LocalFacts{
		Checkouts:                          make([]CheckoutFacts, 0, len(context.Checkouts)),
		OtherTasks:                         []TaskFacts{},
		ObservedLinkedWorktreeAdminEntries: context.WorktreeCount,
		CheckoutsComplete:                  context.WorktreeErr == nil,
		TaskInventoryComplete:              context.TaskErr == nil,
		Runtimes:                           []RuntimeFacts{},
	}
	if context.WorktreeErr == nil {
		count := context.WorktreeCount
		local.LinkedWorktreeCount = &count
	} else {
		*reportErrors = append(*reportErrors, collectionError("worktree-inventory-failed", "local", "worktrees", context.WorktreeErr))
	}
	if context.TaskErr != nil {
		*reportErrors = append(*reportErrors, collectionError("task-inventory-failed", "local", "tasks", context.TaskErr))
	}

	for index, checkout := range context.Checkouts {
		facts := CheckoutFacts{
			Index: index, Path: checkout.Worktree.Path, Canonical: index == 0,
			Ownership: checkout.Ownership, Locked: checkout.Worktree.Locked,
			LockedReason:   optionalString(strings.TrimSpace(checkout.Worktree.LockedReason)),
			Prunable:       checkout.Worktree.Prunable,
			PrunableReason: optionalString(strings.TrimSpace(checkout.Worktree.PrunableReason)),
			Branch:         optionalString(checkout.Branch()), Head: optionalString(checkout.Worktree.Head),
			LastActivity: optionalTime(checkout.LastActivity), LastCommit: optionalTime(checkout.LastCommit),
			LastSubject: optionalString(checkout.LastSubject), Sessions: []RuntimeSession{}, Tasks: []TaskFacts{},
		}
		switch {
		case checkout.PathErr != nil:
			facts.State = "error"
			facts.PathError = optionalString(cleanError(checkout.PathErr))
			*reportErrors = append(*reportErrors, collectionError("checkout-path-probe-failed", "local", checkoutSubject(index), checkout.PathErr))
		case !checkout.Exists || checkout.Worktree.Prunable:
			facts.State = "missing"
			exists := false
			facts.Exists = &exists
		case checkout.Worktree.Bare || index == 0 && context.Repo.Bare:
			facts.State = "bare"
			exists := true
			facts.Exists = &exists
			detached := false
			facts.Detached = &detached
		case checkout.StatusErr != nil:
			facts.State = "error"
			exists := true
			facts.Exists = &exists
			facts.StatusError = optionalString(cleanError(checkout.StatusErr))
			*reportErrors = append(*reportErrors, collectionError("checkout-status-probe-failed", "local", checkoutSubject(index), checkout.StatusErr))
		default:
			facts.State = "available"
			exists := true
			facts.Exists = &exists
			status := checkout.Status
			facts.Status = &status
			detached := checkout.Worktree.Detached || checkout.Status.Detached
			facts.Detached = &detached
		}
		for _, tracked := range checkout.Tasks {
			facts.Tasks = append(facts.Tasks, taskFacts(tracked))
			if tracked != nil {
				if err := tracked.Validate(); err != nil {
					*reportErrors = append(*reportErrors, collectionError("task-record-invalid", "local", "task", err))
				}
			}
		}
		local.Checkouts = append(local.Checkouts, facts)
	}
	for _, tracked := range context.OtherTasks {
		local.OtherTasks = append(local.OtherTasks, taskFacts(tracked))
		if tracked != nil {
			if err := tracked.Validate(); err != nil {
				*reportErrors = append(*reportErrors, collectionError("task-record-invalid", "local", "task", err))
			}
		}
	}

	runtimes := input.Runtimes
	if len(runtimes) == 0 && (context.Runtime != "" || context.RuntimeErr != nil || len(context.Sessions()) > 0) {
		runtimes = []RuntimeInput{{
			Backend: context.Runtime, Available: context.RuntimeErr == nil,
			Sessions: context.Sessions(), Err: context.RuntimeErr,
		}}
	}
	for runtimeIndex, observed := range runtimes {
		backend := strings.TrimSpace(observed.Backend)
		if backend == "" {
			backend = fmt.Sprintf("unknown-%d", runtimeIndex+1)
		}
		runtimeFacts := RuntimeFacts{
			Backend: backend, Available: observed.Available,
			SourceID: "local.runtime", Sessions: []RuntimeSession{},
		}
		if observed.Err != nil {
			runtimeFacts.Error = optionalString(cleanError(observed.Err))
			*reportErrors = append(*reportErrors, collectionError("runtime-inventory-failed", "local", "runtime:"+safeIdentifier(backend), observed.Err))
		}
		for _, session := range observed.Sessions {
			indices := sessionCheckoutIndices(context, session)
			if len(indices) == 0 {
				continue
			}
			projected := RuntimeSession{
				Backend: backend, Handle: session.Handle, Label: optionalString(session.Label),
				AgentStatus:   optionalString(session.AgentStatus),
				AgentSessions: append([]string{}, session.AgentSessions...),
				Focused:       session.Focused, Checkouts: indices,
			}
			sort.Strings(projected.AgentSessions)
			runtimeFacts.Sessions = append(runtimeFacts.Sessions, projected)
			for _, checkoutIndex := range indices {
				if checkoutIndex >= 0 && checkoutIndex < len(local.Checkouts) {
					local.Checkouts[checkoutIndex].Sessions = append(local.Checkouts[checkoutIndex].Sessions, projected)
				}
			}
		}
		sortRuntimeSessions(runtimeFacts.Sessions)
		local.Runtimes = append(local.Runtimes, runtimeFacts)
	}
	sort.Slice(local.Runtimes, func(i, j int) bool { return local.Runtimes[i].Backend < local.Runtimes[j].Backend })
	for index := range local.Checkouts {
		sortRuntimeSessions(local.Checkouts[index].Sessions)
	}
	return local
}

func buildRemoteFacts(now time.Time, input BuildInput, reportErrors []CollectionError) ([]Remote, ForgeFacts, []CollectionError) {
	forgeFacts := ForgeFacts{Error: optionalError(input.Forge.Err)}
	if !input.Forge.ObservedAt.IsZero() && input.Forge.Authority != "" {
		sourceID := "forge.inventory"
		forgeFacts.SourceID = &sourceID
		records := len(input.Forge.Records)
		forgeFacts.RecordsConsidered = &records
		complete := input.Forge.Completeness == assessment.CompletenessComplete
		forgeFacts.Complete = &complete
	} else if input.Forge.Err != nil {
		reportErrors = append(reportErrors, collectionError("forge-inventory-unavailable", "forge", "inventory", input.Forge.Err))
	}
	if input.Forge.Err != nil && !input.Forge.ObservedAt.IsZero() {
		reportErrors = append(reportErrors, collectionError("forge-inventory-partial", "forge", "inventory", input.Forge.Err))
	}
	if input.TopologyErr != nil {
		reportErrors = append(reportErrors, collectionError("remote-topology-failed", "local", "remotes", input.TopologyErr))
		if forgeFacts.SourceID != nil {
			zero := 0
			forgeFacts.MatchingRecords = &zero
		}
		return []Remote{}, forgeFacts, reportErrors
	}

	recordsByIdentity := forgeRecordsByIdentity(input.Forge.Records)
	currentUpstream := ""
	if input.SelectedCheckout >= 0 && input.SelectedCheckout < len(input.Context.Checkouts) {
		status := input.Context.Checkouts[input.SelectedCheckout].Status
		if input.Context.Checkouts[input.SelectedCheckout].StatusErr == nil && status.Upstream != "" {
			currentUpstream, _, _ = strings.Cut(status.Upstream, "/")
		}
	}
	matchedRecords := map[string]struct{}{}
	remotes := make([]Remote, 0, len(input.Topology.Remotes))
	for _, observed := range input.Topology.Remotes {
		remote := Remote{Name: observed.Name, Roles: remoteRoles(observed.Name, currentUpstream), Fetch: []RemoteEndpoint{}, Push: []RemoteEndpoint{}}
		for index, raw := range observed.FetchURLs {
			endpoint, matched, endpointErr := buildRemoteEndpoint(raw, recordsByIdentity, forgeFacts.SourceID != nil, input.Forge.Authority == assessment.AuthorityRemoteLive && input.Forge.Freshness == assessment.FreshnessFresh && input.Forge.Completeness == assessment.CompletenessComplete)
			remote.Fetch = append(remote.Fetch, endpoint)
			for _, key := range matched {
				matchedRecords[key] = struct{}{}
			}
			if endpointErr != nil {
				reportErrors = append(reportErrors, CollectionError{
					Code: endpointErr.Code, Scope: "remote", Subject: fmt.Sprintf("%s.fetch.%d", safeIdentifier(observed.Name), index), Message: endpointErr.Message,
				})
			}
		}
		for index, raw := range observed.PushURLs {
			endpoint, matched, endpointErr := buildRemoteEndpoint(raw, recordsByIdentity, forgeFacts.SourceID != nil, input.Forge.Authority == assessment.AuthorityRemoteLive && input.Forge.Freshness == assessment.FreshnessFresh && input.Forge.Completeness == assessment.CompletenessComplete)
			remote.Push = append(remote.Push, endpoint)
			for _, key := range matched {
				matchedRecords[key] = struct{}{}
			}
			if endpointErr != nil {
				reportErrors = append(reportErrors, CollectionError{
					Code: endpointErr.Code, Scope: "remote", Subject: fmt.Sprintf("%s.push.%d", safeIdentifier(observed.Name), index), Message: endpointErr.Message,
				})
			}
		}
		remotes = append(remotes, remote)
	}
	if forgeFacts.SourceID != nil {
		matches := len(matchedRecords)
		forgeFacts.MatchingRecords = &matches
	}
	return remotes, forgeFacts, reportErrors
}

type endpointBuildError struct {
	Code    string
	Message string
}

func buildRemoteEndpoint(raw string, records map[string][]forge.RemoteRepo, forgeAvailable, exactForge bool) (RemoteEndpoint, []string, *endpointBuildError) {
	transport, displayIdentity, displayOK := publicEndpointIdentity(raw)
	forgeMatch := "unavailable"
	if forgeAvailable {
		forgeMatch = "none"
	}
	endpoint := RemoteEndpoint{Transport: transport, Identity: optionalString(displayIdentity), ForgeMatch: forgeMatch}
	privateIdentity := privateRemoteIdentity(raw)
	if !displayOK {
		message := "endpoint has no display-safe cross-host identity"
		endpoint.Error = &message
		return endpoint, nil, &endpointBuildError{Code: "remote-identity-unavailable", Message: message}
	}

	candidates := dedupeForgeRecords(records[privateIdentity])
	var exact *forge.RemoteRepo
	matched := []string{}
	switch len(candidates) {
	case 1:
		if exactForge {
			exact = &candidates[0]
		}
		endpoint.ForgeMatch = "exact"
		matched = append(matched, forgeRecordKey(candidates[0]))
	case 0:
		if forgeAvailable {
			endpoint.ForgeMatch = "none"
		}
	default:
		endpoint.ForgeMatch = "ambiguous"
		message := fmt.Sprintf("normalized endpoint identity matches %d forge records", len(candidates))
		endpoint.Error = &message
		for _, candidate := range candidates {
			matched = append(matched, forgeRecordKey(candidate))
		}
		if web, ok := forge.DeriveWebURL(forge.WebURLRequest{Remote: raw}); ok {
			endpoint.WebURL = &web
		}
		return endpoint, matched, &endpointBuildError{Code: "forge-match-ambiguous", Message: message}
	}
	if web, ok := forge.DeriveWebURL(forge.WebURLRequest{Remote: raw, Exact: exact}); ok {
		endpoint.WebURL = &web
	}
	return endpoint, matched, nil
}

func buildFleetFacts(now time.Time, input FleetInput, localIdentities map[string]struct{}, reportErrors []CollectionError) (FleetFacts, []CollectionError) {
	facts := FleetFacts{Coverage: FleetCoverageConfiguredHostsOnly, Hosts: []FleetHostObservation{}, Error: optionalError(input.Err)}
	if !input.Configured {
		if input.Err == nil {
			facts.Error = optionalString("fleet configuration was not collected")
		}
		if facts.Error != nil {
			reportErrors = append(reportErrors, CollectionError{Code: "fleet-config-unavailable", Scope: "fleet", Subject: "configuration", Message: *facts.Error})
		}
		return facts, reportErrors
	}
	count := len(input.ConfiguredHostNames)
	facts.ConfiguredHosts = &count
	results := map[string]fleet.HostResult{}
	for _, result := range input.Results {
		if _, exists := results[result.Name]; !exists {
			results[result.Name] = result
		}
	}
	for index, name := range input.ConfiguredHostNames {
		result, found := results[name]
		if !found {
			result = fleet.HostResult{Name: name, State: fleet.HostUnreachable, Error: "configured host produced no observation"}
		}
		host := FleetHostObservation{
			Name: name, State: result.State, CachedAt: optionalTimeValue(result.CachedAt),
			Match: FleetMatchUnavailable, Repositories: []FleetRepository{}, Error: optionalString(cleanText(result.Error)),
		}
		if result.Snapshot != nil {
			matches := matchingFleetRepositories(result.Snapshot.Repositories, localIdentities)
			for _, matched := range matches {
				host.Repositories = append(host.Repositories, fleetRepositoryFacts(matched))
			}
			switch {
			case len(localIdentities) == 0:
				host.Match = FleetMatchUnavailable
				if host.Error == nil {
					host.Error = optionalString("local repository has no normalized remote identity")
				}
			case len(matches) == 0:
				host.Match = FleetMatchNotFound
			case len(matches) == 1:
				host.Match = FleetMatchExact
			default:
				host.Match = FleetMatchAmbiguous
				message := fmt.Sprintf("%d repositories on this host share a local normalized remote identity", len(matches))
				if host.Error == nil {
					host.Error = &message
				}
				reportErrors = append(reportErrors, CollectionError{Code: "fleet-match-ambiguous", Scope: "fleet", Subject: "host:" + safeIdentifier(name), Message: message})
			}

			observedAt := result.Snapshot.GeneratedAt
			if observedAt.IsZero() && result.CachedAt != nil {
				observedAt = *result.CachedAt
			}
			if !observedAt.IsZero() {
				sourceID := fmt.Sprintf("fleet.host.%d", index)
				host.SourceID = &sourceID
			}
		}
		if result.Error != "" || result.Snapshot == nil {
			message := result.Error
			if message == "" {
				message = "host produced no repository snapshot"
			}
			reportErrors = append(reportErrors, CollectionError{Code: "fleet-host-unavailable", Scope: "fleet", Subject: "host:" + safeIdentifier(name), Message: cleanText(message)})
		}
		facts.Hosts = append(facts.Hosts, host)
	}
	return facts, reportErrors
}

func buildSources(now time.Time, input BuildInput, report Report) []Source {
	sources := []Source{}
	selectedFacts := any(nil)
	checkoutCompleteness := assessment.CompletenessUnknown
	if report.SelectedCheckout != nil {
		selectedFacts = report.Local.Checkouts[report.SelectedCheckout.Index]
		checkoutCompleteness = assessment.CompletenessComplete
		selected := input.Context.Checkouts[report.SelectedCheckout.Index]
		if selected.PathErr != nil || selected.StatusErr != nil {
			checkoutCompleteness = assessment.CompletenessPartial
		}
	}
	sources = append(sources, newSource("local.checkout", assessment.AuthorityLocalLive, assessment.FreshnessFresh, checkoutCompleteness, now, now, selectedFacts))

	taskCompleteness := assessment.CompletenessComplete
	if input.Context.TaskErr != nil {
		taskCompleteness = assessment.CompletenessPartial
	}
	sources = append(sources, newSource("local.tasks", assessment.AuthorityLocalLive, assessment.FreshnessFresh, taskCompleteness, now, now, struct {
		Checkouts  []CheckoutFacts `json:"checkouts"`
		OtherTasks []TaskFacts     `json:"other_tasks"`
	}{report.Local.Checkouts, report.Local.OtherTasks}))

	worktreeCompleteness := assessment.CompletenessComplete
	if input.Context.WorktreeErr != nil {
		worktreeCompleteness = assessment.CompletenessPartial
	}
	sources = append(sources, newSource("local.worktrees", assessment.AuthorityLocalLive, assessment.FreshnessFresh, worktreeCompleteness, now, now, struct {
		Count     *int            `json:"count"`
		Checkouts []CheckoutFacts `json:"checkouts"`
	}{report.Local.LinkedWorktreeCount, report.Local.Checkouts}))

	runtimeCompleteness := assessment.CompletenessComplete
	if len(report.Local.Runtimes) == 0 {
		runtimeCompleteness = assessment.CompletenessUnknown
	}
	for _, observed := range report.Local.Runtimes {
		if observed.Error != nil {
			runtimeCompleteness = assessment.CompletenessPartial
		}
	}
	sources = append(sources, newSource("local.runtime", assessment.AuthorityLocalLive, assessment.FreshnessFresh, runtimeCompleteness, now, now, report.Local.Runtimes))

	remoteCompleteness := assessment.CompletenessComplete
	if input.TopologyErr != nil {
		remoteCompleteness = assessment.CompletenessPartial
	}
	sources = append(sources, newSource("local.remotes", assessment.AuthorityLocalLive, assessment.FreshnessFresh, remoteCompleteness, now, now, report.Remotes))

	if input.Fleet.Configured {
		sources = append(sources, newSource("local.fleet-config", assessment.AuthorityLocalLive, assessment.FreshnessFresh, assessment.CompletenessComplete, now, now, input.Fleet.ConfiguredHostNames))
	}
	if !input.Forge.ObservedAt.IsZero() && input.Forge.Authority != "" {
		freshness := input.Forge.Freshness
		if freshness == "" {
			freshness = assessment.FreshnessUnknown
		}
		completeness := input.Forge.Completeness
		if completeness == "" {
			completeness = assessment.CompletenessUnknown
		}
		sources = append(sources, newSource("forge.inventory", input.Forge.Authority, freshness, completeness, input.Forge.ObservedAt, now, struct {
			Remotes []Remote   `json:"remotes"`
			Forge   ForgeFacts `json:"forge"`
		}{report.Remotes, report.Forge}))
	}
	resultByName := map[string]fleet.HostResult{}
	for _, result := range input.Fleet.Results {
		resultByName[result.Name] = result
	}
	for index, name := range input.Fleet.ConfiguredHostNames {
		result, ok := resultByName[name]
		if !ok || result.Snapshot == nil || index >= len(report.Fleet.Hosts) {
			continue
		}
		observedAt := result.Snapshot.GeneratedAt
		if observedAt.IsZero() && result.CachedAt != nil {
			observedAt = *result.CachedAt
		}
		if observedAt.IsZero() {
			continue
		}
		authority := assessment.AuthorityRemoteLive
		freshness := assessment.FreshnessFresh
		if result.FromCache {
			authority = assessment.AuthorityCache
			if input.Fleet.CacheTTL > 0 && now.Sub(observedAt) > input.Fleet.CacheTTL {
				freshness = assessment.FreshnessStale
			}
		}
		completeness := assessment.CompletenessComplete
		if result.Error != "" || result.State != fleet.HostOK && !result.FromCache {
			completeness = assessment.CompletenessPartial
		}
		sources = append(sources, newSource(fmt.Sprintf("fleet.host.%d", index), authority, freshness, completeness, observedAt, now, report.Fleet.Hosts[index]))
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources
}

func buildCapabilities(input BuildInput, report Report) []Capability {
	capabilities := []Capability{}
	localState := CapabilityAvailable
	if input.TopologyErr != nil || input.Context.WorktreeErr != nil {
		localState = CapabilityPartial
	}
	for _, checkout := range input.Context.Checkouts {
		if checkout.PathErr != nil || checkout.StatusErr != nil {
			localState = CapabilityPartial
		}
	}
	capabilities = append(capabilities, Capability{Code: "local-git", State: localState, SourceIDs: []string{"local.checkout", "local.worktrees", "local.remotes"}})

	taskState := CapabilityAvailable
	if input.Context.TaskErr != nil {
		taskState = CapabilityPartial
	}
	capabilities = append(capabilities, Capability{Code: "task-inventory", State: taskState, SourceIDs: []string{"local.tasks"}})

	runtimeState := CapabilityAvailable
	if len(report.Local.Runtimes) == 0 {
		runtimeState = CapabilityUnavailable
	}
	for _, observed := range report.Local.Runtimes {
		if observed.Error != nil {
			runtimeState = CapabilityPartial
		}
	}
	capabilities = append(capabilities, Capability{Code: "runtime-inventory", State: runtimeState, SourceIDs: []string{"local.runtime"}})

	forgeCapability := Capability{Code: "forge-inventory", State: CapabilityUnavailable, SourceIDs: []string{}}
	if report.Forge.SourceID != nil {
		forgeCapability.State = CapabilityAvailable
		forgeCapability.SourceIDs = []string{*report.Forge.SourceID}
		if input.Forge.Err != nil || input.Forge.Completeness != assessment.CompletenessComplete {
			forgeCapability.State = CapabilityPartial
		}
	}
	if report.Forge.Error != nil {
		forgeCapability.Detail = *report.Forge.Error
	}
	capabilities = append(capabilities, forgeCapability)

	fleetCapability := Capability{Code: "fleet-inventory", State: CapabilityUnavailable, SourceIDs: []string{}}
	if input.Fleet.Configured {
		fleetCapability.State = CapabilityAvailable
		fleetCapability.SourceIDs = []string{"local.fleet-config"}
		for _, host := range report.Fleet.Hosts {
			if host.SourceID != nil {
				fleetCapability.SourceIDs = append(fleetCapability.SourceIDs, *host.SourceID)
			}
			if host.Error != nil || host.SourceID == nil {
				fleetCapability.State = CapabilityPartial
			}
		}
	}
	if report.Fleet.Error != nil {
		fleetCapability.Detail = *report.Fleet.Error
	}
	capabilities = append(capabilities, fleetCapability)
	capabilities = append(capabilities, Capability{
		Code: "whole-clone-eviction", State: CapabilityNotImplemented, SourceIDs: []string{},
		Detail: "context does not implement recovery receipt or restore-verification proof",
	})
	for index := range capabilities {
		sort.Strings(capabilities[index].SourceIDs)
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Code < capabilities[j].Code })
	return capabilities
}

func readinessGate(code string, readiness Readiness, sources []assessment.Source) assessment.Gate {
	return assessment.Gate{Code: code, Outcome: readiness.Outcome, Sources: sources, Reasons: append([]assessment.Reason{}, readiness.Reasons...)}
}

func sourcesNamed(sources map[string]assessment.Source, names ...string) []assessment.Source {
	result := make([]assessment.Source, 0, len(names))
	for _, name := range names {
		if source, ok := sources[name]; ok {
			result = append(result, source)
		}
	}
	return result
}

func newSource(id string, authority assessment.Authority, freshness assessment.Freshness, completeness assessment.Completeness, observedAt, now time.Time, facts any) Source {
	observedAt = observedAt.Round(0).UTC()
	encoded, err := json.Marshal(facts)
	if err != nil {
		encoded = []byte(fmt.Sprintf("unencodable:%T", facts))
	}
	age := int64(0)
	if now.After(observedAt) {
		age = int64(now.Sub(observedAt) / time.Second)
	}
	return Source{
		ID: id, Authority: authority, Freshness: freshness, Completeness: completeness,
		ObservedAt: observedAt, AgeSeconds: age, Fingerprint: assessment.FingerprintBytes(encoded),
	}
}

func taskFacts(tracked *task.Task) TaskFacts {
	if tracked == nil {
		return TaskFacts{}
	}
	return TaskFacts{
		ID: tracked.ID, Name: tracked.Name, Branch: tracked.Branch, Base: optionalString(tracked.Base),
		Mode: tracked.EffectiveMode(), State: tracked.State, Owner: optionalString(tracked.Owner),
		Next: optionalString(tracked.Next), Tags: append([]string{}, tracked.Tags...),
		AgentSession: optionalString(tracked.AgentSession), RuntimeName: optionalString(tracked.RuntimeName),
		RuntimeHandle: optionalString(tracked.RuntimeHandle), CreatedAt: optionalTime(tracked.Created), UpdatedAt: optionalTime(tracked.Updated),
	}
}

func sessionCheckoutIndices(context inventory.RepoContext, session runtime.Session) []int {
	seen := map[int]struct{}{}
	for _, dir := range session.Dirs {
		if index, ok := context.CheckoutIndexForPath(dir); ok {
			seen[index] = struct{}{}
		}
	}
	if len(seen) == 0 && session.Handle != "" {
		for index, checkout := range context.Checkouts {
			for _, assigned := range checkout.Sessions {
				if assigned.Handle == session.Handle {
					seen[index] = struct{}{}
				}
			}
		}
	}
	indices := make([]int, 0, len(seen))
	for index := range seen {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func sortRuntimeSessions(sessions []RuntimeSession) {
	sort.Slice(sessions, func(i, j int) bool {
		left, right := sessions[i], sessions[j]
		return strings.Join([]string{left.Backend, left.Handle, valueOrEmpty(left.Label)}, "\x00") <
			strings.Join([]string{right.Backend, right.Handle, valueOrEmpty(right.Label)}, "\x00")
	})
}

func privateRemoteIdentities(topology gitx.RecoveryTopology) map[string]struct{} {
	identities := map[string]struct{}{}
	for _, remote := range topology.Remotes {
		for _, raw := range append(append([]string{}, remote.FetchURLs...), remote.PushURLs...) {
			if identity := privateRemoteIdentity(raw); identity != "" {
				identities[identity] = struct{}{}
			}
		}
	}
	return identities
}

func privateRemoteIdentity(raw string) string {
	if !utf8.ValidString(raw) || containsControl(raw) {
		return ""
	}
	return catalog.NormalizeRemoteIdentity(raw)
}

func publicEndpointIdentity(raw string) (transport, identity string, ok bool) {
	if !utf8.ValidString(raw) || containsControl(raw) {
		return "unknown", "", false
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "unknown", "", false
	}
	redacted := trimmed
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		transport = strings.ToLower(parsed.Scheme)
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		redacted = parsed.String()
	} else {
		if index := strings.IndexAny(redacted, "?#"); index >= 0 {
			redacted = redacted[:index]
		}
		colon := strings.IndexByte(redacted, ':')
		at := strings.LastIndexByte(redacted, '@')
		if at >= 0 && colon > at {
			transport = "ssh"
			redacted = "git@" + redacted[at+1:]
		} else if filepath.IsAbs(redacted) || strings.HasPrefix(redacted, ".") || strings.HasPrefix(redacted, "~") {
			transport = "local"
		} else {
			transport = "unknown"
		}
	}
	identity = catalog.NormalizeRemoteIdentity(redacted)
	if identity == "" || containsControl(identity) {
		return transport, "", false
	}
	return transport, identity, true
}

func forgeRecordsByIdentity(records []forge.RemoteRepo) map[string][]forge.RemoteRepo {
	result := map[string][]forge.RemoteRepo{}
	for _, record := range records {
		seen := map[string]struct{}{}
		for _, raw := range []string{record.CloneURL, record.SSHURL, record.URL} {
			identity := privateRemoteIdentity(raw)
			if identity == "" {
				continue
			}
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			result[identity] = append(result[identity], record)
		}
	}
	return result
}

func dedupeForgeRecords(records []forge.RemoteRepo) []forge.RemoteRepo {
	seen := map[string]struct{}{}
	result := make([]forge.RemoteRepo, 0, len(records))
	for _, record := range records {
		key := forgeRecordKey(record)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return forgeRecordKey(result[i]) < forgeRecordKey(result[j]) })
	return result
}

func forgeRecordKey(record forge.RemoteRepo) string {
	return strings.Join([]string{string(record.Forge), record.FullName, record.URL}, "\x00")
}

func remoteRoles(name, currentUpstream string) []RemoteRole {
	roles := []RemoteRole{}
	if name == currentUpstream && currentUpstream != "" {
		roles = append(roles, RemoteRoleCurrentUpstream)
	}
	if name == "origin" {
		roles = append(roles, RemoteRoleOrigin)
	}
	if name == "upstream" {
		roles = append(roles, RemoteRoleUpstream)
	}
	return roles
}

func matchingFleetRepositories(repositories []fleet.RepoSnapshot, localIdentities map[string]struct{}) []fleet.RepoSnapshot {
	if len(localIdentities) == 0 {
		return nil
	}
	matches := []fleet.RepoSnapshot{}
	for _, repository := range repositories {
		matched := false
		for _, raw := range repository.RemoteIdentities {
			identity := catalog.NormalizeRemoteIdentity(raw)
			if identity == "" {
				continue
			}
			if _, ok := localIdentities[identity]; ok {
				matched = true
				break
			}
		}
		if matched {
			matches = append(matches, repository)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return strings.Join([]string{matches[i].Display, matches[i].Path}, "\x00") <
			strings.Join([]string{matches[j].Display, matches[j].Path}, "\x00")
	})
	return matches
}

func fleetRepositoryFacts(repository fleet.RepoSnapshot) FleetRepository {
	status := repository.Status
	return FleetRepository{
		Name: repository.Name, Display: repository.Display, Path: repository.Path,
		Branch: optionalString(repository.Branch), Git: &status,
		GitEvidence: "snapshot-v1-unqualified", Worktrees: repository.Worktrees, Tasks: repository.Tasks,
		Runtime: optionalString(repository.Runtime), RuntimeHandle: optionalString(repository.RuntimeHandle),
		AgentStatus: optionalString(repository.AgentStatus), LastActivity: optionalTime(repository.LastActivity),
	}
}

func collectionError(code, scope, subject string, err error) CollectionError {
	return CollectionError{Code: code, Scope: scope, Subject: subject, Message: cleanError(err)}
}

func checkoutSubject(index int) string { return fmt.Sprintf("checkout:%d", index) }

func optionalError(err error) *string {
	if err == nil {
		return nil
	}
	return optionalString(cleanError(err))
}

func cleanError(err error) string {
	if err == nil {
		return ""
	}
	return cleanText(err.Error())
}

// cleanText removes controls and structurally redacts URL/scp-like tokens while
// preserving enough of an error to diagnose which probe failed.
func cleanText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	fields := strings.Fields(value)
	for index, field := range fields {
		fields[index] = redactTextToken(field)
	}
	return strings.TrimSpace(strings.Join(fields, " "))
}

func redactTextToken(token string) string {
	prefix := strings.TrimLeft(token, "([{<\"'")
	leading := token[:len(token)-len(prefix)]
	core := strings.TrimRight(prefix, ")]}>\"',;.")
	trailing := prefix[len(core):]
	if parsed, err := url.Parse(core); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return leading + parsed.String() + trailing
	}
	colon := strings.IndexByte(core, ':')
	at := strings.LastIndexByte(core, '@')
	if at > 0 && (colon > at || strings.Contains(core[:at], ":")) {
		core = "[redacted]@" + core[at+1:]
		if query := strings.IndexAny(core, "?#"); query >= 0 {
			core = core[:query]
		}
	}
	return leading + core + trailing
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0
}

func safeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "unknown"
	}
	return result
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.Round(0).UTC()
	return &copy
}

func optionalTimeValue(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	copy := value.Round(0).UTC()
	return &copy
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
