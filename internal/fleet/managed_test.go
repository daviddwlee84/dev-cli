package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedFragmentDirDerivation(t *testing.T) {
	tests := []struct {
		primary string
		want    string
	}{
		{primary: filepath.Join("x", "remotes.toml"), want: filepath.Join("x", "remotes.d")},
		{primary: filepath.Join("x", "lab.toml"), want: filepath.Join("x", "lab.d")},
		{primary: filepath.Join("x", "lab"), want: filepath.Join("x", "lab.d")},
		{primary: filepath.Join("x", "lab.TOML"), want: filepath.Join("x", "lab.TOML.d")},
	}
	for _, test := range tests {
		t.Run(filepath.Base(test.primary), func(t *testing.T) {
			if got := ManagedFragmentDir(test.primary); got != test.want {
				t.Fatalf("ManagedFragmentDir(%q) = %q, want %q", test.primary, got, test.want)
			}
		})
	}
}

func TestLoadConfigMergesManagedFragmentsAfterPrimaryWithoutChangingPrimary(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "lab.toml")
	primaryBytes := []byte(`# keep this primary comment byte-for-byte
schema_version = 1
[defaults]
connect_timeout = "9s"
command_timeout = "3m"
cache_ttl = "10m"
max_parallel = 2
dev_path = "auto"

[[hosts]]
name = "primary"
ssh_alias = "shared"
`)
	if err := os.WriteFile(primary, primaryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	fixedMtime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(primary, fixedMtime, fixedMtime); err != nil {
		t.Fatal(err)
	}
	writeManagedFixture(t, primary, ManagedHost{Name: "zeta", SSHAlias: "zeta", RemoteOS: RemoteOSWindows})
	writeManagedFixture(t, primary, ManagedHost{Name: "alpha", SSHAlias: "alpha", RemoteOS: RemoteOSPOSIX})

	cfg, err := LoadConfig(primary)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := []string{cfg.Hosts[0].Name, cfg.Hosts[1].Name, cfg.Hosts[2].Name}
	if want := []string{"primary", "alpha", "zeta"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("merged host order = %v, want %v", gotNames, want)
	}
	if cfg.Hosts[0].Managed() || cfg.Hosts[0].Origin() != primary {
		t.Fatalf("primary provenance = managed %v origin %q", cfg.Hosts[0].Managed(), cfg.Hosts[0].Origin())
	}
	for _, host := range cfg.Hosts[1:] {
		if !host.Managed() || filepath.Dir(host.Origin()) != ManagedFragmentDir(primary) {
			t.Fatalf("managed provenance = managed %v origin %q", host.Managed(), host.Origin())
		}
		if host.ConnectTimeout.Duration != 9*time.Second || host.CommandTimeout.Duration != 3*time.Minute || host.DevPath != "auto" {
			t.Fatalf("primary defaults were not applied after merge: %+v", host)
		}
	}
	if cfg.Hosts[1].RemoteOS != RemoteOSPOSIX || cfg.Hosts[2].RemoteOS != RemoteOSWindows {
		t.Fatalf("merged remote OS values = %q, %q", cfg.Hosts[1].RemoteOS, cfg.Hosts[2].RemoteOS)
	}
	after, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(primary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, primaryBytes) || !info.ModTime().Equal(fixedMtime) {
		t.Fatalf("loading changed primary bytes or mtime: bytes_equal=%v mtime=%v", bytes.Equal(after, primaryBytes), info.ModTime())
	}
}

func TestLoadConfigAcceptsManagedFragmentsWhenPrimaryIsAbsent(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	fragment := writeManagedFixture(t, primary, ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX})
	cfg, err := LoadConfig(primary)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != "" || len(cfg.Hosts) != 1 || !cfg.Hosts[0].Managed() || cfg.Hosts[0].Origin() != fragment {
		t.Fatalf("config from absent primary = %+v, origin=%q managed=%v", cfg, cfg.Hosts[0].Origin(), cfg.Hosts[0].Managed())
	}
}

func TestManagedFragmentRenderingAndStrictParsing(t *testing.T) {
	host := ManagedHost{Name: `lab "one"`, SSHAlias: "lab-one", RemoteOS: RemoteOSWindows}
	first, err := RenderManagedFragment(host)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderManagedFragment(host)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || !bytes.HasPrefix(first, []byte(ManagedFragmentHeader)) {
		t.Fatalf("managed rendering is not deterministic:\n%s", first)
	}
	rendered := string(first)
	if strings.Count(rendered, "[host]") != 1 || strings.Contains(rendered, "[[host]]") ||
		strings.Count(rendered, "name =") != 1 || strings.Count(rendered, "ssh_alias =") != 1 || strings.Count(rendered, "remote_os =") != 1 {
		t.Fatalf("managed rendering does not contain exactly one strict [host] table:\n%s", first)
	}
	path := filepath.Join("remotes.d", "ssh-lab-one.toml")
	parsed, err := ParseManagedFragment(path, first)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != host {
		t.Fatalf("parsed fragment = %+v, want %+v", parsed, host)
	}

	tests := map[string][]byte{
		"missing header":         bytes.TrimPrefix(first, []byte(ManagedFragmentHeader)),
		"wrong schema":           bytes.Replace(first, []byte("schema_version = 1"), []byte("schema_version = 2"), 1),
		"defaults forbidden":     bytes.Replace(first, []byte("\n[host]"), []byte("\n[defaults]\nmax_parallel = 4\n\n[host]"), 1),
		"unknown host field":     append(bytes.Clone(first), []byte("dev_path = \"auto\"\n")...),
		"noncanonical comment":   append(bytes.Clone(first), []byte("# manual edit\n")...),
		"second host table":      append(bytes.Clone(first), []byte("\n[host]\nname = \"other\"\n")...),
		"missing required field": []byte(ManagedFragmentHeader + "schema_version = 1\n\n[host]\nname = \"lab\"\nssh_alias = \"lab-one\"\n"),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManagedFragment(path, content); err == nil {
				t.Fatalf("ParseManagedFragment accepted:\n%s", content)
			}
		})
	}
	if _, err := ParseManagedFragment(filepath.Join("remotes.d", "ssh-other.toml"), first); err == nil {
		t.Fatal("ParseManagedFragment accepted an ssh_alias that differs from its filename")
	}
}

func TestManagedFragmentWriterAndRemoverSeamsAreIdempotent(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	writes := 0
	writer := ManagedFragmentWriteFunc(func(request ManagedFragmentWriteRequest) error {
		writes++
		if request.Existed {
			t.Fatalf("unexpected existing write request: %+v", request)
		}
		if err := os.MkdirAll(filepath.Dir(request.Path), managedFragmentDirectoryMode); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(request.Path), managedFragmentDirectoryMode); err != nil {
			return err
		}
		return os.WriteFile(request.Path, request.Content, managedFragmentFileMode)
	})
	path, err := WriteManagedFragment(context.Background(), primary, host, writer)
	if err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("writer calls = %d, want 1", writes)
	}
	if _, err := WriteManagedFragment(context.Background(), primary, host, writer); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("idempotent write called writer again: %d", writes)
	}

	removes := 0
	remover := ManagedFragmentRemoveFunc(func(request ManagedFragmentRemoveRequest) error {
		removes++
		current, err := os.ReadFile(request.Path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, request.ExpectedContent) {
			return errors.New("remove request did not carry expected bytes")
		}
		return os.Remove(request.Path)
	})
	removedPath, err := RemoveManagedFragment(context.Background(), primary, host.SSHAlias, remover)
	if err != nil {
		t.Fatal(err)
	}
	if removedPath != path || removes != 1 {
		t.Fatalf("remove path/calls = %q/%d, want %q/1", removedPath, removes, path)
	}
	if _, err := RemoveManagedFragment(context.Background(), primary, host.SSHAlias, remover); err != nil {
		t.Fatal(err)
	}
	if removes != 1 {
		t.Fatalf("idempotent remove called remover again: %d", removes)
	}
}

func TestManagedFragmentContextCancellationStopsLockWaitWithoutMutation(t *testing.T) {
	for _, operation := range []string{"write", "remove"} {
		t.Run(operation, func(t *testing.T) {
			primary := filepath.Join(t.TempDir(), "remotes.toml")
			initial := ManagedHost{Name: "initial", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
			if _, err := WriteManagedFragment(context.Background(), primary, initial, nil); err != nil {
				t.Fatal(err)
			}
			firstDesired := ManagedHost{Name: "first", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
			held := make(chan struct{})
			release := make(chan struct{})
			firstDone := make(chan error, 1)
			go func() {
				_, err := WriteManagedFragment(context.Background(), primary, firstDesired, ManagedFragmentWriteFunc(func(request ManagedFragmentWriteRequest) error {
					close(held)
					<-release
					return writeManagedFragmentOS(request)
				}))
				firstDone <- err
			}()
			select {
			case <-held:
			case <-time.After(5 * time.Second):
				t.Fatal("first writer did not acquire managed fragment lock")
			}

			arrived := make(chan struct{})
			var arrivedOnce sync.Once
			managedFragmentBeforeLock = func(string) { arrivedOnce.Do(func() { close(arrived) }) }
			t.Cleanup(func() { managedFragmentBeforeLock = nil })
			ctx, cancel := context.WithCancel(context.Background())
			secondDone := make(chan error, 1)
			go func() {
				if operation == "write" {
					_, err := WriteManagedFragment(ctx, primary, ManagedHost{Name: "second", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}, nil)
					secondDone <- err
					return
				}
				_, err := RemoveManagedFragment(ctx, primary, "lab", nil)
				secondDone <- err
			}()
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("contender did not reach managed fragment lock")
			}
			cancel()
			select {
			case err := <-secondDone:
				if !errors.Is(err, context.Canceled) {
					close(release)
					t.Fatalf("canceled %s = %v", operation, err)
				}
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatalf("canceled %s did not stop lock wait", operation)
			}
			managedFragmentBeforeLock = nil
			close(release)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}
			path, _ := ManagedFragmentPath(primary, "lab")
			got, err := ValidateManagedFragment(path)
			if err != nil || got != firstDesired {
				t.Fatalf("canceled %s mutated after cancellation: got %+v err %v", operation, got, err)
			}
		})
	}
}

func TestRemoveManagedFragmentRefusesManualDrift(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	host := ManagedHost{Name: "lab", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
	path := writeManagedFixture(t, primary, host)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("# drift\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = RemoveManagedFragment(context.Background(), primary, host.SSHAlias, ManagedFragmentRemoveFunc(func(ManagedFragmentRemoveRequest) error {
		called = true
		return nil
	}))
	if err == nil || called {
		t.Fatalf("drift removal error/called = %v/%v", err, called)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("drifted fragment was removed: %v", err)
	}
}

func TestPrimaryDuplicateAliasesRemainCompatible(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	body := `schema_version = 1
[[hosts]]
name = "one"
ssh_alias = "shared"
[[hosts]]
name = "two"
ssh_alias = "shared"
`
	if err := os.WriteFile(primary, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(primary)
	if err != nil || len(cfg.Hosts) != 2 {
		t.Fatalf("legacy duplicate aliases failed: cfg=%+v err=%v", cfg, err)
	}
}

func TestManagedAliasCollisionAndGlobalNameCollisionFail(t *testing.T) {
	tests := []struct {
		name    string
		primary string
		managed ManagedHost
		want    string
	}{
		{
			name: "alias", primary: "SHARED", managed: ManagedHost{Name: "managed", SSHAlias: "shared", RemoteOS: RemoteOSPOSIX},
			want: "ssh_alias",
		},
		{
			name: "name", primary: "primary", managed: ManagedHost{Name: "same-name", SSHAlias: "managed", RemoteOS: RemoteOSPOSIX},
			want: "duplicate host name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := filepath.Join(t.TempDir(), "remotes.toml")
			primaryName := "primary"
			if test.name == "name" {
				primaryName = "same-name"
			}
			body := "schema_version = 1\n[[hosts]]\nname = \"" + primaryName + "\"\nssh_alias = \"" + test.primary + "\"\n"
			if err := os.WriteFile(primary, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			writeManagedFixture(t, primary, test.managed)
			if _, err := LoadConfig(primary); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadConfig error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWriteManagedFragmentRefusesInvalidMergedConfig(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	body := "schema_version = 1\n[[hosts]]\nname = \"primary\"\nssh_alias = \"LAB\"\n"
	if err := os.WriteFile(primary, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	path, err := WriteManagedFragment(context.Background(), primary, ManagedHost{
		Name: "managed", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX,
	}, ManagedFragmentWriteFunc(func(ManagedFragmentWriteRequest) error {
		called = true
		return nil
	}))
	if err == nil || !strings.Contains(err.Error(), "ssh_alias") || called {
		t.Fatalf("WriteManagedFragment error/called = %v/%v", err, called)
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Fatalf("invalid merged fragment was created: %v", statErr)
	}
}

func TestWriteManagedFragmentRollsBackPostPublicationMergedCollision(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		primary := filepath.Join(t.TempDir(), "remotes.toml")
		if err := os.WriteFile(primary, []byte("schema_version = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		host := ManagedHost{Name: "managed", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
		path, err := WriteManagedFragment(context.Background(), primary, host, ManagedFragmentWriteFunc(func(request ManagedFragmentWriteRequest) error {
			if err := writeManagedFragmentOS(request); err != nil {
				return err
			}
			return os.WriteFile(primary, []byte("schema_version = 1\n[[hosts]]\nname = \"primary\"\nssh_alias = \"LAB\"\n"), 0o600)
		}))
		if err == nil || !strings.Contains(err.Error(), "publication rolled back") {
			t.Fatalf("post-publication create collision = %v", err)
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("colliding create survived rollback: %v", statErr)
		}
	})

	t.Run("update", func(t *testing.T) {
		primary := filepath.Join(t.TempDir(), "remotes.toml")
		if err := os.WriteFile(primary, []byte("schema_version = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		original := ManagedHost{Name: "old-name", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
		if _, err := WriteManagedFragment(context.Background(), primary, original, nil); err != nil {
			t.Fatal(err)
		}
		desired := ManagedHost{Name: "new-name", SSHAlias: "lab", RemoteOS: RemoteOSPOSIX}
		path, err := WriteManagedFragment(context.Background(), primary, desired, ManagedFragmentWriteFunc(func(request ManagedFragmentWriteRequest) error {
			if err := writeManagedFragmentOS(request); err != nil {
				return err
			}
			return os.WriteFile(primary, []byte("schema_version = 1\n[[hosts]]\nname = \"new-name\"\nssh_alias = \"other\"\n"), 0o600)
		}))
		if err == nil || !strings.Contains(err.Error(), "publication rolled back") {
			t.Fatalf("post-publication update collision = %v", err)
		}
		restored, validateErr := ValidateManagedFragment(path)
		if validateErr != nil || restored != original {
			t.Fatalf("update rollback = %+v, err %v", restored, validateErr)
		}
		if _, loadErr := LoadConfig(primary); loadErr != nil {
			t.Fatalf("rollback did not restore a valid merged config: %v", loadErr)
		}
	})
}

func TestRemoteOSDefaultsValidationJSONAndEndpointIdentity(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "remotes.toml")
	if err := os.WriteFile(primary, []byte("schema_version = 1\n[[hosts]]\nname = \"lab\"\nssh_alias = \"lab\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(primary)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hosts[0].RemoteOS != RemoteOSPOSIX || cfg.Hosts[0].EffectiveRemoteOS() != RemoteOSPOSIX {
		t.Fatalf("default remote OS = %q/%q", cfg.Hosts[0].RemoteOS, cfg.Hosts[0].EffectiveRemoteOS())
	}
	encoded, err := json.Marshal(cfg.Hosts[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"remote_os":"posix"`)) {
		t.Fatalf("host JSON lacks remote_os: %s", encoded)
	}
	windows := cfg.Hosts[0]
	windows.RemoteOS = RemoteOSWindows
	if EndpointID(cfg.Hosts[0]) == EndpointID(windows) {
		t.Fatal("endpoint identity did not change with remote_os")
	}
	invalid := cfg
	invalid.Hosts = append([]Host(nil), cfg.Hosts...)
	invalid.Hosts[0].RemoteOS = "darwin"
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "remote_os") {
		t.Fatalf("invalid remote_os error = %v", err)
	}
}

func TestDevPathUsesRemoteOSAbsolutePathSemantics(t *testing.T) {
	tests := []struct {
		name     string
		remoteOS string
		devPath  string
		managed  bool
		valid    bool
	}{
		{name: "posix absolute", remoteOS: RemoteOSPOSIX, devPath: "/opt/dev", valid: true},
		{name: "posix relative", remoteOS: RemoteOSPOSIX, devPath: "bin/dev"},
		{name: "posix tilde is not local expansion", remoteOS: RemoteOSPOSIX, devPath: "~/bin/dev"},
		{name: "posix environment is not local expansion", remoteOS: RemoteOSPOSIX, devPath: "$HOME/bin/dev"},
		{name: "posix rejects drive path", remoteOS: RemoteOSPOSIX, devPath: `C:\dev.exe`},
		{name: "windows drive backslash", remoteOS: RemoteOSWindows, devPath: `C:\Tools\dev.exe`, valid: true},
		{name: "windows drive slash", remoteOS: RemoteOSWindows, devPath: `D:/Tools/dev.exe`, valid: true},
		{name: "windows UNC", remoteOS: RemoteOSWindows, devPath: `\\server\share\dev.exe`, valid: true},
		{name: "windows extended drive", remoteOS: RemoteOSWindows, devPath: `\\?\C:\Tools\dev.exe`, valid: true},
		{name: "windows extended UNC", remoteOS: RemoteOSWindows, devPath: `\\?\unc\server\share\dev.exe`, valid: true},
		{name: "windows rejects device namespace", remoteOS: RemoteOSWindows, devPath: `\\.\PhysicalDrive0`},
		{name: "windows rejects POSIX", remoteOS: RemoteOSWindows, devPath: "/opt/dev"},
		{name: "windows rejects drive relative", remoteOS: RemoteOSWindows, devPath: `C:dev.exe`},
		{name: "windows rejects environment", remoteOS: RemoteOSWindows, devPath: `%USERPROFILE%\dev.exe`},
		{name: "managed windows explicit path", remoteOS: RemoteOSWindows, devPath: `C:\Tools\dev.exe`, managed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Hosts = []Host{{Name: "lab", SSHAlias: "lab", DevPath: test.devPath, RemoteOS: test.remoteOS, managed: test.managed}}
			cfg.ApplyDefaults()
			err := cfg.Validate()
			if (err == nil) != test.valid {
				t.Fatalf("Validate dev_path %q for %s (managed=%v) = %v, valid want %v", test.devPath, test.remoteOS, test.managed, err, test.valid)
			}
		})
	}
}

func writeManagedFixture(t *testing.T, primary string, host ManagedHost) string {
	t.Helper()
	content, err := RenderManagedFragment(host)
	if err != nil {
		t.Fatal(err)
	}
	path, err := ManagedFragmentPath(primary, host.SSHAlias)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
