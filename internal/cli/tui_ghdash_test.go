package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

const ghDashFixture = `package main
import("encoding/json"; "fmt"; "os")
func main(){
 cwd,_:=os.Getwd(); info,_:=os.Stat(cwd); mode:=uint32(0); if info!=nil{mode=uint32(info.Mode().Perm())}
 f,_:=os.OpenFile(os.Getenv("DEV_GHDASH_TEST_LOG"),os.O_APPEND|os.O_CREATE|os.O_WRONLY,0600)
 if f!=nil{json.NewEncoder(f).Encode(map[string]any{"args":os.Args[1:],"repo":os.Getenv("GH_REPO"),"host":os.Getenv("GH_HOST"),"cwd":cwd,"mode":mode,"update":os.Getenv("GH_NO_UPDATE_NOTIFIER"),"extension_update":os.Getenv("GH_NO_EXTENSION_UPDATE_NOTIFIER")});f.Close()}
 if len(os.Args)==3 && os.Args[1]=="extension" && os.Args[2]=="list" { fmt.Println("gh dash dlvhdr/gh-dash v4.0.0");return }
 if len(os.Args)==2 && os.Args[1]=="dash" { if os.Getenv("DEV_GHDASH_TEST_WRITE")=="1" {os.WriteFile("created.txt",[]byte("native user work"),0600)}; os.Exit(7) }
 os.Exit(99)
}`

type ghDashInvocation struct {
	Args            []string
	Repo, Host, Cwd string
	Mode            uint32
	Update          string
	ExtensionUpdate string `json:"extension_update"`
}

func TestGHDashProbeAndNativeLaunchKeepExactContextAndCleanNeutralDirectory(t *testing.T) {
	bin, directory := t.TempDir(), t.TempDir()
	testutil.GoCommand(t, bin, "gh", ghDashFixture)
	log := filepath.Join(directory, "calls.jsonl")
	t.Setenv("DEV_GHDASH_TEST_LOG", log)
	t.Setenv("PATH", bin)
	t.Setenv("GH_REPO", "unrelated/old")
	t.Setenv("GH_HOST", "github.old.test")
	if !ghDashInstalled(context.Background()) {
		t.Fatal("local extension not detected")
	}
	readCalls := func() []ghDashInvocation {
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var calls []ghDashInvocation
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var call ghDashInvocation
			err := decoder.Decode(&call)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, call)
		}
		return calls
	}
	if calls := readCalls(); len(calls) != 1 || strings.Join(calls[0].Args, " ") != "extension list" {
		t.Fatalf("probe launched interactive provider: %+v", calls)
	}
	workflow := &ghDashWorkflow{ctx: context.Background(), app: App{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}, request: tui.GHDashRequest{Repository: forge.RemoteRepo{Forge: forge.GitHub, FullName: "owner/project", URL: "https://github.enterprise.test/owner/project"}}}
	if err := workflow.Run(); err == nil {
		t.Fatal("native child exit status was lost")
	}
	calls := readCalls()
	if len(calls) != 3 {
		t.Fatalf("unexpected calls: %+v", calls)
	}
	launched := calls[2]
	if strings.Join(launched.Args, " ") != "dash" || launched.Repo != "github.enterprise.test/owner/project" || launched.Host != "github.enterprise.test" || launched.Update != "1" || launched.ExtensionUpdate != "1" {
		t.Fatalf("wrong launch context: %+v", launched)
	}
	if !strings.HasPrefix(filepath.Base(launched.Cwd), "dev-gh-dash-") {
		t.Fatalf("did not use neutral directory: %s", launched.Cwd)
	}
	if runtime.GOOS != "windows" && launched.Mode != 0o700 {
		t.Fatalf("neutral directory mode=%o", launched.Mode)
	}
	if _, err := os.Stat(launched.Cwd); !os.IsNotExist(err) {
		t.Fatalf("temporary directory survived child failure: %v", err)
	}
	if os.Getenv("GH_REPO") != "unrelated/old" || os.Getenv("GH_HOST") != "github.old.test" {
		t.Fatal("launcher changed controller environment")
	}
	// Explicit native keybindings may create work in their working directory.
	// Returning from gh-dash must retain that work even when the process fails.
	t.Setenv("DEV_GHDASH_TEST_WRITE", "1")
	warnings := &bytes.Buffer{}
	workflow.app.Err = warnings
	if err := workflow.Run(); err == nil {
		t.Fatal("native child exit status was lost")
	}
	calls = readCalls()
	retained := calls[len(calls)-1].Cwd
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	data, err := os.ReadFile(filepath.Join(retained, "created.txt"))
	if err != nil || string(data) != "native user work" || !strings.Contains(warnings.String(), filepath.Base(retained)) {
		t.Fatalf("native work was deleted or its retained path hidden: %q, %v", data, err)
	}
}

func TestGHDashExtensionInventoryAndEnvironmentAreExact(t *testing.T) {
	for _, test := range []struct {
		text string
		want bool
	}{
		{"gh dash dlvhdr/gh-dash v4.0.0\n", true}, {"gh dash other/gh-dash v4.0.0\n", true}, {"gh dash /local/development\n", true}, {"gh dash-other dlvhdr/gh-dash\n", false}, {"gh dashboard dlvhdr/gh-dash\n", false}, {"gh other dlvhdr/gh-dash\n", false}, {"", false},
	} {
		if got := ghDashExtensionListed(test.text); got != test.want {
			t.Fatalf("%q=%v", test.text, got)
		}
	}
	environment := ghDashEnvironment([]string{"GH_HOST=old", "gh_host=duplicate", "GH_REPO=old/repo", "KEEP=value", "GH_FORCE_TTY=1"}, map[string]string{"GH_HOST": "github.com", "GH_REPO": "github.com/me/repo"}, "GH_FORCE_TTY")
	if strings.Join(environment, "\n") != "KEEP=value\nGH_HOST=github.com\nGH_REPO=github.com/me/repo" {
		t.Fatal(environment)
	}
	buffer := &ghDashBoundedBuffer{}
	data := bytes.Repeat([]byte("x"), 128*1024)
	if size, err := buffer.Write(data); err != nil || size != len(data) || !buffer.overflow || buffer.Len() != 64*1024 {
		t.Fatalf("unbounded provider output: bytes=%d overflow=%v err=%v", buffer.Len(), buffer.overflow, err)
	}
}

func TestGHDashRejectsNonGitHubBeforeInteractiveLaunch(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workflow := &ghDashWorkflow{ctx: context.Background(), request: tui.GHDashRequest{Repository: forge.RemoteRepo{Forge: forge.GitLab, FullName: "owner/project", URL: "https://gitlab.com/owner/project"}}}
	if err := workflow.Run(); err == nil || !strings.Contains(err.Error(), "exact GitHub repository") {
		t.Fatalf("unsupported target tried tool discovery: %v", err)
	}
}
