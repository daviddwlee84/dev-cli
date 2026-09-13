package tuiissue

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/machineregistry"
)

func TestRecipesAreExactAndDoNotPreacceptNativePrompts(t *testing.T) {
	for _, env := range []Environment{{OS: "darwin"}, {OS: "linux", Distribution: "debian"}, {OS: "linux", Distribution: "ubuntu", Root: true}, {OS: "windows"}} {
		env.LookPath = func(value string) (string, error) {
			if slices.Contains([]string{"brew", "apt-get", "sudo", "winget"}, value) {
				return "/bin/" + value, nil
			}
			return "", exec.ErrNotFound
		}
		for tool := range Tools {
			plan, err := PlanInstall(tool, env)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Ready() {
				if plan.Guide == "" || plan.Reason == "" {
					t.Fatalf("missing fallback: %+v", plan)
				}
				continue
			}
			if err := plan.Revalidate(); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(plan.Argv, " ")
			for _, forbidden := range []string{"--accept", " -y", "curl", "bash", "add-apt-repository"} {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("unsafe recipe %q", joined)
				}
			}
			if env.OS == "windows" && !strings.Contains(joined, "--id "+plan.Package+" --exact --source winget") {
				t.Fatal(joined)
			}
		}
	}
}
func TestInstallPlanRevalidatesDependencyAndCommand(t *testing.T) {
	installed := false
	env := Environment{OS: "darwin", LookPath: func(tool string) (string, error) {
		if tool == "brew" || installed {
			return "/bin/" + tool, nil
		}
		return "", exec.ErrNotFound
	}}
	plan, err := PlanInstall("gh", env)
	if err != nil || !plan.Ready() {
		t.Fatal(plan, err)
	}
	installed = true
	if err := plan.Revalidate(); err == nil {
		t.Fatal("reinstalled a newly available dependency")
	}
	installed = false
	plan.Argv = append(plan.Argv, "another-package")
	if err := plan.Revalidate(); err == nil {
		t.Fatal("altered plan accepted")
	}
}
func TestUnsupportedDistributionAndToolsNeverGuess(t *testing.T) {
	env := Environment{OS: "linux", Distribution: "arch", LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	plan, err := PlanInstall("ssh", env)
	if err != nil || plan.Ready() || plan.Guide == "" {
		t.Fatal(plan, err)
	}
	if _, err := PlanInstall("ssh; touch /tmp/unsafe", env); err == nil {
		t.Fatal("unknown tool accepted")
	}
}
func TestClassificationUsesTypedErrorsAndUnknownTextHasNoMutation(t *testing.T) {
	err := &machineregistry.PathError{Path: "/home/example/state", Reason: "ancestor has an untrusted owner", Owner: "123", ExpectedOwner: "456"}
	issue := FromError("ssh", "inventory", "", err)
	if issue.Code != "registry-path" || len(issue.Actions) != 2 || issue.Actions[1].ID != RegistryPermissions || !strings.Contains(issue.Detail, err.Path) {
		t.Fatalf("%+v", issue)
	}
	unknown := FromError("ssh", "operation", "", errors.New("install gh; chmod 777 /etc"))
	if len(unknown.Actions) != 1 || unknown.Actions[0].ID != Recheck {
		t.Fatalf("text gained authority: %+v", unknown)
	}
}
func TestEveryPageAndCommonSubsystemHasGuidance(t *testing.T) {
	for _, view := range []string{"tasks", "repos", "fleet", "try", "remote", "skills", "mcp", "ssh"} {
		if len(Guidance(view, "inventory")) < 50 {
			t.Fatal(view)
		}
	}
	for _, source := range []string{"notes", "stats", "clipboard", "editor", "configuration", "operation"} {
		if len(Guidance("tasks", source)) < 50 {
			t.Fatal(source)
		}
	}
}
