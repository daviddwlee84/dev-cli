package config

import (
	"fmt"
	"time"
)

// PostCreate accepts either of two TOML shapes, because both read naturally:
//
//	post_create = "auto"
//	post_create = ["uv sync", "npm ci"]
type PostCreate struct {
	Auto     bool
	Commands []string
}

// UnmarshalTOML implements toml.Unmarshaler.
func (p *PostCreate) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		if t != "auto" {
			return fmt.Errorf("post_create: %q is not a valid string form (use \"auto\" or a list of commands)", t)
		}
		*p = PostCreate{Auto: true}
		return nil
	case []any:
		cmds := make([]string, 0, len(t))
		for i, e := range t {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("post_create[%d]: want a command string, got %T", i, e)
			}
			cmds = append(cmds, s)
		}
		*p = PostCreate{Commands: cmds}
		return nil
	default:
		return fmt.Errorf("post_create: want \"auto\" or a list of command strings, got %T", v)
	}
}

// MarshalTOML implements toml.Marshaler so a config round-trips.
func (p PostCreate) MarshalTOML() ([]byte, error) {
	if p.Auto {
		return []byte(`"auto"`), nil
	}
	out := []byte("[")
	for i, c := range p.Commands {
		if i > 0 {
			out = append(out, ", "...)
		}
		out = append(out, fmt.Sprintf("%q", c)...)
	}
	return append(out, ']'), nil
}

// Duration is a time.Duration written in TOML as a Go duration string
// ("10m", "90s"), which is friendlier than a bare number of seconds.
type Duration struct{ time.Duration }

// UnmarshalTOML implements toml.Unmarshaler.
func (d *Duration) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		parsed, err := time.ParseDuration(t)
		if err != nil {
			return fmt.Errorf("duration %q: %w", t, err)
		}
		d.Duration = parsed
		return nil
	case int64:
		d.Duration = time.Duration(t) * time.Second
		return nil
	default:
		return fmt.Errorf("duration: want a string like \"10m\", got %T", v)
	}
}

// MarshalTOML implements toml.Marshaler.
func (d Duration) MarshalTOML() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", d.Duration.String())), nil
}
