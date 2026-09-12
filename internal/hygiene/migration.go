package hygiene

import (
	"bytes"
	"errors"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"go.yaml.in/yaml/v3"
)

// migrateScannerHooks only removes a known uncustomized validation hook. The
// specialized artifact checker/finalizer remains a separate dependency.
func migrateScannerHooks(data []byte) ([]byte, []string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, nil, nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) != 1 {
		return nil, nil, errors.New("existing hooks need manual review")
	}
	repos := mapping(doc.Content[0], "repos")
	if repos == nil || repos.Kind != yaml.SequenceNode {
		return nil, nil, errors.New("existing hooks need manual review")
	}
	var notices []string
	changed := false
	retainedRepos := []*yaml.Node{}
	for _, repo := range repos.Content {
		hooks := mapping(repo, "hooks")
		if hooks == nil || hooks.Kind != yaml.SequenceNode {
			return nil, nil, errors.New("invalid hook list")
		}
		retained := []*yaml.Node{}
		for _, hook := range hooks.Content {
			id := mapping(hook, "id")
			if id == nil {
				return nil, nil, errors.New("hook identity is missing")
			}
			if id.Value == "redact-agent-secrets" {
				source, rev := mapping(repo, "repo"), mapping(repo, "rev")
				if source == nil || source.Value != "https://github.com/daviddwlee84/agent-skills" || rev == nil || rev.Value != "ahh-v2.0.1" || mapping(hook, "args") != nil || mapping(hook, "entry") != nil {
					return nil, nil, errors.New("legacy mutating or customized artifact redactor retained; migrate its finalizer boundary manually first")
				}
			}
			if id.Value == "check-agent-artifact-secrets" || id.Value == "redact-agent-secrets" {
				notices = append(notices, "Retained agent-history-hygiene artifact checker/finalizer dependency")
			}
			if id.Value != "gitleaks-system" {
				retained = append(retained, hook)
				continue
			}
			source, rev := mapping(repo, "repo"), mapping(repo, "rev")
			if source == nil || source.Value != "https://github.com/gitleaks/gitleaks" || rev == nil || (rev.Value != "v8.22.1" && rev.Value != "v8.30.0" && rev.Value != "v8.30.1") {
				return nil, nil, errors.New("custom scanner source/version retained; review migration manually")
			}
			for i := 0; i+1 < len(hook.Content); i += 2 {
				key, value := hook.Content[i].Value, hook.Content[i+1]
				if key == "id" || key == "name" {
					continue
				}
				if key == "stages" && value.Kind == yaml.SequenceNode && len(value.Content) == 1 && value.Content[0].Value == "pre-commit" {
					continue
				}
				return nil, nil, errors.New("custom scanner options retained; review migration manually")
			}
			changed = true
			notices = append(notices, "Replace gitleaks-system with dev-hygiene using the existing repository scanner configuration")
		}
		hooks.Content = retained
		if len(retained) != 0 {
			retainedRepos = append(retainedRepos, repo)
		}
	}
	if !changed {
		return data, notices, nil
	}
	repos.Content = retainedRepos
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, err
	}
	return out.Bytes(), notices, nil
}

const legacyPasswordRegex = `(?i)(?:passwo?r?d|passphrase|sudo[_-]?pass(?:word)?)[ \t]*[:=][ \t]*["']?([^\s"'#]{6,})`

// updateKnownPasswordRule patches only the exact former rule expression and
// adds its configuration-only unquoted companion. Other rules/comments survive.
func updateKnownPasswordRule(data []byte) ([]byte, bool, string, error) {
	type document struct{ Rules []map[string]any }
	var current, defaults document
	if _, err := toml.Decode(string(data), &current); err != nil {
		return nil, false, "", errors.New("scanner configuration needs manual review")
	}
	if _, err := toml.Decode(string(DefaultGitleaks), &defaults); err != nil {
		return nil, false, "", err
	}
	var old map[string]any
	for _, rule := range current.Rules {
		if rule["id"] == "generic-password-assignment-unquoted" {
			return data, false, "Existing unquoted password rule retained", nil
		}
		if rule["id"] == "generic-password-assignment" {
			if old != nil {
				return nil, false, "", errors.New("duplicate password rules need manual review")
			}
			old = rule
		}
	}
	if old == nil {
		return data, false, "Custom scanner rules retained", nil
	}
	if old["regex"] != legacyPasswordRegex {
		return data, false, "Custom or current password rule retained", nil
	}
	for key := range old {
		if key != "id" && key != "description" && key != "regex" && key != "secretGroup" && key != "tags" {
			return data, false, "Scoped custom password rule retained; update it manually", nil
		}
	}
	if old["secretGroup"] != int64(1) {
		return data, false, "Custom password capture retained", nil
	}
	var expression string
	for _, rule := range defaults.Rules {
		if rule["id"] == "generic-password-assignment" {
			expression, _ = rule["regex"].(string)
		}
	}
	if expression == "" {
		return nil, false, "", errors.New("bundled password rule unavailable")
	}
	rx := regexp.MustCompile(`(?m)^([ \t]*regex[ \t]*=[ \t]*)'''` + regexp.QuoteMeta(legacyPasswordRegex) + `'''`)
	if len(rx.FindAllIndex(data, -1)) != 1 {
		return data, false, "Custom scanner formatting retained; update the password rule manually", nil
	}
	updated := rx.ReplaceAllFunc(data, func(match []byte) []byte {
		parts := rx.FindSubmatch(match)
		return []byte(string(parts[1]) + "'''" + expression + "'''")
	})
	const idLine = `id          = "generic-password-assignment-single-quoted"`
	pos := strings.Index(string(DefaultGitleaks), idLine)
	if pos < 0 {
		return nil, false, "", errors.New("bundled unquoted password rule unavailable")
	}
	start := strings.LastIndex(string(DefaultGitleaks[:pos]), "[[rules]]")
	updated = append(updated, '\n')
	updated = append(updated, DefaultGitleaks[start:]...)
	return updated, true, "Update the recognized password rule while preserving other scanner rules and comments", nil
}
