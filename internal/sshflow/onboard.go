package sshflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type OnboardTarget struct {
	Alias         string `json:"alias"`
	Label         string `json:"label"`
	HostName      string `json:"host_name"`
	User          string `json:"user,omitempty"`
	Port          int    `json:"port"`
	Auth          string `json:"auth"`
	To            string `json:"to,omitempty"`
	RemoteOS      string `json:"remote_os,omitempty"`
	MachineID     string `json:"machine_id,omitempty"`
	AdvertisesSSH bool   `json:"advertises_tailscale_ssh"`
}

var ErrOnboardUnknown = errors.New("remote or durable effect may have completed")

type OnboardPlan struct {
	SchemaVersion int             `json:"schema_version"`
	Kind          string          `json:"kind"`
	Targets       []OnboardTarget `json:"targets"`
	targets       []OnboardTarget
}
type OnboardOutcome struct {
	Alias  string `json:"alias"`
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type OnboardResult struct {
	SchemaVersion int              `json:"schema_version"`
	Kind          string           `json:"kind"`
	Status        string           `json:"status"`
	Outcomes      []OnboardOutcome `json:"outcomes"`
}

func PlanOnboarding(targets []OnboardTarget) (OnboardPlan, error) {
	p := OnboardPlan{SchemaVersion: 1, Kind: "ssh_onboarding_plan"}
	if len(targets) == 0 {
		return p, errors.New("select at least one connection")
	}
	seen := map[string]bool{}
	for _, t := range targets {
		if err := sshhost.ValidateLookupAlias(t.Alias); err != nil {
			return p, err
		}
		if seen[strings.ToLower(t.Alias)] {
			return p, fmt.Errorf("duplicate selected alias %q", t.Alias)
		}
		seen[strings.ToLower(t.Alias)] = true
		if t.Auth != "config" && t.Auth != "existing" && t.Auth != "key" {
			return p, errors.New("choose config, existing authentication or a key")
		}
		if t.To != "" && t.To != "fleet" && t.To != "herdr" && t.To != "both" {
			return p, errors.New("--to must be fleet, herdr or both")
		}
		if t.Auth == "config" && t.To != "" {
			return p, errors.New("config-only cannot register fleet or Herdr")
		}
		if t.Port < 0 || t.Port > 65535 {
			return p, errors.New("SSH port must be 1–65535 or the default 0")
		}
		if t.AdvertisesSSH && (t.Port == 0 || t.Port == 22) && t.Auth == "key" {
			return p, errors.New("target advertises Tailscale SSH on port 22; choose existing authentication or an explicit ordinary sshd port")
		}
		if t.To != "" && t.RemoteOS != "posix" && t.RemoteOS != "windows" {
			return p, errors.New("registration requires a known target OS")
		}
	}
	p.Targets = append([]OnboardTarget{}, targets...)
	p.targets = append([]OnboardTarget{}, targets...)
	return p, nil
}

// ApplyOnboarding keeps independent hosts and providers in a partial ledger.
// The caller implements effects through existing source-owning services.
func ApplyOnboarding(ctx context.Context, p OnboardPlan, apply func(context.Context, OnboardTarget, string) error) (OnboardResult, error) {
	r := OnboardResult{SchemaVersion: 1, Kind: "ssh_onboarding_result", Status: "ready", Outcomes: []OnboardOutcome{}}
	if len(p.targets) == 0 || !reflect.DeepEqual(p.Targets, p.targets) {
		return r, errors.New("onboarding plan changed")
	}
	failed := false
	run := func(t OnboardTarget, stage string) bool {
		err := ctx.Err()
		if err == nil {
			err = apply(ctx, t, stage)
		}
		o := OnboardOutcome{Alias: t.Alias, Stage: stage, Status: "ready"}
		if err != nil {
			o.Status = "failed"
			if errors.Is(err, ErrOnboardUnknown) {
				o.Status = "unknown"
			}
			o.Error = err.Error()
			failed = true
		}
		r.Outcomes = append(r.Outcomes, o)
		return err == nil
	}
	configured := make([]bool, len(p.targets))
	authenticated := make([]bool, len(p.targets))
	// Finish all config/key mutations before recording source fingerprints. A
	// later alias in this batch must not immediately stale an earlier binding.
	for i, t := range p.targets {
		configured[i] = run(t, "configure")
	}
	for i, t := range p.targets {
		if !configured[i] {
			continue
		}
		authenticated[i] = true
		if t.Auth == "key" || t.Auth == "existing" && t.To == "" {
			authenticated[i] = run(t, "authenticate")
		}
	}
	for i, t := range p.targets {
		if !configured[i] {
			continue
		}
		bound := run(t, "bind")
		if !authenticated[i] || !bound {
			continue
		}
		if t.To == "herdr" || t.To == "both" {
			run(t, "herdr")
		}
		if t.To == "fleet" || t.To == "both" {
			run(t, "fleet")
		}
	}
	if failed {
		r.Status = "partial"
		return r, errors.New("SSH onboarding is partial; completed configuration and registrations were retained")
	}
	return r, nil
}

// SourceGuard freezes the static Include closure shown in the initial preview.
// Check is called under the ordinary SSH operation lock before configuration.
type SourceGuard struct {
	service    *sshhost.Service
	inventory  sshhost.Inventory
	files      configedit.Plan
	fileGuards map[string]configedit.Plan
}

func GuardSources(ctx context.Context, s *sshhost.Service) (SourceGuard, error) {
	g := SourceGuard{service: s}
	var err error
	g.inventory, err = s.Discover(ctx)
	if err != nil {
		return g, err
	}
	paths := []string{g.inventory.Root}
	seen := map[string]bool{g.inventory.Root: true}
	for _, f := range g.inventory.Files {
		if !seen[f.Path] {
			paths = append(paths, f.Path)
			seen[f.Path] = true
		}
	}
	g.files, err = configedit.New(ctx, nil, nil, paths)
	if err != nil {
		return g, err
	}
	g.fileGuards = map[string]configedit.Plan{}
	for _, path := range paths {
		p, e := configedit.New(ctx, nil, nil, []string{path})
		if e != nil {
			return g, e
		}
		g.fileGuards[path] = p
	}
	return g, err
}

// Advance permits only the exact local files successfully published by this
// reviewed batch to change. Remote activity never advances this authority.
func (g SourceGuard) Advance(ctx context.Context, allowed []string) (SourceGuard, error) {
	next, err := GuardSources(ctx, g.service)
	if err != nil {
		return g, err
	}
	mutable := map[string]bool{}
	for _, p := range allowed {
		mutable[p] = true
	}
	for path, guard := range g.fileGuards {
		if !mutable[path] {
			if err := guard.Check(ctx); err != nil {
				return g, err
			}
		}
	}
	for path := range next.fileGuards {
		if _, known := g.fileGuards[path]; !known && !mutable[path] {
			return g, fmt.Errorf("unexpected SSH source appeared: %w", sshhost.ErrSourceChanged)
		}
	}
	return next, nil
}
func (g SourceGuard) Check(ctx context.Context) error {
	if g.service == nil {
		return errors.New("missing SSH source guard")
	}
	if err := g.files.Check(ctx); err != nil {
		return err
	}
	current, err := g.service.Discover(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(g.inventory, current) {
		return fmt.Errorf("SSH Include closure changed since preview: %w", sshhost.ErrSourceChanged)
	}
	return nil
}
