package cli

import (
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/sshdiscovery"
	"github.com/daviddwlee84/dev-cli/internal/sshflow"
)

func sshTUIProfileFor(t *testing.T, app *App, alias string) sshflow.ConnectionProfile {
	t.Helper()
	service, err := app.sshHosts()
	if err != nil {
		t.Fatal(err)
	}
	hints, err := service.ConnectionHints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, hint := range hints {
		if hint.Alias == alias {
			return sshflow.ConnectionProfile{ID: alias, Alias: hint.Alias, HostName: hint.HostName, User: hint.User, Port: hint.Port, Fingerprint: hint.Fingerprint, State: hint.State, Source: hint.Source}
		}
	}
	t.Fatalf("missing connection hint %s", alias)
	return sshflow.ConnectionProfile{}
}

func TestSSHTUIKeyInstallForExistingProfilesKeepsConnectionSettings(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	app, f, _ := sshNativeTestApp(t)
	f.initSSH()
	f.createManaged("lab", "lab.example")
	f.appendRootConfig("Host foreign\n    HostName foreign.example\n    User tester\n")
	key := writeSSHCLIKey(t, f, "work-key")
	for alias, host := range map[string]string{"lab": "lab.example", "foreign": "foreign.example"} {
		profile := sshTUIProfileFor(t, app, alias)
		plan, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Profile: &profile, Alias: alias, Auth: "key", KeyPath: key, RemoteOS: "posix", HerdrSession: "default"})
		if err != nil {
			t.Fatalf("%s: %v", alias, err)
		}
		if targets := plan.Preview().Targets; len(targets) != 1 || targets[0].Alias != alias || targets[0].HostName != host {
			t.Fatalf("%s key install changed or lost its connection: %+v", alias, targets)
		}
	}
	profile := sshTUIProfileFor(t, app, "lab")
	if _, err := prepareSSHTUIOnboarding(t.Context(), app, sshflow.OnboardRequest{Profile: &profile, Alias: "lab", Auth: "key", KeyPath: key, Candidate: &sshdiscovery.Candidate{}}); err == nil {
		t.Fatal("profile key install accepted a discovery candidate")
	}
	choices, err := listSSHTUIKeys(t.Context(), app, "lab")
	if err != nil || len(choices) < 2 {
		t.Fatalf("choices=%+v err=%v", choices, err)
	}
	found := false
	for _, choice := range choices[:len(choices)-1] {
		found = found || choice.Path == key && !choice.Generate
	}
	if last := choices[len(choices)-1]; !found || !last.Generate || filepath.Base(last.Path) != "id_ed25519_dev_lab" {
		t.Fatalf("key choices=%+v", choices)
	}
}
