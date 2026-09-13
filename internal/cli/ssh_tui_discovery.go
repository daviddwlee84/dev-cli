package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

// loadSSHTUIInventory keeps readable sources visible when an independent source
// fails. Reports owned by the running dashboard take precedence over cache; the
// cache is never required for passing completed observations back into the UI.
func loadSSHTUIInventory(ctx context.Context, app *App, memory []sshdiscovery.Report) (sshflow.MachineInventory, error) {
	management, err := app.sshManagement()
	if err != nil {
		return sshflow.MachineInventory{}, err
	}
	local, localErr := management.List(ctx)
	hints, hintErr := management.SSH.ConnectionHints(ctx)
	registry, registryErr := app.machineStore().Read(ctx)
	if registryErr != nil {
		registry = machineregistry.Snapshot{}
	}
	cached, cacheErr := sshdiscovery.ReadCache(ctx, sshDiscoveryCacheDir(), nowSSH())
	reports := mergeSSHTUIReports(cached, memory, nowSSH())
	out := sshflow.JoinMachinesWithRegistryStatus(local, hints, registry, reports, fleetConfigPath(app), filepath.Dir(management.SSH.Paths().RootConfig), nowSSH(), registryErr == nil)
	if localErr != nil || hintErr != nil {
		out.Sources["ssh"] = "unavailable"
		out.Complete = false
	}
	if cacheErr != nil {
		out.Sources["cache"] = "unavailable"
		out.Complete = false
	}
	return out, errors.Join(localErr, hintErr, registryErr, cacheErr)
}

func mergeSSHTUIReports(cached, memory []sshdiscovery.Report, now time.Time) []sshdiscovery.Report {
	result := sshdiscovery.MergeReports(append(append([]sshdiscovery.Report(nil), memory...), cached...))
	for i := range result {
		result[i].Stale = result[i].Stale || now.Before(result[i].ObservedAt) || now.Sub(result[i].ObservedAt) >= sshdiscovery.CacheTTL
	}
	return result
}

func discoverSSHTUI(ctx context.Context, app *App, request tui.SSHDiscoveryRequest, progress func(sshdiscovery.Progress)) (tui.SSHDiscoveryResult, error) {
	var report sshdiscovery.Report
	var err error
	switch request.Source {
	case sshdiscovery.SourceLAN:
		report, err = app.sshDiscovery().LANWithProgress(ctx, request.LAN, progress)
	case sshdiscovery.SourceTailscale:
		report, err = app.sshDiscovery().Tailscale(ctx)
	default:
		return tui.SSHDiscoveryResult{}, errors.New("choose LAN or Tailscale discovery")
	}
	result := tui.SSHDiscoveryResult{Report: report}
	if len(report.Candidates) > 0 || report.Complete {
		// Persist observations already acquired even if the scan was canceled.
		cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if cacheErr := sshdiscovery.WriteCache(cacheCtx, sshDiscoveryCacheDir(), report); cacheErr != nil {
			result.CacheErr = cacheErr
			result.CacheError = fmt.Sprintf("Discovery results are available for this session; cache could not be saved: %v", cacheErr)
		}
	}
	return result, err
}
