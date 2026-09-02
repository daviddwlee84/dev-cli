package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func TestLoadConfigAppliesDefaultsAndValidatesHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.toml")
	body := `schema_version = 1
[defaults]
connect_timeout = "9s"
command_timeout = "3m"
cache_ttl = "10m"
max_parallel = 2
dev_path = "auto"

[[hosts]]
name = "lab"
ssh_alias = "lab"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].ConnectTimeout.Duration != 9*time.Second || cfg.Hosts[0].DevPath != "auto" {
		t.Fatalf("loaded config = %+v", cfg)
	}
}

func TestPlainPasswordRequiresPrivateFile(t *testing.T) {
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
ssh_alias = "lab"
ssh_login_password_source = { type = "plain", value = "secret" }
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateMode(path, cfg); err == nil {
		t.Fatal("CheckPrivateMode accepted an unprotected plaintext password file")
	}
}

func TestRemoteCommandUsesDeterministicPathAndQuotesArguments(t *testing.T) {
	command := remoteCommand(Host{DevPath: "auto"}, []string{"fleet", "_snapshot", "a'b"})
	if !strings.Contains(command, `$HOME/.local/bin`) || !strings.Contains(command, `'a'"'"'b'`) {
		t.Fatalf("remote command = %s", command)
	}
}

func TestSSHArgsSeparateKeyAndPasswordAuthentication(t *testing.T) {
	host := Host{Name: "lab", SSHAlias: "lab", IdentityFile: "~/.ssh/id", Port: 2222}
	host.ConnectTimeout.Duration = 5 * time.Second
	keyArgs := strings.Join(sshArgs(host, false, false), " ")
	if !strings.Contains(keyArgs, "BatchMode=yes") || !strings.Contains(keyArgs, " -i ") {
		t.Fatalf("key args = %s", keyArgs)
	}
	passwordArgs := strings.Join(sshArgs(host, false, true), " ")
	if !strings.Contains(passwordArgs, "PreferredAuthentications=keyboard-interactive,password") || strings.Contains(passwordArgs, " -i ") {
		t.Fatalf("password args = %s", passwordArgs)
	}
}

func TestTransportPreservesRemoteNoDevExitCode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(bin, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, remoteOS := range []string{RemoteOSPOSIX, RemoteOSWindows} {
		t.Run(remoteOS, func(t *testing.T) {
			host := Host{Name: "lab", SSHAlias: "lab", DevPath: "auto", RemoteOS: remoteOS}
			host.ConnectTimeout.Duration = time.Second
			// The timeout is not under test. Leave enough headroom for a full -race
			// suite, where process startup can be delayed by other package tests.
			host.CommandTimeout.Duration = time.Minute
			result := (Transport{}).Run(context.Background(), host, []string{"fleet", "_snapshot"}, nil, false)
			if result.ExitCode != 127 {
				t.Fatalf("transport exit = %d, stderr=%s", result.ExitCode, result.Stderr)
			}
		})
	}
}

func TestCacheRejectsChangedEndpoint(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := Host{Name: "lab", SSHAlias: "lab", Port: 22, DevPath: "auto"}
	host.ConnectTimeout.Duration = time.Second
	host.CommandTimeout.Duration = time.Minute
	snapshot := Snapshot{SchemaVersion: SnapshotSchemaVersion, Host: "lab", GeneratedAt: time.Now().UTC()}
	if err := SaveCache(host, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := LoadCache(host); !ok {
		t.Fatal("fresh cache was not loaded")
	}
	changedAlias := host
	changedAlias.SSHAlias = "different"
	if _, _, ok := LoadCache(changedAlias); ok {
		t.Fatal("cache survived SSH alias change")
	}
	changedPort := host
	changedPort.Port = 2222
	if _, _, ok := LoadCache(changedPort); ok {
		t.Fatal("cache survived SSH port change")
	}
	changedOS := host
	changedOS.RemoteOS = RemoteOSWindows
	if _, _, ok := LoadCache(changedOS); ok {
		t.Fatal("cache survived remote OS change")
	}
}

func TestCacheRejectsOversizedFileAndFields(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := Host{Name: "lab", SSHAlias: "lab", Port: 22, DevPath: "auto"}
	host.ConnectTimeout.Duration = time.Second
	host.CommandTimeout.Duration = time.Minute
	snapshot := Snapshot{
		SchemaVersion: SnapshotSchemaVersion, Host: "lab", GeneratedAt: time.Now().UTC(),
		Repositories: []RepoSnapshot{{Name: "api", Display: "api", Path: strings.Repeat("x", (16<<10)+1)}},
	}
	if err := SaveCache(host, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := LoadCache(host); ok {
		t.Fatal("cache with oversized repository path was accepted")
	}
	if err := os.Truncate(cachePath(host), maxCacheBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := LoadCache(host); ok {
		t.Fatal("oversized cache file was accepted")
	}
	snapshot.Repositories = []RepoSnapshot{{
		Name: "api", Display: "api", Path: "/src/api", Status: gitx.Status{Changed: -1},
	}}
	if err := SaveCache(host, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := LoadCache(host); ok {
		t.Fatal("cache with a negative Git status count was accepted")
	}
}

func TestCanceledCacheSaveDoesNotCreateTarget(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := Host{Name: "lab", SSHAlias: "lab", Port: 22, DevPath: "auto"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := SaveCacheContext(ctx, host, Snapshot{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("save error = %v", err)
	}
	if _, err := os.Stat(cachePath(host)); !os.IsNotExist(err) {
		t.Fatalf("canceled save created target: %v", err)
	}
}
