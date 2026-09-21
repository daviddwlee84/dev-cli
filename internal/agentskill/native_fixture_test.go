package agentskill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

type providerFixture struct {
	Version    string
	Help       string
	Stdout     string
	Exit       int
	Marker     string
	MarkerText string
}

func writeProviderFixture(t *testing.T, path string, fixture providerFixture) string {
	t.Helper()
	path = testutil.GoCommand(t, filepath.Dir(path), filepath.Base(path), `package main
import ("encoding/json";"fmt";"os")
type config struct { Version, Help, Stdout, Marker, MarkerText string; Exit int }
func main() {
 executable,err:=os.Executable();if err!=nil {panic(err)}
 data,err:=os.ReadFile(executable+".json");if err!=nil {panic(err)}
 var cfg config;if err=json.Unmarshal(data,&cfg);err!=nil {panic(err)}
 args:=os.Args[1:]
 if len(args)==1 && args[0]=="--version" && cfg.Version!="" {fmt.Println(cfg.Version);return}
 if len(args)==1 && args[0]=="--help" && cfg.Help!="" {fmt.Println(cfg.Help);return}
 if cfg.Marker!="" {if err=os.WriteFile(cfg.Marker,[]byte(cfg.MarkerText),0600);err!=nil {panic(err)}}
 fmt.Print(cfg.Stdout)
 os.Exit(cfg.Exit)
}
`)
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path+".json", data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
