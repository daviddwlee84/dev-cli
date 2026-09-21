package forge

import (
	"encoding/json"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

type forgeFixtureRule struct {
	Arg, Contains, Prefix, Stdout, Stderr string
	Exit                                  int
}

func installForgeFixture(t *testing.T, name string, rules []forgeFixtureRule) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	executable := testutil.GoCommand(t, dir, name, `package main
import ("encoding/json";"fmt";"os";"strings";"slices")
type rule struct { Arg, Contains, Prefix, Stdout, Stderr string; Exit int }
type config struct { Log string; Rules []rule }
func main() {
 exe,err:=os.Executable();if err!=nil {panic(err)}
 data,err:=os.ReadFile(exe+".json");if err!=nil {panic(err)}
 var cfg config;if err=json.Unmarshal(data,&cfg);err!=nil {panic(err)}
 argv:=strings.Join(os.Args[1:]," ")
 f,err:=os.OpenFile(cfg.Log,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if err!=nil {panic(err)}
 if _,err=fmt.Fprintln(f,argv);err!=nil {panic(err)};if err=f.Close();err!=nil {panic(err)}
 for _,r:=range cfg.Rules { if (r.Arg=="" || slices.Contains(os.Args[1:],r.Arg)) && strings.Contains(argv,r.Contains) && strings.HasPrefix(argv,r.Prefix) {fmt.Print(r.Stdout);fmt.Fprint(os.Stderr,r.Stderr);os.Exit(r.Exit)} }
 fmt.Fprintln(os.Stderr,"unexpected fixture invocation:",argv);os.Exit(97)
}
`)
	body, err := json.Marshal(struct {
		Log   string
		Rules []forgeFixtureRule
	}{log, rules})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(executable+".json", body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}
