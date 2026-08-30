// Package scaffold loads and resolves repository scaffold presets.
//
// It deliberately stops at an inspectable plan: callers own interaction,
// external skill installation, hook execution, Git operations, and remote
// creation. File templates are the one operation this package can apply
// directly, because keeping their path validation next to planning prevents a
// caller from accidentally weakening the traversal and symlink checks.
package scaffold

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

const (
	// CurrentVersion is the only scaffolds.toml schema understood by this
	// release. Every user-authored source must declare it explicitly.
	CurrentVersion = 1
)

// Config is the merged scaffold catalog. Sources are ordered from least to
// most specific; it is useful when explaining effective configuration.
type Config struct {
	Version       int               `toml:"version" json:"version"`
	DefaultPreset string            `toml:"default_preset" json:"default_preset"`
	DefaultAgents []string          `toml:"default_agents" json:"default_agents,omitempty"`
	Presets       map[string]Preset `toml:"presets" json:"presets"`
	Sources       []string          `toml:"-" json:"sources,omitempty"`

	defaultAgentsSet bool
}

// Preset is one repository setup recipe. Pointer booleans distinguish an
// omitted child value (inherit) from an explicit false value.
type Preset struct {
	Extends        string   `toml:"extends" json:"extends,omitempty"`
	Description    string   `toml:"description" json:"description,omitempty"`
	Readme         *bool    `toml:"readme" json:"readme,omitempty"`
	Gitignore      []string `toml:"gitignore" json:"gitignore,omitempty"`
	ClaudePlans    *bool    `toml:"claude_plans" json:"claude_plans,omitempty"`
	AgentContract  string   `toml:"agent_contract" json:"agent_contract,omitempty"`
	License        string   `toml:"license" json:"license,omitempty"`
	Remote         string   `toml:"remote" json:"remote,omitempty"`
	Handoff        string   `toml:"handoff" json:"handoff,omitempty"`
	Template       string   `toml:"template" json:"template,omitempty"`
	TemplateRef    string   `toml:"template_ref" json:"template_ref,omitempty"`
	TemplateSubdir string   `toml:"template_subdir" json:"template_subdir,omitempty"`

	InitialBranch  string `toml:"initial_branch" json:"initial_branch,omitempty"`
	InitialCheckIn string `toml:"initial_check_in" json:"initial_check_in,omitempty"`
	InitialCommit  *bool  `toml:"initial_commit" json:"initial_commit,omitempty"`
	CommitMessage  string `toml:"commit_message" json:"commit_message,omitempty"`

	Inputs  []Input        `toml:"inputs" json:"inputs,omitempty"`
	Files   []File         `toml:"files" json:"files,omitempty"`
	Hooks   []Hook         `toml:"hooks" json:"hooks,omitempty"`
	Skills  []Skill        `toml:"skills" json:"skills,omitempty"`
	Catalog []SkillCatalog `toml:"catalog" json:"catalog,omitempty"`

	Origin string `toml:"-" json:"origin,omitempty"`
}

// InputType is a value a preset can request from an interactive or scripted
// caller. The scaffold package validates values but never prompts for them.
type InputType string

const (
	InputString InputType = "string"
	InputBool   InputType = "bool"
	InputChoice InputType = "choice"
)

// Input declares a typed template input. Default is intentionally any because
// TOML booleans and strings must retain their native types until validation.
type Input struct {
	ID          string    `toml:"id" json:"id"`
	Type        InputType `toml:"type" json:"type"`
	Label       string    `toml:"label" json:"label,omitempty"`
	Description string    `toml:"description" json:"description,omitempty"`
	Required    *bool     `toml:"required" json:"required,omitempty"`
	Default     any       `toml:"default" json:"default,omitempty"`
	Choices     []string  `toml:"choices" json:"choices,omitempty"`
	Origin      string    `toml:"-" json:"origin,omitempty"`
}

// IsRequired reports the effective required flag.
func (i Input) IsRequired() bool { return i.Required != nil && *i.Required }

// File is a text template rendered into the repository. Exactly one of Source
// and Content must be supplied after inheritance is resolved. Source is always
// relative to a templates/ directory beside its scaffolds.toml source.
type File struct {
	ID          string  `toml:"id" json:"id"`
	Destination string  `toml:"destination" json:"destination"`
	Source      string  `toml:"source" json:"source,omitempty"`
	Content     *string `toml:"content" json:"content,omitempty"`
	Mode        string  `toml:"mode" json:"mode,omitempty"`
	Enabled     *bool   `toml:"enabled" json:"enabled,omitempty"`

	Origin         string `toml:"-" json:"origin,omitempty"`
	TemplateOrigin string `toml:"-" json:"template_origin,omitempty"`
}

// FileMode returns the configured permission bits, defaulting to 0644.
func (f File) FileMode() (fs.FileMode, error) {
	if f.Mode == "" {
		return 0o644, nil
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(f.Mode, "0o"), 8, 32)
	if err != nil || v > 0o777 {
		return 0, fmt.Errorf("file %q mode %q: want octal permissions such as 0644", f.ID, f.Mode)
	}
	return fs.FileMode(v), nil
}

// HookPhase fixes execution ordering without making the scaffold package an
// executor.
type HookPhase string

const (
	BeforeCommit HookPhase = "before_commit"
	AfterCommit  HookPhase = "after_commit"
	AfterRemote  HookPhase = "after_remote"
)

// Hook describes an external command for a caller to run. Command is the safe
// argv form; Run is an explicitly requested shell expression. They are
// mutually exclusive.
type Hook struct {
	ID          string    `toml:"id" json:"id"`
	Phase       HookPhase `toml:"phase" json:"phase"`
	Command     []string  `toml:"command" json:"command,omitempty"`
	Run         string    `toml:"run" json:"run,omitempty"`
	Interactive *bool     `toml:"interactive" json:"interactive,omitempty"`
	Required    *bool     `toml:"required" json:"required,omitempty"`
	Timeout     Duration  `toml:"timeout" json:"timeout,omitempty"`
	Enabled     *bool     `toml:"enabled" json:"enabled,omitempty"`
	Origin      string    `toml:"-" json:"origin,omitempty"`
}

// IsInteractive reports whether a shell hook explicitly requests an
// interactive shell.
func (h Hook) IsInteractive() bool { return h.Interactive != nil && *h.Interactive }

// IsRequired reports whether failure must stop the surrounding setup flow.
func (h Hook) IsRequired() bool { return h.Required != nil && *h.Required }

// Skill describes one optional skill installation plus its declared setup
// entrypoint. The package plans these records but never downloads or runs them.
type Skill struct {
	ID      string      `toml:"id" json:"id"`
	Source  string      `toml:"source" json:"source"`
	Name    string      `toml:"name" json:"name"`
	Agents  []string    `toml:"agents" json:"agents,omitempty"`
	Default *bool       `toml:"default" json:"default,omitempty"`
	Enabled *bool       `toml:"enabled" json:"enabled,omitempty"`
	Setup   *SkillSetup `toml:"setup" json:"setup,omitempty"`
	Origin  string      `toml:"-" json:"origin,omitempty"`
}

// IsDefault reports whether the skill is selected without a caller override.
func (s Skill) IsDefault() bool { return s.Default != nil && *s.Default }

// SkillSetup is an entrypoint relative to the installed skill directory.
// Confinement to that directory belongs to the executor, which alone knows
// where the provider installed it.
type SkillSetup struct {
	Phase       HookPhase `toml:"phase" json:"phase"`
	Interpreter string    `toml:"interpreter" json:"interpreter,omitempty"`
	Script      string    `toml:"script" json:"script,omitempty"`
	Builtin     string    `toml:"builtin,omitempty" json:"builtin,omitempty"`
	Args        []string  `toml:"args" json:"args,omitempty"`
	Required    *bool     `toml:"required" json:"required,omitempty"`
	Timeout     Duration  `toml:"timeout" json:"timeout,omitempty"`
}

// IsRequired reports whether setup failure must stop the surrounding flow.
func (s SkillSetup) IsRequired() bool { return s.Required != nil && *s.Required }

// SkillCatalog is the prompt-neutral metadata from which a CLI can construct
// a skill picker. A matching Skill record carries installation details.
type SkillCatalog struct {
	ID          string `toml:"id" json:"id"`
	Source      string `toml:"source" json:"source"`
	Label       string `toml:"label" json:"label"`
	Description string `toml:"description" json:"description,omitempty"`
	Default     *bool  `toml:"default" json:"default,omitempty"`
	Enabled     *bool  `toml:"enabled" json:"enabled,omitempty"`
	Origin      string `toml:"-" json:"origin,omitempty"`
}

// IsDefault reports whether the catalog item is selected without a caller
// override.
func (c SkillCatalog) IsDefault() bool { return c.Default != nil && *c.Default }

// Duration is a TOML duration string. Its zero value means no package-level
// timeout; execution policy remains with the caller.
type Duration struct{ time.Duration }

// UnmarshalTOML implements toml.Unmarshaler.
func (d *Duration) UnmarshalTOML(v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("duration: want a string such as \"5m\", got %T", v)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalTOML implements toml.Marshaler.
func (d Duration) MarshalTOML() ([]byte, error) {
	return []byte(strconv.Quote(d.Duration.String())), nil
}

// MarshalJSON keeps inspectable plans readable instead of exposing
// time.Duration's nanosecond integer representation.
func (d Duration) MarshalJSON() ([]byte, error) {
	if d.Duration == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(d.Duration.String())
}

func boolp(v bool) *bool       { return &v }
func stringp(v string) *string { return &v }
