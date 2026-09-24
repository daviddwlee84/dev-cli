package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	gostdruntime "runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/spf13/cobra"
)

type repoCDRuntime struct {
	runtime.None
	listed, opened, activated int
}

func (r *repoCDRuntime) Name() string                                    { return "herdr" }
func (r *repoCDRuntime) List(context.Context) ([]runtime.Session, error) { r.listed++; return nil, nil }
func (r *repoCDRuntime) Open(context.Context, string, string) (runtime.OpenResult, error) {
	r.opened++
	return runtime.OpenResult{Handle: "unexpected", Opened: true}, nil
}
func (r *repoCDRuntime) OpenWorktree(ctx context.Context, path, label string) (runtime.OpenResult, error) {
	return r.Open(ctx, path, label)
}
func (r *repoCDRuntime) Activate(context.Context, string) error { r.activated++; return nil }

func repoCDTestApp(t *testing.T) (*App, *gittest.Repo, *repoCDRuntime, *bytes.Buffer) {
	t.Helper()
	t.Setenv("DEV_SHELL_CD_FILE", "")
	t.Setenv("DEV_SHELL_CD_FD", "")
	r := gittest.New(t)
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{filepath.Dir(r.Root)}
	cfg.Paths.RepoPaths = nil
	cfg.Runtime.Backend = "herdr"
	out := new(bytes.Buffer)
	spy := &repoCDRuntime{}
	return &App{Cfg: cfg, Out: out, Err: new(bytes.Buffer), runtimeInstance: spy, runtimeOverride: "herdr", runtimesByName: map[string]runtime.Runtime{"herdr": spy}}, r, spy, out
}

func TestRepoCDUsesLoadedConfigAndBypassesEveryRuntimeSelection(t *testing.T) {
	app, r, spy, out := repoCDTestApp(t)
	loaded := app.Cfg
	app.Cfg = config.Config{}
	cmd := newRepoCDCmd(app)
	// Simulate the root's Load after the command tree has been constructed.
	app.Cfg = loaded
	root := &cobra.Command{Use: "dev", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().StringVar(&app.runtimeOverride, "runtime", "", "runtime override")
	family := &cobra.Command{Use: "repo"}
	family.AddCommand(cmd)
	root.AddCommand(family)
	root.SetArgs([]string{"--runtime", "herdr", "repo", "cd", "repo"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if spy.listed != 0 || spy.opened != 0 || spy.activated != 0 {
		t.Fatalf("cd contacted a runtime: %+v", spy)
	}
	if app.runtimeInstance != spy || app.runtimeOverride != "herdr" || app.Cfg.Runtime.Backend != "herdr" {
		t.Fatal("cd changed the shared App runtime selection")
	}
	if !strings.HasSuffix(out.String(), "cd "+shellQuote(r.Root)+"\n") {
		t.Fatalf("missing shell navigation: %q", out.String())
	}
	// An ordinary subsequent open still uses the configured/injected backend.
	open := newRepoOpenCmd(app)
	open.SetArgs([]string{r.Root})
	if err := open.Execute(); err != nil {
		t.Fatal(err)
	}
	if spy.opened != 1 || spy.activated != 1 {
		t.Fatalf("subsequent open lost its runtime: %+v", spy)
	}
}

func TestRepoCDPreservesStandaloneQuotingAndFileTransport(t *testing.T) {
	for _, fileTransport := range []bool{false, true} {
		t.Run(fmt.Sprint(fileTransport), func(t *testing.T) {
			app, r, spy, out := repoCDTestApp(t)
			target := filepath.Join(filepath.Dir(r.Root), "lazy set's repo")
			if err := os.Rename(r.Root, target); err != nil {
				t.Fatal(err)
			}
			directive := filepath.Join(t.TempDir(), "directory")
			if fileTransport {
				t.Setenv("DEV_SHELL_CD_FILE", directive)
			}
			cmd := newRepoCDCmd(app)
			cmd.SetArgs([]string{target})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if spy.listed != 0 || spy.opened != 0 || spy.activated != 0 {
				t.Fatalf("runtime called: %+v", spy)
			}
			if fileTransport {
				body, err := os.ReadFile(directive)
				if err != nil || string(body) != target+"\x00" {
					t.Fatalf("directory transport=%q %v", body, err)
				}
				if strings.Contains(out.String(), "\ncd ") {
					t.Fatal("wrapper transport also printed a shell directive")
				}
			} else if !strings.HasSuffix(out.String(), "cd "+shellQuote(target)+"\n") {
				t.Fatalf("incorrect quoting: %q", out.String())
			}
		})
	}
}

func TestRepoCDResolutionArgumentsAndCompletionMatchOpen(t *testing.T) {
	app, r, spy, _ := repoCDTestApp(t)
	other := gittest.New(t)
	for i, repository := range []*gittest.Repo{r, other} {
		renamed := filepath.Join(filepath.Dir(repository.Root), fmt.Sprintf("project-%d", i+1))
		if err := os.Rename(repository.Root, renamed); err != nil {
			t.Fatal(err)
		}
		repository.Root = renamed
	}
	app.Cfg.Paths.ScanRoots = []string{filepath.Dir(r.Root), filepath.Dir(other.Root)}
	for _, args := range [][]string{{"not-a-repository"}, {"project"}, {}, {r.Root, "extra"}} {
		var errs []string
		for _, cmd := range []*cobra.Command{newRepoOpenCmd(app), newRepoCDCmd(app)} {
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs(args)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected resolution/argument error for %v", args)
			}
			errs = append(errs, err.Error())
		}
		if errs[0] != errs[1] {
			t.Fatalf("open/cd errors differ for %v: %v", args, errs)
		}
	}
	app.configPath = filepath.Join(t.TempDir(), "config.toml")
	body := fmt.Sprintf("[paths]\nscan_roots = [%q, %q]\nrepo_paths = []\nstate_dir = %q\n", filepath.Dir(r.Root), filepath.Dir(other.Root), t.TempDir())
	if err := os.WriteFile(app.configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	open, cd := newRepoOpenCmd(app), newRepoCDCmd(app)
	a, ad := open.ValidArgsFunction(open, nil, "proj")
	b, bd := cd.ValidArgsFunction(cd, nil, "proj")
	if len(a) != 2 || !reflect.DeepEqual(a, b) || ad != bd {
		t.Fatalf("completion differs: %v/%v vs %v/%v", a, ad, b, bd)
	}
	if spy.listed != 0 || spy.opened != 0 || spy.activated != 0 {
		t.Fatalf("failed resolution contacted runtime: %+v", spy)
	}
	if registered, _, err := newRepoCmd(app).Find([]string{"cd"}); err != nil || registered.Name() != "cd" {
		t.Fatalf("repo cd is not registered: %v", err)
	}
}

func TestRepoCDPrivateDescriptor(t *testing.T) {
	if os.Getenv("DEV_REPO_CD_HELPER") == "1" {
		out := new(bytes.Buffer)
		spy := &repoCDRuntime{}
		app := &App{Cfg: config.Default(), Out: out, Err: new(bytes.Buffer), runtimeInstance: spy, runtimeOverride: "herdr"}
		cmd := newRepoCDCmd(app)
		cmd.SetArgs([]string{os.Getenv("DEV_REPO_CD_TARGET")})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if spy.listed != 0 || spy.opened != 0 || spy.activated != 0 {
			t.Fatal("descriptor navigation contacted runtime")
		}
		if strings.Contains(out.String(), "\ncd ") {
			t.Fatal("descriptor transport also printed fallback")
		}
		return
	}
	if gostdruntime.GOOS == "windows" {
		t.Skip("Windows uses the file transport tested above; ExtraFiles is POSIX-only")
	}
	_, r, _, _ := repoCDTestApp(t)
	file, err := os.CreateTemp(t.TempDir(), "directive")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestRepoCDPrivateDescriptor$")
	child.ExtraFiles = []*os.File{file}
	child.Env = append(os.Environ(), "DEV_REPO_CD_HELPER=1", "DEV_REPO_CD_TARGET="+r.Root, "DEV_SHELL_CD_FD=3", "DEV_SHELL_CD_FILE=")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("descriptor helper: %v\n%s", err, output)
	}
	body, err := os.ReadFile(file.Name())
	if err != nil || string(body) != r.Root+"\x00" {
		t.Fatalf("descriptor transport=%q %v", body, err)
	}
}
