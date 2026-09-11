// Package hygiene owns repository scanning, policy and reviewed text changes.
package hygiene

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Mode string

const (
	Block Mode = "block"
	Warn  Mode = "warn"
	Off   Mode = "off"
)

func validMode(m Mode) bool { return m == Block || m == Warn || m == Off }

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,95}$`)

type Rule struct {
	ID          string   `toml:"id" json:"id"`
	Kind        string   `toml:"kind" json:"kind"`
	Value       string   `toml:"value" json:"-"`
	Replacement string   `toml:"replacement" json:"replacement"`
	Action      Mode     `toml:"action" json:"action,omitempty"`
	Paths       []string `toml:"paths" json:"paths,omitempty"`
}
type Exception struct {
	Finding string `toml:"finding" json:"finding,omitempty"`
	Rule    string `toml:"rule" json:"rule,omitempty"`
	Path    string `toml:"path" json:"path,omitempty"`
	Pattern string `toml:"pattern" json:"-"`
	Reason  string `toml:"reason" json:"reason"`
}
type Policy struct {
	Version    int         `toml:"version" json:"version"`
	Secrets    Mode        `toml:"secrets" json:"secrets"`
	Known      Mode        `toml:"known" json:"known"`
	Generic    Mode        `toml:"generic" json:"generic"`
	Rules      []Rule      `toml:"rules" json:"rules,omitempty"`
	Exceptions []Exception `toml:"exceptions" json:"exceptions,omitempty"`
}

func DefaultPolicy() Policy { return Policy{Version: 1, Secrets: Block, Known: Block, Generic: Warn} }
func DecodePolicy(data []byte) (Policy, error) {
	var p Policy
	meta, err := toml.Decode(string(data), &p)
	if err != nil || len(meta.Undecoded()) != 0 {
		return p, errors.New("invalid hygiene policy; check keys and TOML locally")
	}
	if p.Version != 1 {
		return p, errors.New("unsupported hygiene policy version")
	}
	for _, m := range []Mode{p.Secrets, p.Known, p.Generic} {
		if m != "" && !validMode(m) {
			return p, errors.New("hygiene modes must be block, warn or off")
		}
	}
	seen := map[string]bool{}
	for _, r := range p.Rules {
		if seen[r.ID] {
			return p, errors.New("duplicate hygiene rule ID")
		}
		seen[r.ID] = true
		if _, err := compileRule(r); err != nil {
			return p, err
		}
	}
	for _, e := range p.Exceptions {
		if strings.TrimSpace(e.Reason) == "" {
			return p, errors.New("exceptions require a reason")
		}
		if e.Finding != "" {
			if len(e.Finding) != 64 {
				return p, errors.New("invalid finding exception")
			}
			continue
		}
		if !identifier.MatchString(e.Rule) || e.Path == "" || !strings.HasPrefix(e.Pattern, "^") || !strings.HasSuffix(e.Pattern, "$") {
			return p, errors.New("portable exceptions require rule, path, anchored match pattern and reason")
		}
		if _, err := regexp.Compile(e.Pattern); err != nil {
			return p, errors.New("invalid exception pattern")
		}
	}
	return p, nil
}
func MergePolicy(a, b Policy) Policy {
	if b.Secrets != "" {
		a.Secrets = b.Secrets
	}
	if b.Known != "" {
		a.Known = b.Known
	}
	if b.Generic != "" {
		a.Generic = b.Generic
	}
	rules := map[string]Rule{}
	for _, r := range a.Rules {
		rules[r.ID] = r
	}
	for _, r := range b.Rules {
		rules[r.ID] = r
	}
	a.Rules = nil
	for _, r := range rules {
		a.Rules = append(a.Rules, r)
	}
	sort.Slice(a.Rules, func(i, j int) bool { return a.Rules[i].ID < a.Rules[j].ID })
	a.Exceptions = append(a.Exceptions, b.Exceptions...)
	return a
}
func policyBytes(p Policy) ([]byte, error) {
	var b strings.Builder
	err := toml.NewEncoder(&b).Encode(p)
	return []byte(b.String()), err
}
func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func keyedID(key []byte, parts ...string) string {
	h := hmac.New(sha256.New, key)
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func policyDigest(p Policy) string { b, _ := policyBytes(p); return digest(b) }

type compiledRule struct {
	rule Rule
	re   *regexp.Regexp
	cidr netip.Prefix
}

var ipToken = regexp.MustCompile(`[0-9A-Fa-f:.]+(?:%[a-zA-Z0-9_-]+)?`)

func compileRule(r Rule) (compiledRule, error) {
	c := compiledRule{rule: r}
	if !identifier.MatchString(r.ID) || r.Value == "" || len(r.Value) > 8192 || len(r.Replacement) > 4096 {
		return c, errors.New("invalid hygiene rule ID or size")
	}
	if r.Action != "" && !validMode(r.Action) {
		return c, errors.New("invalid hygiene rule action")
	}
	for _, p := range r.Paths {
		if _, err := path.Match(p, ""); err != nil {
			return c, errors.New("invalid rule path glob")
		}
	}
	var err error
	switch r.Kind {
	case "literal":
		// Match the exact string or JSON-escaped representation, without exposing it.
		encoded, _ := json.Marshal(r.Value)
		escaped := string(encoded[1 : len(encoded)-1])
		pattern := regexp.QuoteMeta(r.Value)
		if escaped != r.Value {
			pattern += "|" + regexp.QuoteMeta(escaped)
		}
		c.re, err = regexp.Compile(pattern)
	case "regex":
		c.re, err = regexp.Compile(r.Value)
	case "cidr":
		c.cidr, err = netip.ParsePrefix(r.Value)
	default:
		err = errors.New("unsupported rule kind")
	}
	if err != nil {
		return c, errors.New("invalid hygiene rule expression")
	}
	return c, nil
}
func pathMatches(pattern, file string) bool {
	ok, _ := path.Match(pattern, file)
	return ok || strings.HasSuffix(pattern, "/**") && strings.HasPrefix(file, strings.TrimSuffix(pattern, "**"))
}
func (c compiledRule) indices(file string, b []byte) [][2]int {
	if len(c.rule.Paths) > 0 {
		ok := false
		for _, p := range c.rule.Paths {
			ok = ok || pathMatches(p, file)
		}
		if !ok {
			return nil
		}
	}
	var result [][2]int
	if c.re != nil {
		for _, m := range c.re.FindAllIndex(b, -1) {
			if m[0] == m[1] {
				continue
			}
			if c.rule.Kind == "literal" && (!literalBoundary(b, m[0], true) || !literalBoundary(b, m[1], false)) {
				continue
			}
			result = append(result, [2]int{m[0], m[1]})
		}
		return result
	}
	for _, m := range ipToken.FindAllIndex(b, -1) {
		raw := strings.Trim(string(b[m[0]:m[1]]), ".")
		addr, err := netip.ParseAddr(raw)
		if err == nil && c.cidr.Contains(addr.WithZone("")) {
			start := m[0] + strings.Index(string(b[m[0]:m[1]]), raw)
			result = append(result, [2]int{start, start + len(raw)})
		}
	}
	return result
}
func literalBoundary(b []byte, pos int, left bool) bool {
	if left {
		if pos == 0 {
			return true
		}
		pos--
	} else if pos == len(b) {
		return true
	}
	x := b[pos]
	return !(x >= 'a' && x <= 'z' || x >= 'A' && x <= 'Z' || x >= '0' && x <= '9' || x == '_' || x == '-' || x == '.')
}

var genericRules = []Rule{
	{ID: "privacy-email", Kind: "regex", Value: `\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`, Replacement: "user@example.invalid"},
	{ID: "privacy-home-path", Kind: "regex", Value: `(?:/Users/|/home/|[A-Za-z]:[\\/]Users[\\/])[^\s/\\<>"` + "`" + `]+`, Replacement: "/home/user"},
	{ID: "privacy-ip", Kind: "cidr", Value: "0.0.0.0/0", Replacement: "192.0.2.1"},
	{ID: "privacy-ipv6", Kind: "cidr", Value: "::/0", Replacement: "2001:db8::1"},
}
