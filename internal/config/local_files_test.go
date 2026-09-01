package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestLocalFileLimitsDefaultAndDownwardOverride(t *testing.T) {
	defaults := config.Default().LocalFiles.Limits()
	if defaults != safefile.DefaultLimits() {
		t.Fatalf("default local-files limits = %+v, want %+v", defaults, safefile.DefaultLimits())
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`[local_files]
max_files = 2
max_file_bytes = 1024
max_total_bytes = 2048
max_path_bytes = 128
max_component_bytes = 64
max_path_depth = 8
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	limits := cfg.LocalFiles.Limits()
	if limits.MaxFiles != 2 || limits.MaxFileBytes != 1024 || limits.MaxTotalBytes != 2048 ||
		limits.MaxPathBytes != 128 || limits.MaxComponentBytes != 64 || limits.MaxPathDepth != 8 {
		t.Fatalf("loaded limits = %+v", limits)
	}
}

func TestLocalFileLimitsCannotRaiseCompiledCeilings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`[local_files]
max_files = 129
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("above-ceiling local-files policy should fail")
	}
}
