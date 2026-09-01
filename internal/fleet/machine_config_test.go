package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigAcceptsOptionalExpectedMachineID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.toml")
	body := `schema_version = 1
[defaults]
connect_timeout = "15s"
command_timeout = "5m"
cache_ttl = "15m"
max_parallel = 4
dev_path = "auto"

[[hosts]]
name = "lab"
machine_id = "00000000-0000-4000-8000-000000000001"
ssh_alias = "lab"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Hosts[0].MachineID; got != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("machine ID = %q", got)
	}
}

func TestConfigRejectsInvalidExpectedMachineID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hosts = []Host{{Name: "lab", MachineID: "not-a-uuid", SSHAlias: "lab"}}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "canonical non-zero UUID") {
		t.Fatalf("Validate = %v", err)
	}
}

func TestEndpointIDIncludesMachinePinAndPort(t *testing.T) {
	host := Host{Name: "lab", SSHAlias: "lab", DevPath: "auto"}
	host.ConnectTimeout.Duration = time.Second
	host.CommandTimeout.Duration = time.Minute
	original := EndpointID(host)
	host.MachineID = "00000000-0000-4000-8000-000000000001"
	withMachine := EndpointID(host)
	if withMachine == original {
		t.Fatal("endpoint fingerprint ignored machine_id")
	}
	host.Port = 2222
	if withPort := EndpointID(host); withPort == withMachine {
		t.Fatal("endpoint fingerprint ignored port")
	}
}
