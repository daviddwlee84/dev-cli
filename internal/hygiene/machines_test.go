package hygiene

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineid"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/sshremote"
)

type cachedMachineRunner struct{}

func (cachedMachineRunner) Run(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
	return sshhost.RunResult{Stdout: []byte(`{"BackendState":"Running","Self":{"ID":"self"},"CurrentTailnet":{"MagicDNSSuffix":"example.invalid"},"Peer":{"one":{"ID":"one","HostName":"machine","DNSName":"machine.example.invalid.","TailscaleIPs":["100.64.7.8"]}}}`)}, nil
}

func TestCachedMachineImportRevalidatesWithoutRefreshing(t *testing.T) {
	s, _ := testService(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.CacheDir = filepath.Join(root, "ssh-discovery")
	observed := time.Now().Add(-time.Hour).UTC()
	report, err := sshdiscovery.NewService(cachedMachineRunner{}, sshdiscovery.ServiceOptions{Now: func() time.Time { return observed }}).Tailscale(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = sshdiscovery.WriteCache(t.Context(), s.CacheDir, report); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Candidates(t.Context(), "machines")
	if err != nil || !rows.Complete || len(rows.Items) != 2 {
		t.Fatalf("candidates: %v", err)
	}
	for _, row := range rows.Items {
		if !row.Stale || row.ObservedAt == nil || !row.ObservedAt.Equal(observed) {
			t.Fatal("lost cache observation")
		}
	}
	public, _ := json.Marshal(rows)
	if bytes.Contains(public, []byte("100.64.7.8")) || bytes.Contains(public, []byte("machine.example.invalid")) {
		t.Fatal("private values leaked")
	}
	plan, err := s.PreviewImport(t.Context(), "machines", []string{rows.Items[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	report.Candidates[0].Addresses = []string{"100.64.7.9"}
	if err = sshdiscovery.WriteCache(t.Context(), s.CacheDir, report); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{}); !errors.Is(err, ErrStale) {
		t.Fatalf("changed cache accepted: %v", err)
	}
	rows, err = s.Candidates(t.Context(), "machines")
	if err != nil {
		t.Fatal(err)
	}
	plan, err = s.PreviewImport(t.Context(), "machines", []string{rows.Items[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), plan.ID, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestMissingAndCorruptMachineCachesRemainExplicit(t *testing.T) {
	s, _ := testService(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	s.CacheDir = filepath.Join(root, "missing")
	rows, err := s.Candidates(t.Context(), "machines")
	if err != nil || len(rows.Items) != 0 || len(rows.Gaps) == 0 {
		t.Fatal("missing cache not reported")
	}
	if _, err = os.Stat(s.CacheDir); !os.IsNotExist(err) {
		t.Fatal("passive read created cache")
	}
	if err = os.Mkdir(s.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.CacheDir, "tailscale-corrupt.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows, err = s.Candidates(t.Context(), "machines"); err == nil || rows.Complete {
		t.Fatal("corrupt cache became complete")
	}
}

type noMachineExecution struct{ t *testing.T }

func (r noMachineExecution) Run(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
	r.t.Fatal("cached import executed a command")
	return sshhost.RunResult{}, nil
}

func TestFleetCacheProvidesOnlyReviewedEndpointCandidates(t *testing.T) {
	s, _ := testService(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "source-home")
	if err = privatefile.EnsureDir(filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}
	if err = configedit.WritePrivate(t.Context(), filepath.Join(home, ".ssh", "config"), []byte("Host target\n HostName 192.0.2.44\n User private-fleet-user\n"), false); err != nil {
		t.Fatal(err)
	}
	paths, err := sshhost.NewPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	service, err := sshhost.NewService(paths, noMachineExecution{t})
	if err != nil {
		t.Fatal(err)
	}
	server := sshremote.NewServer(service)
	server.Identity = machineid.NewStore(filepath.Join(root, "identity", "identity.json"))
	server.User = "source-user"
	capability, err := server.Capability(t.Context(), sshremote.CapabilityRequest{Header: sshremote.NewHeader()})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := server.Inventory(t.Context(), sshremote.NewRequest(capability.Origin))
	if err != nil {
		t.Fatal(err)
	}
	inventory.ObservedAt = time.Now().Add(-time.Hour).UTC()
	s.CacheDir = filepath.Join(root, "cache")
	if err = sshremote.SaveCache(t.Context(), filepath.Join(s.CacheDir, "fleet"), fleet.Host{Name: "source", SSHAlias: "source"}, inventory); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Candidates(t.Context(), "machines")
	if err != nil || !rows.Complete || len(rows.Items) != 2 {
		t.Fatalf("fleet candidates: %+v %v", rows, err)
	}
	for _, row := range rows.Items {
		if row.Source != "fleet cache" || !row.Stale {
			t.Fatal("fleet observation missing")
		}
	}
	data, _ := json.Marshal(rows)
	if bytes.Contains(data, []byte("192.0.2.44")) || bytes.Contains(data, []byte("private-fleet-user")) {
		t.Fatal("endpoint leaked")
	}
}
