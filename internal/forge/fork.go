package forge

import (
	"context"
	"errors"
)

// RepoForker is optional: providers without a fork workflow retain the existing
// Forge interface. Planning only reads provider state; applying never changes
// a local checkout, adds a remote, or pushes a branch.
type RepoForker interface {
	PlanFork(context.Context, string) (ForkPlan, error)
	ApplyFork(context.Context, ForkPlan) (ForkResult, error)
}

// ForkRepository is a verified repository identity. An absent prospective fork
// has canonical URLs but ID zero; only Exists or a successful Apply confirms it.
type ForkRepository struct {
	ID            int64  `json:"id"`
	Host          string `json:"host"`
	FullName      string `json:"full_name"`
	URL           string `json:"url"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	DefaultBranch string `json:"default_branch"`
	Owner         string `json:"owner"`
	IsFork        bool   `json:"is_fork"`
	NetworkID     int64  `json:"network_id"`
}

// ForkPlan binds the authenticated account and both repository identities.
// When the requested source is already the user's fork, Source is its parent.
type ForkPlan struct {
	Source ForkRepository `json:"source"`
	Fork   ForkRepository `json:"fork"`
	Owner  string         `json:"owner"`
	Exists bool           `json:"exists"`
}

// ForkResult retains provider observations even if a later stage fails.
// Unknown means a create was attempted but its outcome could not be verified.
// Created is only true after successful creation and structured verification;
// Reused describes a fork verified before any create attempt.
type ForkResult struct {
	Plan    ForkPlan `json:"plan"`
	Created bool     `json:"created"`
	Reused  bool     `json:"reused"`
	Unknown bool     `json:"unknown"`
}

var ErrForkPlanChanged = errors.New("fork account or repository changed since planning; plan the operation again")

func PlanFork(ctx context.Context, f Forge, source string) (ForkPlan, error) {
	forker, ok := f.(RepoForker)
	if !ok {
		return ForkPlan{}, &ErrUnsupported{Kind: f.Kind(), Operation: "repository forking"}
	}
	return forker.PlanFork(ctx, source)
}

func ApplyFork(ctx context.Context, f Forge, plan ForkPlan) (ForkResult, error) {
	forker, ok := f.(RepoForker)
	if !ok {
		return ForkResult{}, &ErrUnsupported{Kind: f.Kind(), Operation: "repository forking"}
	}
	return forker.ApplyFork(ctx, plan)
}
