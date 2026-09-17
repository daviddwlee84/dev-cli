package taskflow

import (
	"context"
	"fmt"
	"strings"
)

// BaseKind names how a containment base resolved.
type BaseKind string

const (
	BaseLocalBranch    BaseKind = "local-branch"
	BaseRemoteTracking BaseKind = "remote-tracking"
	BaseCommit         BaseKind = "commit"
)

// baseResolution is the exact containment base observed for a destructive
// plan. Apply re-resolves the same input and requires the same kind, ref and
// commit, so a branch created with the name of a recorded commit makes the
// plan stale instead of silently changing the proof.
type baseResolution struct {
	input  string
	ref    string
	kind   BaseKind
	exists bool
	oid    string
	err    error
}

// resolveBaseRef resolves a recorded or explicitly selected base in a fixed
// order: exact local branch, remote-tracking branch, then any commit-ish such
// as a recorded fork-point OID. A probe failure stops resolution rather than
// falling through to a weaker kind.
func (s *lifecycleService) resolveBaseRef(ctx context.Context, repoPath, input string) baseResolution {
	resolved := baseResolution{input: input}
	if input == "" || repoPath == "" {
		return resolved
	}
	if input != strings.TrimSpace(input) || strings.ContainsRune(input, '\x00') || strings.HasPrefix(input, "-") {
		resolved.err = fmt.Errorf("base %q is not a normalized ref or commit", input)
		return resolved
	}
	type candidate struct {
		ref  string
		kind BaseKind
	}
	var candidates []candidate
	switch {
	case strings.HasPrefix(input, "refs/heads/"):
		candidates = []candidate{{input, BaseLocalBranch}}
	case strings.HasPrefix(input, "refs/remotes/"):
		candidates = []candidate{{input, BaseRemoteTracking}}
	default:
		candidates = []candidate{
			{"refs/heads/" + input, BaseLocalBranch},
			{"refs/remotes/" + input, BaseRemoteTracking},
			{input, BaseCommit},
		}
	}
	for _, next := range candidates {
		exists, err := s.gitRefState(ctx, repoPath, next.ref)
		if err != nil {
			resolved.err = err
			return resolved
		}
		if !exists {
			continue
		}
		resolved.ref, resolved.kind, resolved.exists = next.ref, next.kind, true
		resolved.oid, resolved.err = s.resolveRefOID(ctx, repoPath, next.ref)
		return resolved
	}
	return resolved
}

func (b baseResolution) authority() string {
	return authorityHash("taskflow-base-resolution-v1",
		b.input, b.ref, string(b.kind), boolString(b.exists), b.oid, errorString(b.err))
}

// describe renders the resolved base for condition evidence.
func (b baseResolution) describe() string {
	switch b.kind {
	case BaseLocalBranch:
		return fmt.Sprintf("local branch %s at %s", b.ref, b.oid)
	case BaseRemoteTracking:
		return fmt.Sprintf("remote-tracking branch %s at %s", b.ref, b.oid)
	case BaseCommit:
		return fmt.Sprintf("commit %s", b.oid)
	default:
		return "an unresolved ref"
	}
}

// aliasesBranch reports whether the base names the task branch itself, so
// branch deletion never removes the base it is proved against.
func (b baseResolution) aliasesBranch(branch string) bool {
	if branch == "" {
		return false
	}
	local := "refs/heads/" + branch
	return b.input == branch || b.input == "heads/"+branch || b.input == local ||
		(b.kind == BaseLocalBranch && b.ref == local)
}
