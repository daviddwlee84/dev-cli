package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/fleet"
	"github.com/daviddwlee84/dev-cli/internal/gitx/gittest"
)

func TestFleetHerdrRepoPathAndRemoteIdentityMustBothMatch(t *testing.T) {
	r := gittest.New(t)
	r.Git("remote", "add", "origin", "https://github.com/acme/correct.git")
	cfg := config.Default()
	cfg.Paths.ScanRoots = []string{filepath.Dir(r.Root)}
	cfg.Paths.RepoPaths = nil
	app := &App{Cfg: cfg}
	request := fleet.OpenRequest{Path: r.Root, RemoteIdentity: "github.com/acme/wrong"}
	if _, err := resolveFleetOpenRepository(t.Context(), app, request); err == nil {
		t.Fatal("path shortcut bypassed remote identity")
	}
	request.RemoteIdentity = catalog.NormalizeRemoteIdentity("https://github.com/acme/correct.git")
	if _, err := resolveFleetOpenRepository(t.Context(), app, request); err != nil {
		t.Fatal(err)
	}
	request.Path = filepath.Join(r.Root, "other")
	if _, err := resolveFleetOpenRepository(t.Context(), app, request); err == nil {
		t.Fatal("remote identity bypassed mismatched exact path")
	}
}

func TestFleetHerdrRepoCheckAndPrepareStayScopedAndDetached(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX Herdr target")
	}
	r := gittest.New(t)
	home := dotfileTestHome(t)
	configPath := filepath.Join(home, "dev.toml")
	dotfileTestWrite(t, configPath, fmt.Sprintf("[paths]\nscan_roots=[%q]\n", filepath.Dir(r.Root)))
	log := filepath.Join(home, "herdr-calls")
	t.Setenv("DEV_HERDR_REPO_LOG", log)
	t.Setenv("DEV_HERDR_REPO_STATE", filepath.Join(home, "workspace-created"))
	t.Setenv("DEV_HERDR_REPO_PANES", fmt.Sprintf(`{"result":{"panes":[{"workspace_id":"w7","pane_id":"w7:p1","cwd":%q}]}}`, r.Root))
	bin := filepath.Join(home, "bin", "herdr")
	program := `#!/bin/sh
printf '%s\n' "$*" >> "$DEV_HERDR_REPO_LOG"
[ "$1" = --session ] && [ "$2" = agents ] || exit 91
shift 2
case "$1 $2" in
'workspace list')
  if [ -e "$DEV_HERDR_REPO_STATE" ]; then printf '{"result":{"workspaces":[{"workspace_id":"w7"}]}}'; else printf '{"result":{"workspaces":[]}}'; fi;;
'pane list')
  if [ -e "$DEV_HERDR_REPO_STATE" ]; then printf '%s' "$DEV_HERDR_REPO_PANES"; else printf '{"result":{"panes":[]}}'; fi;;
'worktree open')
  case " $* " in *' --no-focus '*) ;; *) exit 92;; esac
  : > "$DEV_HERDR_REPO_STATE"
  printf '{"result":{"workspace":{"workspace_id":"w7"},"root_pane":{"pane_id":"w7:p1","workspace_id":"w7"},"already_open":false}}';;
*) exit 93;;
esac
`
	dotfileTestWrite(t, bin, program)
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_SESSION", "wrong-controller-session")
	request := fleet.HerdrRepoRequest{SchemaVersion: 1, Phase: "check", Session: "agents", Repository: fleet.OpenRequest{Path: r.Root}}
	run := func(request fleet.HerdrRepoRequest) (fleet.HerdrRepoResult, error) {
		t.Helper()
		data, _ := json.Marshal(request)
		var out, errOut bytes.Buffer
		app := &App{In: bytes.NewReader(data), Out: &out, Err: &errOut}
		cleanups := 0
		root := newRootCommandWithCleanup(app, func() { cleanups++ })
		root.SetArgs([]string{"--config", configPath, "fleet", fleet.HerdrRepoHelper})
		err := root.Execute()
		if cleanups != 0 || app.Catalog != nil || app.Tasks != nil {
			t.Fatal("helper ran startup effects")
		}
		var result fleet.HerdrRepoResult
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &result)
		}
		return result, err
	}
	checked, err := run(request)
	if err != nil || checked.RuntimeState != "ready" || checked.Workspace != "" {
		t.Fatalf("check = %+v / %v", checked, err)
	}
	if err := checked.Validate(request); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "open") || strings.Contains(string(calls), "focus") || strings.Contains(string(calls), "create") {
		t.Fatalf("check changed runtime: %s", calls)
	}
	request.Phase, request.ExpectedIdentity = "prepare", checked.RepositoryIdentity
	prepared, err := run(request)
	if err != nil || prepared.Workspace != "w7" || prepared.Session != "agents" || !prepared.Created {
		t.Fatalf("prepare = %+v / %v", prepared, err)
	}
	if err := prepared.Validate(request); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if strings.Contains(string(calls), "workspace focus") || strings.Contains(string(calls), "--remote") {
		t.Fatalf("preparation activated a client: %s", calls)
	}
	t.Setenv("DEV_HERDR_REPO_PANES", `{"result":{"panes":[{"workspace_id":"w7","pane_id":"w7:p1","cwd":"/wrong-checkout"}]}}`)
	if _, err := run(request); err == nil || !strings.Contains(err.Error(), "retained for inspection") {
		t.Fatalf("wrong checkout coverage was accepted or rolled back: %v", err)
	}
	calls, _ = os.ReadFile(log)
	if strings.Contains(string(calls), "workspace close") || strings.Contains(string(calls), "workspace focus") {
		t.Fatalf("unknown coverage changed focus or removed a workspace: %s", calls)
	}
	request.ExpectedIdentity = strings.Repeat("a", 64)
	before := string(calls)
	if _, err := run(request); err == nil {
		t.Fatal("stale repository check was accepted")
	}
	calls, _ = os.ReadFile(log)
	if string(calls) != before {
		t.Fatal("stale repository check invoked native runtime")
	}
	request.Phase, request.Session, request.ExpectedIdentity = "check", "", ""
	if _, err := run(request); err == nil {
		t.Fatal("helper accepted ambient session")
	}
}

func TestFleetHerdrRepoReadOnlyCheckAllowsNativeBootstrapLater(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("POSIX Herdr target")
	}
	r := gittest.New(t)
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(gitBin, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cfg := config.Default()
	cfg.Paths.ScanRoots, cfg.Paths.RepoPaths = []string{filepath.Dir(r.Root)}, nil
	request := fleet.HerdrRepoRequest{SchemaVersion: 1, Phase: "check", Session: "new-server", Repository: fleet.OpenRequest{Path: r.Root}}
	result, err := prepareFleetHerdrRepository(t.Context(), &App{Cfg: cfg}, request)
	if err != nil || result.RuntimeState != "needs-server" || result.RuntimeReason != "herdr-not-installed" || result.Workspace != "" {
		t.Fatalf("check = %+v / %v", result, err)
	}
	request.Phase, request.ExpectedIdentity = "prepare", result.RepositoryIdentity
	if _, err := prepareFleetHerdrRepository(t.Context(), &App{Cfg: cfg}, request); err == nil {
		t.Fatal("prepare ran before native server bootstrap")
	}
}
