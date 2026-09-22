package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/scaffold"
)

func promptScaffoldComposition(p *prompter, catalog scaffold.Config, presetName, root string, flags *repoBootstrapFlags) (scaffold.Preset, error) {
	preset, err := catalog.ResolveComposition(presetName, flags.components)
	if err != nil {
		return scaffold.Preset{}, err
	}
	names := make([]string, 0, len(catalog.Components))
	for name := range catalog.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		fmt.Fprintln(p.out, "  Available language/tool components:")
		for _, name := range names {
			fmt.Fprintf(p.out, "    %-12s %s\n", name, catalog.Components[name].Description)
		}
		fallback := strings.Join(preset.Components, ",")
		if fallback == "" {
			fallback = "none"
		}
		for {
			value, err := p.line("Languages/tools (comma-separated; none to skip)", fallback)
			if err != nil {
				return scaffold.Preset{}, err
			}
			components := splitCommaValues(value)
			if len(components) == 0 {
				components = []string{"none"}
			}
			resolved, err := catalog.ResolveComposition(presetName, components)
			if err != nil {
				fmt.Fprintln(p.out, "  "+p.style.warning(err.Error()))
				continue
			}
			flags.components, preset = components, resolved
			break
		}
	}
	checked := map[string]bool{}
	for _, item := range preset.Catalog {
		if !showScaffoldSkillChoice(preset, *flags, item) {
			continue
		}
		fallback := item.IsDefault()
		source := item.Source
		installable := false
		for _, skill := range preset.Skills {
			if skill.ID == item.ID {
				installable = true
				fallback = scaffoldSkillSelected(preset, *flags, item.ID)
				source = skill.Source
				break
			}
		}
		if selections, _ := parseSelectionValues(flags.enable, flags.disable); item.Origin != "builtin" && item.Default != nil {
			if _, explicit := selections[item.ID]; !explicit {
				fallback = item.IsDefault()
			}
		}
		if item.Description != "" {
			fmt.Fprintln(p.out, "  "+item.Description)
		}
		fmt.Fprintln(p.out, "  Source: "+source)
		chosen, err := p.confirm("Install "+item.Label+"?", fallback)
		if err != nil {
			return scaffold.Preset{}, err
		}
		if chosen && installable {
			chosen, err = promptRepoSkillProvider(p, root, item.Label)
			if err != nil {
				return scaffold.Preset{}, err
			}
		}
		setSelection(flags, item.ID, chosen)
		checked[item.ID] = true
	}
	selected := false
	for _, skill := range preset.Skills {
		if !scaffoldSkillSelected(preset, *flags, skill.ID) {
			continue
		}
		// Authored presets can select skills without catalog/picker metadata.
		// Keep those selections unless the user explicitly skips them too.
		if !checked[skill.ID] {
			keep, err := promptRepoSkillProvider(p, root, skill.Name)
			if err != nil {
				return scaffold.Preset{}, err
			}
			if !keep {
				setSelection(flags, skill.ID, false)
				continue
			}
		}
		selected = true
	}
	if selected {
		agents := catalog.DefaultAgents
		if flags.agents != nil {
			agents = flags.agents
		}
		value, err := p.line("Skill agents (comma-separated)", strings.Join(agents, ","))
		if err != nil {
			return scaffold.Preset{}, err
		}
		flags.agents = splitCommaValues(value)
		if len(flags.agents) == 0 {
			return scaffold.Preset{}, fmt.Errorf("at least one skill agent is required")
		}
	}
	return preset, nil
}

// Provider discovery only inspects executable paths. Installation and the
// final source-bound checks remain in the workflow's validation/apply path.
func promptRepoSkillProvider(p *prompter, root, label string) (bool, error) {
	provider := agentskill.MutationProviderStatusFor(root)
	if provider.Available {
		return true, nil
	}
	fmt.Fprintln(p.out, "  "+p.style.warning("Skills installer unavailable for "+label+": "+provider.Detail))
	choice, err := p.choiceOf("For "+label, "", []string{"skip", "cancel"}, map[string]string{
		"skip": "skip", "s": "skip", "cancel": "cancel", "c": "cancel",
	})
	if err != nil {
		return false, err
	}
	if choice == "cancel" {
		return false, errPromptCanceled
	}
	fmt.Fprintln(p.out, "  Skipped "+label+"; other repository settings are kept.")
	return false, nil
}

func scaffoldSkillSelected(preset scaffold.Preset, flags repoBootstrapFlags, id string) bool {
	selections, _ := parseSelectionValues(flags.enable, flags.disable)
	if value, ok := selections[id]; ok {
		return value
	}
	for _, skill := range preset.Skills {
		if skill.ID == id {
			return skill.IsDefault()
		}
	}
	return false
}

func showScaffoldSkillChoice(preset scaffold.Preset, flags repoBootstrapFlags, item scaffold.SkillCatalog) bool {
	if item.Origin != "builtin" || item.ID != "agent-history-hygiene" && item.ID != "project-knowledge-harness" {
		return true
	}
	for _, skill := range preset.Skills {
		if skill.ID == item.ID && skill.Origin != "builtin" {
			return true
		}
	}
	return scaffoldSkillSelected(preset, flags, item.ID)
}

// Component suggestions must not redirect the existing additional-skills
// browser to one language skill's subdirectory. Only the original preset's
// catalog can override that browser's default upstream source.
func repoBrowseSkillSource(prepared preparedRepoScaffold) string {
	preset, err := prepared.Config.ResolvePreset(prepared.Plan.Preset)
	if err != nil {
		return agentskill.DefaultSource
	}
	ids := map[string]bool{}
	for _, item := range preset.Catalog {
		ids[item.ID] = true
	}
	for _, item := range prepared.Plan.Catalog {
		if ids[item.ID] && item.Source != "" {
			return item.Source
		}
	}
	return agentskill.DefaultSource
}
