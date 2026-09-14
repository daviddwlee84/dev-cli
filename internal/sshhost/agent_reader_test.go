package sshhost

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// Agent-protocol parsing remains portable even where explicit socket/pipe
// attestation is unavailable. These fixtures never execute ssh-add or attest a
// synthetic Unix socket on Windows.
func TestAgentReaderDistinguishesKnownEmptyFromFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		result   RunResult
		runErr   error
		complete bool
	}{
		{"success-empty", RunResult{}, nil, true},
		{"native-empty", RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n")}, nil, true},
		{"generic-exit-one", RunResult{ExitCode: 1}, nil, false},
		{"fetch-failed", RunResult{ExitCode: 1, Stderr: []byte("error fetching identities")}, nil, false},
		{"empty-plus-error", RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n"), Stderr: []byte("error")}, nil, false},
		{"stderr-only-empty", RunResult{ExitCode: 1, Stderr: []byte("The agent has no identities.\n")}, nil, false},
		{"unavailable", RunResult{ExitCode: 2}, nil, false},
		{"truncated-empty", RunResult{ExitCode: 1, Stdout: []byte("The agent has no identities.\n"), StdoutTruncated: true}, nil, false},
		{"truncated-stderr", RunResult{StderrTruncated: true}, nil, false},
		{"invalid-record", RunResult{Stdout: []byte("invalid public record")}, nil, false},
		{"launch-failed", RunResult{}, errors.New("native launch failed"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			s := &Service{runner: keyCatalogRunnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
				calls++
				if request.Name != "ssh-add" || !reflect.DeepEqual(request.Args, []string{"-L"}) || !reflect.DeepEqual(request.Env, []string{"LC_ALL=C", "SSH_AUTH_SOCK="}) {
					t.Fatalf("unexpected native request: %+v", request)
				}
				return test.result, test.runErr
			})}
			records, diagnostics, err := s.readAgentKeys(t.Context(), &keyAgentContext{}, "fixture inventory")
			if err != nil || calls != 1 || (len(diagnostics) == 0) != test.complete || len(records) != 0 {
				t.Fatalf("records=%d diagnostics=%v err=%v calls=%d", len(records), diagnostics, err, calls)
			}
		})
	}
}

func TestAgentReaderCancellationPreventsExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &Service{runner: panicRunner{}}
	if _, _, err := s.readAgentKeys(ctx, &keyAgentContext{}, "fixture inventory"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query=%v", err)
	}
}
