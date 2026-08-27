package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Path templates are deliberately *not* text/template: the syntax users write
// in config.toml ("{{worktree_root}}/{{repo}}/{{branch|slug}}") needs to stay
// readable and to fail loudly on an unknown variable rather than silently
// rendering "<no value>".

var tmplVar = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:\|\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*)?\}\}`)

// Vars is the substitution environment for a path template.
type Vars map[string]string

// Render substitutes every {{var}} / {{var|filter}} occurrence in tmpl.
// An unknown variable or filter is an error — a typo in worktree_path should
// surface at create time, not as a directory literally named "{{rep}}".
func Render(tmpl string, vars Vars) (string, error) {
	var firstErr error
	out := tmplVar.ReplaceAllStringFunc(tmpl, func(m string) string {
		g := tmplVar.FindStringSubmatch(m)
		name, filter := g[1], g[2]
		val, ok := vars[name]
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown template variable %q in %q (known: %s)", name, tmpl, strings.Join(sortedKeys(vars), ", "))
			}
			return m
		}
		switch filter {
		case "":
			return val
		case "slug":
			return Slug(val)
		case "lower":
			return strings.ToLower(val)
		case "base":
			if i := strings.LastIndexByte(val, '/'); i >= 0 {
				return val[i+1:]
			}
			return val
		default:
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown template filter %q in %q (known: slug, lower, base)", filter, tmpl)
			}
			return m
		}
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

var slugStrip = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Slug makes a branch name safe to use as a single filesystem component:
// "feat/auth/oauth-refresh" becomes "feat-auth-oauth-refresh". Leading dots
// and dashes are trimmed so the result can never be a hidden directory or be
// mistaken for a flag.
func Slug(s string) string {
	s = slugStrip.ReplaceAllString(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-._")
	if s == "" {
		return "unnamed"
	}
	return s
}

func sortedKeys(v Vars) []string {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	// small map; insertion sort keeps this dependency-free
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
