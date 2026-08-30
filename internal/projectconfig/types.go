// Package projectconfig loads the small, deliberately constrained part of
// dev configuration that a repository is allowed to carry with its code.
// Host policy remains in the user's global config; project files may only
// describe worktree provisioning and defaults for setting up that repository.
package projectconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const (
	// DirectoryName is the fixed repository-local configuration directory.
	DirectoryName = ".dev-cli"
	// ConfigFilename is the allowlisted project override file.
	ConfigFilename = "config.toml"
	// ScaffoldsFilename is the repository-local scaffold overlay file.
	ScaffoldsFilename = "scaffolds.toml"

	// ConfigRelativePath and ScaffoldsRelativePath are slash-separated names
	// suitable for help and diagnostics. Use ResolvePaths for filesystem work.
	ConfigRelativePath    = ".dev-cli/config.toml"
	ScaffoldsRelativePath = ".dev-cli/scaffolds.toml"

	// LegacySource is used when callers supply an old .dev.toml layer without
	// an explicit source name.
	LegacySource = ".dev.toml"
)

// FilePaths are the fixed project configuration paths below one canonical
// repository root.
type FilePaths struct {
	Root      string
	Config    string
	Scaffolds string
}

// ResolvePaths canonicalizes repoRoot and proves both fixed files remain below
// it. This also rejects a .dev-cli directory or file symlink that escapes the
// repository.
func ResolvePaths(repoRoot string) (FilePaths, error) {
	root, err := pathx.Canonical(repoRoot)
	if err != nil {
		return FilePaths{}, fmt.Errorf("canonicalize repository root: %w", err)
	}
	configPath, err := pathx.CanonicalChild(root, filepath.Join(root, DirectoryName, ConfigFilename))
	if err != nil {
		return FilePaths{}, fmt.Errorf("resolve %s: %w", ConfigRelativePath, err)
	}
	scaffoldsPath, err := pathx.CanonicalChild(root, filepath.Join(root, DirectoryName, ScaffoldsFilename))
	if err != nil {
		return FilePaths{}, fmt.Errorf("resolve %s: %w", ScaffoldsRelativePath, err)
	}
	return FilePaths{Root: root, Config: configPath, Scaffolds: scaffoldsPath}, nil
}

// ConfigPath returns the canonical fixed project config path.
func ConfigPath(repoRoot string) (string, error) {
	paths, err := ResolvePaths(repoRoot)
	return paths.Config, err
}

// ScaffoldsPath returns the canonical fixed project scaffold path.
func ScaffoldsPath(repoRoot string) (string, error) {
	paths, err := ResolvePaths(repoRoot)
	return paths.Scaffolds, err
}

// Override is the complete project-owned configuration surface. Pointer
// fields distinguish an omitted value from an explicit empty list or false.
type Override struct {
	Worktree WorktreeOverride `toml:"worktree"`
	Repo     RepoOverride     `toml:"repo"`
}

// WorktreeOverride contains only provisioning settings. Placement and other
// host policy intentionally do not appear here.
type WorktreeOverride struct {
	Include          *[]string          `toml:"include"`
	Link             *[]string          `toml:"link"`
	PostCreate       *config.PostCreate `toml:"post_create"`
	Strategy         *string            `toml:"strategy"`
	Strategies       *map[string]string `toml:"strategies"`
	ProvisionTimeout *config.Duration   `toml:"provision_timeout"`
}

// RepoOverride groups repository bootstrap defaults.
type RepoOverride struct {
	Setup SetupOverride `toml:"setup"`
}

// SetupOverride changes wizard defaults only. It cannot silently publish a
// repository or select a host runtime backend.
type SetupOverride struct {
	Preset  *string `toml:"preset"`
	Handoff *string `toml:"handoff"`
	Commit  *bool   `toml:"commit"`
}

// Layer is a legacy project override supplied by the caller. Load applies it
// first, then overlays .dev-cli/config.toml when that file exists.
type Layer struct {
	Source   string
	Override Override
}

// DiagnosticKind classifies ignored repository-owned configuration.
type DiagnosticKind string

const (
	// DiagnosticDenied marks a known global/host section that projects may not
	// override, such as runtime or paths.
	DiagnosticDenied DiagnosticKind = "denied"
	// DiagnosticUnknown marks an unrecognized key, usually a typo or a field
	// from a newer dev version.
	DiagnosticUnknown DiagnosticKind = "unknown"
)

// Diagnostic reports an ignored key without echoing its value.
type Diagnostic struct {
	Kind    DiagnosticKind
	Source  string
	Key     string
	Message string
}

// Result is the non-mutating outcome of loading the two fixed project files.
type Result struct {
	Paths FilePaths

	// Effective is legacy data overlaid by the new project config.
	Effective Override
	// Project is only the parsed .dev-cli/config.toml layer.
	Project Override
	// Sources records the winning source for each effective leaf key.
	Sources map[string]string
	// Layers lists applied sources from lowest to highest precedence.
	Layers []string

	ConfigPresent    bool
	ScaffoldsPresent bool
	Diagnostics      []Diagnostic

	// ExecutionHash is empty when the project supplied no executable settings.
	// Otherwise it is a domain-separated SHA-256 digest of project-owned
	// post-create commands and scaffold hooks/skill setup.
	ExecutionHash string
}

// SourceFor returns the winning source for a dotted effective key.
func (r Result) SourceFor(key string) (string, bool) {
	source, ok := r.Sources[key]
	return source, ok
}

// RequiresTrust reports whether project-owned configuration can execute code.
func (r Result) RequiresTrust() bool { return r.ExecutionHash != "" }

func (o Override) validate() error {
	w := o.Worktree
	validStrategy := func(value string) bool {
		switch value {
		case "reinstall", "copy", "link", "skip":
			return true
		default:
			return false
		}
	}
	if w.Strategy != nil && !validStrategy(*w.Strategy) {
		return fmt.Errorf("worktree.strategy %q: want reinstall, copy, link or skip", *w.Strategy)
	}
	if w.Strategies != nil {
		for ecosystem, strategy := range *w.Strategies {
			if strings.TrimSpace(ecosystem) == "" {
				return fmt.Errorf("worktree.strategies contains an empty ecosystem name")
			}
			if !validStrategy(strategy) {
				return fmt.Errorf("worktree.strategies.%s %q: want reinstall, copy, link or skip", ecosystem, strategy)
			}
		}
	}
	if w.ProvisionTimeout != nil && w.ProvisionTimeout.Duration <= 0 {
		return fmt.Errorf("worktree.provision_timeout must be positive")
	}
	if w.Link != nil {
		for index, value := range *w.Link {
			if err := validateRelativePath(value); err != nil {
				return fmt.Errorf("worktree.link[%d]: %w", index, err)
			}
		}
	}
	setup := o.Repo.Setup
	if setup.Preset != nil && strings.TrimSpace(*setup.Preset) == "" {
		return fmt.Errorf("repo.setup.preset must not be empty")
	}
	if setup.Handoff != nil {
		switch *setup.Handoff {
		case "stay", "cd", "open", "start":
		default:
			return fmt.Errorf("repo.setup.handoff %q: want stay, cd, open or start", *setup.Handoff)
		}
	}
	return nil
}

func validateRelativePath(value string) error {
	if value == "" {
		return fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return fmt.Errorf("path %q must be repository-relative", value)
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("path %q contains parent traversal", value)
		}
	}
	if filepath.Clean(value) == "." {
		return fmt.Errorf("path %q names the repository root", value)
	}
	return nil
}
