package hygiene

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type Candidate struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Source      string     `json:"source"`
	Line        int        `json:"line,omitempty"`
	Recommended Mode       `json:"recommended"`
	ObservedAt  *time.Time `json:"observed_at,omitempty"`
	Stale       bool       `json:"stale,omitempty"`
	rule        Rule
}
type Candidates struct {
	Complete    bool        `json:"complete"`
	Items       []Candidate `json:"items"`
	Gaps        []string    `json:"gaps,omitempty"`
	fingerprint string
}

func (s *Service) Candidates(ctx context.Context, from string) (Candidates, error) {
	if err := s.prepare(ctx); err != nil {
		return Candidates{}, err
	}
	return s.candidates(ctx, from)
}
func (s *Service) candidates(ctx context.Context, from string) (Candidates, error) {
	result := Candidates{Complete: true, Items: []Candidate{}}
	metadata := map[string]candidateObservation{}
	var binding any
	literals := []sshhost.PrivacyLiteral{}
	switch from {
	case "ssh":
		service, err := sshhost.NewDefaultService(nil)
		if err != nil {
			return result, err
		}
		literals, result.Complete, err = service.PrivacyLiterals(ctx)
		if err != nil {
			return result, errors.New("SSH privacy source unavailable")
		}
	case "machines":
		var err error
		literals, metadata, binding, result.Gaps, err = s.machineLiterals(ctx)
		result.Complete = err == nil
		if err != nil {
			return result, errors.New("machine cache is incomplete or unsafe; inspect cache diagnostics")
		}
	case "local":
		home, err := os.UserHomeDir()
		if err != nil {
			return result, err
		}
		literals = append(literals, sshhost.PrivacyLiteral{Kind: "home-path", Value: home}, sshhost.PrivacyLiteral{Kind: "local-user", Value: filepath.Base(home)})
		name, e := gitBytes(ctx, s.Root, nil, "config", "--get", "user.name")
		if e == nil {
			literals = append(literals, sshhost.PrivacyLiteral{Kind: "git-name", Value: strings.TrimSpace(string(name))})
		}
		email, err := gitBytes(ctx, s.Root, nil, "config", "--get", "user.email")
		if err == nil {
			literals = append(literals, sshhost.PrivacyLiteral{Kind: "git-email", Value: strings.TrimSpace(string(email))})
		}
	default:
		return result, errors.New("import source must be ssh, local or machines")
	}
	seen := map[string]bool{}
	for _, v := range literals {
		if len(v.Value) < 3 || seen[v.Value] {
			continue
		}
		seen[v.Value] = true
		id := v.Kind + "-" + keyedID(s.key, v.Kind, v.Value)[:12]
		replacement := "[REDACTED:" + id + "]"
		mode := Block
		if addr, err := netip.ParseAddr(v.Value); err == nil {
			replacement = "192.0.2.1"
			if addr.Is6() {
				replacement = "2001:db8::1"
			}
			if addr.IsLoopback() || addr.IsUnspecified() {
				mode = Warn
			}
		}
		switch v.Kind {
		case "home-path":
			replacement = "/home/user"
		case "git-name":
			replacement = "Example Developer"
			mode = Warn
		case "git-email":
			replacement = "user@example.invalid"
			mode = Warn
		case "ssh-host":
			if !strings.Contains(v.Value, ":") && strings.Contains(v.Value, ".") {
				replacement = "host.example.invalid"
			}
		}
		for _, common := range []string{"root", "git", "ubuntu", "admin", "localhost", "github", "github.com", "gitlab", "gitlab.com"} {
			if strings.EqualFold(v.Value, common) {
				mode = Warn
			}
		}
		rule := Rule{ID: id, Kind: "literal", Value: v.Value, Replacement: replacement, Action: mode}
		if addr, e := netip.ParseAddr(v.Value); e == nil {
			addr = addr.Unmap()
			rule.Kind = "cidr"
			rule.Value = netip.PrefixFrom(addr, addr.BitLen()).String()
		}
		source := from
		if v.Source.Path != "" {
			source = "SSH configuration source"
		}
		observation := metadata[v.Value]
		if observation.Source != "" {
			source = observation.Source
		}
		result.Items = append(result.Items, Candidate{ID: id, Kind: v.Kind, Source: source, Line: v.Source.Line, Recommended: mode, ObservedAt: observation.At, Stale: observation.Stale, rule: rule})
	}
	data, err := json.Marshal(struct {
		Literals []sshhost.PrivacyLiteral
		Binding  any
	}{literals, binding})
	if err != nil {
		return result, err
	}
	// PrivacyLiteral deliberately omits values in JSON; include them only in the keyed digest.
	parts := []string{string(data)}
	for _, v := range literals {
		parts = append(parts, v.Kind, v.Value, v.Source.Path)
	}
	result.fingerprint = keyedID(s.key, parts...)
	return result, nil
}
func (s *Service) PreviewImport(ctx context.Context, from string, ids []string) (Plan, error) {
	candidates, err := s.Candidates(ctx, from)
	if err != nil {
		return Plan{}, err
	}
	if !candidates.Complete {
		return Plan{}, errors.New("privacy source is incomplete; inspect source before importing")
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = false
	}
	rules := []Rule{}
	for _, c := range candidates.Items {
		if _, ok := selected[c.ID]; ok {
			rules = append(rules, c.rule)
			selected[c.ID] = true
		}
	}
	if len(rules) == 0 {
		return Plan{}, errors.New("select candidate IDs explicitly")
	}
	for _, found := range selected {
		if !found {
			return Plan{}, errors.New("candidate changed; preview import again")
		}
	}
	return s.previewRules(ctx, rules, nil, Policy{Version: 1}, from, candidates.fingerprint)
}
func (s *Service) PreviewRules(ctx context.Context, rules []Rule, exceptions []Exception, overrides Policy) (Plan, error) {
	return s.previewRules(ctx, rules, exceptions, overrides, "", "")
}
func (s *Service) previewRules(ctx context.Context, rules []Rule, exceptions []Exception, overrides Policy, source, sourceDigest string) (Plan, error) {
	target := filepath.Join(s.Dir, "policy.toml")
	local := Policy{Version: 1}
	if b, err := safefile.ReadStablePath(ctx, target, 1<<20); err == nil {
		local, err = DecodePolicy(b)
		if err != nil {
			return Plan{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Plan{}, err
	}
	overrides.Version = 1
	overrides.Rules = rules
	overrides.Exceptions = exceptions
	updated := MergePolicy(local, overrides)
	data, err := policyBytes(updated)
	if err != nil {
		return Plan{}, err
	}
	if _, err = DecodePolicy(data); err != nil {
		return Plan{}, err
	}
	p, err := s.newPlan(ctx, "hygiene_rules")
	if err != nil {
		return Plan{}, err
	}
	if err = s.addChange(ctx, &p, "local policy", target, data, 0); err != nil {
		return Plan{}, err
	}
	p.ImportSource, p.ImportDigest = source, sourceDigest
	effective := MergePolicy(s.Policy, overrides)
	p.Plan.Policy = &effective
	if err = s.savePlan(ctx, &p); err != nil {
		return Plan{}, err
	}
	return p.Plan, nil
}
func (s *Service) PreviewAllow(ctx context.Context, reportID, findingID, reason string) (Plan, error) {
	var r scanRecord
	if err := s.load(ctx, reportID, &r); err != nil {
		return Plan{}, err
	}
	if r.Report.RepoID != s.RepoID {
		return Plan{}, ErrStale
	}
	found := false
	for _, f := range r.Report.Findings {
		found = found || f.ID == findingID
	}
	if !found {
		return Plan{}, errors.New("finding does not belong to report")
	}
	return s.PreviewRules(ctx, nil, []Exception{{Finding: findingID, Reason: reason}}, Policy{Version: 1})
}
