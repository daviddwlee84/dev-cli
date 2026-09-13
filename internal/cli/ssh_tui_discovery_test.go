package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

func sshNativeTestApp(t *testing.T) (*App, *sshCLIFixture, sshdiscovery.LANRequest) {
	t.Helper()
	f := newSSHCLIFixture(t)
	canonicalHome, err := filepath.EvalSymlinks(f.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(canonicalHome, ".cache"))
	scope := sshdiscovery.InterfaceScope{Interface: "lan0", Index: 2, Address: "192.168.55.10", Prefix: "192.168.55.0/24"}
	service := sshdiscovery.NewService(nil, sshdiscovery.ServiceOptions{
		Interfaces: func() ([]sshdiscovery.InterfaceScope, error) { return []sshdiscovery.InterfaceScope{scope}, nil },
		LookupAddr: func(context.Context, string) ([]string, error) { return nil, errors.New("no PTR") },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() { defer server.Close(); _, _ = io.WriteString(server, "SSH-2.0-OpenSSH\r\n") }()
			return client, nil
		},
	})
	app := &App{Cfg: config.Default(), In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, remotesPath: f.remotesPath, sshHostRunner: f.runner, sshDiscoveryService: service, interactiveCheck: func() bool { return false }}
	return app, f, sshdiscovery.LANRequest{Interface: "lan0", Ranges: []string{"192.168.55.20"}, Ports: []int{22}}
}

func TestSSHTUIDiscoveryEmptyHostRetainsReportWhenCacheFails(t *testing.T) {
	app, f, request := sshNativeTestApp(t)
	if err := os.WriteFile(filepath.Join(f.home, ".cache"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := discoverSSHTUI(t.Context(), app, tui.SSHDiscoveryRequest{Source: "lan", LAN: request}, nil)
	if err != nil || !result.Report.Complete || len(result.Report.Candidates) != 1 || result.CacheError == "" || result.CacheErr == nil {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	inventory, loadErr := loadSSHTUIInventory(t.Context(), app, []sshdiscovery.Report{result.Report})
	if len(inventory.Machines) != 1 || len(inventory.Machines[0].LAN) != 1 {
		t.Fatalf("cached-independent candidates missing: %+v %v", inventory, loadErr)
	}
	for _, path := range []string{f.rootConfigPath(), app.machineStore().Path, f.remotesPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discovery created %s: %v", path, err)
		}
	}
}

func TestSSHTUICandidateSetupWithoutCacheCreatesReviewedFirstConfig(t *testing.T) {
	app, f, request := sshNativeTestApp(t)
	report, err := app.sshDiscovery().LAN(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	setup := sshflow.OnboardRequest{Candidate: &report.Candidates[0], Report: &report, Alias: "lab", HostName: "192.168.55.20", User: "operator", Port: 22, Auth: "config"}
	reviewed, err := prepareSSHTUIOnboarding(t.Context(), app, setup)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Preview().Init.Action != sshhost.ActionCreate {
		t.Fatalf("missing first-time init: %+v", reviewed.Preview())
	}
	for _, path := range []string{f.rootConfigPath(), app.machineStore().Path} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preview wrote %s", path)
		}
	}
	// A caller cannot alter the private plan by editing its display projection.
	display := reviewed.Preview()
	display.Targets[0].HostName = "unreviewed.example"
	f.runner.resetCalls()
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: reviewed}}
	if err := workflow.Run(); err != nil {
		t.Fatal(err)
	}
	result := workflow.Result().Onboarding
	if result == nil || result.Status != "ready" || result.Init == nil || !result.Init.Changed || !result.Configurations["lab"].Changed || result.Bindings["lab"].MachineID == "" {
		t.Fatalf("lost typed result: %+v", result)
	}
	body, err := os.ReadFile(f.managedPath("lab"))
	if err != nil || !strings.Contains(string(body), "192.168.55.20") {
		t.Fatalf("configured wrong endpoint: %s %v", body, err)
	}
	for _, call := range f.runner.callSnapshot() {
		if call.Name == "ssh" && !hasSSHArg(call.Args, "-G") || call.Name == "ssh-keygen" || call.Name == "herdr" || call.Name == "tailscale" {
			t.Fatalf("config only invoked %s", call.Name)
		}
	}
	if err := workflow.Run(); err == nil {
		t.Fatal("consumed plan applied twice")
	}
}

func TestSSHTUIOnboardingReturnsRetainedConfigurationAfterAuthenticationFailure(t *testing.T) {
	app, f, request := sshNativeTestApp(t)
	report, err := app.sshDiscovery().LAN(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Candidate: &report.Candidates[0], Report: &report, Alias: "lab", HostName: "192.168.55.20", User: "operator", Port: 22, Auth: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	f.runner.ordinaryReady = false
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: reviewed}}
	if err := workflow.Run(); err == nil {
		t.Fatal("failed authentication reported success")
	}
	result := workflow.Result().Onboarding
	if result == nil || result.Status != "partial" || result.Init == nil || !result.Init.Changed || !result.Configurations["lab"].Changed || result.Bindings["lab"].MachineID == "" {
		t.Fatalf("lost partial effects: %+v", result)
	}
	if _, err := os.Stat(f.managedPath("lab")); err != nil {
		t.Fatal("configuration missing after authentication failed", err)
	}
}

func TestSSHTUIOnboardingRejectsChangedConfigurationBeforeInit(t *testing.T) {
	app, f, request := sshNativeTestApp(t)
	report, err := app.sshDiscovery().LAN(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Candidate: &report.Candidates[0], Report: &report, Alias: "lab", HostName: "192.168.55.20", User: "operator", Port: 22, Auth: "config"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.rootConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	changed := []byte("Host external\n HostName 192.168.55.99\n")
	if err := os.WriteFile(f.rootConfigPath(), changed, 0600); err != nil {
		t.Fatal(err)
	}
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: reviewed}}
	if err := workflow.Run(); err == nil {
		t.Fatal("changed source applied")
	}
	if _, err := os.Stat(f.managedPath("lab")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale plan wrote alias")
	}
	if result := workflow.Result().Onboarding; result == nil || result.Init != nil || result.Status != "failed" {
		t.Fatalf("missing early failure result: %+v", result)
	}
}

func TestSSHTUIOnboardingRejectsLANScopeChangedSinceReview(t *testing.T) {
	app, f, request := sshNativeTestApp(t)
	report, err := app.sshDiscovery().LAN(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Candidate: &report.Candidates[0], Report: &report, Alias: "lab", HostName: "192.168.55.20", User: "operator", Port: 22, Auth: "config"})
	if err != nil {
		t.Fatal(err)
	}
	app.sshDiscoveryService = sshdiscovery.NewService(nil, sshdiscovery.ServiceOptions{Interfaces: func() ([]sshdiscovery.InterfaceScope, error) {
		return []sshdiscovery.InterfaceScope{{Interface: "other", Index: 3, Address: "192.168.55.11", Prefix: "192.168.55.0/24"}}, nil
	}})
	workflow := &sshTUIWorkflow{ctx: t.Context(), app: *app, request: tui.SSHWorkflowRequest{Action: "onboard", Onboarding: reviewed}}
	if err := workflow.Run(); err == nil || !strings.Contains(err.Error(), "scope changed") {
		t.Fatalf("changed LAN accepted: %v", err)
	}
	if _, err := os.Stat(f.rootConfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("changed LAN created config")
	}
}
