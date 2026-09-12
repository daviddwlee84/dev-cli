// Package sshflow joins local SSH, fleet and Herdr records and owns explicit
// management plans. Listing and planning never authenticate or bootstrap hosts.
package sshflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type FleetHost struct {
	Name     string `json:"name"`
	Alias    string `json:"ssh_alias,omitempty"`
	OS       string `json:"remote_os"`
	Source   string `json:"source"`
	Managed  bool   `json:"managed"`
	HostName string `json:"host_name,omitempty"`
	User     string `json:"user,omitempty"`
	Port     int    `json:"port,omitempty"`
}
type Alias struct {
	Name     string             `json:"name"`
	Status   string             `json:"status"`
	Patterns []string           `json:"patterns"`
	Sources  []sshhost.Location `json:"sources"`
}
type Inventory struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	SSH           sshhost.Inventory     `json:"-"`
	Root          string                `json:"root"`
	Complete      bool                  `json:"complete"`
	Aliases       []Alias               `json:"aliases"`
	Fleet         []FleetHost           `json:"fleet"`
	FleetStatus   string                `json:"fleet_status"`
	Herdr         herdrremote.Inventory `json:"herdr"`
}
type Service struct {
	SSH          *sshhost.Service
	Herdr        herdrremote.Service
	FleetPath    string
	RecoveryPath string
}

func (s Service) List(ctx context.Context) (Inventory, error) {
	inv := Inventory{SchemaVersion: 1, Kind: "ssh_management_inventory", Fleet: []FleetHost{}, FleetStatus: "ready"}
	var err error
	inv.SSH, err = s.SSH.Discover(ctx)
	if err != nil {
		return inv, err
	}
	inv.Root = inv.SSH.Root
	inv.Complete = inv.SSH.Complete
	inv.Aliases = []Alias{}
	for _, a := range inv.SSH.Aliases {
		row := Alias{Name: a.Name, Status: "active", Patterns: []string{}, Sources: []sshhost.Location{}}
		if a.Conflict {
			row.Status = "conflict"
		}
		for _, d := range a.Definitions {
			row.Patterns = append(row.Patterns, d.Patterns...)
			row.Sources = append(row.Sources, d.Source)
			if d.Reachability != sshhost.Reachable {
				row.Status = string(d.Reachability)
			}
		}
		inv.Aliases = append(inv.Aliases, row)
	}
	cfg, err := fleet.LoadConfig(s.FleetPath)
	if err != nil {
		inv.FleetStatus = "unavailable"
	} else {
		for _, h := range cfg.Hosts {
			inv.Fleet = append(inv.Fleet, FleetHost{Name: h.Name, Alias: h.SSHAlias, OS: h.EffectiveRemoteOS(), Source: h.Origin(), Managed: h.Managed(), HostName: h.Hostname, User: h.User, Port: h.Port})
		}
	}
	inv.Herdr, _ = s.Herdr.List(ctx)
	return inv, nil
}

type Request struct {
	Action        string   `json:"action"`
	To            string   `json:"to,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
	FleetHosts    []string `json:"fleet_hosts,omitempty"`
	HerdrProfiles []string `json:"herdr_profiles,omitempty"`
	Name          string   `json:"name,omitempty"`
	FleetName     string   `json:"fleet_name,omitempty"`
	HerdrLabel    string   `json:"herdr_label,omitempty"`
	Session       string   `json:"session,omitempty"`
	RemoteOS      string   `json:"remote_os,omitempty"`
}
type Operation struct {
	Target            string              `json:"target"`
	Identity          string              `json:"identity"`
	Action            string              `json:"action"`
	Status            string              `json:"status"`
	Reason            string              `json:"reason,omitempty"`
	Effects           []string            `json:"effects,omitempty"`
	Herdr             *herdrremote.Plan   `json:"herdr,omitempty"`
	Fleet             *fleet.HostEditPlan `json:"fleet,omitempty"`
	FleetRegistration *fleet.ManagedHost  `json:"fleet_registration,omitempty"`
	registration      *fleet.ManagedHost
	existing          *FleetHost
	request           Request
}
type Plan struct {
	SchemaVersion int         `json:"schema_version"`
	Kind          string      `json:"kind"`
	Operations    []Operation `json:"operations"`
	operations    []Operation
	guards        configedit.Plan
	sshSnapshot   *sshhost.Inventory
}
type Outcome struct {
	Target     string   `json:"target"`
	Identity   string   `json:"identity"`
	Action     string   `json:"action"`
	Status     string   `json:"status"`
	Error      string   `json:"error,omitempty"`
	Receipt    string   `json:"receipt,omitempty"`
	ProfileIDs []string `json:"profile_ids,omitempty"`
}
type Result struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	Outcomes      []Outcome `json:"outcomes"`
}

func (s Service) Plan(ctx context.Context, r Request) (Plan, error) {
	p := Plan{SchemaVersion: 1, Kind: "ssh_management_plan", Operations: []Operation{}}
	if s.SSH == nil {
		return p, errors.New("SSH service is missing")
	}
	switch r.Action {
	case "register", "probe", "rename", "remove", "enable", "disable":
	default:
		return p, errors.New("action must be register, probe, rename, remove, enable or disable")
	}
	if r.Action != "register" && (r.To != "" || r.FleetName != "" || r.HerdrLabel != "" || r.RemoteOS != "" || r.Session != "" && r.Session != "default") {
		return p, errors.New("registration flags require --action register")
	}
	if r.Action != "rename" && r.Name != "" {
		return p, errors.New("--name requires --action rename")
	}
	if r.Action == "register" && (r.To != "fleet" && r.To != "herdr" && r.To != "both") {
		return p, errors.New("register requires --to fleet, herdr or both")
	}
	if r.Action == "register" || r.Action == "probe" {
		if len(r.Aliases) == 0 || len(r.FleetHosts)+len(r.HerdrProfiles) > 0 {
			return p, errors.New("register/probe requires SSH aliases only")
		}
	} else if len(r.Aliases) > 0 || len(r.FleetHosts)+len(r.HerdrProfiles) == 0 {
		return p, errors.New("select exact fleet hosts or Herdr profile IDs")
	}
	if r.Action == "rename" && (len(r.FleetHosts)+len(r.HerdrProfiles) != 1 || r.Name == "") {
		return p, errors.New("rename requires exactly one record and --name")
	}
	if (r.Action == "enable" || r.Action == "disable") && len(r.FleetHosts) > 0 {
		return p, errors.New("enable/disable applies to Herdr profiles only")
	}
	if len(r.Aliases) > 1 && (r.FleetName != "" || r.HerdrLabel != "") {
		return p, errors.New("custom registration names require one SSH alias")
	}
	if r.Session == "" {
		r.Session = "default"
	}
	inv, err := s.List(ctx)
	if err != nil {
		return p, err
	}
	appendOp := func(o Operation) {
		p.operations = append(p.operations, o)
		public := o
		if o.Herdr != nil {
			copy := *o.Herdr
			public.Herdr = &copy
		}
		if o.Fleet != nil {
			copy := *o.Fleet
			public.Fleet = &copy
		}
		p.Operations = append(p.Operations, public)
	}
	seen := map[string]bool{}
	for _, alias := range r.Aliases {
		if err := sshhost.ValidateLookupAlias(alias); err != nil {
			return p, err
		}
		key := strings.ToLower(alias)
		if seen[key] {
			return p, errors.New("duplicate SSH alias selection")
		}
		seen[key] = true
		a, found := inv.SSH.Find(alias)
		ready := found && inv.SSH.Complete && !a.Conflict && len(a.Definitions) == 1 && a.Definitions[0].Reachability == sshhost.Reachable
		if !ready {
			appendOp(Operation{Target: "ssh", Identity: alias, Action: r.Action, Status: "blocked", Reason: "SSH alias is ambiguous, inactive, unknown or incompletely observed"})
			continue
		}
		if r.Action == "probe" {
			appendOp(Operation{Target: "ssh", Identity: alias, Action: "probe", Status: "planned"})
			continue
		}
		if r.To == "fleet" || r.To == "both" {
			op := Operation{Target: "fleet", Identity: alias, Action: "register", Status: "planned", request: r}
			if inv.FleetStatus != "ready" {
				op.Status = "blocked"
				op.Reason = "fleet configuration is unavailable"
			} else {
				for _, h := range inv.Fleet {
					if strings.EqualFold(h.Alias, alias) {
						op.Status = "noop"
						op.Identity = h.Name
						copy := h
						op.existing = &copy
					}
				}
				if op.Status != "noop" {
					name := r.FleetName
					if name == "" {
						name = alias
					}
					host := fleet.ManagedHost{Name: name, SSHAlias: alias, RemoteOS: r.RemoteOS}
					if _, err := fleet.RenderManagedFragment(host); err != nil {
						op.Status = "blocked"
						op.Reason = "fleet registration requires a supported exact alias and --target-os posix or windows"
					}
					for _, h := range inv.Fleet {
						if h.Name == name {
							op.Status = "blocked"
							op.Reason = "fleet name belongs to another endpoint"
						}
					}
					op.registration = &host
					preview := host
					op.FleetRegistration = &preview
					op.Effects = []string{"Verify a fresh ordinary SSH login; save an alias-based fleet registration"}
				}
			}
			appendOp(op)
		}
		if r.To == "herdr" || r.To == "both" {
			op := Operation{Target: "herdr", Identity: alias, Action: "register", Status: "planned", request: r}
			if r.RemoteOS == "windows" {
				op.Status = "blocked"
				op.Reason = "Herdr remote servers support Linux/macOS"
			} else {
				native, err := s.Herdr.Plan(inv.Herdr, herdrremote.Request{Action: "register", Alias: alias, Label: r.HerdrLabel, Session: r.Session})
				if err != nil {
					op.Status = "blocked"
					op.Reason = err.Error()
				} else {
					op.Herdr = &native
					op.Status = native.Status
					op.Effects = native.Effects
				}
			}
			appendOp(op)
		}
	}
	if r.Action == "remove" && len(r.FleetHosts) > 1 {
		edits, e := fleet.PlanHostRemovals(ctx, s.FleetPath, r.FleetHosts)
		if e != nil {
			return p, e
		}
		for _, edit := range edits {
			copy := edit
			appendOp(Operation{Target: "fleet", Identity: edit.Name, Action: "remove", Status: "planned", Fleet: &copy})
		}
		r.FleetHosts = nil
	}
	for _, name := range r.FleetHosts {
		if seen["fleet:"+name] {
			return p, errors.New("duplicate fleet selection")
		}
		seen["fleet:"+name] = true
		op := Operation{Target: "fleet", Identity: name, Action: r.Action, Status: "planned"}
		edit, err := fleet.PlanHostEdit(ctx, s.FleetPath, name, r.Action, r.Name)
		if err != nil {
			op.Status = "blocked"
			op.Reason = err.Error()
		} else {
			op.Fleet = &edit
			if edit.Files.Empty() {
				op.Status = "noop"
			}
		}
		appendOp(op)
	}
	for _, id := range r.HerdrProfiles {
		if seen["herdr:"+id] {
			return p, errors.New("duplicate Herdr selection")
		}
		seen["herdr:"+id] = true
		op := Operation{Target: "herdr", Identity: id, Action: r.Action, Status: "planned"}
		native, err := s.Herdr.Plan(inv.Herdr, herdrremote.Request{Action: r.Action, ProfileID: id, Label: r.Name})
		if err != nil {
			op.Status = "blocked"
			op.Reason = err.Error()
		} else {
			op.Herdr = &native
			op.Status = native.Status
			op.Effects = native.Effects
		}
		appendOp(op)
	}
	// Capture the complete active SSH source closure only for actions using SSH.
	if len(r.Aliases) > 0 {
		var paths []string
		seenPaths := map[string]bool{}
		for _, f := range inv.SSH.Files {
			if !seenPaths[f.Path] {
				paths = append(paths, f.Path)
				seenPaths[f.Path] = true
			}
		}
		p.sshSnapshot = &inv.SSH
		p.guards, err = configedit.New(ctx, nil, nil, paths)
		if err != nil {
			return p, err
		}
		after, err := s.SSH.Discover(ctx)
		if err != nil {
			return p, err
		}
		if !reflect.DeepEqual(inv.SSH, after) {
			return p, configedit.ErrStale
		}
	}
	return p, nil
}

func (s Service) Apply(ctx context.Context, p Plan, interactive bool) (Result, error) {
	result := Result{SchemaVersion: 1, Kind: "ssh_management_result", Status: "not_run", Outcomes: []Outcome{}}
	if p.operations == nil {
		return result, errors.New("invalid management plan")
	}
	// All source authority must still match before the first effect.
	if err := s.checkAuthority(ctx, p); err != nil {
		return result, err
	}
	for _, o := range p.operations {
		if o.Status == "blocked" {
			continue
		}
		if o.existing != nil {
			if err := s.checkMembership(ctx, *o.existing); err != nil {
				return result, err
			}
		}
		if o.Fleet != nil {
			if err := o.Fleet.Files.Check(ctx); err != nil {
				return result, err
			}
		}
		if o.Herdr != nil {
			if err := s.Herdr.Check(ctx, *o.Herdr); err != nil {
				return result, err
			}
		}
	}
	result.Status = "applied"
	var failures []error
	for _, o := range p.operations {
		out := Outcome{Target: o.Target, Identity: o.Identity, Action: o.Action, Status: o.Status}
		var err error
		if o.Status == "blocked" {
			out.Error = o.Reason
			failures = append(failures, errors.New(o.Reason))
			result.Outcomes = append(result.Outcomes, out)
			continue
		}
		if o.Status == "noop" && o.Herdr == nil {
			if o.existing != nil {
				if err := s.checkMembership(ctx, *o.existing); err != nil {
					out.Status = "not_run"
					out.Error = err.Error()
					failures = append(failures, err)
				}
			}
			result.Outcomes = append(result.Outcomes, out)
			continue
		}
		if err = ctx.Err(); err != nil {
			out.Status = "not_run"
		} else if err = s.checkAuthority(ctx, p); err != nil {
			out.Status = "not_run"
		} else {
			switch {
			case o.Target == "ssh":
				probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				proof, e := s.SSH.Probe(probeCtx, o.Identity)
				cancel()
				err = e
				out.Status = "failed"
				if err == nil && proof.Ready {
					out.Status = "ready"
				} else if err == nil {
					err = errors.New("fresh SSH login was not ready")
				}
			case o.Herdr != nil:
				native, e := s.Herdr.Apply(ctx, *o.Herdr, interactive)
				err = e
				out.Status = native.Status
				out.ProfileIDs = native.ProfileIDs
			case o.Fleet != nil:
				edited, e := fleet.ApplyHostEdit(ctx, *o.Fleet, s.RecoveryPath)
				err = e
				out.Status = edited.Status
				out.Receipt = edited.Receipt
			case o.registration != nil:
				// Recheck merged membership immediately before a fresh proof and write.
				cfg, e := fleet.LoadConfig(s.FleetPath)
				err = e
				if err == nil {
					for _, h := range cfg.Hosts {
						if h.Name == o.registration.Name || strings.EqualFold(h.SSHAlias, o.registration.SSHAlias) {
							err = configedit.ErrStale
							break
						}
					}
				}
				out.Status = "failed"
				if err == nil {
					probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					proof, e := s.SSH.Probe(probeCtx, o.registration.SSHAlias)
					cancel()
					err = e
					if err == nil && !proof.Ready {
						err = errors.New("fresh SSH login failed; use dev ssh setup if key bootstrap is needed")
					}
				}
				if err == nil {
					err = s.checkAuthority(ctx, p)
				}
				if err == nil {
					_, err = fleet.WriteManagedFragment(ctx, s.FleetPath, *o.registration, nil)
				}
				if err == nil {
					out.Status = "applied"
				}
			default:
				err = errors.New("invalid management operation")
				out.Status = "not_run"
			}
		}
		if err != nil {
			if out.Status == "planned" || out.Status == "" {
				out.Status = "failed"
			}
			out.Error = err.Error()
			failures = append(failures, fmt.Errorf("%s %s: %w", o.Target, o.Identity, err))
		}
		result.Outcomes = append(result.Outcomes, out)
	}
	if len(failures) > 0 {
		result.Status = "partial"
	}
	return result, errors.Join(failures...)
}

func RecoveryPath(dataHome string) string { return filepath.Join(dataHome, "dev", "ssh-recovery") }

func (s Service) checkAuthority(ctx context.Context, p Plan) error {
	if err := p.guards.Check(ctx); err != nil {
		return err
	}
	if p.sshSnapshot != nil {
		now, err := s.SSH.Discover(ctx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(now, *p.sshSnapshot) {
			return configedit.ErrStale
		}
	}
	return nil
}

func (s Service) checkMembership(ctx context.Context, expected FleetHost) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := fleet.LoadConfig(s.FleetPath)
	if err != nil {
		return err
	}
	for _, h := range cfg.Hosts {
		got := FleetHost{Name: h.Name, Alias: h.SSHAlias, OS: h.EffectiveRemoteOS(), Source: h.Origin(), Managed: h.Managed(), HostName: h.Hostname, User: h.User, Port: h.Port}
		if got == expected {
			return nil
		}
	}
	return configedit.ErrStale
}
