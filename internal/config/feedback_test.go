package config

import (
	"path/filepath"
	"testing"
)

func TestFeedbackSourceRequiresStableAbsolutePath(t *testing.T) {
	cfg := Default()
	cfg.Feedback.SourceRepo = "relative/dev-cli"
	if cfg.Validate() == nil {
		t.Fatal("relative configured source accepted")
	}
	cfg.Feedback.SourceRepo = filepath.Join(t.TempDir(), "dev-cli")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Feedback.SourceRepo = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
