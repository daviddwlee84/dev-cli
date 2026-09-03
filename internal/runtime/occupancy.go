package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// OccupancyProfile selects the policy applied to recognized-agent evidence.
// Session, pane, and activity observations are retained identically in every
// profile; only Blocking changes.
type OccupancyProfile uint8

const (
	// OccupancyStrict is for claiming a checkout for writing or adoption. Every
	// recognized agent except the current caller is blocking, regardless of state.
	// It is the zero value so an omitted profile fails toward exclusive ownership.
	OccupancyStrict OccupancyProfile = iota
	// OccupancyCleanup permits closeable idle and done agents. Unknown activity
	// remains blocking unless CloseUnknown is set.
	OccupancyCleanup
)

// OccupancyOptions supplies live caller identity and action-specific policy.
// CallerWorkspaceID and CallerPaneID must describe the invoking runtime, not a
// handle remembered in durable task state.
type OccupancyOptions struct {
	Profile           OccupancyProfile
	CallerWorkspaceID string
	CallerPaneID      string
	CloseUnknown      bool
}

// OccupancyObservation distinguishes an observed empty inventory from evidence
// that was unsupported, unattempted, or failed.
type OccupancyObservation struct {
	Supported bool
	Attempted bool
	Err       error
}

// Observed reports whether the observation completed successfully.
func (o OccupancyObservation) Observed() bool {
	return o.Supported && o.Attempted && o.Err == nil
}

// Unknown reports whether this evidence was not successfully observed.
func (o OccupancyObservation) Unknown() bool { return !o.Observed() }

// OccupancySession is one live session whose panes cover the target checkout.
type OccupancySession struct {
	Runtime  Session
	Panes    []Pane
	Mixed    []Pane
	IsCaller bool
}

// OccupancyAgent is one recognized agent whose cwd covers the target checkout.
type OccupancyAgent struct {
	Activity      AgentActivity
	Status        string
	SessionHandle string
	IsCaller      bool
	Blocking      bool
}

// Occupancy is structured, read-only evidence about one checkout. A remembered
// runtime handle is deliberately absent: Sessions and Agents are selected only
// from fresh canonical checkout coverage.
type Occupancy struct {
	Target               string
	Backend              string
	Profile              OccupancyProfile
	CallerWorkspaceID    string
	ReportedCallerPaneID string
	CallerPaneID         string
	SessionList          OccupancyObservation
	AgentActivityList    OccupancyObservation
	CurrentPane          OccupancyObservation
	SessionCoverageErr   error
	Sessions             []OccupancySession
	Agents               []OccupancyAgent
}

// InspectOccupancy collects independent runtime, recognized-agent, and caller
// evidence for target. Initial target canonicalization errors are returned;
// observation failures are retained in their corresponding fields so each
// action can apply its own fail-closed acknowledgement policy.
func InspectOccupancy(ctx context.Context, rt Runtime, target string, opts OccupancyOptions) (Occupancy, error) {
	checkout, err := inspectCheckoutIdentity(ctx, target)
	if err != nil {
		return Occupancy{}, fmt.Errorf("canonicalize occupancy target: %w", err)
	}
	result := Occupancy{
		Target:               checkout.path,
		Backend:              "none",
		Profile:              opts.Profile,
		CallerWorkspaceID:    opts.CallerWorkspaceID,
		ReportedCallerPaneID: opts.CallerPaneID,
		CallerPaneID:         opts.CallerPaneID,
	}
	if rt == nil {
		return result, nil
	}
	result.Backend = rt.Name()
	if result.Backend == "none" {
		return result, nil
	}

	result.SessionList.Supported = true
	resolver, resolvesCurrentPane := rt.(CurrentPaneResolver)
	result.CurrentPane.Supported = resolvesCurrentPane
	if resolvesCurrentPane && opts.CallerPaneID != "" {
		result.CurrentPane.Attempted = true
		result.CallerPaneID = ""
		paneID, resolveErr := resolver.CurrentPaneID(ctx)
		if resolveErr != nil {
			result.CurrentPane.Err = resolveErr
		} else if paneID == "" {
			result.CurrentPane.Err = fmt.Errorf("current pane resolver returned an empty pane id")
		} else {
			result.CallerPaneID = paneID
		}
	}

	result.SessionList.Attempted = true
	sessions, listErr := rt.List(ctx)
	if listErr != nil {
		result.SessionList.Err = listErr
	} else {
		for _, observed := range sessions {
			covering, mixed, classifyErr := classifyOccupancySession(ctx, checkout, observed, opts.Profile)
			if classifyErr != nil {
				result.SessionCoverageErr = classifyErr
				result.Sessions = nil
				break
			}
			if len(covering) == 0 {
				continue
			}
			result.Sessions = append(result.Sessions, OccupancySession{
				Runtime: observed,
				Panes:   covering,
				Mixed:   mixed,
				IsCaller: observed.Handle == opts.CallerWorkspaceID ||
					occupancyPanePresent(observed.Panes, result.CallerPaneID),
			})
		}
	}

	lister, listsAgents := rt.(AgentActivityLister)
	result.AgentActivityList.Supported = listsAgents
	if !listsAgents {
		// Some backends can identify an agent on a pane without providing the
		// richer recognized-agent inventory. Preserve that live evidence as the
		// strict-profile fallback rather than treating an unsupported lister as an
		// observed empty list.
		for _, session := range result.Sessions {
			for _, pane := range session.Panes {
				if pane.Agent == "" && pane.AgentSession == "" {
					continue
				}
				cwd := pane.CWD
				if cwd == "" {
					cwd = pane.ShellCWD
				}
				activity := AgentActivity{
					PaneID: pane.ID, WorkspaceID: session.Runtime.Handle,
					Agent: pane.Agent, Status: pane.AgentStatus, CWD: cwd,
				}
				if coverageErr := appendOccupancyAgent(ctx, &result, checkout, opts, activity); coverageErr != nil {
					result.SessionCoverageErr = coverageErr
					result.Agents = nil
					return result, nil
				}
			}
		}
		return result, nil
	}
	result.AgentActivityList.Attempted = true
	activities, activityErr := lister.AgentActivities(ctx)
	if activityErr != nil {
		result.AgentActivityList.Err = activityErr
		return result, nil
	}
	for _, activity := range activities {
		if coverageErr := appendOccupancyAgent(ctx, &result, checkout, opts, activity); coverageErr != nil {
			result.AgentActivityList.Err = coverageErr
			result.Agents = nil
			return result, nil
		}
	}
	return result, nil
}

func appendOccupancyAgent(ctx context.Context, result *Occupancy, checkout checkoutIdentity, opts OccupancyOptions, activity AgentActivity) error {
	covered, err := activityCoversCheckout(ctx, checkout, activity.CWD, opts.Profile)
	if err != nil {
		return fmt.Errorf("classify recognized agent pane %s: %w", activity.PaneID, err)
	}
	if !covered {
		return nil
	}
	status := normalizeOccupancyStatus(activity.Status)
	caller := result.CallerPaneID != "" && activity.PaneID == result.CallerPaneID
	handle := correlatedSession(result.Sessions, activity)
	result.Agents = append(result.Agents, OccupancyAgent{
		Activity:      activity,
		Status:        status,
		SessionHandle: handle,
		IsCaller:      caller,
		Blocking:      occupancyAgentBlocks(opts, status, caller, handle != ""),
	})
	return nil
}

type checkoutIdentity struct {
	path   string
	git    bool
	linked bool
}

func inspectCheckoutIdentity(ctx context.Context, dir string) (checkoutIdentity, error) {
	canonical, err := pathx.Canonical(dir)
	if err != nil {
		return checkoutIdentity{}, err
	}
	repo, discoverErr := gitx.Discover(ctx, canonical)
	if discoverErr == nil && repo.Root != "" {
		root, canonicalErr := pathx.Canonical(repo.Root)
		if canonicalErr != nil {
			return checkoutIdentity{}, canonicalErr
		}
		return checkoutIdentity{path: root, git: true, linked: repo.IsLinkedWorktree}, nil
	}
	if discoverErr != nil && !errors.Is(discoverErr, gitx.ErrNotARepo) {
		return checkoutIdentity{}, discoverErr
	}
	return checkoutIdentity{path: canonical}, nil
}

func activityCoversCheckout(ctx context.Context, target checkoutIdentity, cwd string, profile OccupancyProfile) (bool, error) {
	if cwd == "" {
		return false, fmt.Errorf("recognized agent has no cwd")
	}
	activity, err := inspectCheckoutIdentity(ctx, cwd)
	if err != nil {
		return false, err
	}
	if target.git && activity.git {
		if target.path == activity.path {
			return true, nil
		}
		if profile == OccupancyCleanup && target.linked {
			return pathx.Contains(target.path, activity.path)
		}
		return false, nil
	}
	return pathx.Contains(target.path, activity.path)
}

func classifyOccupancySession(ctx context.Context, target checkoutIdentity, session Session, profile OccupancyProfile) ([]Pane, []Pane, error) {
	panes := session.Panes
	if len(panes) == 0 {
		for i, dir := range session.Dirs {
			panes = append(panes, Pane{ID: fmt.Sprintf("%s:%d", session.Handle, i), CWD: dir})
		}
	}
	var covering, mixed []Pane
	for _, pane := range panes {
		paths := []string{pane.CWD}
		if pane.ShellCWD != "" && pane.ShellCWD != pane.CWD {
			paths = append(paths, pane.ShellCWD)
		}
		paneCovers, paneOutside, observed := false, false, false
		for _, path := range paths {
			if path == "" {
				continue
			}
			observed = true
			inside, err := activityCoversCheckout(ctx, target, path, profile)
			if err != nil {
				return nil, nil, fmt.Errorf("compare runtime pane %s path: %w", pane.ID, err)
			}
			if inside {
				paneCovers = true
			} else {
				paneOutside = true
			}
		}
		if paneCovers {
			covering = append(covering, pane)
		}
		if paneOutside || !observed {
			mixed = append(mixed, pane)
		}
	}
	return covering, mixed, nil
}

func occupancyPanePresent(panes []Pane, id string) bool {
	if id == "" {
		return false
	}
	for _, pane := range panes {
		if pane.ID == id {
			return true
		}
	}
	return false
}

func correlatedSession(sessions []OccupancySession, activity AgentActivity) string {
	for _, session := range sessions {
		if activity.WorkspaceID != "" && session.Runtime.Handle == activity.WorkspaceID {
			return session.Runtime.Handle
		}
		if occupancyPanePresent(session.Runtime.Panes, activity.PaneID) {
			return session.Runtime.Handle
		}
	}
	return ""
}

func normalizeOccupancyStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return "unknown"
	}
	return status
}

func occupancyAgentBlocks(opts OccupancyOptions, status string, caller, correlated bool) bool {
	if opts.Profile != OccupancyCleanup {
		return !caller
	}
	// Cleanup can never close the caller or an activity that does not correlate
	// with a freshly listed covering session.
	if caller || !correlated {
		return true
	}
	switch status {
	case "idle", "done":
		return false
	case "unknown":
		return !opts.CloseUnknown
	default:
		return true
	}
}
