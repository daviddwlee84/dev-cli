package runtime

import (
	"github.com/daviddwlee84/dev-cli/internal/testutil"
	"testing"
)

func nativeRuntimeFixture(t *testing.T, name, mode string) string {
	t.Helper()
	t.Setenv("DEV_RUNTIME_TEST_MODE", mode)
	return testutil.GoCommand(t, t.TempDir(), name, `package main
import ("fmt"; "os"; "strings")
func main() {
 args := os.Args[1:]
 switch os.Getenv("DEV_RUNTIME_TEST_MODE") {
 case "record":
  if err := os.WriteFile(os.Getenv("DEV_TEST_RECORD"), []byte(strings.Join(args," ")),0600); err != nil { panic(err) }
 case "herdr-attach":
  if len(args)>0 && args[0]=="workspace" { fmt.Print("{\"result\":{}}") } else if err := os.WriteFile(os.Getenv("DEV_TEST_RECORD"), []byte("attach"),0600); err != nil { panic(err) }
 case "herdr-scoped":
  for _, key := range []string{"HERDR_SOCKET_PATH","HERDR_CLIENT_SOCKET_PATH","HERDR_SESSION"} { if _,ok := os.LookupEnv(key); ok {os.Exit(91)} }
  if os.Getenv("HERDR_ENV")!="caller" {os.Exit(94)}
  if len(args)!=4 || args[0]!="--session" || args[1]!="agents" {os.Exit(92)}
  switch strings.Join(args[2:]," ") {
  case "workspace list": fmt.Print("{\"result\":{\"workspaces\":[]}}")
  case "pane list": fmt.Print("{\"result\":{\"panes\":[]}}")
  default: os.Exit(93)
  }
 default: os.Exit(95)
 }
}
`)
}
