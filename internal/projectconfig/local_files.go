package projectconfig

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

var localFileDependencyComponents = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "vendor": true,
	"__pycache__": true, ".tox": true, "target": true, ".terraform": true,
	"dist": true, "build": true, ".next": true,
}

// ValidateLocalFilePattern validates the deliberately small cross-platform
// grammar accepted by [local_files]. Literal path components use portable slash
// paths; '*' and '?' match within one component and a component equal to '**'
// matches zero or more components. Character classes, escaping, volumes, and
// platform-specific separators are intentionally unsupported.
func ValidateLocalFilePattern(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("pattern is required and must be trimmed")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("pattern is not valid UTF-8")
	}
	if len(value) > safefile.CompiledMaxPathBytes {
		return fmt.Errorf("pattern is %d bytes; maximum is %d", len(value), safefile.CompiledMaxPathBytes)
	}
	if strings.ContainsRune(value, '\\') {
		return fmt.Errorf("pattern must use '/' separators")
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return fmt.Errorf("pattern must be a clean repository-relative slash path")
	}
	if strings.ContainsAny(value, "[]{}") {
		return fmt.Errorf("pattern supports only '*', '?' and whole-component '**' wildcards")
	}
	components := strings.Split(value, "/")
	if len(components) > safefile.CompiledMaxPathDepth {
		return fmt.Errorf("pattern has %d components; maximum is %d", len(components), safefile.CompiledMaxPathDepth)
	}
	for _, component := range components {
		if localFileDependencyComponents[strings.ToLower(component)] {
			return fmt.Errorf("pattern enters dependency directory %q", component)
		}
		if component == "**" {
			continue
		}
		if strings.Contains(component, "**") {
			return fmt.Errorf("'**' must occupy a complete path component")
		}
		placeholder := strings.NewReplacer("*", "x", "?", "x").Replace(component)
		if err := pathx.ValidatePortableComponent(placeholder, safefile.CompiledMaxComponentBytes); err != nil {
			return fmt.Errorf("component %q: %w", component, err)
		}
		for _, r := range component {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return fmt.Errorf("component %q contains a control character", component)
			}
		}
	}
	return nil
}
