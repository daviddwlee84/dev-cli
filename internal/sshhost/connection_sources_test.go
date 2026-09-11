package sshhost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPreparedConnectionRejectsSourceMutationDuringEvaluation(t *testing.T) {
	for _, nativeOnly := range []bool{false, true} {
		mode := "resolved-route"
		if nativeOnly {
			mode = "native-proxy"
		}
		for _, phase := range []string{"prepare", "run"} {
			for _, change := range []string{"root", "included-content", "include-membership"} {
				t.Run(mode+"/"+phase+"/"+change, func(t *testing.T) {
					paths := fixturePaths(t)
					included := filepath.Join(paths.SSHDir, "sources", "target.conf")
					writeFixture(t, paths.RootConfig, "Include sources/*.conf\n")
					writeFixture(t, included, "Host target\n HostName old.example\n")
					mutate := phase == "prepare"
					sessions := 0
					s, err := NewService(paths, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
						if request.Name != "ssh" || request.Args[0] != "-G" {
							sessions++
							return RunResult{}, nil
						}
						if mutate {
							mutate = false
							switch change {
							case "root":
								writeFixture(t, paths.RootConfig, "Host target\n HostName changed.example\nInclude sources/*.conf\n")
							case "included-content":
								writeFixture(t, included, "Host target\n HostName changed.example\n")
							case "include-membership":
								writeFixture(t, filepath.Join(paths.SSHDir, "sources", "000-new.conf"), "Host target\n HostName changed.example\n")
							}
						}
						// Match exec may change a source after its old HostName was
						// parsed; unchanged effective output is not a source proof.
						output := "hostname old.example\nuser test\nport 22\n"
						if nativeOnly {
							output += "proxycommand provider proxy\n"
						}
						return RunResult{Stdout: []byte(output)}, nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					connection, err := s.PrepareConnection(t.Context(), "target", nil)
					if phase == "run" {
						if err != nil {
							t.Fatal(err)
						}
						mutate = true
						_, err = connection.Run(t.Context(), ConnectionOptions{})
					}
					if !errors.Is(err, ErrSourceChanged) || sessions != 0 {
						t.Fatalf("changed source accepted: err=%v sessions=%d", err, sessions)
					}
				})
			}
		}
	}
}

func TestPreparedConnectionRejectsNewRootBeforeFreshEvaluation(t *testing.T) {
	queries := 0
	s, paths := bootstrapService(t, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Name != "ssh" || request.Args[0] != "-G" {
			t.Fatal("changed source reached native session")
		}
		queries++
		return RunResult{Stdout: []byte("hostname target.example\nuser test\nport 22\n")}, nil
	}))
	connection, err := s.PrepareConnection(t.Context(), "target", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, paths.RootConfig, "Host target\n HostName changed.example\n")
	if _, err := connection.Run(t.Context(), ConnectionOptions{}); !errors.Is(err, ErrSourceChanged) || queries != 1 {
		t.Fatalf("new source was evaluated: err=%v queries=%d", err, queries)
	}
}

func TestPreparedConnectionRechecksSourcesAfterSelectedKeyVerification(t *testing.T) {
	paths := fixturePaths(t)
	writeFixture(t, paths.RootConfig, "Host target\n HostName old.example\n")
	identity := filepath.Join(paths.SSHDir, "selected")
	publicLine := testPublicLine(0xd2, "selected fixture")
	writeFixture(t, identity, "opaque fixture identity")
	writeFixture(t, identity+".pub", string(publicLine)+"\n")
	sessions, verifications := 0, 0
	s, err := NewService(paths, runnerFunc(func(_ context.Context, request RunRequest) (RunResult, error) {
		if request.Name == "ssh-keygen" {
			verifications++
			writeFixture(t, paths.RootConfig, "Host target\n HostName changed.example\n")
			return RunResult{Stdout: publicLine}, nil
		}
		if request.Name == "ssh" && request.Args[0] == "-G" {
			return RunResult{Stdout: []byte("hostname old.example\nuser test\nport 22\n")}, nil
		}
		sessions++
		return RunResult{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.Catalog(t.Context(), KeyCatalogRequest{LocalOnly: true, NoAgent: true})
	if err != nil || len(catalog.Candidates) != 1 {
		t.Fatal(catalog, err)
	}
	connection, err := s.PrepareConnection(t.Context(), "target", &catalog.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Run(t.Context(), ConnectionOptions{}); !errors.Is(err, ErrSourceChanged) || sessions != 0 || verifications != 1 {
		t.Fatalf("late source mutation accepted: err=%v sessions=%d verifications=%d", err, sessions, verifications)
	}
}
