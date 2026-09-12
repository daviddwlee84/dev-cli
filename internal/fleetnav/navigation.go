// Package fleetnav owns navigation ordering across host-local fleet records,
// saved Herdr machines, checked remote repositories, and terminal handoff.
// The adapters retain native prompts and never replace selection with a
// session-wide focus operation.
package fleetnav

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
)

var ErrCanceled = errors.New("fleet navigation canceled")

type Request struct {
	Host             string
	ExpectedEndpoint string
	Repository       *fleet.OpenRequest
	RepositoryQuery  string
	InsideHerdr      bool
	NoRuntime        bool
}

type Target struct {
	Host       fleet.Host
	EndpointID string
	Repository *fleet.OpenRequest
}

type Capabilities struct {
	Installed       bool
	RemoteAvailable bool
	Catalog         herdrremote.Inventory
}

type Selection struct {
	Exists  bool
	Profile herdrremote.Profile
	Session string
}

type EnsureResult struct {
	Status  string // unchanged, applied, or unknown; retained even when Err != nil
	Profile herdrremote.Profile
}

type Result struct {
	State, Stage, Summary         string
	Host, ProfileID, ProfileLabel string
	Transport, Session            string
	CatalogState                  string
	CatalogAction                 string
	RepositoryState               string
	Path, Workspace               string
}

func (r Result) RefreshHerdr() bool { return r.Transport == "herdr" }

// Service has no UI or subprocess policy. Every operation uses the immutable
// target selected at Resolve; checks and native authority remain adapter-owned.
type Service struct {
	Resolve           func(context.Context, Request) (Target, error)
	Capabilities      func(context.Context) (Capabilities, error)
	ChooseSSH         func(context.Context, string) (bool, error)
	PickProfile       func(context.Context, []herdrremote.Profile) (herdrremote.Profile, error)
	CheckTarget       func(context.Context, Target) error
	CheckProfile      func(context.Context, herdrremote.Profile) error
	EnsureProfile     func(context.Context, Target, Selection) (EnsureResult, error)
	CheckRepository   func(context.Context, Target, string, string) (fleet.HerdrRepoResult, error)
	PrepareRepository func(context.Context, Target, string, string) (fleet.HerdrRepoResult, error)
	Attach            func(context.Context, Target, string) error
	SSH               func(context.Context, Target) error
}

func HerdrTargetReason(host fleet.Host) string {
	if host.EffectiveRemoteOS() == fleet.RemoteOSWindows {
		return "Herdr remote servers require Linux or macOS"
	}
	if host.SSHAlias == "" || host.User != "" || host.Port != 0 || host.IdentityFile != "" {
		return "Herdr requires an SSH alias containing all connection settings"
	}
	if host.PasswordKind() != "none" {
		return "Herdr uses native OpenSSH authentication; fleet password sources are not forwarded"
	}
	return ""
}

func (s Service) Navigate(ctx context.Context, request Request) (Result, error) {
	result := Result{State: "failed", Stage: "resolve", CatalogState: "unchanged", RepositoryState: "not-requested"}
	fail := func(err error) (Result, error) {
		if errors.Is(err, ErrCanceled) || errors.Is(err, context.Canceled) {
			result.State = "canceled"
		} else {
			result.State = "failed"
		}
		if result.CatalogState == "applied" || result.CatalogState == "unknown" || result.RepositoryState == "prepared" || result.RepositoryState == "unknown" {
			result.State = "partial"
		}
		catalog := result.CatalogState
		if result.CatalogAction != "" {
			catalog = result.CatalogAction + " " + catalog
		}
		result.Summary = fmt.Sprintf("Fleet navigation on %s %s at %s (session %s; Herdr catalog: %s; repository: %s).", result.Host, result.State, result.Stage, result.Session, catalog, result.RepositoryState)
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	target, err := s.Resolve(ctx, request)
	if err != nil {
		return fail(err)
	}
	result.Host = target.Host.Name
	ssh := func() (Result, error) {
		result.Transport, result.Stage = "ssh", "ssh"
		if err := s.CheckTarget(ctx, target); err != nil {
			return fail(err)
		}
		if err := s.SSH(ctx, target); err != nil {
			return fail(err)
		}
		result.State, result.Summary = "returned", "Returned from SSH on "+target.Host.Name+"."
		return result, nil
	}
	if request.NoRuntime {
		return ssh()
	}
	result.Stage = "capabilities"
	capabilities, capabilityErr := s.Capabilities(ctx)
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	reason := HerdrTargetReason(target.Host)
	if reason == "" && !capabilities.Installed {
		reason = "Herdr is not installed"
	}
	if reason == "" && !request.InsideHerdr && !capabilities.RemoteAvailable {
		reason = "this Herdr binary does not support native remote attachment"
	}
	if reason == "" && capabilities.Catalog.Status == "unsupported" {
		reason = "this Herdr binary does not support the saved-machine catalog required for exact session selection"
	}
	if reason != "" {
		chosen, err := s.ChooseSSH(ctx, reason)
		if err != nil {
			return fail(err)
		}
		if !chosen {
			return fail(ErrCanceled)
		}
		return ssh()
	}
	result.Transport = "herdr"
	if capabilityErr != nil || capabilities.Catalog.Status != "ready" {
		return fail(errors.New("Herdr saved machines are unavailable; refresh the catalog or choose SSH explicitly"))
	}
	result.Stage = "select-profile"
	selection := Selection{Session: "default"}
	var profiles []herdrremote.Profile
	if capabilities.Catalog.Status == "ready" {
		for _, profile := range capabilities.Catalog.Profiles {
			if profile.Target == target.Host.SSHAlias {
				profiles = append(profiles, profile)
			}
		}
	}
	if len(profiles) > 0 {
		selected := profiles[0]
		if len(profiles) > 1 {
			selected, err = s.PickProfile(ctx, append([]herdrremote.Profile(nil), profiles...))
			if err != nil {
				return fail(err)
			}
		}
		matched := false
		for _, profile := range profiles {
			if sameProfile(profile, selected) {
				selection = Selection{Exists: true, Profile: profile, Session: profile.Session}
				matched = true
				break
			}
		}
		if !matched {
			return fail(errors.New("the selected Herdr profile is no longer in the reviewed catalog"))
		}
	}
	result.Session = selection.Session
	if selection.Exists {
		result.ProfileID, result.ProfileLabel = selection.Profile.ID, selection.Profile.Label
	}
	var checked fleet.HerdrRepoResult
	if target.Repository != nil {
		result.Stage = "check-repository"
		checked, err = s.CheckRepository(ctx, target, selection.Session, "")
		if err != nil {
			return fail(err)
		}
		result.Path, result.RepositoryState = checked.Path, "checked"
		if !request.InsideHerdr && checked.RuntimeState != "ready" {
			return fail(fmt.Errorf("Herdr session %q is not ready (%s); use host Enter / Connect with Herdr first, then retry the repository", selection.Session, checked.RuntimeState))
		}
	}
	if err := s.CheckTarget(ctx, target); err != nil {
		return fail(err)
	}
	if request.InsideHerdr && (!selection.Exists || !selection.Profile.Enabled) {
		result.Stage = "ensure-profile"
		result.CatalogAction = "register"
		if selection.Exists {
			result.CatalogAction = "enable"
		}
		ensured, err := s.EnsureProfile(ctx, target, selection)
		if ensured.Status != "" {
			result.CatalogState = ensured.Status
		}
		if err != nil {
			return fail(err)
		}
		if ensured.Profile.ID == "" || ensured.Profile.Target != target.Host.SSHAlias || ensured.Profile.Session != selection.Session || !ensured.Profile.Enabled {
			return fail(errors.New("the enabled Herdr profile could not be verified"))
		}
		if selection.Exists {
			expected := selection.Profile
			expected.Enabled = true
			if !sameProfile(expected, ensured.Profile) {
				return fail(errors.New("native enable returned a different Herdr profile than the selected record"))
			}
		}
		selection.Exists, selection.Profile = true, ensured.Profile
		result.ProfileID, result.ProfileLabel = ensured.Profile.ID, ensured.Profile.Label
	}
	if selection.Exists {
		if err := s.CheckProfile(ctx, selection.Profile); err != nil {
			return fail(err)
		}
	}
	if target.Repository != nil {
		result.Stage = "recheck-repository"
		if err := s.CheckTarget(ctx, target); err != nil {
			return fail(err)
		}
		if request.InsideHerdr {
			checked, err = s.CheckRepository(ctx, target, selection.Session, checked.RepositoryIdentity)
			if err != nil {
				return fail(err)
			}
		}
		if checked.RuntimeState != "ready" {
			if result.CatalogAction == "enable" && result.CatalogState == "applied" {
				return fail(fmt.Errorf("Herdr profile is enabled, but session %q is not ready yet (%s); select the machine in the native sidebar, then retry Enter", selection.Session, checked.RuntimeState))
			}
			return fail(fmt.Errorf("Herdr session %q is not ready (%s); open its native machine connection, then retry the repository", selection.Session, checked.RuntimeState))
		}
		result.Stage, result.RepositoryState = "prepare-repository", "unknown"
		prepared, err := s.PrepareRepository(ctx, target, selection.Session, checked.RepositoryIdentity)
		if err != nil {
			return fail(err)
		}
		result.RepositoryState, result.Path, result.Workspace = "prepared", prepared.Path, prepared.Workspace
	}
	result.Stage = "finish"
	if err := s.CheckTarget(ctx, target); err != nil {
		return fail(err)
	}
	if selection.Exists {
		if err := s.CheckProfile(ctx, selection.Profile); err != nil {
			return fail(err)
		}
	}
	if request.InsideHerdr {
		result.State = "sidebar-ready"
		label := selection.Profile.Label
		if label == "" {
			label = target.Host.SSHAlias
		}
		if result.Workspace != "" {
			result.Summary = fmt.Sprintf("Prepared %s in Herdr session %s. Select %s, then workspace %s in the native sidebar.", result.Path, selection.Session, label, result.Workspace)
		} else {
			result.Summary = fmt.Sprintf("Herdr entry %s is enabled for session %s. Select that machine in the native sidebar.", label, selection.Session)
		}
		return result, nil
	}
	result.Stage = "attach"
	if err := s.Attach(ctx, target, selection.Session); err != nil {
		return fail(err)
	}
	result.State = "returned"
	result.Summary = fmt.Sprintf("Returned from Herdr on %s (session %s).", target.Host.Name, selection.Session)
	if result.Workspace != "" {
		result.Summary += fmt.Sprintf(" Prepared workspace %s; selection remains in the native sidebar.", result.Workspace)
	}
	return result, nil
}

func sameProfile(a, b herdrremote.Profile) bool {
	a.Selected, b.Selected = false, false
	return a == b
}

func IsProfileAction(action string) bool {
	return action == "herdr-add" || strings.HasPrefix(action, "herdr-enable:") || strings.HasPrefix(action, "herdr-disable:") || strings.HasPrefix(action, "herdr-remove:")
}
