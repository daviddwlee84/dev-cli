package forge

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PRRole is why a request is in your inbox.
type PRRole string

const (
	// RoleAuthor is a request you opened.
	RoleAuthor PRRole = "author"
	// RoleReviewer is a request that asked for your review.
	RoleReviewer PRRole = "reviewer"
)

// PRState is a request's lifecycle position.
type PRState string

const (
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
	// PRStateAll asks an adapter for every state it can report.
	PRStateAll PRState = "all"
)

// PRDetail records which provider surface produced a row.
//
// This is load-bearing rather than bookkeeping: the account-wide search cannot
// report a head branch or a review decision at all, so without it a consumer
// cannot tell "nobody has reviewed this" from "this surface does not know".
type PRDetail string

const (
	// PRDetailSummary came from the cheap account-wide search. Head branch,
	// review decision and checks are absent.
	PRDetailSummary PRDetail = "summary"
	// PRDetailFull came from the per-repository listing.
	PRDetailFull PRDetail = "full"
)

// PRQuery narrows a pull-request listing.
type PRQuery struct {
	// Roles defaults to author and reviewer when empty.
	Roles []PRRole
	// AnyRole asks a repository-scoped inventory for every request regardless
	// of its relationship to the authenticated user. It is for workspace
	// closeout, not the personal inbox. Account-wide listings reject it.
	AnyRole bool
	// State defaults to open. Only the per-repository surface can answer
	// anything else.
	State PRState
	// Limit caps rows per underlying request. Zero means the adapter default.
	Limit int
}

// PullRequest is one pull or merge request.
//
// The JSON tags are an automation contract: fields may be added, never renamed
// or removed. Everything the account-wide search cannot fill is omitempty, so
// an absent field means "not reported by this surface", which PRDetail
// disambiguates.
type PullRequest struct {
	Forge  Kind     `json:"forge"`
	Host   string   `json:"host,omitempty"`
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	State  PRState  `json:"state"`
	Draft  bool     `json:"draft"`
	Author string   `json:"author"`
	Roles  []PRRole `json:"roles,omitempty"`
	Detail PRDetail `json:"detail"`

	HeadRepo        string `json:"head_repo,omitempty"`
	CrossRepository bool   `json:"is_cross_repository,omitempty"`
	HeadBranch      string `json:"head_branch,omitempty"`
	BaseBranch      string `json:"base_branch,omitempty"`
	ReviewDecision  string `json:"review_decision,omitempty"`
	Mergeable       string `json:"mergeable,omitempty"`
	Checks          string `json:"checks,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`

	// headRepoID is a provider-internal lookup key. GitLab's list payload
	// exposes source_project_id but not its namespace; adapters resolve it before
	// returning a cross-repository request.
	headRepoID int
}

// PRLister is implemented by forge adapters that can enumerate requests. It is
// deliberately separate from Forge, following RepoPublisher: an adapter that
// cannot do this should be absent from the capability rather than carry a stub
// that always errors.
//
// The two methods exist because the two provider surfaces genuinely differ in
// cost and in what they can report; one method would have to lie about one of
// them.
type PRLister interface {
	// ListAccountPRs enumerates open requests across every repository the
	// authenticated user can see. Cheap and field-poor.
	ListAccountPRs(ctx context.Context, q PRQuery) ([]PullRequest, error)
	// ListRepoPRs enumerates requests for one owner/name repository, including
	// the head branch and review state an inbox cannot get from the search
	// surface. Inbox queries cost up to one call per requested role and
	// repository; AnyRole costs one.
	ListRepoPRs(ctx context.Context, repo string, q PRQuery) ([]PullRequest, error)
}

// ListAccountPRs runs f's account-wide listing, reporting a clear refusal when
// the adapter does not support one.
func ListAccountPRs(ctx context.Context, f Forge, q PRQuery) ([]PullRequest, error) {
	lister, ok := f.(PRLister)
	if !ok {
		return nil, &ErrUnsupported{Kind: f.Kind(), Operation: "pull request inventory"}
	}
	return lister.ListAccountPRs(ctx, q)
}

// ListRepoPRs runs f's per-repository listing.
func ListRepoPRs(ctx context.Context, f Forge, repo string, q PRQuery) ([]PullRequest, error) {
	lister, ok := f.(PRLister)
	if !ok {
		return nil, &ErrUnsupported{Kind: f.Kind(), Operation: "pull request inventory"}
	}
	return lister.ListRepoPRs(ctx, repo, q)
}

// Key identifies a request across providers, for dedup between the account and
// per-repository surfaces.
func (p PullRequest) Key() string {
	return string(p.Forge) + "/" + strings.ToLower(p.Host) + "/" + strings.ToLower(p.Repo) + "#" + strconv.Itoa(p.Number)
}

// Roles are the only thing the per-repository surface cannot determine, so a
// merge keeps the richer row's fields and unions both rows' roles.
func MergePR(summary, full PullRequest) PullRequest {
	merged := full
	merged.Roles = unionRoles(summary.Roles, full.Roles)
	if merged.UpdatedAt.Before(summary.UpdatedAt) {
		merged.UpdatedAt = summary.UpdatedAt
	}
	if merged.Title == "" {
		merged.Title = summary.Title
	}
	if merged.Author == "" {
		merged.Author = summary.Author
	}
	return merged
}

func unionRoles(a, b []PRRole) []PRRole {
	seen := map[PRRole]bool{}
	for _, role := range append(append([]PRRole{}, a...), b...) {
		seen[role] = true
	}
	out := make([]PRRole, 0, len(seen))
	for role := range seen {
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// EffectiveRoles resolves the query's roles, defaulting to the full inbox.
func (q PRQuery) EffectiveRoles() []PRRole {
	if len(q.Roles) == 0 {
		return []PRRole{RoleAuthor, RoleReviewer}
	}
	return q.Roles
}

// EffectiveState resolves the query's state, defaulting to open.
func (q PRQuery) EffectiveState() PRState {
	if q.State == "" {
		return PRStateOpen
	}
	return q.State
}

// EffectiveLimit resolves the per-request cap.
func (q PRQuery) EffectiveLimit() int {
	if q.Limit <= 0 {
		return 200
	}
	return q.Limit
}

// Check outcome vocabulary reported in PullRequest.Checks.
const (
	ChecksPassing = "passing"
	ChecksFailing = "failing"
	ChecksPending = "pending"
	ChecksNone    = "none"
)

// checkOutcome is one entry of a provider's check rollup. GitHub mixes two
// shapes in the same array: check runs carry Status plus Conclusion, while
// legacy commit statuses carry only State.
type checkOutcome struct {
	Status     string
	Conclusion string
	State      string
}

// foldChecks reduces a rollup to one word. Failure dominates pending, which
// dominates success, because the summary exists to answer "is anything wrong"
// rather than "how much passed".
func foldChecks(checks []checkOutcome) string {
	var passed, failed, pending int
	for _, c := range checks {
		switch classifyCheck(c) {
		case ChecksFailing:
			failed++
		case ChecksPending:
			pending++
		case ChecksPassing:
			passed++
		}
	}
	switch {
	case failed > 0:
		return ChecksFailing
	case pending > 0:
		return ChecksPending
	case passed > 0:
		return ChecksPassing
	default:
		return ChecksNone
	}
}

func classifyCheck(c checkOutcome) string {
	// A commit status has no Status/Conclusion pair, only State.
	if c.Status == "" && c.Conclusion == "" {
		switch strings.ToUpper(c.State) {
		case "SUCCESS":
			return ChecksPassing
		case "FAILURE", "ERROR":
			return ChecksFailing
		case "PENDING", "EXPECTED":
			return ChecksPending
		}
		return ""
	}
	// A check run that has not completed is pending whatever it may conclude.
	if strings.ToUpper(c.Status) != "COMPLETED" {
		return ChecksPending
	}
	switch strings.ToUpper(c.Conclusion) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return ChecksPassing
	case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return ChecksFailing
	}
	return ""
}
