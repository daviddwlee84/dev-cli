package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshactivity"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sshActivityStore(app *App) *sshactivity.Store {
	return sshactivity.NewStore(filepath.Join(app.Cfg.StateDir(), "ssh", "activity"))
}

func sshActivityProfile(ctx context.Context, service *sshhost.Service, alias string) (sshflow.ConnectionProfile, bool) {
	hints, err := service.ConnectionHints(ctx)
	if err != nil {
		return sshflow.ConnectionProfile{}, false
	}
	for _, hint := range hints {
		if strings.EqualFold(hint.Alias, alias) && hint.Fingerprint != "" {
			return sshflow.ConnectionProfile{ID: sshflow.ReferenceID("ssh", service.Paths().RootConfig, strings.ToLower(hint.Alias)), Alias: hint.Alias, Fingerprint: hint.Fingerprint, HostName: hint.HostName, User: hint.User, Port: hint.Port, Source: hint.Source, State: hint.State}, true
		}
	}
	return sshflow.ConnectionProfile{}, false
}

func sshUseObserver(app *App, service *sshhost.Service, expected sshflow.ConnectionProfile) func(time.Time) {
	var once sync.Once
	return func(at time.Time) {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			current, found := sshActivityProfile(ctx, service, expected.Alias)
			if !found || current != expected {
				return
			}
			if _, err := sshActivityStore(app).RecordUse(ctx, expected.ID, expected.Fingerprint, at); err != nil {
				app.warnf("SSH activity was not saved: %v", err)
			}
		})
	}
}

func decorateSSHActivityActions(state *tuiAppState, actions *tui.SSHActions) {
	actions.LoadActivity = func(ctx context.Context) (map[string]sshactivity.ProfileRecord, error) {
		active := *state.Current()
		app := &active
		service, err := app.sshHosts()
		if err != nil {
			return nil, err
		}
		hints, err := service.ConnectionHints(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(hints))
		for _, hint := range hints {
			ids = append(ids, sshflow.ReferenceID("ssh", service.Paths().RootConfig, strings.ToLower(hint.Alias)))
		}
		return sshActivityStore(app).ReadProfiles(ctx, ids)
	}
	actions.Test = func(ctx context.Context, request tui.SSHTestRequest, emit func(tui.SSHTestProgress)) (tui.SSHTestResult, error) {
		app := *state.Current()
		return sshTUITestProfiles(ctx, &app, request, emit)
	}
}

func sshTUITestProfiles(ctx context.Context, app *App, request tui.SSHTestRequest, emit func(tui.SSHTestProgress)) (tui.SSHTestResult, error) {
	seen := map[string]bool{}
	profiles := []sshflow.ConnectionProfile{}
	for _, profile := range request.Profiles {
		if profile.ID == "" || seen[profile.ID] {
			continue
		}
		seen[profile.ID] = true
		profiles = append(profiles, profile)
	}
	result := tui.SSHTestResult{Total: len(profiles)}
	// Initialize the operation-local service before workers share its read-only
	// scanner/runner; App's lazy service field must not be written concurrently.
	if _, err := app.sshHosts(); err != nil {
		return result, err
	}
	jobs := make(chan sshflow.ConnectionProfile)
	out := make(chan tui.SSHTestProgress, 4)
	var workers sync.WaitGroup
	for range min(4, len(profiles)) {
		workers.Go(func() {
			for profile := range jobs {
				if ctx.Err() != nil {
					return
				}
				out <- sshTUITestProfile(ctx, app, profile, request.Full)
			}
		})
	}
	go func() {
		defer close(jobs)
		for _, profile := range profiles {
			select {
			case jobs <- profile:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(out) }()
	for progress := range out {
		result.Completed++
		result.Outcomes = append(result.Outcomes, progress)
		if emit != nil {
			emit(progress)
		}
	}
	result.Canceled = ctx.Err() != nil
	return result, ctx.Err()
}

func sshTUITestProfile(ctx context.Context, app *App, profile sshflow.ConnectionProfile, full bool) tui.SSHTestProgress {
	progress := tui.SSHTestProgress{ProfileID: profile.ID}
	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := revalidateSSHTUIProfile(testCtx, app, profile); err != nil {
		progress.Err = err
		return progress
	}
	service, err := app.sshHosts()
	if err != nil {
		progress.Err = err
		return progress
	}
	mode := "network"
	if full {
		mode = "full"
	}
	diagnosis, testErr := service.Diagnose(testCtx, sshhost.DiagnoseRequest{Target: profile.Alias, Ping: true, NetworkOnly: !full, Timeout: 30 * time.Second})
	record := sshactivity.FromDiagnosis(profile.Fingerprint, mode, time.Now(), diagnosis)
	if ctx.Err() != nil && (errors.Is(testErr, context.Canceled) || diagnosis.Status == "incomplete") {
		record.Status = "canceled"
	}
	// Saving a canceled test has its own bounded lifetime; it cannot restart work.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer saveCancel()
	if changed := revalidateSSHTUIProfile(saveCtx, app, profile); changed != nil {
		record.Status, testErr = "stale", errors.Join(testErr, changed)
	}
	progress.Record, err = sshActivityStore(app).RecordTest(saveCtx, profile.ID, record)
	if err != nil {
		progress.Record.SchemaVersion, progress.Record.ProfileID = 1, profile.ID
		progress.Record.LastTest = &record
		if progress.Record.Fingerprint == "" {
			progress.Record.Fingerprint = profile.Fingerprint
		}
	}
	progress.Err = errors.Join(testErr, err)
	return progress
}

func recordSSHDiagnosis(ctx context.Context, app *App, service *sshhost.Service, expected sshflow.ConnectionProfile, request sshhost.DiagnoseRequest, diagnosis sshhost.Diagnosis) {
	if expected.ID == "" {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	current, found := sshActivityProfile(saveCtx, service, expected.Alias)
	mode := "full"
	if request.NetworkOnly {
		mode = "network"
	}
	record := sshactivity.FromDiagnosis(expected.Fingerprint, mode, time.Now(), diagnosis)
	if !found || current != expected {
		record.Status = "stale"
	}
	if ctx.Err() != nil && diagnosis.Status == "incomplete" {
		record.Status = "canceled"
	}
	if _, err := sshActivityStore(app).RecordTest(saveCtx, expected.ID, record); err != nil {
		app.warnf("SSH test observation was not saved: %v", err)
	}
}

func recordSSHProbe(ctx context.Context, app *App, service *sshhost.Service, expected sshflow.ConnectionProfile, result sshhost.ProbeResult) {
	status, state := "not_ready", "unknown"
	if result.Ready {
		status, state = "ready", "passed"
	}
	d := sshhost.Diagnosis{Status: status, Stages: []sshhost.DiagnosticStage{{Name: "authentication", State: state, Code: result.Code}}}
	recordSSHDiagnosis(ctx, app, service, expected, sshhost.DiagnoseRequest{Target: expected.Alias}, d)
}
