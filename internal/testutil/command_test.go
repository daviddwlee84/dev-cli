package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoCommandRunsNativelyWithIsolatedPathAndHome(t *testing.T) {
	const source = `package main
import ("fmt"; "os"; "strings")
func main() { home, err := os.UserHomeDir(); if err != nil { panic(err) }; fmt.Print(home+"\n"+strings.Join(os.Args[1:], "\n")); os.Exit(7) }
`
	dir := t.TempDir()
	first := GoCommand(t, dir, "provider", source)
	home := filepath.Join(t.TempDir(), "isolated-home")
	SetHome(t, home)
	t.Setenv("PATH", dir)
	second := GoCommand(t, dir, "other-provider", source)
	for _, command := range []string{first, second} {
		out, err := exec.Command(command, "a value with spaces", "literal&argument").CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 7 {
			t.Fatalf("fixture exit = %v, output %q", err, out)
		}
		if want := home + "\na value with spaces\nliteral&argument"; string(out) != want {
			t.Fatalf("fixture output = %q, want %q", out, want)
		}
	}
	if found, err := exec.LookPath("provider"); err != nil || !strings.EqualFold(found, first) {
		t.Fatalf("native fixture lookup = %q, %v; want %q", found, err, first)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home isolation should not create user state: %v", err)
	}
}
