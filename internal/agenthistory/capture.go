package agenthistory

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/daviddwlee84/dev-cli/internal/config"
)

// External capture owns one native output_dir scalar. Changing that file must
// invalidate a handoff; otherwise an old directory could look safely archived
// while the recorder has moved to another location. Native CLI overrides remain
// outside dev's invocation boundary and are never inferred from cached sessions.
func (s *Service) captureFingerprint(ctx context.Context) (string, error) {
	if s.Policy.Source != "specstory" {
		return "not-specstory", nil
	}
	path := filepath.Join(s.Root, ".specstory", "cli", "config.toml")
	data, e := readOptional(ctx, path)
	if e != nil {
		return "", errors.New("native SpecStory config unavailable")
	}
	return hash(data), nil
}

func (s *Service) validateCapture(ctx context.Context) error {
	if s.Policy.Capture == "external" {
		data, e := readOptional(ctx, filepath.Join(s.Root, ".specstory", "cli", "config.toml"))
		if e != nil {
			return errors.New("native SpecStory config unavailable")
		}
		var native struct {
			Local struct {
				Output  string `toml:"output_dir"`
				Enabled *bool  `toml:"enabled"`
			} `toml:"local_sync"`
		}
		if _, e := toml.Decode(string(data), &native); e != nil {
			return errors.New("invalid SpecStory capture config")
		}
		if native.Local.Enabled != nil && !*native.Local.Enabled {
			return errors.New("managed local capture was disabled; review capture policy")
		}
		actual := config.Expand(native.Local.Output)
		if !filepath.IsAbs(actual) {
			actual = filepath.Join(s.Root, actual)
		}
		if native.Local.Output == "" || filepath.Clean(actual) != s.Binding.CaptureDir {
			return errors.New("SpecStory output_dir no longer matches this checkout's capture binding; run setup")
		}
	}
	return nil
}
