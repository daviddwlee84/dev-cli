package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/sshcredential"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

func TestSSHPasswordContextsSeparateAccountRouteAndOrigin(t *testing.T) {
	route := sshhost.Route{Hops: []sshhost.RouteHop{{Alias: "gateway", HostName: "edge.example", User: "operator", Port: 22}, {Alias: "database", HostName: "10.0.0.7", User: "db", Port: 22, Target: true}}}
	registry := machineregistry.Snapshot{Imports: []machineregistry.SSHImport{{LocalAlias: "database", OriginID: "remote-origin", ProfileID: "remote-profile"}}}
	original, e := sshPasswordContexts("/home/me/.ssh/config", route, registry)
	if e != nil {
		t.Fatal(e)
	}
	if original[1].Origin != "remote-origin" || original[1].Profile != "remote-profile" {
		t.Fatal("lost remote origin")
	}
	for _, change := range []func(){func() { route.Hops[0].Port = 2222 }, func() { route.Hops[1].User = "another" }, func() { registry.Imports[0].OriginID = "other-origin" }} {
		change()
		next, e := sshPasswordContexts("/home/me/.ssh/config", route, registry)
		if e != nil {
			t.Fatal(e)
		}
		if next[1].ID() == original[1].ID() {
			t.Fatal("credential scope reused after route/account/origin change")
		}
	}
}

type sshCLIProvider struct {
	puts int
	fail bool
}

func (*sshCLIProvider) ID() string                      { return "system" }
func (*sshCLIProvider) Available(context.Context) error { return nil }
func (*sshCLIProvider) Get(context.Context, sshcredential.Context, sshcredential.Reference) ([]byte, error) {
	return nil, sshcredential.ErrNotFound
}
func (p *sshCLIProvider) Put(_ context.Context, c sshcredential.Context, _ *sshcredential.Reference, _ []byte) (sshcredential.Reference, error) {
	p.puts++
	if p.fail {
		return sshcredential.Reference{}, sshcredential.ErrUnknown
	}
	return sshcredential.Reference{Provider: "system", ID: c.ID()}, nil
}
func (*sshCLIProvider) Delete(context.Context, sshcredential.Context, sshcredential.Reference) error {
	return nil
}
func TestSSHPasswordSaveChoicesAndProviderFailureDoNotLeak(t *testing.T) {
	for _, decision := range []string{"no", "yes", "never", "failed"} {
		t.Run(decision, func(t *testing.T) {
			dir, e := filepath.EvalSymlinks(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			provider := &sshCLIProvider{fail: decision == "failed"}
			store := sshcredential.NewStore(filepath.Join(dir, "private", "ssh-credentials.toml"))
			manager := sshcredential.Manager{Store: store, Providers: map[string]sshcredential.Provider{"system": provider}}
			var output bytes.Buffer
			app := &App{Out: &output, Err: &output, In: strings.NewReader(""), interactiveCheck: func() bool { return true }}
			prompts := 0
			app.pickerSelect = func(_ context.Context, r picker.Request) (picker.Result, error) {
				prompts++
				if r.Items[0].Value != "no" {
					t.Fatal("save default is not No")
				}
				selected := decision
				if selected == "failed" {
					selected = "yes"
				}
				for _, item := range r.Items {
					if item.Value == selected {
						return picker.Result{Item: item}, nil
					}
				}
				return picker.Result{}, errors.New("missing choice")
			}
			scope := sshcredential.Context{Origin: "local-origin", Profile: "target", Route: "route", Host: "target.example", User: "tester", Port: 22, Kind: "password"}
			secret := []byte("fixture-only-sensitive-password")
			offerSSHPasswordSave(context.Background(), app, manager, scope, secret, "system")
			if strings.Contains(output.String(), string(secret)) {
				t.Fatal("password reached output")
			}
			contents, readErr := os.ReadFile(store.Path)
			if readErr == nil && bytes.Contains(contents, secret) {
				t.Fatal("password reached metadata")
			}
			switch decision {
			case "no":
				if provider.puts != 0 || !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("No had durable effects")
				}
			case "yes":
				if provider.puts != 1 {
					t.Fatal("Yes did not store")
				}
			case "never":
				offerSSHPasswordSave(context.Background(), app, manager, scope, secret, "system")
				if prompts != 1 || provider.puts != 0 {
					t.Fatal("Never prompted or stored")
				}
			case "failed":
				if provider.puts != 1 || !strings.Contains(output.String(), "login succeeded") {
					t.Fatal("provider failure changed login result or retried")
				}
			}
		})
	}
}
