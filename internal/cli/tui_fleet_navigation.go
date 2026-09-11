package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/fleetnav"
	"github.com/daviddwlee84/dev-cli/internal/herdrremote"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	devruntime "github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func (b *tuiFleetBackend) Navigate(ctx context.Context, row tui.FleetRow) (*tui.FleetExecution, error) {
	if row.Local {
		if row.Repository == nil {
			return nil, errors.New("local host navigation belongs in REPOS")
		}
		path := row.Repository.Path
		return &tui.FleetExecution{Run: func(runCtx context.Context, in io.Reader, out, errOut io.Writer) (tui.FleetExecutionResult, error) {
			app := *b.current()
			process, err := tuiFleetProcess(runCtx, &app, "repo", "open", path)
			if err != nil {
				return tui.FleetExecutionResult{}, err
			}
			process.Stdin, process.Stdout, process.Stderr = in, out, errOut
			err = process.Run()
			return tui.FleetExecutionResult{Summary: "Returned from local repository " + path}, err
		}}, nil
	}
	request := fleetnav.Request{Host: row.Host, ExpectedEndpoint: row.EndpointID}
	if row.Repository != nil {
		repository := fleet.OpenRequest{Name: row.Repository.Display, Path: row.Repository.Path}
		if len(row.Repository.RemoteIdentities) > 0 {
			repository.RemoteIdentity = row.Repository.RemoteIdentities[0]
		}
		request.Repository = &repository
	}
	// Check the selected endpoint before suspending, and again in Resolve at
	// the terminal boundary. No repository snapshot is needed for a host row.
	if _, err := b.host(tui.FleetHostDescriptor{Key: row.HostKey, Name: row.Host, EndpointID: row.EndpointID}); err != nil {
		return nil, err
	}
	return &tui.FleetExecution{Run: func(runCtx context.Context, in io.Reader, out, errOut io.Writer) (tui.FleetExecutionResult, error) {
		app := *b.current()
		app.In, app.Out, app.Err = in, out, errOut
		request.InsideHerdr, request.NoRuntime = os.Getenv("HERDR_ENV") == "1", app.noRuntime
		backend := newTUIFleetBackend(func() *App { return &app })
		backend.run, backend.protocolRun = b.run, b.protocolRun
		result, err := newFleetNavigation(&app, backend).Navigate(runCtx, request)
		return tui.FleetExecutionResult{Summary: result.Summary, RefreshHerdr: result.RefreshHerdr()}, err
	}}, nil
}

func newFleetNavigation(app *App, backend *tuiFleetBackend) fleetnav.Service {
	// Navigation owns a real terminal; unlike the background observer, any
	// explicitly needed authentication prompt must be visible on that terminal.
	// Production callers pass a navigation-local backend, never the UI worker.
	if backend.run == nil {
		backend.run = func(ctx context.Context, host fleet.Host, args []string, options fleet.RunOptions) fleet.Result {
			return (fleet.Transport{Err: app.Err}).RunWithOptions(ctx, host, args, nil, options)
		}
	}
	checkTarget := func(ctx context.Context, target fleetnav.Target) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := backend.host(fleetDescriptor(target.Host))
		return err
	}
	return fleetnav.Service{
		Resolve: func(ctx context.Context, request fleetnav.Request) (fleetnav.Target, error) {
			cfg, err := loadFleetConfig(app)
			if err != nil {
				return fleetnav.Target{}, err
			}
			var target fleetnav.Target
			found := false
			for _, host := range cfg.Hosts {
				if host.Name == request.Host {
					target = fleetnav.Target{Host: host, EndpointID: fleet.EndpointID(host)}
					found = true
					break
				}
			}
			if !found {
				return target, fmt.Errorf("unknown fleet host %q", request.Host)
			}
			if request.ExpectedEndpoint != "" && target.EndpointID != request.ExpectedEndpoint {
				return target, errors.New("fleet host connection changed; refresh the selected host before navigation")
			}
			if request.Repository != nil {
				copy := *request.Repository
				target.Repository = &copy
			}
			if request.RepositoryQuery != "" {
				observed, err := backend.loadHost(ctx, fleetDescriptor(target.Host), fleet.RetryAuthentication)
				if err != nil {
					return target, err
				}
				if observed.State != fleet.HostOK || observed.Snapshot == nil {
					return target, fmt.Errorf("cannot resolve repository on %s: %s %s", target.Host.Name, observed.State, observed.Error)
				}
				repository, err := selectFleetRepository(observed.Snapshot.Repositories, request.RepositoryQuery)
				if err != nil {
					return target, err
				}
				selected := fleet.OpenRequest{Name: repository.Display, Path: repository.Path}
				if len(repository.RemoteIdentities) > 0 {
					selected.RemoteIdentity = repository.RemoteIdentities[0]
				}
				target.Repository = &selected
			}
			return target, nil
		},
		Capabilities: func(ctx context.Context) (fleetnav.Capabilities, error) {
			catalog, err := backend.LoadHerdr(ctx)
			capabilities := fleetnav.Capabilities{Installed: catalog.Status != "unavailable" || catalog.RemoteAvailable, RemoteAvailable: catalog.RemoteAvailable, Catalog: herdrremote.Inventory{Status: catalog.Status, Profiles: []herdrremote.Profile{}}}
			for _, p := range catalog.Profiles {
				capabilities.Catalog.Profiles = append(capabilities.Catalog.Profiles, herdrremote.Profile{ID: p.ID, Label: p.Label, Target: p.Target, Session: p.Session, Enabled: p.Enabled})
			}
			return capabilities, err
		},
		ChooseSSH: func(ctx context.Context, reason string) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			fmt.Fprintln(app.Out, reason+".")
			if !app.interactive() {
				return false, errors.New("choose SSH explicitly with --no-runtime or the host SSH action")
			}
			chosen, err := newPrompter(app).confirm("Open this host through SSH instead?", false)
			return chosen, navigationPromptError(err)
		},
		PickProfile: func(ctx context.Context, profiles []herdrremote.Profile) (herdrremote.Profile, error) {
			items := make([]picker.Item, 0, len(profiles))
			for _, p := range profiles {
				state := "disabled"
				if p.Enabled {
					state = "enabled"
				}
				items = append(items, picker.Item{Value: p.ID, Label: p.Label, Description: fmt.Sprintf("%s · session %s · %s · %s", p.Target, p.Session, state, p.ID)})
			}
			picked, err := sshPick(ctx, app, "Choose the exact Herdr profile", items, false)
			if err != nil {
				return herdrremote.Profile{}, navigationPromptError(err)
			}
			if len(picked) != 1 {
				return herdrremote.Profile{}, fleetnav.ErrCanceled
			}
			for _, p := range profiles {
				if p.ID == picked[0].Value {
					return p, nil
				}
			}
			return herdrremote.Profile{}, errors.New("invalid Herdr profile selection")
		},
		CheckTarget: checkTarget,
		CheckProfile: func(ctx context.Context, profile herdrremote.Profile) error {
			return checkFleetNavigationProfile(ctx, app, profile)
		},
		EnsureProfile: func(ctx context.Context, target fleetnav.Target, selection fleetnav.Selection) (fleetnav.EnsureResult, error) {
			return ensureFleetNavigationProfile(ctx, app, backend, target, selection)
		},
		CheckRepository: func(ctx context.Context, target fleetnav.Target, session, expected string) (fleet.HerdrRepoResult, error) {
			return runFleetNavigationRepository(ctx, app, backend, target, "check", session, expected)
		},
		PrepareRepository: func(ctx context.Context, target fleetnav.Target, session, expected string) (fleet.HerdrRepoResult, error) {
			return runFleetNavigationRepository(ctx, app, backend, target, "prepare", session, expected)
		},
		Attach: func(ctx context.Context, target fleetnav.Target, session string) error {
			if os.Getenv("HERDR_ENV") == "1" {
				return errors.New("use Herdr's native sidebar to select this machine; a nested client is not opened")
			}
			if err := checkTarget(ctx, target); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "Attach Herdr to %s, session %s. Choose the prepared repository in its native sidebar.\n", target.Host.Name, session)
			process := exec.CommandContext(ctx, "herdr", "--remote", target.Host.SSHAlias, "--session", session)
			process.Env = devruntime.HerdrSessionEnvironment(os.Environ())
			process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
			return process.Run()
		},
		SSH: func(ctx context.Context, target fleetnav.Target) error {
			if target.Repository == nil {
				return runTUIFleetSSH(ctx, app, target.Host)
			}
			// Preserve key-first authentication. A configured password source is
			// used only after a fresh ordinary SSH attempt proves it is needed.
			observed, err := backend.loadHost(ctx, fleetDescriptor(target.Host), fleet.RetryAuthentication)
			if err != nil {
				return err
			}
			if observed.State != fleet.HostOK || observed.Snapshot == nil {
				return fmt.Errorf("cannot verify the selected SSH repository on %s: %s", target.Host.Name, observed.State)
			}
			matched := false
			for _, repository := range observed.Snapshot.Repositories {
				if repository.Path != target.Repository.Path {
					continue
				}
				if target.Repository.RemoteIdentity == "" {
					matched = true
					break
				}
				for _, identity := range repository.RemoteIdentities {
					if catalog.NormalizeRemoteIdentity(identity) == catalog.NormalizeRemoteIdentity(target.Repository.RemoteIdentity) {
						matched = true
						break
					}
				}
			}
			if !matched {
				return errors.New("the selected repository identity changed before opening its SSH shell")
			}
			return (fleet.Transport{Err: app.Err}).Interactive(ctx, target.Host, []string{"fleet", "_shell", "--request", encodeOpenRequest(*target.Repository)}, observed.PasswordAuth)
		},
	}
}

func navigationPromptError(err error) error {
	if errors.Is(err, errPromptCanceled) {
		return fleetnav.ErrCanceled
	}
	return err
}

func checkFleetNavigationProfile(ctx context.Context, app *App, expected herdrremote.Profile) error {
	inv, err := (herdrremote.Service{Runner: app.sshHostRunner}).List(ctx)
	if err != nil {
		return err
	}
	expected.Selected = false
	for _, actual := range inv.Profiles {
		actual.Selected = false
		if actual == expected {
			return nil
		}
	}
	return errors.New("Herdr profile changed during navigation; choose it again")
}

func runFleetNavigationRepository(ctx context.Context, app *App, backend *tuiFleetBackend, target fleetnav.Target, phase, session, expected string) (fleet.HerdrRepoResult, error) {
	if target.Repository == nil {
		return fleet.HerdrRepoResult{}, errors.New("no repository selected")
	}
	if _, err := backend.host(fleetDescriptor(target.Host)); err != nil {
		return fleet.HerdrRepoResult{}, err
	}
	request := fleet.HerdrRepoRequest{SchemaVersion: fleet.HerdrRepoSchemaVersion, Phase: phase, Session: session, Repository: *target.Repository, ExpectedIdentity: expected}
	if err := request.Validate(); err != nil {
		return fleet.HerdrRepoResult{}, err
	}
	data, err := fleet.MarshalBounded(request, fleet.MaxHerdrRepoBytes)
	if err != nil {
		return fleet.HerdrRepoResult{}, err
	}
	// This protocol is explicit and terminal-owned. No cached observation or
	// legacy helper is a fallback for an unsupported/malformed response.
	run := backend.protocolRun
	if run == nil {
		run = (fleet.Transport{Err: app.Err, StdinLimit: fleet.MaxHerdrRepoBytes, StdoutLimit: fleet.MaxHerdrRepoBytes}).RunWithOptions
	}
	response := run(ctx, target.Host, []string{"fleet", fleet.HerdrRepoHelper}, data, fleet.RunOptions{Retry: fleet.RetryAuthentication})
	if response.ExitCode != 0 || response.CaptureError != "" {
		if response.ExitCode == 127 {
			return fleet.HerdrRepoResult{}, errors.New("remote dev is missing; install or update dev before opening a repository")
		}
		if response.TimedOut {
			return fleet.HerdrRepoResult{}, errors.New("remote Herdr repository check/preparation timed out")
		}
		return fleet.HerdrRepoResult{}, fmt.Errorf("remote dev could not %s this repository with the session-aware helper; update remote dev or inspect its native Herdr session", phase)
	}
	var result fleet.HerdrRepoResult
	if err := fleet.UnmarshalStrict(response.Stdout, fleet.MaxHerdrRepoBytes, &result); err != nil {
		return result, errors.New("invalid remote Herdr repository response")
	}
	if err := result.Validate(request); err != nil {
		return result, err
	}
	return result, nil
}

func ensureFleetNavigationProfile(ctx context.Context, app *App, backend *tuiFleetBackend, target fleetnav.Target, selection fleetnav.Selection) (fleetnav.EnsureResult, error) {
	result := fleetnav.EnsureResult{Status: "unchanged"}
	if !app.interactive() {
		return result, errors.New("enabling or adding a Herdr machine requires confirmation in a terminal")
	}
	if _, err := backend.host(fleetDescriptor(target.Host)); err != nil {
		return result, err
	}
	var ids []string
	if selection.Exists {
		service := herdrremote.Service{Runner: app.sshHostRunner}
		inv, err := service.List(ctx)
		if err != nil {
			return result, err
		}
		found := false
		for _, p := range inv.Profiles {
			p.Selected = false
			old := selection.Profile
			old.Selected = false
			if p == old {
				found = true
			}
		}
		if !found {
			return result, errors.New("selected Herdr profile changed before enable")
		}
		plan, err := service.Plan(inv, herdrremote.Request{Action: "enable", ProfileID: selection.Profile.ID})
		if err != nil {
			return result, err
		}
		fmt.Fprintf(app.Out, "Enable Herdr profile %s\nSSH target: %s\nSession: %s\nProfile ID: %s\n", selection.Profile.Label, selection.Profile.Target, selection.Profile.Session, selection.Profile.ID)
		for _, effect := range plan.Effects {
			fmt.Fprintln(app.Out, "  "+effect)
		}
		confirmed, err := newPrompter(app).confirm("Enable this saved machine?", false)
		if err != nil {
			return result, navigationPromptError(err)
		}
		if !confirmed {
			return result, fleetnav.ErrCanceled
		}
		if _, err := backend.host(fleetDescriptor(target.Host)); err != nil {
			return result, err
		}
		applied, err := service.Apply(ctx, plan, true)
		result.Status = navigationChangeStatus(applied.Status)
		ids = applied.ProfileIDs
		if err != nil {
			return result, err
		}
	} else {
		// A concurrently added alias profile must return to explicit selection,
		// including when its session differs from the proposed default.
		before, err := (herdrremote.Service{Runner: app.sshHostRunner}).List(ctx)
		if err != nil {
			return result, err
		}
		for _, profile := range before.Profiles {
			if profile.Target == target.Host.SSHAlias {
				return result, errors.New("a Herdr profile appeared for this host; choose the profile before navigation")
			}
		}
		service, err := app.sshManagement()
		if err != nil {
			return result, err
		}
		plan, err := service.Plan(ctx, sshflow.Request{Action: "register", To: "herdr", Aliases: []string{target.Host.SSHAlias}, HerdrLabel: target.Host.Name, Session: selection.Session})
		if err != nil {
			return result, err
		}
		renderSSHManagementPlan(app, plan)
		confirmed, err := newPrompter(app).confirm("Add this machine to Herdr?", false)
		if err != nil {
			return result, navigationPromptError(err)
		}
		if !confirmed {
			return result, fleetnav.ErrCanceled
		}
		if _, err := backend.host(fleetDescriptor(target.Host)); err != nil {
			return result, err
		}
		latest, err := service.Herdr.List(ctx)
		if err != nil {
			return result, err
		}
		for _, profile := range latest.Profiles {
			if profile.Target == target.Host.SSHAlias {
				return result, errors.New("a Herdr profile appeared while confirming; choose the profile before navigation")
			}
		}
		applied, err := service.Apply(ctx, plan, true)
		for _, outcome := range applied.Outcomes {
			if outcome.Target == "herdr" {
				result.Status = navigationChangeStatus(outcome.Status)
				ids = append(ids, outcome.ProfileIDs...)
			}
		}
		if err != nil {
			return result, err
		}
	}
	inv, err := (herdrremote.Service{Runner: app.sshHostRunner}).List(ctx)
	if err != nil {
		return result, err
	}
	if len(ids) != 1 {
		return result, errors.New("native Herdr did not return one exact profile")
	}
	expected := herdrremote.Profile{ID: ids[0], Target: target.Host.SSHAlias, Label: target.Host.Name, Session: selection.Session, Enabled: true}
	if selection.Exists {
		expected = selection.Profile
		expected.Enabled = true
	}
	expected.Selected = false
	for _, p := range inv.Profiles {
		candidate := p
		candidate.Selected = false
		if candidate == expected {
			result.Profile = p
			return result, nil
		}
	}
	return result, errors.New("native Herdr change could not be verified; inspect saved machines before retrying")
}

func navigationChangeStatus(status string) string {
	switch status {
	case "applied":
		return "applied"
	case "unknown":
		return "unknown"
	default:
		return "unchanged"
	}
}

func fleetActionSummary(action, host string, err error) string {
	if err != nil {
		if errors.Is(err, fleetnav.ErrCanceled) || errors.Is(err, errPromptCanceled) || errors.Is(err, context.Canceled) {
			return "Canceled Fleet action on " + host + "."
		}
		if fleetnav.IsProfileAction(action) {
			return "Herdr operation on " + host + " did not complete; saved-machine state may have changed. Refresh or inspect the native catalog."
		}
		return "Fleet action on " + host + " did not complete."
	}
	switch {
	case strings.HasPrefix(action, "herdr-disable:"):
		return "Herdr profile disabled; remote sessions keep running."
	case strings.HasPrefix(action, "herdr-remove:"):
		return "Herdr profile removed; remote sessions keep running."
	case strings.HasPrefix(action, "herdr-enable:"):
		return "Herdr profile enabled; choose the machine in the native sidebar."
	case action == "herdr-add":
		return "Herdr machine added; choose it in the native sidebar."
	case action == "authenticated-refresh":
		return "Refreshed " + host + " with authentication."
	case action == "dotfile-status":
		return "Read dotfile status on " + host + "."
	default:
		return "Returned from " + host + "."
	}
}
