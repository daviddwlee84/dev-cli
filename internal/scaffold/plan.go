package scaffold

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// PlanOptions are all decisions needed to turn a resolved preset into an
// execution-neutral plan.
type PlanOptions struct {
	Preset string
	Root   string
	Name   string
	Inputs map[string]any

	// Variables supplements name/path/preset/input for templates. Reserved
	// variables cannot be replaced.
	Variables map[string]any

	// Selections overrides item defaults by ID. A single value intentionally
	// applies to a matching skill and its catalog metadata.
	Selections map[string]bool
}

// Plan is everything the scaffold layer would contribute to repository
// creation. It is safe to serialize for --json or render for a confirmation
// screen before the caller mutates Git, installs skills, or runs hooks.
type Plan struct {
	Preset   string         `json:"preset"`
	Root     string         `json:"root"`
	Name     string         `json:"name"`
	Settings Preset         `json:"settings"`
	Inputs   map[string]any `json:"inputs,omitempty"`
	Files    []FilePlan     `json:"files,omitempty"`
	Hooks    []HookPlan     `json:"hooks,omitempty"`
	Skills   []SkillPlan    `json:"skills,omitempty"`
	Catalog  []CatalogPlan  `json:"catalog,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

// FilePlan contains already-rendered text and a canonical destination.
type FilePlan struct {
	ID           string      `json:"id"`
	RelativePath string      `json:"relative_path"`
	Path         string      `json:"path"`
	Content      string      `json:"content"`
	Mode         fs.FileMode `json:"mode"`
	Source       string      `json:"source,omitempty"`
	Origin       string      `json:"origin,omitempty"`
}

// HookPlan contains rendered argv or shell text but does not execute it.
type HookPlan struct {
	ID          string    `json:"id"`
	Phase       HookPhase `json:"phase"`
	Command     []string  `json:"command,omitempty"`
	Run         string    `json:"run,omitempty"`
	Interactive bool      `json:"interactive"`
	Required    bool      `json:"required"`
	Timeout     Duration  `json:"timeout,omitempty"`
	Origin      string    `json:"origin,omitempty"`
}

// SkillPlan is an installation selected by defaults or caller input.
type SkillPlan struct {
	ID     string     `json:"id"`
	Source string     `json:"source"`
	Name   string     `json:"name"`
	Agents []string   `json:"agents,omitempty"`
	Setup  *SetupPlan `json:"setup,omitempty"`
	Origin string     `json:"origin,omitempty"`
}

// SetupPlan is a rendered skill setup entrypoint for a later executor.
type SetupPlan struct {
	Phase       HookPhase `json:"phase"`
	Interpreter string    `json:"interpreter,omitempty"`
	Script      string    `json:"script"`
	Builtin     string    `json:"builtin,omitempty"`
	Args        []string  `json:"args,omitempty"`
	Required    bool      `json:"required"`
	Timeout     Duration  `json:"timeout,omitempty"`
}

// CatalogPlan keeps both visible prompt metadata and the effective selection.
type CatalogPlan struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Selected    bool   `json:"selected"`
	Origin      string `json:"origin,omitempty"`
}

// BuildPlan resolves inheritance, validates inputs, renders templates, reads
// local template sources, and canonicalizes destinations without writing the
// repository.
func BuildPlan(cfg Config, options PlanOptions) (Plan, error) {
	presetName := options.Preset
	if presetName == "" {
		presetName = cfg.DefaultPreset
	}
	preset, err := cfg.ResolvePreset(presetName)
	if err != nil {
		return Plan{}, err
	}
	inputs, err := ResolveInputs(preset, options.Inputs)
	if err != nil {
		return Plan{}, fmt.Errorf("preset %q: %w", presetName, err)
	}
	if options.Root == "" {
		return Plan{}, fmt.Errorf("scaffold root is required")
	}
	root, err := pathx.Canonical(options.Root)
	if err != nil {
		return Plan{}, fmt.Errorf("scaffold root %q: %w", options.Root, err)
	}
	name := options.Name
	if name == "" {
		name = filepath.Base(root)
	}

	variables := map[string]any{
		"name": name, "path": root, "preset": presetName, "input": inputs,
	}
	for key, value := range options.Variables {
		if _, reserved := variables[key]; reserved {
			return Plan{}, fmt.Errorf("template variable %q is reserved", key)
		}
		if !validVariableExpression(key) || filepath.Base(key) != key {
			return Plan{}, fmt.Errorf("invalid top-level template variable %q", key)
		}
		variables[key] = value
	}

	knownSelections := map[string]bool{}
	for _, file := range preset.Files {
		knownSelections[file.ID] = true
	}
	for _, hook := range preset.Hooks {
		knownSelections[hook.ID] = true
	}
	for _, skill := range preset.Skills {
		knownSelections[skill.ID] = true
	}
	for _, item := range preset.Catalog {
		knownSelections[item.ID] = true
	}
	for id := range options.Selections {
		if !knownSelections[id] {
			return Plan{}, fmt.Errorf("selection %q does not match a file, hook, skill, or catalog item", id)
		}
	}

	plan := Plan{
		Preset: presetName, Root: root, Name: name, Settings: preset,
		Inputs: inputs,
	}
	for _, file := range preset.Files {
		if selected(options.Selections, file.ID, true) == false {
			continue
		}
		destination, err := RenderTemplate(file.Destination, variables)
		if err != nil {
			return Plan{}, fmt.Errorf("file %q destination: %w", file.ID, err)
		}
		relative, absolute, err := safeDestination(root, destination)
		if err != nil {
			return Plan{}, fmt.Errorf("file %q: %w", file.ID, err)
		}
		content := ""
		source := "inline"
		if file.Source != "" {
			source, err = safeTemplateSource(file.TemplateOrigin, file.Source)
			if err != nil {
				return Plan{}, fmt.Errorf("file %q: %w", file.ID, err)
			}
			data, readErr := os.ReadFile(source)
			if readErr != nil {
				return Plan{}, fmt.Errorf("read file %q template %q: %w", file.ID, source, readErr)
			}
			content = string(data)
		} else {
			content = *file.Content
		}
		content, err = RenderTemplate(content, variables)
		if err != nil {
			return Plan{}, fmt.Errorf("file %q content: %w", file.ID, err)
		}
		mode, err := file.FileMode()
		if err != nil {
			return Plan{}, err
		}
		plan.Files = append(plan.Files, FilePlan{
			ID: file.ID, RelativePath: relative, Path: absolute, Content: content,
			Mode: mode, Source: source, Origin: file.Origin,
		})
	}
	for _, hook := range preset.Hooks {
		if !selected(options.Selections, hook.ID, true) {
			continue
		}
		planned := HookPlan{
			ID: hook.ID, Phase: hook.Phase, Interactive: hook.IsInteractive(),
			Required: hook.IsRequired(), Timeout: hook.Timeout, Origin: hook.Origin,
		}
		for _, argument := range hook.Command {
			rendered, renderErr := RenderTemplate(argument, variables)
			if renderErr != nil {
				return Plan{}, fmt.Errorf("hook %q command: %w", hook.ID, renderErr)
			}
			planned.Command = append(planned.Command, rendered)
		}
		if hook.Run != "" {
			planned.Run, err = RenderTemplate(hook.Run, variables)
			if err != nil {
				return Plan{}, fmt.Errorf("hook %q run: %w", hook.ID, err)
			}
		}
		if len(planned.Command) > 0 && planned.Command[0] == "" {
			return Plan{}, fmt.Errorf("hook %q rendered an empty executable", hook.ID)
		}
		plan.Hooks = append(plan.Hooks, planned)
	}
	for _, skill := range preset.Skills {
		if !selected(options.Selections, skill.ID, skill.IsDefault()) {
			continue
		}
		agents := skill.Agents
		if agents == nil {
			agents = cfg.DefaultAgents
		}
		planned := SkillPlan{
			ID: skill.ID, Source: skill.Source, Name: skill.Name,
			Agents: cloneSlice(agents), Origin: skill.Origin,
		}
		if skill.Setup != nil {
			setup := &SetupPlan{
				Phase: skill.Setup.Phase, Interpreter: skill.Setup.Interpreter,
				Script: skill.Setup.Script, Builtin: skill.Setup.Builtin, Required: skill.Setup.IsRequired(),
				Timeout: skill.Setup.Timeout,
			}
			for _, argument := range skill.Setup.Args {
				rendered, renderErr := RenderTemplate(argument, variables)
				if renderErr != nil {
					return Plan{}, fmt.Errorf("skill %q setup args: %w", skill.ID, renderErr)
				}
				setup.Args = append(setup.Args, rendered)
			}
			planned.Setup = setup
		}
		plan.Skills = append(plan.Skills, planned)
	}
	for _, item := range preset.Catalog {
		label, renderErr := RenderTemplate(item.Label, variables)
		if renderErr != nil {
			return Plan{}, fmt.Errorf("catalog item %q label: %w", item.ID, renderErr)
		}
		description, renderErr := RenderTemplate(item.Description, variables)
		if renderErr != nil {
			return Plan{}, fmt.Errorf("catalog item %q description: %w", item.ID, renderErr)
		}
		plan.Catalog = append(plan.Catalog, CatalogPlan{
			ID: item.ID, Source: item.Source, Label: label, Description: description,
			Selected: selected(options.Selections, item.ID, item.IsDefault()), Origin: item.Origin,
		})
	}
	// Stable order makes JSON and confirmation output deterministic even when a
	// future caller constructs plans from map-backed extensions.
	sort.SliceStable(plan.Catalog, func(i, j int) bool { return plan.Catalog[i].ID < plan.Catalog[j].ID })
	return plan, nil
}

func selected(overrides map[string]bool, id string, fallback bool) bool {
	if value, ok := overrides[id]; ok {
		return value
	}
	return fallback
}
