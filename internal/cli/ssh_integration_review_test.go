package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sshReviewMutatingRemote struct {
	base         *sshFleetFixtureRunner
	afterResolve func(int)
}

func (r sshReviewMutatingRemote) RunWithOptions(ctx context.Context, host fleet.Host, args []string, stdin []byte, options fleet.RunOptions) fleet.Result {
	result := r.base.RunWithOptions(ctx, host, args, stdin, options)
	if len(args) == 2 && args[1] == "_ssh-resolve" && r.afterResolve != nil {
		r.afterResolve(r.base.resolutions)
	}
	return result
}
func runSSHImportReview(t *testing.T, f *sshCLIFixture, remote sshReviewMutatingRemote, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &stderr, sshHostRunner: f.runner, sshRemoteRunner: remote, interactiveCheck: func() bool { return false }}
	root := newRootCommand(app)
	root.SetArgs(append([]string{"--config", f.configPath, "--remotes", f.remotesPath, "--color", "never"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestSSHImportLocalSourceMutationDuringRecheckStopsBeforeInit(t *testing.T) {
	f, remote := setupSSHFleetFixture(t)
	sshDir := filepath.Join(f.home, ".ssh")
	if e := os.MkdirAll(sshDir, 0o700); e != nil {
		t.Fatal(e)
	}
	foreign := filepath.Join(sshDir, "foreign.conf")
	before := []byte("Include ~/.ssh/foreign.conf\n")
	if e := os.WriteFile(f.rootConfigPath(), before, 0o600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(foreign, []byte("# original source\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{sshDir, f.rootConfigPath(), foreign} {
		protectSSHFixture(t, path)
	}
	mutated := false
	runner := sshReviewMutatingRemote{base: remote, afterResolve: func(n int) {
		if n == 2 {
			if e := os.WriteFile(foreign, []byte("# changed by native revalidation\n"), 0o600); e != nil {
				t.Fatal(e)
			}
			mutated = true
		}
	}}
	_, err := runSSHImportReview(t, f, runner, "ssh", "setup", "database-local", "--from", "fleet:gateway/database", "--config-only", "--yes", "--json")
	if !mutated {
		t.Fatalf("mutation seam not reached: %v", err)
	}
	if err == nil {
		t.Fatal("changed Include source accepted")
	}
	after, e := os.ReadFile(f.rootConfigPath())
	if e != nil || !bytes.Equal(before, after) {
		t.Fatalf("stale source initialized managed Include: %q %v", after, e)
	}
	if _, e = os.Lstat(f.managedPath("database-local")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("stale source wrote imported profile", e)
	}
}
func TestSSHImportLocalSourceMutationDuringConfigureStopsBeforeKeyGeneration(t *testing.T) {
	f, remote := setupSSHFleetFixture(t)
	f.initSSH()
	foreign := filepath.Join(f.home, ".ssh", "foreign.conf")
	if e := os.WriteFile(foreign, []byte("# original source\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	f.appendRootConfig("\nInclude ~/.ssh/foreign.conf\n")
	key := filepath.Join(f.home, ".ssh", "new-import-key")
	mutated := false
	runner := sshReviewMutatingRemote{base: remote, afterResolve: func(n int) {
		if n == 3 {
			if e := os.WriteFile(foreign, []byte("# changed during configure revalidation\n"), 0o600); e != nil {
				t.Fatal(e)
			}
			mutated = true
		}
	}}
	_, err := runSSHImportReview(t, f, runner, "ssh", "setup", "database-local", "--from", "fleet:gateway/database", "--generate-key", "--key-path", key, "--no-passphrase", "--target-os", "posix", "--yes", "--json")
	if !mutated {
		t.Fatalf("configure mutation seam not reached: %v", err)
	}
	if err == nil {
		t.Fatal("stale configuration accepted")
	}
	for _, path := range []string{key, key + ".pub", f.managedPath("database-local")} {
		if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("stale revalidation caused key/config write %s: %v", path, e)
		}
	}
}

func TestSSHImportPickerMutationBeforeFinalPreviewRetainsChangedAuthority(t *testing.T) {
	for _, kind := range []string{"target_file", "registry"} {
		t.Run(kind, func(t *testing.T) {
			f, remote := setupSSHFleetFixture(t)
			if _, e := runSSHFleetFixture(t, f, remote, "ssh", "setup", "database-local", "--from", "fleet:gateway/database", "--config-only", "--yes", "--json"); e != nil {
				t.Fatal(e)
			}
			var out, stderr bytes.Buffer
			app := &App{In: strings.NewReader(""), Out: &out, Err: &stderr, configPath: f.configPath, remotesPath: f.remotesPath, sshHostRunner: f.runner, sshRemoteRunner: remote, interactiveCheck: func() bool { return true }}
			if e := app.Load(); e != nil {
				t.Fatal(e)
			}
			host, e := sshFleetHost(app, "gateway")
			if e != nil {
				t.Fatal(e)
			}
			item, e := prepareSSHFleetImportItem(context.Background(), app, "database-local", host, remote.source, sshSetupOptions{configOnly: true, json: true})
			if e != nil {
				t.Fatal(e)
			}
			changedBytes := []byte(nil)
			changedFingerprint := strings.Repeat("b", 64)
			mutated := false
			app.pickerSelect = func(ctx context.Context, request picker.Request) (picker.Result, error) {
				mutated = true
				if kind == "target_file" {
					body, e := os.ReadFile(f.managedPath("database-local"))
					if e != nil {
						t.Fatal(e)
					}
					changedBytes = bytes.ReplaceAll(body, []byte("10.81.0.8"), []byte("10.81.0.99"))
					if e = os.WriteFile(f.managedPath("database-local"), changedBytes, 0o600); e != nil {
						t.Fatal(e)
					}
				} else {
					store := app.machineStore()
					snapshot, e := store.Read(ctx)
					if e != nil {
						t.Fatal(e)
					}
					record, found := snapshot.LookupImport("database-local")
					if !found {
						t.Fatal("missing import")
					}
					record.SourceFingerprint = changedFingerprint
					plan, e := store.Plan(ctx, machineregistry.Request{Action: "record-imports", Imports: []machineregistry.SSHImport{record}})
					if e != nil {
						t.Fatal(e)
					}
					if _, e = store.Apply(ctx, plan); e != nil {
						t.Fatal(e)
					}
				}
				return picker.Result{Item: request.Items[0]}, nil
			}
			if _, e = sshPick(context.Background(), app, "Later hop key picker", []picker.Item{{Value: "unchanged", Label: "Keep current key"}}, false); e != nil {
				t.Fatal(e)
			}
			e = applySSHOnboardItems(context.Background(), app, []sshOnboardItem{item}, false, true, true)
			if !mutated || e == nil {
				t.Fatalf("picker-time authority change accepted: %v", e)
			}
			if kind == "target_file" {
				body, e := os.ReadFile(f.managedPath("database-local"))
				if e != nil || !bytes.Equal(body, changedBytes) {
					t.Fatal("import overwrote local edit made in picker", e)
				}
			} else {
				snapshot, e := app.machineStore().Read(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				record, _ := snapshot.LookupImport("database-local")
				if record.SourceFingerprint != changedFingerprint {
					t.Fatal("import overwrote newer registry intent")
				}
			}
		})
	}
}

type sshReviewSameAliasRouteRunner struct{ t *testing.T }

func (r sshReviewSameAliasRouteRunner) Run(_ context.Context, q sshhost.RunRequest) (sshhost.RunResult, error) {
	if q.Name != "ssh" || len(q.Args) == 0 || q.Args[0] != "-G" {
		r.t.Fatalf("unexpected effect in planned route: %#v", q)
	}
	user, port := "bob", "22"
	for i, arg := range q.Args {
		if i+1 == len(q.Args) {
			break
		}
		if arg == "-l" {
			user = q.Args[i+1]
		}
		if arg == "-p" {
			port = q.Args[i+1]
		}
	}
	proxy := "none"
	if user == "bob" {
		proxy = "alice@same"
	}
	return sshhost.RunResult{Stdout: []byte(fmt.Sprintf("hostname same.example\nuser %s\nport %s\nproxyjump %s\n", user, port, proxy))}, nil
}
func TestSSHPlannedTargetPortOverrideDoesNotLeakToSameAliasJump(t *testing.T) {
	f := newSSHCLIFixture(t)
	paths, e := sshhost.NewPaths(f.home)
	if e != nil {
		t.Fatal(e)
	}
	service, e := sshhost.NewService(paths, sshReviewSameAliasRouteRunner{t})
	if e != nil {
		t.Fatal(e)
	}
	route, e := planSSHLocalRoute(context.Background(), service, "same", nil, false, sshhost.RouteInvocation{Alias: "same", User: "bob", Port: 2222})
	if e != nil {
		t.Fatal(e)
	}
	if len(route.Hops) != 2 || route.Hops[0].User != "alice" || route.Hops[0].Port != 22 || route.Hops[1].User != "bob" || route.Hops[1].Port != 2222 {
		t.Fatalf("target override leaked into recursive same-alias invocation: %#v", route.Hops)
	}
}
