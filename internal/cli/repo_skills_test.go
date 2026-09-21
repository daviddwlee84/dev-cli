package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

func TestInstallRepoSkillsBatchesMatchingSourceAndAgents(t *testing.T) {
	home := t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "cache"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	root := t.TempDir()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	testutil.GoCommand(t, bin, "skills", `package main
import ("fmt";"os";"strings")
func main() {
 f,err:=os.OpenFile(os.Getenv("SKILLS_TEST_LOG"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if err!=nil {panic(err)}
 if _,err=fmt.Fprintln(f,strings.Join(os.Args[1:]," "));err!=nil {panic(err)}
 if err=f.Close();err!=nil {panic(err)}
}
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SKILLS_TEST_LOG", logPath)
	app := &App{In: bytes.NewBuffer(nil), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	result, err := installRepoSkills(t.Context(), app, root, []repoSkillSpec{
		{Name: "one", Source: "owner/catalog", Agents: []string{"codex", "claude-code"}},
		{Name: "two", Source: "owner/catalog", Agents: []string{"codex", "claude-code"}},
		{Name: "three", Source: "other/catalog", Agents: []string{"codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Installed, ",") != "one,two,three" {
		t.Fatalf("installed = %v", result.Installed)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("provider calls = %q", lines)
	}
	if !strings.Contains(lines[0], "--skill one two --agent codex claude-code") {
		t.Fatalf("batched call = %q", lines[0])
	}
}
