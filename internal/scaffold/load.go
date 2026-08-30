package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	ErrUnsupportedVersion = errors.New("unsupported scaffold configuration version")
	ErrPresetNotFound     = errors.New("scaffold preset not found")
	ErrPresetCycle        = errors.New("scaffold preset inheritance cycle")
)

// Load overlays zero or more scaffolds.toml files on the built-in catalog.
// Sources later in the argument list are more specific. Missing files are
// errors; callers that treat project configuration as optional should test
// existence before adding its path.
func Load(paths ...string) (Config, error) {
	cfg := Builtins()
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return Config{}, fmt.Errorf("scaffold config %q: %w", path, err)
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return Config{}, fmt.Errorf("read scaffold config %q: %w", path, err)
		}
		overlay, err := Decode(data, absolute)
		if err != nil {
			return Config{}, err
		}
		cfg = mergeConfig(cfg, overlay)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Decode parses one strict, versioned scaffold source. It intentionally does
// not require presets to be complete: a later Merge may supply an inherited
// parent or the rest of an ID-merged item.
func Decode(data []byte, source string) (Config, error) {
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parse scaffold config %q: %w", source, err)
	}
	if cfg.Version != CurrentVersion {
		return Config{}, fmt.Errorf("scaffold config %q has version %d, want %d: %w",
			source, cfg.Version, CurrentVersion, ErrUnsupportedVersion)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("scaffold config %q has unknown keys: %s", source, strings.Join(keys, ", "))
	}
	if cfg.Presets == nil {
		cfg.Presets = map[string]Preset{}
	}
	cfg.defaultAgentsSet = md.IsDefined("default_agents")
	cfg.Sources = []string{source}
	setOrigins(&cfg, source)
	if err := validateDocument(cfg); err != nil {
		return Config{}, fmt.Errorf("scaffold config %q: %w", source, err)
	}
	return cfg, nil
}

// Merge overlays one already-decoded catalog on another and validates the
// effective result. It is useful to callers that source TOML from somewhere
// other than ordinary files.
func Merge(base, overlay Config) (Config, error) {
	merged := mergeConfig(base, overlay)
	if err := merged.Validate(); err != nil {
		return Config{}, err
	}
	return merged, nil
}

// Validate resolves every preset so missing parents, cycles, and incomplete
// merged items fail before a wizard presents them.
func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("scaffold config has version %d, want %d: %w", c.Version, CurrentVersion, ErrUnsupportedVersion)
	}
	if len(c.Presets) == 0 {
		return fmt.Errorf("scaffold config has no presets")
	}
	if c.DefaultPreset == "" {
		return fmt.Errorf("scaffold config has no default_preset")
	}
	if _, ok := c.Presets[c.DefaultPreset]; !ok {
		return fmt.Errorf("default preset %q: %w", c.DefaultPreset, ErrPresetNotFound)
	}
	names := make([]string, 0, len(c.Presets))
	for name := range c.Presets {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("preset name must not be empty")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := c.ResolvePreset(name); err != nil {
			return err
		}
	}
	return nil
}

func validateDocument(c Config) error {
	for name, preset := range c.Presets {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("preset name must not be empty")
		}
		for kind, ids := range map[string][]string{
			"input":   inputIDs(preset.Inputs),
			"file":    fileIDs(preset.Files),
			"hook":    hookIDs(preset.Hooks),
			"skill":   skillIDs(preset.Skills),
			"catalog": catalogIDs(preset.Catalog),
		} {
			seen := map[string]bool{}
			for _, id := range ids {
				if strings.TrimSpace(id) == "" {
					return fmt.Errorf("preset %q has a %s with no id", name, kind)
				}
				if seen[id] {
					return fmt.Errorf("preset %q repeats %s id %q", name, kind, id)
				}
				seen[id] = true
			}
		}
	}
	return nil
}

func setOrigins(c *Config, source string) {
	for name, preset := range c.Presets {
		preset.Origin = source
		for i := range preset.Inputs {
			preset.Inputs[i].Origin = source
		}
		for i := range preset.Files {
			preset.Files[i].Origin = source
			if preset.Files[i].Source != "" || preset.Files[i].Content != nil {
				preset.Files[i].TemplateOrigin = source
			}
		}
		for i := range preset.Hooks {
			preset.Hooks[i].Origin = source
		}
		for i := range preset.Skills {
			preset.Skills[i].Origin = source
		}
		for i := range preset.Catalog {
			preset.Catalog[i].Origin = source
		}
		c.Presets[name] = preset
	}
}

func mergeConfig(base, overlay Config) Config {
	out := cloneConfig(base)
	out.Version = CurrentVersion
	if overlay.DefaultPreset != "" {
		out.DefaultPreset = overlay.DefaultPreset
	}
	if overlay.defaultAgentsSet || overlay.DefaultAgents != nil {
		out.DefaultAgents = append(make([]string, 0, len(overlay.DefaultAgents)), overlay.DefaultAgents...)
		out.defaultAgentsSet = true
	}
	if out.Presets == nil {
		out.Presets = map[string]Preset{}
	}
	for name, incoming := range overlay.Presets {
		if current, ok := out.Presets[name]; ok {
			out.Presets[name] = mergePreset(current, incoming)
		} else {
			out.Presets[name] = clonePreset(incoming)
		}
	}
	out.Sources = append(out.Sources, overlay.Sources...)
	return out
}

func cloneConfig(in Config) Config {
	out := in
	out.DefaultAgents = cloneSlice(in.DefaultAgents)
	out.Sources = cloneSlice(in.Sources)
	out.Presets = make(map[string]Preset, len(in.Presets))
	for name, preset := range in.Presets {
		out.Presets[name] = clonePreset(preset)
	}
	return out
}

func inputIDs(v []Input) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].ID
	}
	return out
}

func fileIDs(v []File) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].ID
	}
	return out
}

func hookIDs(v []Hook) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].ID
	}
	return out
}

func skillIDs(v []Skill) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].ID
	}
	return out
}

func catalogIDs(v []SkillCatalog) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].ID
	}
	return out
}
