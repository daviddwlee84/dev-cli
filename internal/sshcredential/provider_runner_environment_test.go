package sshcredential

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeProviderRunnerUsesExplicitEnvironmentAndDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("locate own test executable")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve own test directory")
	}
	t.Setenv("BW_SESSION", "ambient-test-value")
	environment := []string{"DEV_SSHCREDENTIAL_FROZEN_TEST=1", "DEV_SSHCREDENTIAL_FROZEN_DIR=" + directory, "BW_SESSION=frozen-test-value"}
	if root := os.Getenv("SystemRoot"); root != "" {
		environment = append(environment, "SystemRoot="+root)
	}
	body, err := (NativeRunner{}).RunWithEnvironment(context.Background(), executable, []string{"-test.run=^TestNativeProviderFrozenEnvironmentHelper$"}, nil, environment, directory)
	defer Wipe(body)
	if err != nil || string(body) != "ok" {
		t.Fatal("native provider did not use the explicit execution context")
	}
	if os.Getenv("BW_SESSION") != "ambient-test-value" {
		t.Fatal("native runner changed the parent environment")
	}
}

func TestNativeProviderRunnerRejectsIncompleteFrozenContext(t *testing.T) {
	for _, input := range []struct {
		name        string
		directory   string
		environment []string
	}{
		{name: "relative-tool", directory: t.TempDir(), environment: []string{}},
		{name: os.Args[0], directory: "relative-directory", environment: []string{}},
		{name: os.Args[0], directory: t.TempDir()},
	} {
		body, err := (NativeRunner{}).RunWithEnvironment(context.Background(), input.name, nil, nil, input.environment, input.directory)
		if !errors.Is(err, ErrUnavailable) || len(body) != 0 {
			t.Fatal("incomplete explicit environment fell back to ambient context")
		}
	}
}

func TestNativeProviderFrozenEnvironmentHelper(t *testing.T) {
	if os.Getenv("DEV_SSHCREDENTIAL_FROZEN_TEST") == "" {
		return
	}
	directory, err := os.Getwd()
	if err != nil || directory != os.Getenv("DEV_SSHCREDENTIAL_FROZEN_DIR") || os.Getenv("BW_SESSION") != "frozen-test-value" {
		os.Exit(2)
	}
	_, _ = io.WriteString(os.Stdout, "ok")
	os.Exit(0)
}
