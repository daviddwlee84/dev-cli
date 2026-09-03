package sshhost

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

type scriptedRun struct {
	result RunResult
	err    error
	call   func(context.Context, RunRequest) (RunResult, error)
}

type scriptedBootstrapRunner struct {
	t         *testing.T
	responses []scriptedRun
	requests  []RunRequest
}

func (r *scriptedBootstrapRunner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	r.requests = append(r.requests, request)
	if len(r.responses) == 0 {
		r.t.Fatalf("unexpected bootstrap request %d: %#v", len(r.requests), request)
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	if response.call != nil {
		return response.call(ctx, request)
	}
	return response.result, response.err
}

func bootstrapService(t *testing.T, runner Runner) (*Service, Paths) {
	t.Helper()
	paths := fixturePaths(t)
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	return service, paths
}

func bindTestCandidate(t *testing.T, service *Service, line []byte) KeyCandidate {
	t.Helper()
	record, err := parsePublicKeyRecord(line)
	if err != nil {
		t.Fatal(err)
	}
	return service.bindKeyMaterial(KeyCandidate{
		Source: KeySourceAgent, Sources: []KeySource{KeySourceAgent},
		Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment,
		Fingerprint: record.metadata.Fingerprint, Provenance: KeyProvenance{Agent: true},
	}, record.normalized)
}

func bindTestRoute(t *testing.T, service *Service, alias string, hops []RouteHop) Route {
	t.Helper()
	if len(hops) == 0 {
		t.Fatal("test route needs a target")
	}
	hops = append([]RouteHop(nil), hops...)
	for index := range hops {
		if hops[index].Reference == "" {
			hops[index].Reference = hops[index].Alias
		}
		if hops[index].HostName == "" {
			hops[index].HostName = hops[index].Alias
		}
		if hops[index].AdminState == "" {
			hops[index].AdminState = AdminUnknown
		}
		if hops[index].RemoteOS == "" {
			hops[index].RemoteOS = RemoteOSUnknown
		}
	}
	hops[len(hops)-1].Target = true
	route := Route{Alias: alias, Hops: hops, TargetRemoteOS: hops[len(hops)-1].RemoteOS}
	states := make([]routeHopState, 0, len(hops))
	var outerReferences []string
	for index, hop := range hops {
		effective := testProofEffective(hop.Alias)
		state := routeHopState{safe: hop, destination: hop.Alias, effective: effective}
		if !hop.Target && len(outerReferences) > 0 {
			state.proxyJump = strings.Join(outerReferences, ",")
		}
		state.proofRoute = make([]proofRouteHop, index+1)
		for proofIndex := 0; proofIndex <= index; proofIndex++ {
			proofHop := hops[proofIndex]
			state.proofRoute[proofIndex] = proofRouteHop{
				alias: proofHop.Alias, reference: proofHop.Reference, hostName: proofHop.HostName,
				user: proofHop.User, port: proofHop.Port, effective: testProofEffective(proofHop.Alias),
			}
		}
		states = append(states, state)
		outerReferences = append(outerReferences, hop.Reference)
	}
	safe := cloneRoute(route)
	route.state = &routeState{serviceID: service.id, safe: safe, hops: states}
	return route
}

func testProofEffective(alias string) EffectiveConfig {
	return EffectiveConfig{
		Alias: alias,
		Values: map[string][]string{
			"stricthostkeychecking": {"yes"},
			"globalknownhostsfile":  {"none"},
			"userknownhostsfile":    {"none"},
			"identityagent":         {"SSH_AUTH_SOCK"},
		},
	}
}

func failedRun() scriptedRun  { return scriptedRun{result: RunResult{ExitCode: 255}} }
func successRun() scriptedRun { return scriptedRun{} }

func hasArgPair(args []string, left, right string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == left && args[index+1] == right {
			return true
		}
	}
	return false
}

func hasArg(args []string, value string) bool {
	for _, argument := range args {
		if argument == value {
			return true
		}
	}
	return false
}

func requestProgram(request RunRequest) string {
	if len(request.Args) == 0 {
		return ""
	}
	return request.Args[len(request.Args)-1]
}

func requestDestination(request RunRequest) string {
	if len(request.Args) < 2 {
		return ""
	}
	return request.Args[len(request.Args)-2]
}

func assertFreshProof(t *testing.T, request RunRequest, exact bool) {
	t.Helper()
	if request.Name != "ssh" || !hasArgPair(request.Args, "-S", "none") || !hasArgPair(request.Args, "-o", "BatchMode=yes") {
		t.Fatalf("proof is not fresh batch SSH: %#v", request)
	}
	assertSSHLiveness(t, request)
	if requestProgram(request) != "exit 0" {
		t.Fatalf("proof command = %q", requestProgram(request))
	}
	if hasArg(request.Args, "-F") != exact {
		t.Fatalf("proof exact=%v private-config args=%#v", exact, request.Args)
	}
	for _, forbidden := range []string{"-i", "IdentitiesOnly=yes", "CertificateFile="} {
		if hasArg(request.Args, forbidden) {
			t.Fatalf("proof leaked additive credential option %q into argv: %#v", forbidden, request.Args)
		}
	}
	if request.Interactive || len(request.Stdin) != 0 {
		t.Fatalf("proof unexpectedly interactive or has stdin: %#v", request)
	}
}

func assertSSHLiveness(t *testing.T, request RunRequest) {
	t.Helper()
	for _, value := range []string{
		"ConnectTimeout=" + sshConnectTimeout,
		"ServerAliveInterval=" + sshServerAliveInterval,
		"ServerAliveCountMax=" + sshServerAliveCountMax,
	} {
		if !hasArgPair(request.Args, "-o", value) {
			t.Fatalf("SSH request omits liveness option %q: %#v", value, request.Args)
		}
	}
}

func TestNativeExactProofConfigPreservesMACsAndExcludesOrdinaryCredentials(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("ssh unavailable: %v", err)
	}
	paths := fixturePaths(t)
	identityA := filepath.Join(paths.SSHDir, "id_a")
	certificateA := filepath.Join(paths.SSHDir, "id_a-cert.pub")
	identityB := filepath.Join(paths.SSHDir, "id_b")
	certificateB := filepath.Join(paths.SSHDir, "id_b-cert.pub")
	knownHosts := filepath.Join(paths.SSHDir, "known_hosts.proof")
	for _, path := range []string{identityA, certificateA, identityB, certificateB, knownHosts} {
		writeFixture(t, path, "fixture\n")
	}
	ordinaryPath := filepath.Join(paths.SSHDir, "ordinary-proof.conf")
	ordinary := strings.Join([]string{
		"Host target",
		"    HostName target.example.invalid",
		"    User proof-user",
		"    Port 2222",
		"    IdentityFile " + quoteConfigValue(identityA),
		"    CertificateFile " + quoteConfigValue(certificateA),
		"    StrictHostKeyChecking yes",
		"    UserKnownHostsFile " + quoteConfigValue(knownHosts),
		"    GlobalKnownHostsFile none",
		"    AddressFamily inet",
		"    Ciphers aes128-ctr",
		"    MACs hmac-sha2-256",
		"    KexAlgorithms curve25519-sha256",
		"    CheckHostIP yes",
		"    VerifyHostKeyDNS no",
		"    UpdateHostKeys no",
		"",
	}, "\n")
	writeFixture(t, ordinaryPath, ordinary)

	runner := ExecRunner{}
	evaluated, err := runner.Run(context.Background(), RunRequest{
		Name: "ssh", Args: []string{"-F", ordinaryPath, "-G", "target"}, Env: []string{"LC_ALL=C"},
		Display: "native ordinary proof config evaluation",
	})
	if err != nil || evaluated.ExitCode != 0 {
		t.Fatalf("ordinary ssh -G: result %#v, err %v", evaluated, err)
	}
	effective, err := ParseEffective("target", evaluated.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !containsOrderedValues(effective.IdentityFiles, []string{identityA}) ||
		!containsOrderedValues(effective.Values["certificatefile"], []string{certificateA}) {
		t.Fatalf("ordinary config did not contain key/certificate A: %#v", effective)
	}

	proof, destination, err := renderExactProofConfig([]proofRouteHop{{
		alias: "target", hostName: effective.HostName, user: effective.User, port: effective.Port, effective: effective,
	}}, keySelector{identity: identityB})
	if err != nil {
		t.Fatal(err)
	}
	if destination == "target" {
		t.Fatal("exact proof did not isolate endpoint behind an internal alias")
	}
	proofPath := filepath.Join(paths.SSHDir, "selected-only-proof.conf")
	writeFixture(t, proofPath, string(proof))
	selected, err := runner.Run(context.Background(), RunRequest{
		Name: "ssh", Args: []string{"-F", proofPath, "-G", destination}, Env: []string{"LC_ALL=C"},
		Display: "native selected-only proof config evaluation",
	})
	if err != nil || selected.ExitCode != 0 {
		t.Fatalf("selected ssh -G: stderr %q, err %v", selected.Stderr, err)
	}
	proofEffective, err := ParseEffective(destination, selected.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if len(proofEffective.IdentityFiles) != 2 || proofEffective.IdentityFiles[0] != "none" || proofEffective.IdentityFiles[1] != identityB {
		t.Fatalf("exact proof identities = %#v; want only none and selected B (ordinary A was %q)", proofEffective.IdentityFiles, identityA)
	}
	certificates := proofEffective.Values["certificatefile"]
	if len(certificates) != 1 || certificates[0] != "none" {
		t.Fatalf("exact proof certificates = %#v; want only none (ordinary cert A was %q)", certificates, certificateA)
	}
	if firstEffectiveValue(proofEffective, "stricthostkeychecking") != "true" ||
		firstEffectiveValue(proofEffective, "userknownhostsfile") != knownHosts ||
		firstEffectiveValue(proofEffective, "globalknownhostsfile") != "none" ||
		firstEffectiveValue(proofEffective, "addressfamily") != "inet" ||
		firstEffectiveValue(proofEffective, "ciphers") != "aes128-ctr" ||
		firstEffectiveValue(proofEffective, "macs") != "hmac-sha2-256" ||
		firstEffectiveValue(proofEffective, "kexalgorithms") != "curve25519-sha256" {
		t.Fatalf("exact proof changed host-key policy: %#v", proofEffective.Values)
	}

	proofWithCertificate, certificateDestination, err := renderExactProofConfig([]proofRouteHop{{
		alias: "target", hostName: effective.HostName, user: effective.User, port: effective.Port, effective: effective,
	}}, keySelector{identity: identityB, certificate: certificateB})
	if err != nil {
		t.Fatal(err)
	}
	certificateProofPath := filepath.Join(paths.SSHDir, "selected-certificate-proof.conf")
	writeFixture(t, certificateProofPath, string(proofWithCertificate))
	selectedCertificate, err := runner.Run(context.Background(), RunRequest{
		Name: "ssh", Args: []string{"-F", certificateProofPath, "-G", certificateDestination}, Env: []string{"LC_ALL=C"},
		Display: "native selected-certificate proof config evaluation",
	})
	if err != nil || selectedCertificate.ExitCode != 0 {
		t.Fatalf("selected-certificate ssh -G: stderr %q, err %v", selectedCertificate.Stderr, err)
	}
	certificateEffective, err := ParseEffective(certificateDestination, selectedCertificate.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	certificates = certificateEffective.Values["certificatefile"]
	if len(certificates) != 2 || certificates[0] != "none" || certificates[1] != certificateB {
		t.Fatalf("selected-certificate proof certificates = %#v; ordinary cert A was %q", certificates, certificateA)
	}
}

func TestNativeExactProofConfigRebuildsProxyRouteWithOrdinaryProxyCredential(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("ssh unavailable: %v", err)
	}
	paths := fixturePaths(t)
	proxyIdentity := filepath.Join(paths.SSHDir, "id_proxy")
	selectedIdentity := filepath.Join(paths.SSHDir, "id_selected")
	writeFixture(t, proxyIdentity, "proxy fixture\n")
	writeFixture(t, selectedIdentity, "selected fixture\n")
	proxyEffective := testProofEffective("jump")
	proxyEffective.IdentityFiles = []string{proxyIdentity}
	proxyEffective.Values["identityfile"] = []string{proxyIdentity}
	targetEffective := testProofEffective("target")
	proof, destination, err := renderExactProofConfig([]proofRouteHop{
		{alias: "jump", hostName: "jump.example.invalid", user: "jump-user", port: 2201, effective: proxyEffective},
		{alias: "target", hostName: "target.example.invalid", user: "target-user", port: 2202, effective: targetEffective},
	}, keySelector{identity: selectedIdentity})
	if err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(paths.SSHDir, "proxy-selected-only.conf")
	writeFixture(t, proofPath, string(proof))
	evaluate := func(alias string) EffectiveConfig {
		t.Helper()
		run, err := (ExecRunner{}).Run(context.Background(), RunRequest{
			Name: "ssh", Args: []string{"-F", proofPath, "-G", alias}, Env: []string{"LC_ALL=C"},
			Display: "native exact proxy config evaluation",
		})
		if err != nil || run.ExitCode != 0 {
			t.Fatalf("ssh -G %s: stderr %q, err %v", alias, run.Stderr, err)
		}
		effective, err := ParseEffective(alias, run.Stdout)
		if err != nil {
			t.Fatal(err)
		}
		return effective
	}
	target := evaluate(destination)
	if target.ProxyJump != "jump" || target.HostName != "target.example.invalid" || target.Port != 2202 ||
		len(target.IdentityFiles) != 2 || target.IdentityFiles[0] != "none" || target.IdentityFiles[1] != selectedIdentity {
		t.Fatalf("target exact route = %#v", target)
	}
	jump := evaluate("jump")
	if jump.ProxyJump != "" || jump.HostName != "jump.example.invalid" || jump.Port != 2201 ||
		len(jump.IdentityFiles) != 2 || jump.IdentityFiles[0] != "none" || jump.IdentityFiles[1] != proxyIdentity {
		t.Fatalf("ordinary proxy route = %#v", jump)
	}
}

func TestNativeExactProofConfigPreservesIPv6AndMixedProxyJumpReferences(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("ssh unavailable: %v", err)
	}
	paths := fixturePaths(t)
	selectedIdentity := filepath.Join(paths.SSHDir, "id_selected_ipv6")
	writeFixture(t, selectedIdentity, "selected fixture\n")
	proof, destination, err := renderExactProofConfig([]proofRouteHop{
		{alias: "edge", reference: "user@edge:2201", hostName: "edge.example.invalid", user: "user", port: 2201, effective: testProofEffective("edge")},
		{alias: "2001:db8::1", reference: "[2001:db8::1]:2222", hostName: "2001:db8::1", port: 2222, effective: testProofEffective("2001:db8::1")},
		{alias: "target", reference: "target", hostName: "target.example.invalid", user: "target-user", port: 22, effective: testProofEffective("target")},
	}, keySelector{identity: selectedIdentity})
	if err != nil {
		t.Fatal(err)
	}
	proofPath := filepath.Join(paths.SSHDir, "ipv6-mixed-proxy-proof.conf")
	writeFixture(t, proofPath, string(proof))
	run, err := (ExecRunner{}).Run(context.Background(), RunRequest{
		Name: "ssh", Args: []string{"-F", proofPath, "-G", destination}, Env: []string{"LC_ALL=C"},
		Display: "native IPv6 exact proxy config evaluation",
	})
	if err != nil || run.ExitCode != 0 {
		t.Fatalf("ssh -G: stderr %q, err %v", run.Stderr, err)
	}
	effective, err := ParseEffective(destination, run.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if effective.ProxyJump != "user@edge:2201,[2001:db8::1]:2222" {
		t.Fatalf("mixed IPv6 ProxyJump = %q", effective.ProxyJump)
	}
	if !strings.Contains(string(proof), "ProxyJump user@edge:2201,[2001:db8::1]:2222") {
		t.Fatalf("proof dropped unambiguous route references:\n%s", proof)
	}
}

func TestExactProofConfigRejectsPolicyItCannotReplay(t *testing.T) {
	selector := keySelector{identity: filepath.Join(t.TempDir(), "id_selected")}
	for _, test := range []struct {
		name      string
		effective EffectiveConfig
	}{
		{
			name: "KnownHostsCommand",
			effective: EffectiveConfig{Values: map[string][]string{
				"stricthostkeychecking": {"yes"}, "globalknownhostsfile": {"none"},
				"userknownhostsfile": {"none"}, "knownhostscommand": {"helper %h"},
			}},
		},
		{
			name: "ambiguous relative known-hosts path",
			effective: EffectiveConfig{Values: map[string][]string{
				"stricthostkeychecking": {"yes"}, "globalknownhostsfile": {"none"},
				"userknownhostsfile": {"known hosts"},
			}},
		},
		{
			name: "tokenized endpoint HostKeyAlias",
			effective: EffectiveConfig{Values: map[string][]string{
				"stricthostkeychecking": {"yes"}, "globalknownhostsfile": {"none"},
				"userknownhostsfile": {"none"}, "hostkeyalias": {"pinned-%n"},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := renderExactProofConfig([]proofRouteHop{{
				alias: "target", hostName: "target.example", effective: test.effective,
			}}, selector)
			if !errors.Is(err, ErrUnsupportedRoute) {
				t.Fatalf("render error = %v, want unsupported route", err)
			}
		})
	}
}

func TestExactProofUsesProtectedEphemeralConfig(t *testing.T) {
	var configPath string
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{{
		call: func(_ context.Context, request RunRequest) (RunResult, error) {
			for index := 0; index+1 < len(request.Args); index++ {
				if request.Args[index] == "-F" {
					configPath = request.Args[index+1]
					break
				}
			}
			if configPath == "" {
				t.Fatalf("exact proof has no -F config: %#v", request.Args)
			}
			snapshot, err := readSecureFile(configPath, false)
			if err != nil {
				t.Fatalf("exact proof config is not platform-protected: %v", err)
			}
			content := string(snapshot.data)
			if strings.Count(content, "    IdentityFile ") != 2 ||
				!strings.Contains(content, "    IdentityFile none\n") ||
				strings.Count(content, "    CertificateFile ") != 1 ||
				!strings.Contains(content, "    CertificateFile none\n") {
				t.Fatalf("exact proof credential config = %q", content)
			}
			return RunResult{}, nil
		},
	}}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x60, "ephemeral"))
	material, err := service.validateKeyCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	selector, cleanup, err := service.prepareKeySelector(material)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	verified, err := service.runSSHProof(context.Background(), route.state.hops[0], selector, true)
	if err != nil || !verified {
		t.Fatalf("verified = %v, err %v", verified, err)
	}
	if configPath == "" {
		t.Fatal("runner did not observe exact proof config")
	}
	if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact proof config remained after proof: %v", err)
	}
}

func TestBootstrapPOSIXUsesFreshExactAndSeparateOrdinaryGate(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		failedRun(), // ordinary before
		failedRun(), // exact before
		successRun(),
		successRun(), // exact after
		successRun(), // ordinary gate
	}}
	service, _ := bootstrapService(t, runner)
	line := testPublicLine(0x61, "bootstrap")
	candidate := bindTestCandidate(t, service, line)
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})

	result, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || !result.FleetReady || result.Partial || result.Status != BootstrapReady || len(result.Hops) != 1 {
		t.Fatalf("result = %#v", result)
	}
	hop := result.Hops[0]
	if !hop.Installed || !hop.Present || !hop.Verified || !hop.OrdinaryReady || hop.OrdinaryBefore || hop.Status != HopInstalled {
		t.Fatalf("hop = %#v", hop)
	}
	if len(runner.requests) != 5 {
		t.Fatalf("requests = %#v", runner.requests)
	}
	assertFreshProof(t, runner.requests[0], false)
	assertFreshProof(t, runner.requests[1], true)
	installer := runner.requests[2]
	if installer.Name != "ssh" || !hasArgPair(installer.Args, "-S", "none") || !hasArgPair(installer.Args, "-o", "BatchMode=yes") || installer.Interactive {
		t.Fatalf("installer request = %#v", installer)
	}
	assertSSHLiveness(t, installer)
	if requestProgram(installer) != posixInstallerProgram() {
		t.Fatalf("installer program changed")
	}
	wantStdin := append(append([]byte(nil), line...), '\n')
	if string(installer.Stdin) != string(wantStdin) {
		t.Fatalf("installer stdin differs from one normalized line")
	}
	for _, argument := range installer.Args {
		if strings.Contains(argument, string(line)) {
			t.Fatal("public line was interpolated into installer argv")
		}
	}
	assertFreshProof(t, runner.requests[3], true)
	assertFreshProof(t, runner.requests[4], false)
	for _, request := range runner.requests {
		joined := strings.ToLower(strings.Join(request.Args, " "))
		for _, forbidden := range []string{"accept-new", "ssh-copy-id", "scp", "generated-identity-material"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("request contains forbidden %q: %#v", forbidden, request.Args)
			}
		}
	}
}

func TestBootstrapSkipsAlreadyWorkingJumpAndProcessesOutermostFirst(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		successRun(), // jump ordinary
		failedRun(),  // jump exact
		successRun(), // jump ordinary gate
		failedRun(),  // target ordinary
		failedRun(),  // target exact
		successRun(), // target installer
		successRun(), // target exact after
		successRun(), // target ordinary gate
	}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x62, "route"))
	route := bindTestRoute(t, service, "target", []RouteHop{
		{Alias: "jump", RemoteOS: RemoteOSWindows},
		{Alias: "target", RemoteOS: RemoteOSPOSIX},
	})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Candidate: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || len(result.Hops) != 2 || result.Hops[0].Status != HopWorkingSkipped || !result.Hops[0].Skipped || !result.Hops[0].Ready {
		t.Fatalf("result = %#v", result)
	}
	if result.Hops[0].Installed || result.Hops[0].Verified || result.Hops[0].Present {
		t.Fatalf("working jump claimed selected key state: %#v", result.Hops[0])
	}
	installers := 0
	for _, request := range runner.requests {
		if requestProgram(request) == posixInstallerProgram() || strings.HasPrefix(requestProgram(request), "powershell.exe ") {
			installers++
			if requestDestination(request) != "target" {
				t.Fatalf("installed on already-working jump: %#v", request)
			}
		}
	}
	if installers != 1 {
		t.Fatalf("installer count = %d, requests %#v", installers, runner.requests)
	}
	for index, request := range runner.requests[:3] {
		if index == 1 {
			assertFreshProof(t, request, true)
			continue
		}
		if requestDestination(request) != "jump" {
			t.Fatalf("jump was not processed first: %#v", runner.requests)
		}
	}
}

func TestBootstrapCanExplicitlyInstallOnWorkingJump(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		successRun(), failedRun(), successRun(), successRun(), successRun(), // jump
		successRun(), successRun(), successRun(), // target already present
	}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x6c, "working-jump"))
	route := bindTestRoute(t, service, "target", []RouteHop{
		{Alias: "jump", RemoteOS: RemoteOSPOSIX},
		{Alias: "target", RemoteOS: RemoteOSPOSIX},
	})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Candidate: candidate, InstallOnWorkingJump: true,
	})
	if err != nil || !result.Ready || !result.Hops[0].Installed || result.Hops[0].Skipped {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	if requestDestination(runner.requests[2]) != "jump" || requestProgram(runner.requests[2]) != posixInstallerProgram() {
		t.Fatalf("jump installer request = %#v", runner.requests[2])
	}
}

func TestBootstrapPresentKeyStillPerformsSecondOrdinaryGate(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{successRun(), successRun(), successRun()}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x63, "present"))
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "target", Route: route, Candidate: candidate})
	if err != nil || !result.Ready || result.Hops[0].Status != HopPresent || result.Hops[0].Installed {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	if len(runner.requests) != 3 {
		t.Fatalf("requests = %#v", runner.requests)
	}
	assertFreshProof(t, runner.requests[0], false)
	assertFreshProof(t, runner.requests[1], true)
	assertFreshProof(t, runner.requests[2], false)
}

func TestBootstrapReusesUnchangedApplyKeyPairProof(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "id_verified_pair")
	public := testPublicLine(0x79, "verified")
	writeFixture(t, identity, "opaque private material")
	writeFixture(t, identity+".pub", string(public)+"\n")
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		{result: RunResult{Stdout: append(testPublicLine(0x79, "derived"), '\n')}},
		successRun(), successRun(), successRun(),
	}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanKey(context.Background(), KeyRequest{Path: identity, Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	key, err := service.ApplyKey(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Key: key, Interactive: true,
	})
	if err != nil || !result.Ready {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	keygenRequests := 0
	for _, request := range runner.requests {
		if request.Name == "ssh-keygen" {
			keygenRequests++
		}
	}
	if keygenRequests != 1 {
		t.Fatalf("unchanged key pair prompted/derived %d times: %#v", keygenRequests, runner.requests)
	}
}

func TestBootstrapRejectsModifiedKeyResultCandidate(t *testing.T) {
	service, _ := bootstrapService(t, panicRunner{})
	candidate := bindTestCandidate(t, service, testPublicLine(0x7c, "tamper"))
	plan, err := service.PlanKey(context.Background(), KeyRequest{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	key, err := service.ApplyKey(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	key.Candidate.Fingerprint = "SHA256:forged"
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	if _, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Key: key,
	}); err == nil || !strings.Contains(err.Error(), "public fields were modified") {
		t.Fatalf("Bootstrap modified key result error = %v", err)
	}
}

func TestBootstrapRejectsMismatchedPrivateCompanionBeforeNetwork(t *testing.T) {
	paths := fixturePaths(t)
	identity := filepath.Join(paths.SSHDir, "id_bootstrap_pair")
	public := testPublicLine(0x7a, "companion")
	writeFixture(t, identity, "opaque private material")
	writeFixture(t, identity+".pub", string(public)+"\n")
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{{
		result: RunResult{Stdout: append(testPublicLine(0x7b, "different"), '\n')},
	}}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	record, err := parsePublicKeyRecord(public)
	if err != nil {
		t.Fatal(err)
	}
	candidate := service.bindKeyMaterial(KeyCandidate{
		Source: KeySourceExplicit, Sources: []KeySource{KeySourceExplicit},
		Algorithm: record.metadata.Algorithm, Comment: record.metadata.Comment, Fingerprint: record.metadata.Fingerprint,
		PublicPath: identity + ".pub", IdentityFile: identity, Provenance: KeyProvenance{Private: true},
	}, record.normalized)
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	if _, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Candidate: candidate,
	}); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("Bootstrap mismatch error = %v", err)
	}
	if len(runner.requests) != 1 || runner.requests[0].Name != "ssh-keygen" {
		t.Fatalf("mismatched companion reached network: %#v", runner.requests)
	}
}

func TestBootstrapInstallCancellationIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		failedRun(), failedRun(),
		{call: func(ctx context.Context, _ RunRequest) (RunResult, error) {
			cancel()
			return RunResult{}, ctx.Err()
		}},
	}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x64, "cancel"))
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(ctx, BootstrapRequest{Alias: "target", Route: route, Candidate: candidate})
	if !errors.Is(err, context.Canceled) || !result.Partial || result.Hops[0].Status != HopUnknown || !result.Hops[0].Unknown {
		t.Fatalf("result = %#v, err %v", result, err)
	}
}

func TestBootstrapFailureRetainsGeneratedPairAndReportsPartial(t *testing.T) {
	paths := fixturePaths(t)
	keyRunner := &keygenRunner{t: t, line: testPublicLine(0x65, "retained")}
	service, err := NewService(paths, keyRunner)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(paths.SSHDir, "id_retained")
	plan, err := service.PlanKey(context.Background(), KeyRequest{
		Operation: KeyGenerate, DestinationIdentity: destination, NoPassphrase: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	keyResult, err := service.ApplyKey(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRunner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		failedRun(), failedRun(), successRun(), failedRun(),
	}}
	service.runner = bootstrapRunner
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "target", Route: route, Key: keyResult})
	if err != nil || !result.Partial || result.Ready || result.FleetReady || !result.Hops[0].Installed || result.Hops[0].Verified {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	for _, path := range []string{destination, destination + ".pub"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("generated asset %s was removed: %v", path, err)
		}
	}
}

func TestBootstrapWindowsStandardInstallerUsesSIDAndFixedStdin(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		failedRun(), failedRun(),
		{result: RunResult{Stdout: []byte("standard")}},
		successRun(), successRun(), successRun(),
	}}
	service, _ := bootstrapService(t, runner)
	line := testPublicLine(0x66, "windows")
	candidate := bindTestCandidate(t, service, line)
	route := bindTestRoute(t, service, "win", []RouteHop{{Alias: "win", RemoteOS: RemoteOSWindows}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "win", Route: route, Candidate: candidate})
	if err != nil || !result.Ready || result.Hops[0].AdminState != AdminStandard {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	if len(runner.requests) != 6 {
		t.Fatalf("requests = %#v", runner.requests)
	}
	adminScript := decodePowerShellProgram(t, requestProgram(runner.requests[2]))
	if !strings.Contains(adminScript, "S-1-5-32-544") || !strings.Contains(adminScript, ".Groups") || len(runner.requests[2].Stdin) != 0 {
		t.Fatalf("admin detection script/request = %q, %#v", adminScript, runner.requests[2])
	}
	installer := runner.requests[3]
	decoded := decodePowerShellProgram(t, requestProgram(installer))
	for _, required := range []string{"authorized_keys", "ReparsePoint", "S-1-5-18", "SetAccessRuleProtection", "VerifyRules", "ReadToEnd", "-ccontains"} {
		if !strings.Contains(decoded, required) {
			t.Errorf("standard installer omits %q", required)
		}
	}
	if strings.Contains(decoded, "administrators_authorized_keys") || strings.Contains(decoded, string(line)) {
		t.Fatal("standard installer contains admin path or interpolated public line")
	}
	if string(installer.Stdin) != string(append(append([]byte(nil), line...), '\n')) {
		t.Fatal("Windows installer did not receive exactly one public line")
	}
}

func TestBootstrapWindowsAdministratorRequiresConsentAndNeverAutomatesUAC(t *testing.T) {
	t.Run("consent blocked before installer", func(t *testing.T) {
		runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
			failedRun(), failedRun(), {result: RunResult{Stdout: []byte("administrator")}},
		}}
		service, _ := bootstrapService(t, runner)
		candidate := bindTestCandidate(t, service, testPublicLine(0x67, "admin"))
		route := bindTestRoute(t, service, "win", []RouteHop{{Alias: "win", RemoteOS: RemoteOSWindows}})
		result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "win", Route: route, Candidate: candidate})
		if err != nil || result.Hops[0].Code != "windows_admin_consent_required" || result.Hops[0].Status != HopManual || len(runner.requests) != 3 {
			t.Fatalf("result = %#v, err %v, requests %#v", result, err, runner.requests)
		}
	})

	t.Run("consented DACL and elevation result", func(t *testing.T) {
		runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
			failedRun(), failedRun(), {result: RunResult{Stdout: []byte("administrator")}},
			{result: RunResult{ExitCode: 1}},
		}}
		service, _ := bootstrapService(t, runner)
		line := testPublicLine(0x68, "admin")
		candidate := bindTestCandidate(t, service, line)
		route := bindTestRoute(t, service, "win", []RouteHop{{Alias: "win", RemoteOS: RemoteOSWindows}})
		result, err := service.Bootstrap(context.Background(), BootstrapRequest{
			Alias: "win", Route: route, Candidate: candidate, AllowWindowsAdminAuthorizedKeys: true,
		})
		if err != nil || result.Hops[0].Code != "windows_elevation_required_install_unknown" || !result.Hops[0].Unknown {
			t.Fatalf("result = %#v, err %v", result, err)
		}
		decoded := decodePowerShellProgram(t, requestProgram(runner.requests[3]))
		for _, required := range []string{"administrators_authorized_keys", "S-1-5-32-544", "S-1-5-18", "VerifyRules"} {
			if !strings.Contains(decoded, required) {
				t.Errorf("administrator installer omits %q", required)
			}
		}
		lower := strings.ToLower(decoded)
		for _, forbidden := range []string{"start-process", "runas", string(line)} {
			if strings.Contains(lower, strings.ToLower(forbidden)) {
				t.Fatalf("administrator installer contains %q", forbidden)
			}
		}
	})
}

func TestBootstrapInteractiveInstallerLeavesProofsBatchOnly(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{
		failedRun(), failedRun(), successRun(), successRun(), successRun(),
	}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x69, "interactive"))
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSPOSIX}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{
		Alias: "target", Route: route, Candidate: candidate, Interactive: true,
	})
	if err != nil || !result.Ready {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	assertFreshProof(t, runner.requests[0], false)
	assertFreshProof(t, runner.requests[1], true)
	installer := runner.requests[2]
	if !installer.Interactive || installer.CaptureStdout || hasArgPair(installer.Args, "-o", "BatchMode=yes") || !hasArgPair(installer.Args, "-S", "none") {
		t.Fatalf("interactive installer = %#v", installer)
	}
	assertSSHLiveness(t, installer)
	assertFreshProof(t, runner.requests[3], true)
	assertFreshProof(t, runner.requests[4], false)
}

func TestBootstrapUnknownTargetOSCanAuthenticateButCannotOpenFleetGate(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{successRun(), successRun(), successRun()}}
	service, _ := bootstrapService(t, runner)
	candidate := bindTestCandidate(t, service, testPublicLine(0x6a, "unknown-os"))
	route := bindTestRoute(t, service, "target", []RouteHop{{Alias: "target", RemoteOS: RemoteOSUnknown}})
	result, err := service.Bootstrap(context.Background(), BootstrapRequest{Alias: "target", Route: route, Candidate: candidate})
	if err != nil || !result.Ready || result.FleetReady || result.TargetRemoteOS != RemoteOSUnknown {
		t.Fatalf("result = %#v, err %v", result, err)
	}
}

func TestPlanBootstrapIsSideEffectFreeAndHonest(t *testing.T) {
	service, _ := bootstrapService(t, panicRunner{})
	plan, err := service.PlanBootstrap(BootstrapRequest{Alias: "target", TargetRemoteOS: RemoteOSPOSIX})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != BootstrapUnknown || plan.RouteKnown || len(plan.Hops) != 0 || len(plan.Steps) == 0 {
		t.Fatalf("plan = %#v", plan)
	}
	for _, step := range plan.Steps {
		if step.State != BootstrapUnknown {
			t.Fatalf("dry-run step claimed knowledge: %#v", step)
		}
	}
}

func TestJumpSSHUsesResolvedOuterPrefixButTargetUsesOrdinaryAliasConfig(t *testing.T) {
	runner := &scriptedBootstrapRunner{t: t, responses: []scriptedRun{successRun(), successRun()}}
	service, _ := bootstrapService(t, runner)
	route := bindTestRoute(t, service, "target", []RouteHop{
		{Alias: "outer", Reference: "user@outer:2200", RemoteOS: RemoteOSPOSIX},
		{Alias: "inner", RemoteOS: RemoteOSPOSIX},
		{Alias: "target", RemoteOS: RemoteOSPOSIX},
	})
	if _, err := service.runHopSSH(context.Background(), route.state.hops[1], true, "exit 0", nil, false, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.runHopSSH(context.Background(), route.state.hops[2], true, "exit 0", nil, false, "test"); err != nil {
		t.Fatal(err)
	}
	if !hasArgPair(runner.requests[0].Args, "-J", "user@outer:2200") {
		t.Fatalf("inner jump omitted resolved prefix: %#v", runner.requests[0].Args)
	}
	if hasArg(runner.requests[1].Args, "-J") {
		t.Fatalf("target ordinary alias was overridden with -J: %#v", runner.requests[1].Args)
	}
}

func TestFixedInstallerProgramsAreIdempotentAndMaterialFree(t *testing.T) {
	line := string(testPublicLine(0x6b, "do-not-interpolate"))
	for _, required := range []string{"grep -F -x -q", "chmod 700", "chmod 600", "authorized_keys", "read -r key"} {
		if !strings.Contains(posixInstallerScript, required) {
			t.Errorf("POSIX installer omits %q", required)
		}
	}
	if strings.Contains(posixInstallerScript, line) {
		t.Fatal("POSIX installer interpolates key material")
	}
	for _, administrator := range []bool{false, true} {
		decoded := decodePowerShellProgram(t, windowsInstallerProgram(administrator))
		for _, required := range []string{"ReadToEnd", "-ccontains", "ReparsePoint", "SetAccessRuleProtection", "VerifyRules"} {
			if !strings.Contains(decoded, required) {
				t.Errorf("Windows installer admin=%v omits %q", administrator, required)
			}
		}
		if strings.Contains(decoded, line) {
			t.Fatalf("Windows installer admin=%v interpolates key material", administrator)
		}
	}
}

func decodePowerShellProgram(t *testing.T, program string) string {
	t.Helper()
	const marker = "-EncodedCommand "
	index := strings.LastIndex(program, marker)
	if index < 0 {
		t.Fatalf("program is not encoded PowerShell: %q", program)
	}
	encoded := strings.TrimSpace(program[index+len(marker):])
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data)%2 != 0 {
		t.Fatalf("decode PowerShell program: %v", err)
	}
	units := make([]uint16, len(data)/2)
	for index := range units {
		units[index] = uint16(data[index*2]) | uint16(data[index*2+1])<<8
	}
	return string(utf16.Decode(units))
}
