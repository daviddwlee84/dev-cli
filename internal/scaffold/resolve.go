package scaffold

import (
	"fmt"
	"slices"
	"strings"
)

// ResolvePreset applies single-parent inheritance and ID-based item merging.
// Items whose effective enabled flag is false are removed from the result.
func (c Config) ResolvePreset(name string) (Preset, error) {
	if name == "" {
		name = c.DefaultPreset
	}
	state := map[string]uint8{}
	cache := map[string]Preset{}
	var stack []string

	var visit func(string) (Preset, error)
	visit = func(current string) (Preset, error) {
		switch state[current] {
		case 1:
			start := slices.Index(stack, current)
			cycle := append(cloneSlice(stack[start:]), current)
			return Preset{}, fmt.Errorf("preset inheritance %s: %w", strings.Join(cycle, " -> "), ErrPresetCycle)
		case 2:
			return clonePreset(cache[current]), nil
		}
		preset, ok := c.Presets[current]
		if !ok {
			return Preset{}, fmt.Errorf("preset %q: %w", current, ErrPresetNotFound)
		}
		state[current] = 1
		stack = append(stack, current)
		resolved := clonePreset(preset)
		if preset.Extends != "" {
			parent, err := visit(preset.Extends)
			if err != nil {
				return Preset{}, fmt.Errorf("preset %q extends %q: %w", current, preset.Extends, err)
			}
			resolved = mergePreset(parent, preset)
		}
		stack = stack[:len(stack)-1]
		state[current] = 2
		cache[current] = clonePreset(resolved)
		return resolved, nil
	}

	resolved, err := visit(name)
	if err != nil {
		return Preset{}, err
	}
	resolved.Files = enabledFiles(resolved.Files)
	resolved.Hooks = enabledHooks(resolved.Hooks)
	resolved.Skills = enabledSkills(resolved.Skills)
	resolved.Catalog = enabledCatalog(resolved.Catalog)
	if err := validateResolvedPreset(name, resolved); err != nil {
		return Preset{}, err
	}
	return resolved, nil
}

func mergePreset(base, overlay Preset) Preset {
	out := clonePreset(base)
	if overlay.Extends != "" {
		out.Extends = overlay.Extends
	}
	if overlay.Description != "" {
		out.Description = overlay.Description
	}
	if overlay.Readme != nil {
		out.Readme = cloneBool(overlay.Readme)
	}
	if overlay.Gitignore != nil {
		out.Gitignore = cloneSlice(overlay.Gitignore)
	}
	if overlay.ClaudePlans != nil {
		out.ClaudePlans = cloneBool(overlay.ClaudePlans)
	}
	if overlay.AgentContract != "" {
		out.AgentContract = overlay.AgentContract
	}
	if overlay.License != "" {
		out.License = overlay.License
	}
	if overlay.Remote != "" {
		out.Remote = overlay.Remote
	}
	if overlay.Handoff != "" {
		out.Handoff = overlay.Handoff
	}
	if overlay.Template != "" {
		out.Template = overlay.Template
	}
	if overlay.TemplateRef != "" {
		out.TemplateRef = overlay.TemplateRef
	}
	if overlay.TemplateSubdir != "" {
		out.TemplateSubdir = overlay.TemplateSubdir
	}
	if overlay.InitialBranch != "" {
		out.InitialBranch = overlay.InitialBranch
	}
	if overlay.InitialCheckIn != "" {
		out.InitialCheckIn = overlay.InitialCheckIn
		out.InitialCommit = nil
	}
	if overlay.InitialCommit != nil {
		out.InitialCommit = cloneBool(overlay.InitialCommit)
		out.InitialCheckIn = ""
	}
	if overlay.CommitMessage != "" {
		out.CommitMessage = overlay.CommitMessage
	}
	out.Inputs = mergeInputs(out.Inputs, overlay.Inputs)
	out.Files = mergeFiles(out.Files, overlay.Files)
	out.Hooks = mergeHooks(out.Hooks, overlay.Hooks)
	out.Skills = mergeSkills(out.Skills, overlay.Skills)
	out.Catalog = mergeCatalog(out.Catalog, overlay.Catalog)
	if overlay.Origin != "" {
		out.Origin = overlay.Origin
	}
	return out
}

func clonePreset(in Preset) Preset {
	out := in
	out.Readme = cloneBool(in.Readme)
	out.Gitignore = cloneSlice(in.Gitignore)
	out.ClaudePlans = cloneBool(in.ClaudePlans)
	out.InitialCommit = cloneBool(in.InitialCommit)
	out.Inputs = cloneSlice(in.Inputs)
	for i := range out.Inputs {
		out.Inputs[i].Required = cloneBool(in.Inputs[i].Required)
		out.Inputs[i].Choices = cloneSlice(in.Inputs[i].Choices)
	}
	out.Files = cloneSlice(in.Files)
	for i := range out.Files {
		out.Files[i].Content = cloneString(in.Files[i].Content)
		out.Files[i].Enabled = cloneBool(in.Files[i].Enabled)
	}
	out.Hooks = cloneSlice(in.Hooks)
	for i := range out.Hooks {
		out.Hooks[i].Command = cloneSlice(in.Hooks[i].Command)
		out.Hooks[i].Interactive = cloneBool(in.Hooks[i].Interactive)
		out.Hooks[i].Required = cloneBool(in.Hooks[i].Required)
		out.Hooks[i].Enabled = cloneBool(in.Hooks[i].Enabled)
	}
	out.Skills = cloneSlice(in.Skills)
	for i := range out.Skills {
		out.Skills[i].Agents = cloneSlice(in.Skills[i].Agents)
		out.Skills[i].Default = cloneBool(in.Skills[i].Default)
		out.Skills[i].Enabled = cloneBool(in.Skills[i].Enabled)
		out.Skills[i].Setup = cloneSkillSetup(in.Skills[i].Setup)
	}
	out.Catalog = cloneSlice(in.Catalog)
	for i := range out.Catalog {
		out.Catalog[i].Default = cloneBool(in.Catalog[i].Default)
		out.Catalog[i].Enabled = cloneBool(in.Catalog[i].Enabled)
	}
	return out
}

func mergeInputs(base, overlay []Input) []Input {
	out := cloneSlice(base)
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, incoming := range overlay {
		if i, ok := index[incoming.ID]; ok {
			current := out[i]
			if incoming.Type != "" {
				current.Type = incoming.Type
			}
			if incoming.Label != "" {
				current.Label = incoming.Label
			}
			if incoming.Description != "" {
				current.Description = incoming.Description
			}
			if incoming.Required != nil {
				current.Required = cloneBool(incoming.Required)
			}
			if incoming.Default != nil {
				current.Default = incoming.Default
			}
			if incoming.Choices != nil {
				current.Choices = cloneSlice(incoming.Choices)
			}
			if incoming.Origin != "" {
				current.Origin = incoming.Origin
			}
			out[i] = current
			continue
		}
		index[incoming.ID] = len(out)
		out = append(out, incoming)
	}
	return out
}

func mergeFiles(base, overlay []File) []File {
	out := cloneSlice(base)
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, incoming := range overlay {
		if i, ok := index[incoming.ID]; ok {
			current := out[i]
			if incoming.Destination != "" {
				current.Destination = incoming.Destination
			}
			if incoming.Source != "" {
				current.Source, current.Content = incoming.Source, nil
				current.TemplateOrigin = incoming.TemplateOrigin
			}
			if incoming.Content != nil {
				current.Content, current.Source = cloneString(incoming.Content), ""
				current.TemplateOrigin = incoming.TemplateOrigin
			}
			if incoming.Mode != "" {
				current.Mode = incoming.Mode
			}
			if incoming.Enabled != nil {
				current.Enabled = cloneBool(incoming.Enabled)
			}
			if incoming.Origin != "" {
				current.Origin = incoming.Origin
			}
			out[i] = current
			continue
		}
		index[incoming.ID] = len(out)
		out = append(out, incoming)
	}
	return out
}

func mergeHooks(base, overlay []Hook) []Hook {
	out := cloneSlice(base)
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, incoming := range overlay {
		if i, ok := index[incoming.ID]; ok {
			current := out[i]
			if incoming.Phase != "" {
				current.Phase = incoming.Phase
			}
			if incoming.Command != nil {
				current.Command, current.Run = cloneSlice(incoming.Command), ""
			}
			if incoming.Run != "" {
				current.Run, current.Command = incoming.Run, nil
			}
			if incoming.Interactive != nil {
				current.Interactive = cloneBool(incoming.Interactive)
			}
			if incoming.Required != nil {
				current.Required = cloneBool(incoming.Required)
			}
			if incoming.Timeout.Duration != 0 {
				current.Timeout = incoming.Timeout
			}
			if incoming.Enabled != nil {
				current.Enabled = cloneBool(incoming.Enabled)
			}
			if incoming.Origin != "" {
				current.Origin = incoming.Origin
			}
			out[i] = current
			continue
		}
		index[incoming.ID] = len(out)
		out = append(out, incoming)
	}
	return out
}

func mergeSkills(base, overlay []Skill) []Skill {
	out := cloneSlice(base)
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, incoming := range overlay {
		if i, ok := index[incoming.ID]; ok {
			current := out[i]
			if incoming.Source != "" {
				current.Source = incoming.Source
			}
			if incoming.Name != "" {
				current.Name = incoming.Name
			}
			if incoming.Agents != nil {
				current.Agents = cloneSlice(incoming.Agents)
			}
			if incoming.Default != nil {
				current.Default = cloneBool(incoming.Default)
			}
			if incoming.Enabled != nil {
				current.Enabled = cloneBool(incoming.Enabled)
			}
			if incoming.Setup != nil {
				current.Setup = mergeSkillSetup(current.Setup, incoming.Setup)
			}
			if incoming.Origin != "" {
				current.Origin = incoming.Origin
			}
			out[i] = current
			continue
		}
		index[incoming.ID] = len(out)
		out = append(out, incoming)
	}
	return out
}

func mergeCatalog(base, overlay []SkillCatalog) []SkillCatalog {
	out := cloneSlice(base)
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, incoming := range overlay {
		if i, ok := index[incoming.ID]; ok {
			current := out[i]
			if incoming.Source != "" {
				current.Source = incoming.Source
			}
			if incoming.Label != "" {
				current.Label = incoming.Label
			}
			if incoming.Description != "" {
				current.Description = incoming.Description
			}
			if incoming.Default != nil {
				current.Default = cloneBool(incoming.Default)
			}
			if incoming.Enabled != nil {
				current.Enabled = cloneBool(incoming.Enabled)
			}
			if incoming.Origin != "" {
				current.Origin = incoming.Origin
			}
			out[i] = current
			continue
		}
		index[incoming.ID] = len(out)
		out = append(out, incoming)
	}
	return out
}

func mergeSkillSetup(base, overlay *SkillSetup) *SkillSetup {
	if base == nil {
		return cloneSkillSetup(overlay)
	}
	out := *base
	if overlay.Phase != "" {
		out.Phase = overlay.Phase
	}
	if overlay.Interpreter != "" {
		out.Interpreter = overlay.Interpreter
	}
	if overlay.Script != "" {
		out.Script = overlay.Script
	}
	if overlay.Builtin != "" {
		out.Builtin = overlay.Builtin
	}
	if overlay.Args != nil {
		out.Args = cloneSlice(overlay.Args)
	}
	if overlay.Required != nil {
		out.Required = cloneBool(overlay.Required)
	}
	if overlay.Timeout.Duration != 0 {
		out.Timeout = overlay.Timeout
	}
	return &out
}

func cloneSkillSetup(in *SkillSetup) *SkillSetup {
	if in == nil {
		return nil
	}
	out := *in
	out.Args = cloneSlice(in.Args)
	out.Required = cloneBool(in.Required)
	return &out
}

func enabledFiles(in []File) []File {
	out := make([]File, 0, len(in))
	for _, item := range in {
		if item.Enabled == nil || *item.Enabled {
			out = append(out, item)
		}
	}
	return out
}

func enabledHooks(in []Hook) []Hook {
	out := make([]Hook, 0, len(in))
	for _, item := range in {
		if item.Enabled == nil || *item.Enabled {
			out = append(out, item)
		}
	}
	return out
}

func enabledSkills(in []Skill) []Skill {
	out := make([]Skill, 0, len(in))
	for _, item := range in {
		if item.Enabled == nil || *item.Enabled {
			out = append(out, item)
		}
	}
	return out
}

func enabledCatalog(in []SkillCatalog) []SkillCatalog {
	out := make([]SkillCatalog, 0, len(in))
	for _, item := range in {
		if item.Enabled == nil || *item.Enabled {
			out = append(out, item)
		}
	}
	return out
}

func validateResolvedPreset(name string, p Preset) error {
	if strings.TrimSpace(p.Template) == "" && (strings.TrimSpace(p.TemplateRef) != "" || strings.TrimSpace(p.TemplateSubdir) != "") {
		return fmt.Errorf("preset %q template_ref and template_subdir require template", name)
	}
	if p.InitialCheckIn != "" {
		switch p.InitialCheckIn {
		case "commit", "stage", "none":
		default:
			return fmt.Errorf("preset %q initial_check_in %q: want commit, stage, or none", name, p.InitialCheckIn)
		}
	}
	if p.Remote != "" {
		switch p.Remote {
		case "none", "ask":
		default:
			return fmt.Errorf("preset %q remote %q: want none or ask", name, p.Remote)
		}
	}
	if p.Handoff != "" {
		switch p.Handoff {
		case "stay", "cd", "open", "start":
		default:
			return fmt.Errorf("preset %q handoff %q: want stay, cd, open, or start", name, p.Handoff)
		}
	}
	seenInputs := map[string]bool{}
	for _, input := range p.Inputs {
		if seenInputs[input.ID] {
			return fmt.Errorf("preset %q repeats input id %q", name, input.ID)
		}
		seenInputs[input.ID] = true
		switch input.Type {
		case InputString, InputBool:
			if len(input.Choices) != 0 {
				return fmt.Errorf("preset %q input %q type %s cannot declare choices", name, input.ID, input.Type)
			}
		case InputChoice:
			if len(input.Choices) == 0 {
				return fmt.Errorf("preset %q choice input %q has no choices", name, input.ID)
			}
			choices := map[string]bool{}
			for _, choice := range input.Choices {
				if choice == "" || choices[choice] {
					return fmt.Errorf("preset %q input %q has an empty or duplicate choice %q", name, input.ID, choice)
				}
				choices[choice] = true
			}
		default:
			return fmt.Errorf("preset %q input %q has type %q; want string, bool, or choice", name, input.ID, input.Type)
		}
		if input.Default != nil {
			if _, err := normalizeInput(input, input.Default); err != nil {
				return fmt.Errorf("preset %q: %w", name, err)
			}
		}
	}
	for _, file := range p.Files {
		if file.Destination == "" {
			return fmt.Errorf("preset %q file %q has no destination", name, file.ID)
		}
		if (file.Source == "") == (file.Content == nil) {
			return fmt.Errorf("preset %q file %q must declare exactly one of source or content", name, file.ID)
		}
		if _, err := file.FileMode(); err != nil {
			return fmt.Errorf("preset %q: %w", name, err)
		}
	}
	for _, hook := range p.Hooks {
		if !validPhase(hook.Phase) {
			return fmt.Errorf("preset %q hook %q has phase %q", name, hook.ID, hook.Phase)
		}
		if (len(hook.Command) == 0) == (hook.Run == "") {
			return fmt.Errorf("preset %q hook %q must declare exactly one of command or run", name, hook.ID)
		}
		if len(hook.Command) > 0 && strings.TrimSpace(hook.Command[0]) == "" {
			return fmt.Errorf("preset %q hook %q has an empty executable", name, hook.ID)
		}
		if hook.IsInteractive() && hook.Run == "" {
			return fmt.Errorf("preset %q hook %q interactive=true requires run", name, hook.ID)
		}
	}
	for _, skill := range p.Skills {
		if skill.Source == "" || skill.Name == "" {
			return fmt.Errorf("preset %q skill %q requires source and name", name, skill.ID)
		}
		if skill.Setup != nil {
			if !validPhase(skill.Setup.Phase) {
				return fmt.Errorf("preset %q skill %q setup has phase %q", name, skill.ID, skill.Setup.Phase)
			}
			if skill.Setup.Builtin != "" && skill.Setup.Script != "" {
				return fmt.Errorf("preset %q skill %q setup must choose script or builtin, not both", name, skill.ID)
			}
			if skill.Setup.Builtin == "" {
				if _, err := cleanRelativePath(skill.Setup.Script); err != nil {
					return fmt.Errorf("preset %q skill %q setup script: %w", name, skill.ID, err)
				}
			} else if skill.Setup.Builtin != "agent-history-hygiene" && skill.Setup.Builtin != "project-knowledge-harness" {
				return fmt.Errorf("preset %q skill %q has unknown built-in setup %q", name, skill.ID, skill.Setup.Builtin)
			}
		}
	}
	for _, item := range p.Catalog {
		if item.Source == "" || item.Label == "" {
			return fmt.Errorf("preset %q catalog item %q requires source and label", name, item.ID)
		}
	}
	return nil
}

func validPhase(phase HookPhase) bool {
	return phase == BeforeCommit || phase == AfterCommit || phase == AfterRemote
}

func cloneBool(in *bool) *bool {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneString(in *string) *string {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}
