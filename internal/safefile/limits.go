package safefile

import (
	"errors"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

const (
	// CompiledMaxFiles is the maximum number of portable files accepted by one
	// operation. Runtime policy may lower, but never raise, this ceiling.
	CompiledMaxFiles = 128
	// CompiledMaxFileBytes is the maximum size of one portable file.
	CompiledMaxFileBytes int64 = 8 << 20
	// CompiledMaxTotalBytes is the maximum aggregate portable payload size.
	CompiledMaxTotalBytes int64 = 32 << 20
	// CompiledMaxPathBytes bounds a slash-separated portable relative path.
	CompiledMaxPathBytes = 4096
	// CompiledMaxComponentBytes bounds one portable path component.
	CompiledMaxComponentBytes = 255
	// CompiledMaxPathDepth bounds portable path nesting.
	CompiledMaxPathDepth = 64
)

var (
	// ErrInvalidLimits reports a zero, negative, or above-ceiling policy.
	ErrInvalidLimits = errors.New("invalid safe-file limits")
	// ErrManifestLimit reports a manifest that exceeds its validated policy.
	ErrManifestLimit = errors.New("safe-file manifest limit exceeded")
	// ErrDuplicatePath reports the same exact portable path more than once.
	ErrDuplicatePath = errors.New("duplicate safe-file path")
)

// Limits is the reusable compiled policy for portable file manifests. Every
// field is required and must be no greater than the corresponding compiled
// ceiling, allowing host configuration to lower limits without weakening them.
type Limits struct {
	MaxFiles          int
	MaxFileBytes      int64
	MaxTotalBytes     int64
	MaxPathBytes      int
	MaxComponentBytes int
	MaxPathDepth      int
}

// DefaultLimits returns the maximum policy supported by this binary.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:          CompiledMaxFiles,
		MaxFileBytes:      CompiledMaxFileBytes,
		MaxTotalBytes:     CompiledMaxTotalBytes,
		MaxPathBytes:      CompiledMaxPathBytes,
		MaxComponentBytes: CompiledMaxComponentBytes,
		MaxPathDepth:      CompiledMaxPathDepth,
	}
}

// Validate rejects incomplete policy and values above the compiled ceilings.
func (limits Limits) Validate() error {
	maximum := DefaultLimits()
	switch {
	case limits.MaxFiles <= 0 || limits.MaxFiles > maximum.MaxFiles:
		return fmt.Errorf("max files %d must be between 1 and %d: %w", limits.MaxFiles, maximum.MaxFiles, ErrInvalidLimits)
	case limits.MaxFileBytes <= 0 || limits.MaxFileBytes > maximum.MaxFileBytes:
		return fmt.Errorf("max file bytes %d must be between 1 and %d: %w", limits.MaxFileBytes, maximum.MaxFileBytes, ErrInvalidLimits)
	case limits.MaxTotalBytes <= 0 || limits.MaxTotalBytes > maximum.MaxTotalBytes:
		return fmt.Errorf("max total bytes %d must be between 1 and %d: %w", limits.MaxTotalBytes, maximum.MaxTotalBytes, ErrInvalidLimits)
	case limits.MaxPathBytes <= 0 || limits.MaxPathBytes > maximum.MaxPathBytes:
		return fmt.Errorf("max path bytes %d must be between 1 and %d: %w", limits.MaxPathBytes, maximum.MaxPathBytes, ErrInvalidLimits)
	case limits.MaxComponentBytes <= 0 || limits.MaxComponentBytes > maximum.MaxComponentBytes:
		return fmt.Errorf("max component bytes %d must be between 1 and %d: %w", limits.MaxComponentBytes, maximum.MaxComponentBytes, ErrInvalidLimits)
	case limits.MaxPathDepth <= 0 || limits.MaxPathDepth > maximum.MaxPathDepth:
		return fmt.Errorf("max path depth %d must be between 1 and %d: %w", limits.MaxPathDepth, maximum.MaxPathDepth, ErrInvalidLimits)
	default:
		return nil
	}
}

// PathLimits projects the filesystem path portion of a validated policy.
func (limits Limits) PathLimits() pathx.PortablePathLimits {
	return pathx.PortablePathLimits{
		MaxPathBytes:      limits.MaxPathBytes,
		MaxComponentBytes: limits.MaxComponentBytes,
		MaxDepth:          limits.MaxPathDepth,
	}
}

// Metadata is the content-free portion of one portable regular file used for
// validating counts, sizes, paths, and collisions before any bytes are opened.
type Metadata struct {
	Path string
	Size int64
}

// ValidateManifest enforces a validated limits policy over the complete file
// set, including exact duplicates and portable case/normalization collisions.
func ValidateManifest(files []Metadata, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if len(files) > limits.MaxFiles {
		return fmt.Errorf("manifest has %d files; maximum is %d: %w", len(files), limits.MaxFiles, ErrManifestLimit)
	}
	paths := make([]string, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	var total int64
	for _, file := range files {
		if file.Size < 0 {
			return fmt.Errorf("file %q has negative size: %w", file.Path, ErrManifestLimit)
		}
		if file.Size > limits.MaxFileBytes {
			return fmt.Errorf("file %q is %d bytes; maximum is %d: %w", file.Path, file.Size, limits.MaxFileBytes, ErrManifestLimit)
		}
		if _, exists := seen[file.Path]; exists {
			return fmt.Errorf("path %q appears more than once: %w", file.Path, ErrDuplicatePath)
		}
		seen[file.Path] = struct{}{}
		paths = append(paths, file.Path)
		if file.Size > limits.MaxTotalBytes-total {
			return fmt.Errorf("manifest exceeds %d total bytes: %w", limits.MaxTotalBytes, ErrManifestLimit)
		}
		total += file.Size
	}
	if err := pathx.ValidatePortablePathSet(paths, limits.PathLimits()); err != nil {
		return err
	}
	return nil
}
