// Package templatex performs strict, one-pass scalar substitution.
//
// Values are never reparsed after insertion. This is important when they come
// from a forge, task note, or another untrusted source: `{{text}}` inside data
// must remain text rather than becoming a second template expression.
package templatex

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Render substitutes {{name}} and caller-defined dotted map values. It exposes
// no functions or evaluation; templates can select scalar values only.
func Render(tmpl string, variables map[string]any) (string, error) {
	var out strings.Builder
	rest := tmpl
	for {
		start := strings.Index(rest, "{{")
		closeOnly := strings.Index(rest, "}}")
		if start < 0 {
			if closeOnly >= 0 {
				return "", fmt.Errorf("template has an unmatched closing delimiter")
			}
			out.WriteString(rest)
			return out.String(), nil
		}
		if closeOnly >= 0 && closeOnly < start {
			return "", fmt.Errorf("template has an unmatched closing delimiter")
		}
		out.WriteString(rest[:start])
		after := rest[start+2:]
		end := strings.Index(after, "}}")
		if end < 0 {
			return "", fmt.Errorf("template has an unmatched opening delimiter")
		}
		expression := strings.TrimSpace(after[:end])
		if !ValidExpression(expression) {
			return "", fmt.Errorf("invalid template variable %q", expression)
		}
		value, ok := lookup(variables, strings.Split(expression, "."))
		if !ok {
			return "", fmt.Errorf("unknown template variable %q", expression)
		}
		rendered, err := scalar(value)
		if err != nil {
			return "", fmt.Errorf("template variable %q: %w", expression, err)
		}
		out.WriteString(rendered)
		rest = after[end+2:]
	}
}

// ValidExpression reports whether expression is a dotted template variable.
func ValidExpression(expression string) bool {
	if expression == "" {
		return false
	}
	for _, component := range strings.Split(expression, ".") {
		if component == "" {
			return false
		}
		for _, r := range component {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

func lookup(current any, components []string) (any, bool) {
	for _, component := range components {
		switch values := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = values[component]
			if !ok {
				return nil, false
			}
		case map[string]string:
			value, ok := values[component]
			if !ok {
				return nil, false
			}
			current = value
		default:
			return nil, false
		}
	}
	return current, true
}

func scalar(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("want a string, boolean, or number, got %T", value)
	}
}
