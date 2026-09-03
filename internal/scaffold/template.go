package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/templatex"
)

var ErrUnsafePath = errors.New("unsafe scaffold path")

// ResolveInputs validates caller-supplied input values and fills defaults.
// Boolean strings are accepted so `--set feature=true` need not pre-parse the
// schema; the returned map always contains native bool/string values.
func ResolveInputs(preset Preset, supplied map[string]any) (map[string]any, error) {
	known := make(map[string]Input, len(preset.Inputs))
	for _, input := range preset.Inputs {
		known[input.ID] = input
	}
	for id := range supplied {
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("unknown scaffold input %q", id)
		}
	}
	resolved := make(map[string]any, len(preset.Inputs))
	for _, input := range preset.Inputs {
		value, ok := supplied[input.ID]
		if !ok {
			value, ok = input.Default, input.Default != nil
		}
		if !ok {
			if input.IsRequired() {
				return nil, fmt.Errorf("scaffold input %q is required", input.ID)
			}
			continue
		}
		normalized, err := normalizeInput(input, value)
		if err != nil {
			return nil, err
		}
		resolved[input.ID] = normalized
	}
	return resolved, nil
}

func normalizeInput(input Input, value any) (any, error) {
	switch input.Type {
	case InputString:
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("scaffold input %q: want string, got %T", input.ID, value)
		}
		if input.IsRequired() && strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("scaffold input %q is required", input.ID)
		}
		return s, nil
	case InputBool:
		switch v := value.(type) {
		case bool:
			return v, nil
		case string:
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("scaffold input %q: %q is not a boolean", input.ID, v)
			}
			return parsed, nil
		default:
			return nil, fmt.Errorf("scaffold input %q: want bool, got %T", input.ID, value)
		}
	case InputChoice:
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("scaffold input %q: want one of %s, got %T", input.ID, strings.Join(input.Choices, ", "), value)
		}
		for _, choice := range input.Choices {
			if s == choice {
				return s, nil
			}
		}
		return nil, fmt.Errorf("scaffold input %q: %q is not one of %s", input.ID, s, strings.Join(input.Choices, ", "))
	default:
		return nil, fmt.Errorf("scaffold input %q has unknown type %q", input.ID, input.Type)
	}
}

// RenderTemplate substitutes {{name}}, {{path}}, {{preset}},
// {{input.<id>}}, and caller-defined dotted map values. The implementation is
// shared with prompt rendering; the wrapper preserves the scaffold API.
func RenderTemplate(tmpl string, variables map[string]any) (string, error) {
	return templatex.Render(tmpl, variables)
}

func validVariableExpression(expression string) bool {
	return templatex.ValidExpression(expression)
}

// cleanRelativePath applies the same interpretation on Unix and Windows:
// either slash is a separator, drive-qualified and UNC paths are absolute,
// and a parent component is always traversal.
func cleanRelativePath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("empty path: %w", ErrUnsafePath)
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("path contains NUL: %w", ErrUnsafePath)
	}
	normalized := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(normalized, "/") || driveQualifiedPath(normalized) {
		return "", fmt.Errorf("absolute path %q: %w", raw, ErrUnsafePath)
	}
	for _, component := range strings.Split(normalized, "/") {
		if component == ".." {
			return "", fmt.Errorf("parent traversal in %q: %w", raw, ErrUnsafePath)
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q does not name a child: %w", raw, ErrUnsafePath)
	}
	return cleaned, nil
}

func driveQualifiedPath(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	letter := p[0]
	return letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z'
}

func safeDestination(root, rendered string) (relative, absolute string, err error) {
	relative, err = cleanRelativePath(rendered)
	if err != nil {
		return "", "", err
	}
	lexical := filepath.Join(root, filepath.FromSlash(relative))
	if info, lstatErr := os.Lstat(lexical); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("destination %q is a symlink: %w", rendered, ErrUnsafePath)
	} else if lstatErr != nil && !errors.Is(lstatErr, fs.ErrNotExist) {
		return "", "", fmt.Errorf("inspect destination %q: %w", rendered, lstatErr)
	}
	absolute, err = pathx.CanonicalChild(root, lexical)
	if err != nil {
		return "", "", fmt.Errorf("destination %q: %w: %w", rendered, ErrUnsafePath, err)
	}
	return relative, absolute, nil
}

func safeTemplateSource(origin, source string) (string, error) {
	relative, err := cleanRelativePath(source)
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(filepath.Dir(origin))
	if err != nil {
		return "", err
	}
	templatesRoot := filepath.Join(base, "templates")
	candidate := filepath.Join(templatesRoot, filepath.FromSlash(relative))
	if relative == "templates" || strings.HasPrefix(relative, "templates/") {
		candidate = filepath.Join(base, filepath.FromSlash(relative))
	}
	resolved, err := pathx.CanonicalChild(templatesRoot, candidate)
	if err != nil {
		return "", fmt.Errorf("template source %q: %w: %w", source, ErrUnsafePath, err)
	}
	return resolved, nil
}
