//go:build windows

package sshhost

import (
	"os"
	"path/filepath"
	"testing"
)

func fixtureHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := platformMakePrivateDirectory(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func makeFixturePrivateDirectory(t *testing.T, path string) {
	t.Helper()
	if err := platformMakePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
}

func protectFixtureFile(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := platformApplyPrivateFile(path, file); err != nil {
		t.Fatal(err)
	}
}
