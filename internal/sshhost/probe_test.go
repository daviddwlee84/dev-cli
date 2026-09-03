package sshhost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestProbeUsesOneFreshBatchOrdinaryAliasLogin(t *testing.T) {
	paths := fixturePaths(t)
	runner := &recordingRunner{}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), "lab")
	if err != nil || !result.Ready || result.Status != ProbeReady {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	want := appendFreshSSHOptions(nil, true)
	want = append(want, "lab", "exit 0")
	if len(runner.requests) != 1 || runner.requests[0].Name != "ssh" || !reflect.DeepEqual(runner.requests[0].Args, want) {
		t.Fatalf("probe request = %#v", runner.requests)
	}
	joined := strings.ToLower(strings.Join(runner.requests[0].Args, " "))
	for _, forbidden := range []string{"accept-new", "stricthostkeychecking", "userknownhostsfile"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("probe weakened host policy with %q", forbidden)
		}
	}
}

func TestProbeReturnsSafeStableFailureAndCancellation(t *testing.T) {
	paths := fixturePaths(t)
	runner := &recordingRunner{result: RunResult{ExitCode: 255, Stderr: []byte("sensitive transport detail")}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), "lab")
	if err != nil || result.Ready || result.Status != ProbeNotReady || result.ExitCode != 255 {
		t.Fatalf("result = %#v, err %v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "sensitive transport detail") {
		t.Fatalf("probe JSON = %s, err %v", encoded, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner.err = context.Canceled
	if _, err := service.Probe(ctx, "lab"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe cancellation error = %v", err)
	}
}
