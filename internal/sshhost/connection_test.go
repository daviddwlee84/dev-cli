package sshhost

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPreparedConnectionExecutesOnceAndPreservesExitCode(t *testing.T) {
	calls := 0
	s, _ := bootstrapService(t, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Args[0] == "-G" {
			return RunResult{Stdout: []byte("hostname target.example\nuser test\nport 22\n")}, nil
		}
		calls++
		if !hasArgPair(request.Args, "-S", "none") || !hasArgPair(request.Args, "-o", "ForwardAgent=no") || !request.Interactive || request.Args[len(request.Args)-1] != "once" {
			t.Fatal(request)
		}
		return RunResult{ExitCode: 255}, nil
	}))
	connection, err := s.PrepareConnection(t.Context(), "target", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := connection.Run(t.Context(), ConnectionOptions{Args: []string{"once"}, Interactive: true, SuppressForwarding: true})
	if err != nil || result.ExitCode != 255 || calls != 1 {
		t.Fatal(result, err, calls)
	}
	if _, err := connection.Run(t.Context(), ConnectionOptions{}); err == nil || calls != 1 {
		t.Fatal("connection replayed")
	}
}

func TestPreparedConnectionSelectedKeyUsesPrivateConfigAndCleans(t *testing.T) {
	configPath := ""
	s, _ := bootstrapService(t, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Args[0] == "-G" {
			return RunResult{Stdout: []byte("hostname target.example\nuser test\nport 22\nidentityagent SSH_AUTH_SOCK\n")}, nil
		}
		configPath = sshArgForCatalogTest(request.Args, "-F")
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"BatchMode no", "PreferredAuthentications publickey", "NumberOfPasswordPrompts 3", "ControlPath none"} {
			if !strings.Contains(string(data), want) {
				t.Fatalf("missing %q", want)
			}
		}
		return RunResult{}, nil
	}))
	candidate := bindTestCandidate(t, s, testPublicLine(0xb5, "selected"))
	connection, err := s.PrepareConnection(t.Context(), "target", &candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Run(t.Context(), ConnectionOptions{Interactive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private config retained: %v", err)
	}
}

func TestPreparedConnectionRejectsChangedPolicyBeforeExecution(t *testing.T) {
	queries := 0
	s, _ := bootstrapService(t, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Args[0] != "-G" {
			t.Fatal("changed route executed")
		}
		queries++
		policy := "yes"
		if queries > 1 {
			policy = "no"
		}
		return RunResult{Stdout: []byte("hostname target.example\nuser test\nport 22\nstricthostkeychecking " + policy + "\n")}, nil
	}))
	connection, err := s.PrepareConnection(t.Context(), "target", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Run(t.Context(), ConnectionOptions{}); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("policy source change accepted: %v", err)
	}
}
