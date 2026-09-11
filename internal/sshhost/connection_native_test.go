package sshhost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedNativeProxyCommandRetainsPolicyWithoutRouteProof(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host target\n HostName target.example\n ProxyCommand provider proxy %h %p\n LocalForward 1234 service:80\n")
	calls := 0
	s, _ := NewService(paths, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Args[0] == "-G" {
			return RunResult{Stdout: []byte("hostname target.example\nuser test\nport 22\nproxycommand provider proxy %h %p\nlocalforward 1234 service:80\n")}, nil
		}
		calls++
		if hasArg(request.Args, "-F") || hasArgPair(request.Args, "-o", "ClearAllForwardings=yes") || !hasArgPair(request.Args, "-o", "ForwardAgent=no") {
			t.Fatal("native-only session rewrote user policy", request)
		}
		return RunResult{ExitCode: 255}, nil
	}))
	connection, err := s.PrepareConnection(t.Context(), "target", nil)
	if err != nil || !connection.NativeOnly || len(connection.Route.Hops) != 0 || connection.Route.state != nil {
		t.Fatal(connection, err)
	}
	result, err := connection.Run(t.Context(), ConnectionOptions{Interactive: true, ForwardAgentNo: true})
	if err != nil || result.ExitCode != 255 || calls != 1 {
		t.Fatal(result, err, calls)
	}
	if _, err := connection.Run(t.Context(), ConnectionOptions{}); err == nil || calls != 1 {
		t.Fatal("native fallback replayed")
	}
}

func TestPreparedNativeProxyCommandRevalidatesStaticClosureAndEffectivePolicy(t *testing.T) {
	for _, change := range []string{"source", "include", "effective"} {
		t.Run(change, func(t *testing.T) {
			paths := fixturePaths(t)
			writeFixture(t, paths.RootConfig, "Include includes/*.conf\nHost target\n HostName target.example\n ProxyCommand provider proxy\n")
			policy := "provider proxy"
			s, _ := NewService(paths, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
				if request.Args[0] != "-G" {
					t.Fatal("stale native connection executed")
				}
				return RunResult{Stdout: []byte("hostname target.example\nuser test\nport 22\nproxycommand " + policy + "\n")}, nil
			}))
			connection, err := s.PrepareConnection(t.Context(), "target", nil)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source":
				writeFixture(t, paths.RootConfig, "Host target\n ProxyCommand provider proxy\n# changed declaration\n")
			case "include":
				writeFixture(t, filepath.Join(paths.SSHDir, "includes", "new.conf"), "# new included source\n")
			case "effective":
				policy = "other provider"
			}
			if _, err := connection.Run(t.Context(), ConnectionOptions{}); !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("%s change accepted: %v", change, err)
			}
		})
	}
}

func TestNativeProxyFallbackNeverSwallowsCycleOrSelectedKeyConstraint(t *testing.T) {
	for _, selected := range []bool{false, true} {
		paths := fixturePaths(t)
		writeFixture(t, paths.RootConfig, "Host target\n HostName target.example\n")
		s, _ := NewService(paths, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
			if request.Args[0] != "-G" {
				t.Fatal("unsupported route executed")
			}
			if selected {
				return RunResult{Stdout: []byte("hostname target.example\nproxycommand provider\n")}, nil
			}
			return RunResult{Stdout: []byte("hostname target.example\nproxyjump target\n")}, nil
		}))
		var candidate *KeyCandidate
		if selected {
			key := bindTestCandidate(t, s, testPublicLine(0xc1, "selected"))
			candidate = &key
		}
		_, err := s.PrepareConnection(t.Context(), "target", candidate)
		if !errors.Is(err, ErrUnsupportedRoute) || !selected && !strings.Contains(err.Error(), "cycle") {
			t.Fatal("native fallback swallowed managed-route refusal", err)
		}
	}
}
