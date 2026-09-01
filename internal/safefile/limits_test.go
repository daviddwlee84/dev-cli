package safefile_test

import (
	"errors"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func TestDefaultLimitsAreValidAndCannotBeRaised(t *testing.T) {
	limits := safefile.DefaultLimits()
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	limits.MaxFiles--
	limits.MaxFileBytes--
	limits.MaxTotalBytes--
	limits.MaxPathBytes--
	limits.MaxComponentBytes--
	limits.MaxPathDepth--
	if err := limits.Validate(); err != nil {
		t.Fatalf("lower policy should be valid: %v", err)
	}
	limits = safefile.DefaultLimits()
	limits.MaxFiles++
	if err := limits.Validate(); !errors.Is(err, safefile.ErrInvalidLimits) {
		t.Fatalf("raised policy error = %v", err)
	}
	if err := (safefile.Limits{}).Validate(); !errors.Is(err, safefile.ErrInvalidLimits) {
		t.Fatalf("zero policy error = %v", err)
	}
}

func TestValidateManifestEnforcesCountFileTotalAndPaths(t *testing.T) {
	limits := safefile.Limits{
		MaxFiles: 2, MaxFileBytes: 5, MaxTotalBytes: 8,
		MaxPathBytes: 32, MaxComponentBytes: 16, MaxPathDepth: 2,
	}
	if err := safefile.ValidateManifest([]safefile.Metadata{{Path: ".env", Size: 4}, {Path: ".mcp/config", Size: 4}}, limits); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	for _, test := range []struct {
		name  string
		files []safefile.Metadata
		want  error
	}{
		{"count", []safefile.Metadata{{Path: "a", Size: 1}, {Path: "b", Size: 1}, {Path: "c", Size: 1}}, safefile.ErrManifestLimit},
		{"file", []safefile.Metadata{{Path: "a", Size: 6}}, safefile.ErrManifestLimit},
		{"total", []safefile.Metadata{{Path: "a", Size: 5}, {Path: "b", Size: 4}}, safefile.ErrManifestLimit},
		{"negative", []safefile.Metadata{{Path: "a", Size: -1}}, safefile.ErrManifestLimit},
		{"duplicate", []safefile.Metadata{{Path: "a", Size: 1}, {Path: "a", Size: 1}}, safefile.ErrDuplicatePath},
		{"collision", []safefile.Metadata{{Path: "A", Size: 1}, {Path: "a", Size: 1}}, pathx.ErrPathCollision},
		{"depth", []safefile.Metadata{{Path: "a/b/c", Size: 1}}, pathx.ErrPathLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := safefile.ValidateManifest(test.files, limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
