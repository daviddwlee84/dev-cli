//go:build !windows

package sshhost

import (
	"os"
	"testing"
)

func fixtureHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func makeFixturePrivateDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func protectFixtureFile(t *testing.T, _ string) {
	t.Helper()
}

func assertFixturePrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated path %s mode = %v", path, info.Mode())
	}
}
