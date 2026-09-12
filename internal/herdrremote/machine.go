// Package herdrremote adapts Herdr's saved-machine CLI. It never writes Herdr's
// private catalog and does not manage Git worktrees or agent processes.
package herdrremote

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

type Profile struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Target   string `json:"target"`
	Session  string `json:"session"`
	Enabled  bool   `json:"enabled"`
	Selected bool   `json:"selected"`
}
type Inventory struct {
	Status   string    `json:"status"`
	Profiles []Profile `json:"profiles"`
}
type Service struct{ Runner sshhost.Runner }

func (s Service) runner() sshhost.Runner {
	if s.Runner != nil {
		return s.Runner
	}
	return sshhost.ExecRunner{}
}
func (s Service) List(ctx context.Context) (Inventory, error) {
	inv := Inventory{Status: "unavailable", Profiles: []Profile{}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := s.runner().Run(ctx, sshhost.RunRequest{Name: "herdr", Args: []string{"machine", "list", "--json"}, Display: "Herdr saved machine inventory"})
	if err != nil {
		return inv, errors.New("Herdr machine CLI is unavailable")
	}
	if r.ExitCode != 0 {
		inv.Status = "unsupported"
		return inv, errors.New("Herdr machine list requires Herdr 0.9.0 or a compatible machine CLI")
	}
	if r.StdoutTruncated || json.Unmarshal(r.Stdout, &inv.Profiles) != nil || inv.Profiles == nil || len(inv.Profiles) > 64 {
		inv.Status = "invalid"
		inv.Profiles = []Profile{}
		return inv, errors.New("invalid Herdr machine inventory")
	}
	var required []struct {
		Enabled *bool `json:"enabled"`
	}
	if json.Unmarshal(r.Stdout, &required) != nil || len(required) != len(inv.Profiles) {
		inv.Status = "invalid"
		inv.Profiles = []Profile{}
		return inv, errors.New("invalid Herdr machine inventory")
	}
	for _, row := range required {
		if row.Enabled == nil {
			inv.Status = "invalid"
			inv.Profiles = []Profile{}
			return inv, errors.New("Herdr inventory omits enabled state")
		}
	}
	seen := map[string]bool{}
	for _, p := range inv.Profiles {
		if !validID(p.ID) || seen[p.ID] || !validTarget(p.Target) || !validText(p.Session, 128) || !validText(p.Label, 128) {
			inv.Status = "invalid"
			inv.Profiles = []Profile{}
			return inv, errors.New("invalid Herdr profile fields")
		}
		seen[p.ID] = true
	}
	inv.Status = "ready"
	return inv, nil
}
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}
func validText(s string, n int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= n && !strings.ContainsFunc(s, unicode.IsControl)
}

type Request struct {
	Action    string `json:"action"`
	Alias     string `json:"alias,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	Label     string `json:"label,omitempty"`
	Session   string `json:"session,omitempty"`
}
type Plan struct {
	Request  Request  `json:"request"`
	Status   string   `json:"status"`
	Effects  []string `json:"effects,omitempty"`
	request  Request
	expected []Profile
	noop     bool
}

func (s Service) Plan(inv Inventory, r Request) (Plan, error) {
	p := Plan{Request: r, Status: "planned", request: r}
	if inv.Status != "ready" {
		return p, errors.New("Herdr machine inventory is unavailable")
	}
	switch r.Action {
	case "register":
		if err := sshhost.ValidateLookupAlias(r.Alias); err != nil {
			return p, err
		}
		if r.Session == "" {
			r.Session = "default"
		}
		if r.Label == "" {
			r.Label = r.Alias
		}
		if !validText(r.Label, 128) || !validText(r.Session, 128) {
			return p, errors.New("invalid Herdr label or session")
		}
		p.expected = matching(inv.Profiles, r)
		if len(p.expected) > 0 {
			p.Status = "noop"
		} else {
			p.Effects = []string{"Connect through SSH; prepare installation and start the remote Herdr server", "Save an enabled machine; open Herdr clients connect automatically", "Native installation or server replacement approval may be required"}
		}
	case "rename", "remove", "enable", "disable":
		if !validID(r.ProfileID) {
			return p, errors.New("select an exact Herdr profile ID")
		}
		p.expected = matching(inv.Profiles, r)
		if len(p.expected) != 1 {
			return p, errors.New("Herdr profile not found")
		}
		old := p.expected[0]
		if r.Action == "rename" {
			if !validText(r.Label, 128) {
				return p, errors.New("invalid Herdr label")
			}
			if old.Label == r.Label {
				p.Status = "noop"
			}
		}
		if r.Action == "enable" && old.Enabled || r.Action == "disable" && !old.Enabled {
			p.Status = "noop"
		}
		if r.Action == "enable" {
			p.Effects = []string{"Enable this saved machine; open clients reconnect through SSH"}
		}
		if r.Action == "remove" || r.Action == "disable" {
			p.Effects = []string{"Detach this machine from local clients; remote sessions keep running"}
		}
	default:
		return p, errors.New("unsupported Herdr machine action")
	}
	p.Request = r
	p.noop = p.Status == "noop"
	p.request = r
	return p, nil
}
func matching(profiles []Profile, r Request) []Profile {
	var found []Profile
	for _, p := range profiles {
		if r.Action == "register" && p.Target == r.Alias && p.Session == r.Session || r.Action != "register" && p.ID == r.ProfileID {
			p.Selected = false
			found = append(found, p)
		}
	}
	return found
}
func (s Service) Check(ctx context.Context, p Plan) error {
	inv, err := s.List(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(matching(inv.Profiles, p.request), p.expected) {
		return errors.New("Herdr profiles changed since planning")
	}
	return nil
}

type Result struct {
	Status     string   `json:"status"`
	ProfileIDs []string `json:"profile_ids,omitempty"`
}

func (s Service) Apply(ctx context.Context, p Plan, interactive bool) (Result, error) {
	result := Result{Status: "not_run"}
	if p.request.Action == "" {
		return result, errors.New("invalid Herdr machine plan")
	}
	if err := s.Check(ctx, p); err != nil {
		return result, err
	}
	r := p.request
	if p.noop {
		result.Status = "noop"
		for _, x := range p.expected {
			result.ProfileIDs = append(result.ProfileIDs, x.ID)
		}
		return result, nil
	}
	action := r.Action
	args := []string{"machine", action, r.ProfileID}
	if action == "register" {
		args = []string{"machine", "add", r.Alias, "--label", r.Label, "--remote-session", r.Session}
	}
	if action == "rename" {
		args = append(args, "--label", r.Label)
	}
	run, err := s.runner().Run(ctx, sshhost.RunRequest{Name: "herdr", Args: args, Interactive: interactive, Display: "Herdr machine " + action})
	result.Status = "unknown"
	if err != nil {
		return result, errors.New("Herdr operation interrupted; remote preparation may have occurred")
	}
	after, listErr := s.List(ctx)
	if listErr != nil {
		return result, listErr
	}
	matches := matching(after.Profiles, r)
	for _, x := range matches {
		result.ProfileIDs = append(result.ProfileIDs, x.ID)
	}
	if run.ExitCode != 0 {
		return result, fmt.Errorf("Herdr operation exited %d; inspect native machine state before retrying", run.ExitCode)
	}
	ok := false
	switch action {
	case "register":
		ok = len(matches) == 1 && matches[0].Label == r.Label && matches[0].Enabled
	case "remove":
		ok = len(matches) == 0
	case "rename", "enable", "disable":
		if len(matches) == 1 && len(p.expected) == 1 {
			// Only the selected field may change. A successful native command
			// must not turn a concurrent retarget/session/label change into a
			// verified result. matching already ignores client-only Selected.
			expected := p.expected[0]
			switch action {
			case "rename":
				expected.Label = r.Label
			case "enable":
				expected.Enabled = true
			case "disable":
				expected.Enabled = false
			}
			ok = reflect.DeepEqual(matches[0], expected)
		}
	}
	if !ok {
		return result, errors.New("Herdr result could not be verified")
	}
	result.Status = "applied"
	return result, nil
}

func validTarget(target string) bool {
	if !validText(target, 1024) {
		return false
	}
	authority := strings.TrimPrefix(target, "ssh://")
	if user, _, ok := strings.Cut(authority, "@"); ok && strings.Contains(user, ":") {
		return false
	}
	return true
}
