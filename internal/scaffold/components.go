package scaffold

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/ignore"
)

// ResolveComposition adds selected components to one resolved preset. A nil
// selection inherits the preset; an explicit empty list or "none" clears it.
// Components never override one another: incompatible skill declarations fail
// during planning, before an installer can replace another selected skill.
func (c Config) ResolveComposition(name string, components []string) (Preset, error) {
	preset, err := c.ResolvePreset(name)
	if err != nil {
		return Preset{}, err
	}
	if components == nil {
		components = preset.Components
	}
	names, err := normalizeComponents(components)
	if err != nil {
		return Preset{}, err
	}
	preset.Components = names
	for _, name := range names {
		component, ok := c.Components[name]
		if !ok {
			return Preset{}, fmt.Errorf("unknown scaffold component %q", name)
		}
		if err := validateComponent(name, component, true); err != nil {
			return Preset{}, err
		}
		component = cloneComponent(component)
		preset.Gitignore = mergeIgnoreTemplates(preset.Gitignore, component.Gitignore)
		for _, skill := range enabledSkills(component.Skills) {
			if slices.ContainsFunc(preset.Files, func(file File) bool { return file.ID == skill.ID }) ||
				slices.ContainsFunc(preset.Hooks, func(hook Hook) bool { return hook.ID == skill.ID }) {
				return Preset{}, fmt.Errorf("component %q skill id %q conflicts with a preset file or hook", name, skill.ID)
			}
			found := false
			for _, previous := range preset.Skills {
				if previous.ID == skill.ID {
					if !c.sameComponentSkill(previous, skill) {
						return Preset{}, fmt.Errorf("component %q conflicts with skill id %q from %s", name, skill.ID, previous.Origin)
					}
					found = true
					break
				}
				if previous.Name == skill.Name {
					return Preset{}, fmt.Errorf("component %q skill %q and skill %q install the same name %q; agent targets can share storage, so use one shared skill id", name, skill.ID, previous.ID, skill.Name)
				}
			}
			if !found {
				preset.Skills = append(preset.Skills, skill)
			}
			// A component skill is always discoverable without a second catalog
			// declaration. Authored preset catalog metadata can enrich its label.
			if !slices.ContainsFunc(preset.Catalog, func(item SkillCatalog) bool { return item.ID == skill.ID }) {
				description := component.Description
				if description != "" {
					description += ". "
				}
				description += "Installs skill guidance only; does not run project setup."
				preset.Catalog = append(preset.Catalog, SkillCatalog{
					ID: skill.ID, Source: skill.Source, Label: skill.Name,
					Description: description, Default: cloneBool(skill.Default), Origin: skill.Origin,
				})
			}
		}
	}
	return preset, nil
}

func normalizeComponents(values []string) ([]string, error) {
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("component name must not be empty; use none to clear selections")
		}
		if strings.EqualFold(value, "none") {
			if len(values) != 1 {
				return nil, fmt.Errorf("component none cannot be combined with other components")
			}
			return []string{}, nil
		}
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result, nil
}

func mergeIgnoreTemplates(base, additions []string) []string {
	var result []string
	seen := map[string]bool{}
	for _, item := range append(cloneSlice(base), additions...) {
		key := ignore.Canonical(item)
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	return result
}

func (c Config) effectiveSkillAgents(skill Skill) []string {
	agents := skill.Agents
	if agents == nil {
		agents = c.DefaultAgents
	}
	result := cloneSlice(agents)
	slices.Sort(result)
	return slices.Compact(result)
}

func (c Config) sameComponentSkill(a, b Skill) bool {
	return a.Source == b.Source && a.Name == b.Name && a.IsDefault() == b.IsDefault() &&
		slices.Equal(c.effectiveSkillAgents(a), c.effectiveSkillAgents(b)) && reflect.DeepEqual(a.Setup, b.Setup)
}

func cloneComponent(in Component) Component {
	out := in
	out.Gitignore = cloneSlice(in.Gitignore)
	out.Skills = clonePreset(Preset{Skills: in.Skills}).Skills
	return out
}

func mergeComponent(base, overlay Component) Component {
	out := cloneComponent(base)
	if overlay.Description != "" {
		out.Description = overlay.Description
	}
	if overlay.Gitignore != nil {
		out.Gitignore = cloneSlice(overlay.Gitignore)
	}
	out.Skills = mergeSkills(out.Skills, overlay.Skills)
	if overlay.Origin != "" {
		out.Origin = overlay.Origin
	}
	return out
}
