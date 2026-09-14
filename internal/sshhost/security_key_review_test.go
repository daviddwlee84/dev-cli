package sshhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurityKeyDerivationReplacementNeverGrantsCleanupAuthority(t *testing.T) {
	for _, public := range []bool{false, true} {
		t.Run(map[bool]string{false: "private", true: "public"}[public], func(t *testing.T) {
			f := newSecurityKeyFixture(t)
			var replaced string
			const foreign = "foreign replacement must survive unchanged"
			f.derive = func(request RunRequest) (RunResult, error) {
				replaced = sshArgForCatalogTest(request.Args, "-f")
				if public {
					replaced += ".pub"
				}
				if err := os.Rename(replaced, replaced+".original"); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, replaced, foreign)
				return RunResult{Stdout: f.line}, nil
			}
			result, err := f.service.ApplyKey(t.Context(), f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{}))
			if err == nil || result.Candidate.state != nil || result.Hardware.RecoveryIdentityPath != "" || result.Hardware.RecoveryPublicPath != "" {
				t.Fatalf("replacement authorized recovery: %+v %v", result, err)
			}
			data, err := os.ReadFile(replaced)
			if err != nil || string(data) != foreign {
				t.Fatalf("foreign replacement was deleted/changed: %q %v", data, err)
			}
			found := false
			for _, file := range result.LocalFiles {
				if file.Path == replaced && file.Status == "unknown" {
					found = true
				}
			}
			if !found {
				t.Fatalf("ambiguous replacement path omitted: %+v", result.LocalFiles)
			}
		})
	}
}

func TestSecurityKeyPartialPublicationRetainsStandalonePathObservation(t *testing.T) {
	f := newSecurityKeyFixture(t)
	plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{})
	var changedPublic string
	const foreign = "foreign public-stage replacement"
	f.service.afterSecurityKeyIdentityCommit = func() {
		paths, err := filepath.Glob(filepath.Join(f.service.paths.SSHDir, ".dev-sk-recovery-*.pub"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("public stage=%v err=%v", paths, err)
		}
		changedPublic = paths[0]
		if err := os.Rename(changedPublic, changedPublic+".original"); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, changedPublic, foreign)
	}
	result, err := f.service.ApplyKey(t.Context(), plan)
	if err == nil || !result.Created || !result.Retained || result.Hardware.RecoveryIdentityPath != "" || result.Hardware.RecoveryPublicPath != "" || result.Candidate.state != nil {
		t.Fatalf("partial pair result=%+v err=%v", result, err)
	}
	found := false
	for _, file := range result.LocalFiles {
		if file.Path == plan.IdentityFile && file.Kind == "identity" && file.Status == "retained" {
			found = true
		}
	}
	if !found {
		t.Fatalf("permanent private stub path lost: %+v", result.LocalFiles)
	}
	if _, err := os.Stat(plan.IdentityFile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(changedPublic)
	if err != nil || string(data) != foreign {
		t.Fatalf("foreign public replacement changed: %q %v", data, err)
	}
	result.LocalFiles[0].Path = "caller mutation"
	again, err := f.service.ApplyKey(t.Context(), plan)
	if !errors.Is(err, ErrBlocked) || again.LocalFiles[0].Path == "caller mutation" {
		t.Fatal("receipt shares mutable path observations")
	}
}

func TestSecurityKeyForeignProviderAcceptsOnlyCapturedSymlink(t *testing.T) {
	f := newSecurityKeyFixture(t)
	real := filepath.Join(f.service.paths.Home, "provider.so")
	link := filepath.Join(f.service.paths.Home, "provider-link.so")
	writeFixture(t, real, "inert provider")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{Provider: link})
	runner := f.service.runner
	f.service.runner = keyCatalogRunnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
		if request.Name == "ssh" && request.Args[0] == "-G" {
			return RunResult{Stdout: []byte("hostname target\nsecuritykeyprovider " + link + "\n")}, nil
		}
		return runner.Run(ctx, request)
	})
	if err := f.service.VerifySecurityKeyPolicy(t.Context(), "target", plan); err != nil {
		t.Fatalf("captured provider symlink rejected: %v", err)
	}
	if plan.SecurityKeyProvider == link {
		t.Fatal("operation did not pin canonical provider")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.service.paths.Home, "other.so")
	writeFixture(t, other, "other provider")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err := f.service.VerifySecurityKeyPolicy(t.Context(), "target", plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("changed provider symlink accepted: %v", err)
	}
}

type hardwareProofStanza map[string][]string

func hardwareProofStanzas(t *testing.T, data []byte) []hardwareProofStanza {
	t.Helper()
	var stanzas []hardwareProofStanza
	for _, line := range strings.Split(string(data), "\n") {
		name, arguments, empty, err := parseConfigLine(line)
		if err != nil {
			t.Fatal(err)
		}
		if empty {
			continue
		}
		name = strings.ToLower(name)
		if name == "host" {
			stanzas = append(stanzas, hardwareProofStanza{})
			continue
		}
		if len(stanzas) == 0 {
			continue
		}
		stanza := stanzas[len(stanzas)-1]
		if name == "identityfile" {
			stanza[name] = append(stanza[name], arguments...)
		} else if _, exists := stanza[name]; !exists {
			stanza[name] = arguments
		}
	}
	return stanzas
}

func TestHardwareBootstrapFinalGateAndProxyInteractionScope(t *testing.T) {
	for _, operation := range []bool{false, true} {
		for _, twoHops := range []bool{false, true} {
			for _, gateFails := range []bool{false, true} {
				name := map[bool]string{false: "direct", true: "operation"}[operation] + "/" + map[bool]string{false: "single", true: "proxy"}[twoHops] + "/" + map[bool]string{false: "ready", true: "gate-fails"}[gateFails]
				t.Run(name, func(t *testing.T) {
					f := newSecurityKeyFixture(t)
					plan := f.plan(t, KeyTypeEd25519SK, SecurityKeyOptions{VerifyRequired: true})
					key, err := f.service.ApplyKey(t.Context(), plan)
					if err != nil {
						t.Fatal(err)
					}
					extra := filepath.Join(f.service.paths.SSHDir, "native-extra")
					configs := map[string]string{
						"target": "hostname target.example\nuser test\nport 22\nidentityfile " + plan.IdentityFile + "\nidentityfile " + extra + "\nidentitiesonly yes\nsecuritykeyprovider internal\npasswordauthentication yes\nkbdinteractiveauthentication yes\nstricthostkeychecking yes\nuserknownhostsfile none\nglobalknownhostsfile none\n",
						"jump":   "hostname jump.example\nuser jump\nport 22\nidentityfile /native/jump-key\nidentityagent /native/jump-agent\nsecuritykeyprovider /native/jump-provider\npasswordauthentication yes\nstricthostkeychecking yes\nuserknownhostsfile none\nglobalknownhostsfile none\n",
					}
					if twoHops {
						configs["target"] += "proxyjump jump\n"
					}
					exactCount, gateCount, proxyChecked := 0, 0, 0
					enrollmentRunner := f.service.runner
					inspect := func(data []byte) (target hardwareProofStanza) {
						for _, stanza := range hardwareProofStanzas(t, data) {
							if strings.Join(stanza["hostname"], "") == "jump.example" {
								if strings.Join(stanza["batchmode"], "") != "yes" || strings.Join(stanza["identityagent"], "") != "/native/jump-agent" || strings.Join(stanza["securitykeyprovider"], "") != "/native/jump-provider" {
									t.Fatalf("hardware interaction leaked into jump policy: %+v", stanza)
								}
								proxyChecked++
							}
							if strings.Join(stanza["hostname"], "") == "target.example" {
								target = stanza
							}
						}
						return target
					}
					f.service.runner = keyCatalogRunnerFunc(func(ctx context.Context, request RunRequest) (RunResult, error) {
						if request.Name != "ssh" && request.Name != f.tools["ssh"] {
							return enrollmentRunner.Run(ctx, request)
						}
						if request.Args[0] == "-G" {
							return RunResult{Stdout: []byte(configs[request.Args[len(request.Args)-1]])}, nil
						}
						configPath := sshArgForCatalogTest(request.Args, "-F")
						if configPath == "" {
							if strings.Contains(strings.Join(request.Args, " "), "jump") && !strings.Contains(strings.Join(request.Args, " "), "target") {
								return RunResult{}, nil
							}
							return RunResult{ExitCode: 255}, nil
						}
						data, err := os.ReadFile(configPath)
						if err != nil {
							t.Fatal(err)
						}
						target := inspect(data)
						if operation && request.Interactive && twoHops {
							paths, _ := filepath.Glob(filepath.Join(f.service.paths.SSHDir, ".dev-tmp-*.tmp"))
							for _, path := range paths {
								b, _ := os.ReadFile(path)
								if strings.HasPrefix(string(b), "# private dev-cli per-hop authentication configuration") {
									inspect(b)
								}
							}
						}
						exit := 0
						if target != nil {
							if !request.Interactive {
								exit = 255
							} else {
								ordinary := false
								for _, identity := range target["identityfile"] {
									ordinary = ordinary || identity == extra
								}
								if ordinary {
									gateCount++
									if gateFails {
										exit = 255
									}
								} else {
									exactCount++
								}
								for field, want := range map[string]string{"batchmode": "no", "passwordauthentication": "no", "kbdinteractiveauthentication": "no", "gssapiauthentication": "no", "hostbasedauthentication": "no", "preferredauthentications": "publickey", "stricthostkeychecking": "yes"} {
									if strings.Join(target[field], "") != want {
										t.Fatalf("target %s=%v want %s", field, target[field], want)
									}
								}
							}
						}
						if log := sshArgForCatalogTest(request.Args, "-E"); log != "" {
							if err := os.WriteFile(log, []byte("Authenticated to host using \"publickey\".\n"), 0o600); err != nil {
								t.Fatal(err)
							}
						}
						return RunResult{ExitCode: exit}, nil
					})
					request := BootstrapRequest{Alias: "target", TargetRemoteOS: RemoteOSPOSIX, Interactive: true, HopKeys: map[string]KeyResult{"target": key}}
					if operation {
						op, err := f.service.PrepareAuthentication(t.Context(), "target")
						if err != nil {
							t.Fatal(err)
						}
						defer op.Close()
						request.Authentication = op
					}
					result, err := f.service.Bootstrap(t.Context(), request)
					if err != nil || result.Ready == gateFails || exactCount != 1 || gateCount != 1 {
						t.Fatalf("bootstrap=%+v err=%v exact=%d gate=%d", result, err, exactCount, gateCount)
					}
					if twoHops && proxyChecked == 0 {
						t.Fatal("proxy interaction scope was not checked")
					}
				})
			}
		}
	}
}
