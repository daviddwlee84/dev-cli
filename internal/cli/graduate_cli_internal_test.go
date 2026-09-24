package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/experiment"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

func graduateWizardFixture(t *testing.T, input string) (*App, *bytes.Buffer, experiment.Item) {
	t.Helper()
	app, out := newRepoWizardApp(t, input)
	app.Cfg.Paths.TriesRoot = filepath.Join(t.TempDir(), "tries")
	service, err := newExperimentService(app)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), experiment.CreateRequest{Name: "scratch", NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created.Item.Live.CurrentPath, "idea.txt"), []byte("current work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return app, out, created.Item
}

func graduateCLI(t *testing.T, app *App, args ...string) error {
	t.Helper()
	cmd := newGraduateCmd(app)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetContext(t.Context())
	cmd.SetArgs(args)
	return cmd.Execute()
}

type graduateForgeFixture struct {
	Calls, URL, PartialOrigin  string
	AuthFailure, CreateFailure bool
}

func installGraduateForges(t *testing.T, github, gitlab graduateForgeFixture) (string, string) {
	t.Helper()
	bin := t.TempDir()
	paths := []string{}
	for index, value := range []graduateForgeFixture{github, gitlab} {
		name := []string{"gh", "glab"}[index]
		value.Calls = filepath.Join(bin, name+"-calls")
		program := testutil.GoCommand(t, bin, name, graduateForgeFixtureSource)
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program+".json", data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, value.Calls)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return paths[0], paths[1]
}

const graduateForgeFixtureSource = `package main
import ("encoding/json"; "fmt"; "os"; "os/exec")
type fixture struct { Calls, URL, PartialOrigin string; AuthFailure, CreateFailure bool }
func main() {
 self, err := os.Executable(); if err != nil { panic(err) }
 body, err := os.ReadFile(self+".json"); if err != nil { panic(err) }
 var cfg fixture; if err := json.Unmarshal(body,&cfg); err != nil { panic(err) }
 log, err := os.OpenFile(cfg.Calls,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); if err != nil { panic(err) }
 if err := json.NewEncoder(log).Encode(os.Args[1:]); err != nil { panic(err) }; log.Close()
 args := os.Args[1:]
 if len(args)>=2 && args[0]=="auth" && args[1]=="status" { if cfg.AuthFailure {os.Exit(2)}; return }
 if len(args)>=2 && args[0]=="repo" && args[1]=="create" {
  if cfg.PartialOrigin!="" { if data, err := exec.Command("git","remote","add","origin",cfg.PartialOrigin).CombinedOutput(); err!=nil {fmt.Fprint(os.Stderr,string(data));os.Exit(8)} }
  if cfg.URL!="" {fmt.Println(cfg.URL)}
  if cfg.CreateFailure {fmt.Fprintln(os.Stderr,"simulated publication failure");os.Exit(9)}
  return
 }
 fmt.Fprintln(os.Stderr,"unexpected forge invocation");os.Exit(99)
}
`

func graduateForgeCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var call []string
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func TestGraduateWizardLocalRenamePreservesMetadataWithoutForgeProbes(t *testing.T) {
	app, out, item := graduateWizardFixture(t, "renamed\nLabs\n\ny\n")
	ghCalls, glabCalls := installGraduateForges(t, graduateForgeFixture{}, graduateForgeFixture{})
	before, err := app.Registry.Update(item.ID, func(entry *catalog.Entry) error {
		entry.Note, entry.Tags = "keep this thought", []string{"important"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := graduateCLI(t, app, item.ID); err != nil {
		t.Fatal(err)
	}
	after, err := app.Catalog.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := after.LocationFor(config.Hostname())
	want := filepath.Join(config.Expand(app.Cfg.Paths.ProjectRoot), "Labs", "renamed")
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "renamed" || after.ID != before.ID || after.Note != before.Note ||
		strings.Join(after.Tags, ",") != "important" || after.Experiment.OriginalPath != before.Experiment.OriginalPath ||
		location.CurrentPath != want || after.Kind != catalog.KindRepository {
		t.Fatalf("graduation changed identity/metadata: %+v", after)
	}
	if data, err := os.ReadFile(filepath.Join(want, "idea.txt")); err != nil || string(data) != "current work\n" {
		t.Fatalf("current contents = %q, %v", data, err)
	}
	if len(graduateForgeCalls(t, ghCalls))+len(graduateForgeCalls(t, glabCalls)) != 0 {
		t.Fatal("local wizard probed a forge")
	}
	for _, text := range []string{"Project name", "Category", "local/add/create", "Graduate this Try?", "renamed is now a project"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("wizard missing %q:\n%s", text, out)
		}
	}
}

func TestGraduateWizardCancellationDoesNotPrepareGit(t *testing.T) {
	for _, input := range []string{"", "renamed\n\nlocal\nn\n"} {
		t.Run(input, func(t *testing.T) {
			app, out, item := graduateWizardFixture(t, input)
			if err := graduateCLI(t, app, item.ID); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Canceled") {
				t.Fatal("cancellation was not reported")
			}
			if _, err := os.Stat(filepath.Join(item.Live.CurrentPath, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancellation prepared Git: %v", err)
			}
			entry, err := app.Catalog.Get(item.ID)
			if err != nil || entry.Kind != catalog.KindTry {
				t.Fatalf("cancellation graduated record: %+v %v", entry, err)
			}
		})
	}
}

func TestGraduateDirectAndDryRunDoNotPrompt(t *testing.T) {
	for _, mode := range []string{"yes", "noninteractive", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			app, out, item := graduateWizardFixture(t, "")
			ghCalls, glabCalls := installGraduateForges(t, graduateForgeFixture{}, graduateForgeFixture{})
			args := []string{item.ID, "--name", "direct"}
			switch mode {
			case "yes":
				args = append(args, "--yes")
			case "noninteractive":
				app.interactiveCheck = func() bool { return false }
			case "dry-run":
				args = append(args, "--dry-run", "--remote")
			}
			if err := graduateCLI(t, app, args...); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "? Project name") || strings.Contains(out.String(), "Canceled") {
				t.Fatalf("direct invocation prompted:\n%s", out)
			}
			if len(graduateForgeCalls(t, ghCalls))+len(graduateForgeCalls(t, glabCalls)) != 0 {
				t.Fatal("local/dry-run invocation probed authentication")
			}
			entry, err := app.Catalog.Get(item.ID)
			if err != nil || (entry.Kind == catalog.KindTry) != (mode == "dry-run") {
				t.Fatalf("unexpected direct result: %+v %v", entry, err)
			}
		})
	}
}

func TestGraduateURLDefaultsToNoPushAndRefreshesProvenance(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "")
	ghCalls, glabCalls := installGraduateForges(t, graduateForgeFixture{}, graduateForgeFixture{})
	// A missing local repository is accepted as a configured URL. Any attempted
	// push would fail, so success proves attachment does not implicitly push.
	remote := filepath.Join(t.TempDir(), "future.git")
	if err := graduateCLI(t, app, item.ID, "--yes", "--remote-url", remote); err != nil {
		t.Fatal(err)
	}
	entry, err := app.Catalog.Get(item.ID)
	if err != nil || entry.Experiment.OriginURL != remote {
		t.Fatalf("origin metadata = %+v, %v", entry, err)
	}
	if len(graduateForgeCalls(t, ghCalls))+len(graduateForgeCalls(t, glabCalls)) != 0 {
		t.Fatal("URL attachment queried a forge")
	}
}

func TestGraduateWizardAddsURLWithoutForgeOrImplicitPush(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "future.git")
	app, out, item := graduateWizardFixture(t, "\n\nadd\n"+remote+"\n\ny\n")
	gh, glab := installGraduateForges(t, graduateForgeFixture{}, graduateForgeFixture{})
	if err := graduateCLI(t, app, item.ID); err != nil {
		t.Fatal(err)
	}
	entry, err := app.Catalog.Get(item.ID)
	if err != nil || entry.Experiment.OriginURL != remote || !strings.Contains(out.String(), "push=false") {
		t.Fatalf("URL wizard = %+v, %v\n%s", entry, err, out)
	}
	if len(graduateForgeCalls(t, gh))+len(graduateForgeCalls(t, glab)) != 0 {
		t.Fatal("URL wizard probed forge authentication")
	}
}

func TestGraduateWizardCreatesUpstreamWithReviewedOptions(t *testing.T) {
	app, out, item := graduateWizardFixture(t, "published\n\ncreate\nteam\n\nn\ny\n")
	gh, _ := installGraduateForges(t, graduateForgeFixture{URL: "https://github.com/team/published"}, graduateForgeFixture{AuthFailure: true})
	if err := graduateCLI(t, app, item.ID); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, call := range graduateForgeCalls(t, gh) {
		if len(call) > 1 && call[0] == "repo" && call[1] == "create" {
			creates++
			if strings.Join(call, " ") != "repo create team/published --private" {
				t.Fatalf("unreviewed create options: %v", call)
			}
		}
	}
	if creates != 1 || !strings.Contains(out.String(), "create github team/published (private); push=false") {
		t.Fatalf("creation review/attempts = %d\n%s", creates, out)
	}
}

func TestGraduateWizardHonorsExplicitPrivateFalse(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "\n\n\n\n\n\ny\n")
	gh, _ := installGraduateForges(t, graduateForgeFixture{URL: "https://github.com/team/scratch"}, graduateForgeFixture{AuthFailure: true})
	if err := graduateCLI(t, app, item.ID, "--remote", "--forge", "github", "--private=false", "--push=false"); err != nil {
		t.Fatal(err)
	}
	for _, call := range graduateForgeCalls(t, gh) {
		if len(call) > 1 && call[0] == "repo" && call[1] == "create" {
			if !strings.Contains(strings.Join(call, " "), "--public") {
				t.Fatalf("explicit public compatibility flag was lost: %v", call)
			}
			return
		}
	}
	t.Fatal("no publication attempt")
}

func TestGraduateURLPushFailureRetainsGraduatedProject(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "")
	remote := filepath.Join(t.TempDir(), "missing.git")
	err := graduateCLI(t, app, item.ID, "--yes", "--remote-url", remote, "--push")
	if err == nil || !strings.Contains(err.Error(), "local graduation succeeded") {
		t.Fatalf("push failure = %v", err)
	}
	detail := app.Err.(*bytes.Buffer).String()
	if !strings.Contains(detail, "Origin attached to "+remote) || !strings.Contains(detail, "configuration is retained") ||
		!strings.Contains(detail, "Push was attempted; its outcome is unconfirmed") || strings.Contains(detail, "rolled back") {
		t.Fatalf("push failure hid or misstated retained origin: %s", detail)
	}
	entry, err := app.Catalog.Get(item.ID)
	if err != nil || entry.Kind != catalog.KindRepository || entry.Experiment.OriginURL != remote {
		t.Fatalf("partial project/provenance lost: %+v, %v", entry, err)
	}
	location, _ := entry.LocationFor(config.Hostname())
	if _, err := os.Stat(filepath.Join(location.CurrentPath, "idea.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestGraduateRejectsConflictingUpstreamFlagsBeforeChanges(t *testing.T) {
	for _, args := range [][]string{
		{"--remote", "--remote-url", "https://example.test/repo.git"},
		{"--remote", "--forge", "none"},
		{"--remote-url", "https://example.test/repo.git", "--namespace", "owner"},
		{"--remote-url", "https://example.test/repo.git", "--private=false"},
		{"--remote-url="},
		{"--remote", "--private", "--visibility", "public"},
		{"--forge", "github", "--visibility", "internal"},
		{"--visibility", "public"},
		{"--forge", "unknown"},
		{"--forge", "github", "--name=--public"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app, _, item := graduateWizardFixture(t, "")
			if err := graduateCLI(t, app, append([]string{item.ID, "--yes"}, args...)...); err == nil {
				t.Fatal("conflicting flags accepted")
			}
			if _, err := os.Stat(filepath.Join(item.Live.CurrentPath, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("flags changed local checkout: %v", err)
			}
		})
	}
}

func TestGraduatePreservesAnyExistingRemoteAndRejectsPublication(t *testing.T) {
	for _, remoteName := range []string{"origin", "upstream"} {
		for _, create := range []bool{false, true} {
			t.Run(fmtGraduateCase(remoteName, create), func(t *testing.T) {
				app, _, item := graduateWizardFixture(t, "")
				if _, err := gitx.Run(t.Context(), item.Live.CurrentPath, "init", "-b", "main"); err != nil {
					t.Fatal(err)
				}
				url := filepath.Join(t.TempDir(), "remote.git")
				if _, err := gitx.Run(t.Context(), item.Live.CurrentPath, "remote", "add", remoteName, url); err != nil {
					t.Fatal(err)
				}
				args := []string{item.ID, "--yes"}
				if create {
					args = append(args, "--remote")
				}
				err := graduateCLI(t, app, args...)
				if create && (err == nil || !strings.Contains(err.Error(), "already has remotes")) || !create && err != nil {
					t.Fatalf("existing remote: %v", err)
				}
				entry, err := app.Catalog.Get(item.ID)
				if err != nil {
					t.Fatal(err)
				}
				location, _ := entry.LocationFor(config.Hostname())
				if got := gitx.Remote(t.Context(), location.CurrentPath, remoteName); got != url {
					t.Fatalf("remote changed: %q", got)
				}
			})
		}
	}
}

func fmtGraduateCase(remote string, create bool) string {
	if create {
		return remote + "-create"
	}
	return remote + "-local"
}

func TestGraduateCreateUsesSelectedForgeWithoutScaffolding(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			app, _, item := graduateWizardFixture(t, "")
			url := "https://github.com/team/renamed"
			if provider == "gitlab" {
				url = "https://gitlab.com/team/renamed"
			}
			gh, glab := installGraduateForges(t, graduateForgeFixture{URL: url}, graduateForgeFixture{URL: url})
			if err := graduateCLI(t, app, item.ID, "--yes", "--name", "renamed", "--forge", provider, "--namespace", "team", "--push=false"); err != nil {
				t.Fatal(err)
			}
			selected, other := gh, glab
			if provider == "gitlab" {
				selected, other = glab, gh
			}
			if len(graduateForgeCalls(t, other)) != 0 {
				t.Fatal("explicit provider queried another forge")
			}
			creates := 0
			for _, call := range graduateForgeCalls(t, selected) {
				if len(call) > 1 && call[0] == "repo" && call[1] == "create" {
					creates++
					if !strings.Contains(strings.Join(call, " "), "--private") {
						t.Fatalf("default create visibility is not private: %v", call)
					}
				}
			}
			if creates != 1 {
				t.Fatalf("creation attempts = %d", creates)
			}
			entry, err := app.Catalog.Get(item.ID)
			if err != nil || !strings.HasPrefix(entry.Experiment.OriginURL, url) {
				t.Fatalf("create provenance: %+v, %v", entry, err)
			}
			location, _ := entry.LocationFor(config.Hostname())
			if _, err := os.Stat(filepath.Join(location.CurrentPath, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("graduation unexpectedly applied a scaffold")
			}
		})
	}
}

func TestGraduateCreationFailureIsPartialAndNeverRetried(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "")
	partial := "git@github.com:team/partial.git"
	gh, _ := installGraduateForges(t, graduateForgeFixture{CreateFailure: true, PartialOrigin: partial}, graduateForgeFixture{AuthFailure: true})
	err := graduateCLI(t, app, item.ID, "--yes", "--remote")
	if err == nil || !strings.Contains(err.Error(), "local graduation succeeded") {
		t.Fatalf("creation failure = %v", err)
	}
	creates := 0
	for _, call := range graduateForgeCalls(t, gh) {
		if len(call) > 1 && call[0] == "repo" && call[1] == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("ambiguous creation retried %d times", creates)
	}
	entry, err := app.Catalog.Get(item.ID)
	if err != nil || entry.Kind != catalog.KindRepository || entry.Experiment.OriginURL != partial {
		t.Fatalf("partial origin not retained: %+v %v", entry, err)
	}
}

func TestGraduateCreatePreflightFailureLeavesTryInPlace(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "")
	installGraduateForges(t, graduateForgeFixture{AuthFailure: true}, graduateForgeFixture{AuthFailure: true})
	if err := graduateCLI(t, app, item.ID, "--yes", "--forge", "github"); err == nil {
		t.Fatal("unauthenticated creation accepted")
	}
	if _, err := os.Stat(filepath.Join(item.Live.CurrentPath, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight prepared the local Git repository: %v", err)
	}
}

func TestGraduateLegacyPreviewAndCancellationKeepCatalogUnchanged(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "dry-run"}[dryRun], func(t *testing.T) {
			app, _, _ := graduateWizardFixture(t, "\n\nlocal\nn\n")
			legacy := filepath.Join(config.Expand(app.Cfg.Paths.TriesRoot), "2026-09-20-legacy")
			if err := os.MkdirAll(legacy, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(legacy, "idea.txt"), []byte("legacy work"), 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot := func() map[string]string {
				t.Helper()
				paths, err := filepath.Glob(filepath.Join(app.Catalog.Dir, "*.toml"))
				if err != nil {
					t.Fatal(err)
				}
				result := make(map[string]string, len(paths))
				for _, path := range paths {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					result[filepath.Base(path)] = string(data)
				}
				return result
			}
			before := snapshot()
			args := []string{legacy}
			if dryRun {
				args = append(args, "--dry-run")
			}
			if err := graduateCLI(t, app, args...); err != nil {
				t.Fatal(err)
			}
			after := snapshot()
			if len(before) != len(after) {
				t.Fatalf("preview/cancel enrolled legacy Try: before=%d after=%d", len(before), len(after))
			}
			for path, data := range before {
				if after[path] != data {
					t.Fatalf("preview/cancel changed catalog %s", path)
				}
			}
			if _, err := os.Stat(filepath.Join(legacy, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preview/cancel prepared Git: %v", err)
			}
		})
	}
}

func TestGraduateAutoForgeSkipsUnauthenticatedGitHub(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "")
	gh, glab := installGraduateForges(t, graduateForgeFixture{AuthFailure: true}, graduateForgeFixture{URL: "https://gitlab.com/team/scratch"})
	if err := graduateCLI(t, app, item.ID, "--yes", "--remote", "--push=false"); err != nil {
		t.Fatal(err)
	}
	for _, call := range graduateForgeCalls(t, gh) {
		if len(call) > 0 && call[0] != "auth" {
			t.Fatalf("unauthenticated GitHub was selected: %v", call)
		}
	}
	creates := 0
	for _, call := range graduateForgeCalls(t, glab) {
		if len(call) > 1 && call[0] == "repo" && call[1] == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("ready GitLab creation attempts = %d", creates)
	}
}

func TestGraduateCreateDefaultsAndExplicitVisibility(t *testing.T) {
	flags := defaultGraduateFlags()
	flags.upstream.remote = true
	request, err := flags.upstreamRequest()
	if err != nil || request.create.Visibility != "private" || !request.push {
		t.Fatalf("creation defaults = %+v, %v", request, err)
	}
	app, _, _ := graduateWizardFixture(t, "\n\n\n")
	installGraduateForges(t, graduateForgeFixture{}, graduateForgeFixture{AuthFailure: true})
	zero := repoBootstrapFlags{}
	if ready, err := promptUpstreamCreation(context.Background(), newPrompter(app), &zero); err != nil || !ready || zero.visibility != "private" {
		t.Fatalf("zero-value creation UI default = %+v, ready=%t err=%v", zero, ready, err)
	}
}

func TestGraduateReportsRetainedLocalEffectsOnFailure(t *testing.T) {
	for _, rolledBack := range []bool{false, true} {
		var out, errOut bytes.Buffer
		app := &App{Out: &out, Err: &errOut}
		result := experiment.GraduateResult{
			Plan:           experiment.GraduatePlan{Source: "/retained/source", Destination: "/retained/project"},
			GitInitialized: true, InitialCommitMade: true, Moved: !rolledBack, RolledBack: rolledBack,
		}
		reportGraduateLocalEffects(app, result, errors.New("injected stale authority"))
		for _, retained := range []string{"Git initialization completed and is retained", "initial commit completed and is retained"} {
			if !strings.Contains(errOut.String(), retained) {
				t.Fatalf("failure hid retained effects: %s", &errOut)
			}
		}
		if out.Len() != 0 || strings.Contains(errOut.String(), "Canceled") || strings.Contains(errOut.String(), "nothing") {
			t.Fatalf("partial failure reported completion/cancellation: %s %s", &out, &errOut)
		}
		if rolledBack && !strings.Contains(errOut.String(), "rolled back") || !rolledBack && !strings.Contains(errOut.String(), "recovery intent") {
			t.Fatalf("failure hid move outcome: %s", &errOut)
		}
	}
}

func graduatedPublicationFixture(t *testing.T) (*App, *experiment.Service, experiment.GraduateResult) {
	t.Helper()
	app, _, item := graduateWizardFixture(t, "")
	service, err := newExperimentService(app)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Graduate(t.Context(), experiment.GraduateRequest{Ref: item.ID})
	if err != nil || result.Publication == nil || result.PublicationError != nil {
		t.Fatalf("graduation publication authority = %+v, %v", result, err)
	}
	return app, service, result
}

func TestGraduatePublicationRejectsDriftBetweenMoveAndPublish(t *testing.T) {
	for _, mutation := range []string{"branch", "head"} {
		t.Run(mutation, func(t *testing.T) {
			app, service, result := graduatedPublicationFixture(t)
			path := result.Plan.Destination
			args := []string{"checkout", "-b", "unreviewed"}
			if mutation == "head" {
				args = []string{"commit", "--allow-empty", "-m", "unreviewed change"}
			}
			if _, err := gitx.Run(t.Context(), path, args...); err != nil {
				t.Fatal(err)
			}
			calls := 0
			err := applyGraduateUpstreamWithPublisher(t.Context(), app, service, &result,
				graduateUpstream{action: "create", push: true, create: repoPublishRequest{Forge: forge.GitHub, Name: "scratch", Push: true}},
				func(context.Context, string, repoPublishRequest, *repoPublishExpectation) (repoPublishResult, error) {
					calls++
					return repoPublishResult{}, nil
				})
			if err == nil || calls != 0 || !strings.Contains(err.Error(), "local graduation succeeded") {
				t.Fatalf("publication drift accepted: calls=%d err=%v", calls, err)
			}
			if remote := gitx.Remote(t.Context(), path, "origin"); remote != "" {
				t.Fatalf("stale publication added origin %q", remote)
			}
		})
	}
}

type graduateChangingForge struct {
	publishTestForge
	change func(context.Context, string) error
}

func (f graduateChangingForge) PublishRepo(ctx context.Context, path string, request forge.RepoRequest) (forge.CreateRepoResult, error) {
	if err := f.change(ctx, path); err != nil {
		return forge.CreateRepoResult{}, err
	}
	return f.publishTestForge.PublishRepo(ctx, path, request)
}

func TestGraduatePublicationRetainsRemoteWhenBranchChangesDuringCreate(t *testing.T) {
	app, service, result := graduatedPublicationFixture(t)
	installGraduateForges(t, graduateForgeFixture{}, graduateForgeFixture{})
	remote := filepath.Join(t.TempDir(), "created.git")
	if _, err := gitx.Run(t.Context(), "", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	calls := 0
	publisher := graduateChangingForge{
		publishTestForge: publishTestForge{remote: remote, publishCalls: &calls},
		change: func(ctx context.Context, path string) error {
			_, err := gitx.Run(ctx, path, "checkout", "-b", "unreviewed")
			return err
		},
	}
	err := applyGraduateUpstreamWithPublisher(t.Context(), app, service, &result,
		graduateUpstream{action: "create", push: true, create: repoPublishRequest{Forge: forge.GitHub, Name: "scratch", Push: true}},
		func(ctx context.Context, path string, request repoPublishRequest, expected *repoPublishExpectation) (repoPublishResult, error) {
			return publishRepositoryWithForgeExpected(ctx, path, publisher, request, expected)
		})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "branch changed") {
		t.Fatalf("during-create drift = %v, attempts=%d", err, calls)
	}
	if out := app.Err.(*bytes.Buffer).String(); !strings.Contains(out, "Upstream created") || !strings.Contains(out, "Push was not started") {
		t.Fatalf("created/skipped effects not reported: %s", out)
	}
	refs, err := gitx.Run(t.Context(), remote, "for-each-ref", "--format=%(refname)")
	if err != nil || refs != "" {
		t.Fatalf("unreviewed branch was pushed: %q, %v", refs, err)
	}
	if _, err := os.Stat(remote); err != nil {
		t.Fatal("created upstream was deleted", err)
	}
	entry, err := app.Catalog.Get(result.Item.ID)
	if err != nil || entry.Kind != catalog.KindRepository {
		t.Fatal("local graduation was rolled back", err)
	}
}

func TestGraduateURLPushUsesReviewedCommitAndSetsTracking(t *testing.T) {
	app, _, item := graduateWizardFixture(t, "")
	remote := filepath.Join(t.TempDir(), "target.git")
	if _, err := gitx.Run(t.Context(), "", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	if err := graduateCLI(t, app, item.ID, "--yes", "--remote-url", remote, "--push"); err != nil {
		t.Fatal(err)
	}
	entry, err := app.Catalog.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := entry.LocationFor(config.Hostname())
	head, err := gitx.Run(t.Context(), location.CurrentPath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	remoteHead, err := gitx.Run(t.Context(), remote, "rev-parse", "refs/heads/main")
	if err != nil || remoteHead != head {
		t.Fatalf("published commit = %s, want %s: %v", remoteHead, head, err)
	}
	tracking, err := gitx.Run(t.Context(), location.CurrentPath, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil || tracking != "origin/main" {
		t.Fatalf("tracking = %q, %v", tracking, err)
	}
}

func TestGraduateDetachedCheckoutAllowsUpstreamWithoutPush(t *testing.T) {
	for _, create := range []bool{false, true} {
		for _, push := range []bool{false, true} {
			t.Run(fmtGraduateCase(map[bool]string{false: "attach", true: "create"}[create], push), func(t *testing.T) {
				app, _, item := graduateWizardFixture(t, "")
				path := item.Live.CurrentPath
				for _, args := range [][]string{{"init", "-b", "main"}, {"add", "--all"}, {"commit", "-m", "initial"}, {"checkout", "--detach", "HEAD"}} {
					if _, err := gitx.Run(t.Context(), path, args...); err != nil {
						t.Fatal(err)
					}
				}
				head, err := gitx.Run(t.Context(), path, "rev-parse", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				gh, _ := installGraduateForges(t, graduateForgeFixture{URL: "https://github.com/team/detached"}, graduateForgeFixture{AuthFailure: true})
				args := []string{item.ID, "--yes"}
				if create {
					args = append(args, "--forge", "github")
				} else {
					args = append(args, "--remote-url", filepath.Join(t.TempDir(), "missing.git"))
				}
				if push {
					args = append(args, "--push")
				} else {
					args = append(args, "--push=false")
				}
				err = graduateCLI(t, app, args...)
				if push && (err == nil || !strings.Contains(err.Error(), "detached")) || !push && err != nil {
					t.Fatalf("detached upstream push=%t: %v", push, err)
				}
				entry, err := app.Catalog.Get(item.ID)
				if err != nil || (entry.Kind == catalog.KindTry) != push {
					t.Fatalf("detached graduation outcome: %+v, %v", entry, err)
				}
				location, _ := entry.LocationFor(config.Hostname())
				after, err := gitx.Run(t.Context(), location.CurrentPath, "rev-parse", "HEAD")
				if err != nil || after != head {
					t.Fatalf("detached HEAD changed: %q, %v", after, err)
				}
				if push {
					for _, call := range graduateForgeCalls(t, gh) {
						if len(call) > 0 && call[0] == "repo" {
							t.Fatal("push rejection created an upstream")
						}
					}
				}
			})
		}
	}
}

func TestGraduateGuardedPushDoesNotFollowUnreviewedTags(t *testing.T) {
	app, service, result := graduatedPublicationFixture(t)
	path := result.Plan.Destination
	for _, args := range [][]string{{"tag", "-a", "unreviewed", "-m", "keep local"}, {"config", "push.followTags", "true"}, {"config", "push.recurseSubmodules", "on-demand"}} {
		if _, err := gitx.Run(t.Context(), path, args...); err != nil {
			t.Fatal(err)
		}
	}
	remote := filepath.Join(t.TempDir(), "exact.git")
	if _, err := gitx.Run(t.Context(), "", "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	if err := applyGraduateUpstream(t.Context(), app, service, &result, graduateUpstream{action: "add", url: remote, push: true}); err != nil {
		t.Fatal(err)
	}
	refs, err := gitx.Run(t.Context(), remote, "for-each-ref", "--format=%(refname)")
	if err != nil || refs != "refs/heads/main" {
		t.Fatalf("push widened the reviewed ref: %q, %v", refs, err)
	}
}
