package repo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// AcquireKind identifies how a canonical repository checkout is obtained.
type AcquireKind string

const (
	AcquireNew   AcquireKind = "new"
	AcquireClone AcquireKind = "clone"
)

// AcquireRequest describes the filesystem/Git part of creating a repository.
// Higher-level scaffolding, commits, remote publication and runtime handoff are
// deliberately owned by callers so the same acquisition path can serve the
// interactive wizard and non-interactive commands.
type AcquireRequest struct {
	Kind          AcquireKind
	Name          string
	CloneRef      string
	Destination   string
	InitialBranch string
}

// AcquireResult is a local checkout ready for scaffolding or handoff.
type AcquireResult struct {
	Kind      AcquireKind `json:"kind"`
	Name      string      `json:"name"`
	Path      string      `json:"path"`
	CloneRef  string      `json:"clone_ref,omitempty"`
	Created   bool        `json:"created"`
	Cloned    bool        `json:"cloned"`
	GitInited bool        `json:"git_initialized"`
}

// Acquire creates a new main-branch repository or clones an existing one into
// an exact, previously absent destination. It refuses nested repositories: a
// caller that intentionally wants a submodule should use Git's submodule flow,
// where ownership and checkout semantics are explicit.
func Acquire(ctx context.Context, request AcquireRequest) (AcquireResult, error) {
	var result AcquireResult
	request.Name = strings.TrimSpace(request.Name)
	request.CloneRef = strings.TrimSpace(request.CloneRef)
	request.Destination = strings.TrimSpace(request.Destination)
	if request.Destination == "" {
		return result, errors.New("repository destination is required")
	}
	if request.Kind != AcquireNew && request.Kind != AcquireClone {
		return result, fmt.Errorf("unknown repository acquisition kind %q", request.Kind)
	}
	if request.Kind == AcquireNew && request.Name == "" {
		return result, errors.New("repository name is required")
	}
	if request.Kind == AcquireClone && request.CloneRef == "" {
		return result, errors.New("clone reference is required")
	}

	destination, err := pathx.Canonical(request.Destination)
	if err != nil {
		return result, fmt.Errorf("resolve repository destination: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return result, fmt.Errorf("%s already exists", destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, fmt.Errorf("inspect repository destination: %w", err)
	}
	if err := rejectNestedRepository(ctx, filepath.Dir(destination)); err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return result, fmt.Errorf("create repository parent: %w", err)
	}

	result = AcquireResult{Kind: request.Kind, Name: request.Name, Path: destination}
	switch request.Kind {
	case AcquireNew:
		if err := os.Mkdir(destination, 0o755); err != nil {
			return result, fmt.Errorf("create repository directory: %w", err)
		}
		result.Created = true
		branch := strings.TrimSpace(request.InitialBranch)
		if branch == "" {
			branch = "main"
		}
		if _, err := gitx.Run(ctx, destination, "init", "-b", branch); err != nil {
			return result, fmt.Errorf("initialize repository: %w", err)
		}
		result.GitInited = true
	case AcquireClone:
		cloneRef := NormalizeCloneRef(request.CloneRef)
		result.CloneRef = RedactCloneRef(cloneRef)
		if _, err := gitx.Run(ctx, filepath.Dir(destination), "clone", cloneRef, destination); err != nil {
			return result, fmt.Errorf("clone repository: %w", RedactCloneError(err, cloneRef, request.CloneRef))
		}
		result.Created, result.Cloned, result.GitInited = true, true, true
		if result.Name == "" {
			result.Name = NameFromRef(RedactCloneRef(request.CloneRef))
		}
	}
	return result, nil
}

func rejectNestedRepository(ctx context.Context, parent string) error {
	probe := parent
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect repository destination parent: %w", err)
		}
		next := filepath.Dir(probe)
		if next == probe {
			break
		}
		probe = next
	}
	if discovered, err := gitx.Discover(ctx, probe); err == nil && discovered.Root != "" {
		return fmt.Errorf("refusing to create a nested repository inside %s", discovered.Root)
	}
	return nil
}

// NormalizeCloneRef expands forge shorthand while leaving explicit URLs, SCP
// references and local paths unchanged.
func NormalizeCloneRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ref
	}
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	if ref == "~" || strings.HasPrefix(ref, "~/") {
		return config.Expand(ref)
	}
	if strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") {
		if absolute, err := filepath.Abs(ref); err == nil {
			return absolute
		}
	}
	if strings.HasPrefix(ref, "file://") || (!strings.Contains(ref, "://") && strings.ContainsAny(ref, "@:")) {
		return ref
	}
	if info, err := os.Stat(ref); err == nil && info.IsDir() {
		if absolute, err := filepath.Abs(ref); err == nil {
			return absolute
		}
	}
	if kind := forge.FromURL(ref); kind != forge.Unknown {
		if adapter, err := forge.For(kind); err == nil {
			return adapter.CloneURL(ref)
		}
		return ref
	}
	if !strings.Contains(ref, "://") && strings.Contains(ref, "/") {
		if adapter, err := forge.Preferred(); err == nil {
			return adapter.CloneURL(ref)
		}
	}
	return ref
}

// NameFromRef derives the checkout directory name from a clone reference.
func NameFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimSuffix(strings.TrimSuffix(ref, "/"), ".git")
	if index := strings.LastIndexAny(ref, "/:\\"); index >= 0 {
		return ref[index+1:]
	}
	return ref
}
