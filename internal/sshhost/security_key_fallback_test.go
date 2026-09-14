package sshhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHardwareDefaultKeySkipsOnlyUnrequestedWorkingJump(t *testing.T) {
	for _, operation := range []bool{false, true} {
		for _, scenario := range []string{"skip", "install-jump", "target-mismatch", "source-changed"} {
			name := map[bool]string{false: "direct", true: "operation"}[operation] + "/" + scenario
			t.Run(name, func(t *testing.T) {
				f := newSecurityKeyFixture(t)
				provider := filepath.Join(f.service.paths.Home, "selected-provider.so")
				writeFixture(t, provider, "inert provider")
				plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{Provider: provider})
				key, err := f.service.ApplyKey(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				targetProvider := plan.SecurityKeyProvider
				if scenario == "target-mismatch" {
					targetProvider = "/native/other-target-provider"
				}
				configs := map[string]string{
					"target": "hostname target.example\nuser test\nport 22\nproxyjump jump\nidentityfile " + plan.IdentityFile + "\nidentitiesonly yes\nsecuritykeyprovider " + targetProvider + "\nstricthostkeychecking yes\nuserknownhostsfile none\nglobalknownhostsfile none\n",
					"jump":   "hostname jump.example\nuser jump\nport 22\nidentityfile /native/jump-key\nidentityagent /native/jump-agent\nsecuritykeyprovider /native/different-jump-provider\nstricthostkeychecking yes\nuserknownhostsfile none\nglobalknownhostsfile none\n",
				}
				jumpLogins, installers, targetProofs := 0, 0, 0
				changed := false
				jumpSuccess := func() (RunResult, error) {
					jumpLogins++
					if scenario == "source-changed" && !changed {
						changed = true
						writeFixture(t, f.tools["ssh"], "changed selected client")
					}
					return RunResult{}, nil
				}
				enrollmentRunner := f.service.runner
				f.service.runner = keyCatalogRunnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
					if request.Name != "ssh" && request.Name != f.tools["ssh"] {
						return enrollmentRunner.Run(ctx, request)
					}
					if request.Args[0] == "-G" {
						return RunResult{Stdout: []byte(configs[request.Args[len(request.Args)-1]])}, nil
					}
					if len(request.Stdin) != 0 {
						installers++
						return RunResult{ExitCode: 255}, nil
					}
					configPath := sshArgForCatalogTest(request.Args, "-F")
					if configPath == "" {
						if request.Args[len(request.Args)-2] == "jump" {
							return jumpSuccess()
						}
						return RunResult{ExitCode: 255}, nil
					}
					data, err := os.ReadFile(configPath)
					if err != nil {
						t.Fatal(err)
					}
					stanzas := hardwareProofStanzas(t, data)
					var target hardwareProofStanza
					for _, stanza := range stanzas {
						if strings.Join(stanza["hostname"], "") == "jump.example" {
							if strings.Join(stanza["batchmode"], "") != "yes" || strings.Join(stanza["securitykeyprovider"], "") != "/native/different-jump-provider" {
								t.Fatalf("working jump policy changed: %+v", stanza)
							}
						}
						if strings.Join(stanza["hostname"], "") == "target.example" {
							target = stanza
						}
					}
					if log := sshArgForCatalogTest(request.Args, "-E"); log != "" {
						if err := os.WriteFile(log, []byte("Authenticated to host using \"publickey\".\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if target == nil {
						return jumpSuccess()
					}
					if !request.Interactive {
						return RunResult{ExitCode: 255}, nil
					}
					targetProofs++
					if strings.Join(target["securitykeyprovider"], "") != plan.SecurityKeyProvider {
						t.Fatalf("target provider changed: %+v", target)
					}
					return RunResult{}, nil
				})
				request := BootstrapRequest{Alias: "target", TargetRemoteOS: RemoteOSPOSIX, Interactive: true, Key: key, InstallOnWorkingJump: scenario == "install-jump"}
				if operation {
					op, err := f.service.PrepareAuthentication(t.Context(), "target")
					if err != nil {
						t.Fatal(err)
					}
					defer op.Close()
					request.Authentication = op
				}
				result, err := f.service.Bootstrap(t.Context(), request)
				if installers != 0 {
					t.Fatalf("local provider incompatibility attempted installation: %d", installers)
				}
				if scenario == "source-changed" {
					if !errors.Is(err, ErrSourceChanged) || result.Ready || result.Hops[0].Skipped {
						t.Fatalf("source change swallowed: %+v %v", result, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "skip" {
					if !result.Ready || !result.Hops[0].Skipped || !result.Hops[0].OrdinaryReady || jumpLogins != 2 || targetProofs != 2 {
						t.Fatalf("default fallback did not skip working jump: %+v jump=%d target=%d", result, jumpLogins, targetProofs)
					}
				} else {
					index := 0
					if scenario == "target-mismatch" {
						index = 1
					}
					if result.Ready || result.Hops[index].Code != "selected_security_key_provider_incompatible" || targetProofs != 0 {
						t.Fatalf("requested hop/provider mismatch not blocked: %+v", result)
					}
					if scenario == "install-jump" && result.Hops[0].Skipped {
						t.Fatal("explicit jump installation request was skipped")
					}
				}
			})
		}
	}
}
