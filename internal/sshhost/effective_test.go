package sshhost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	requests []RunRequest
	result   RunResult
	err      error
}

func (r *recordingRunner) Run(_ context.Context, request RunRequest) (RunResult, error) {
	r.requests = append(r.requests, request)
	return r.result, r.err
}

func TestEffectiveUsesPlainSSHAndModelsAdditiveValues(t *testing.T) {
	paths := fixturePaths(t)
	runner := &recordingRunner{result: RunResult{Stdout: []byte(
		"hostname first.example\n" +
			"hostname ignored.example\n" +
			"user alice\n" +
			"port 2222\n" +
			"proxyjump bastion\n" +
			"identityfile ~/.ssh/one\n" +
			"identityfile ~/.ssh/two\n" +
			"identitiesonly yes\n",
	)}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	config, err := service.Effective(context.Background(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d", len(runner.requests))
	}
	request := runner.requests[0]
	if request.Name != "ssh" || !reflect.DeepEqual(request.Args, []string{"-G", "lab"}) {
		t.Fatalf("request = %#v; expected plain ssh -G alias", request)
	}
	if config.HostName != "first.example" || config.User != "alice" || config.Port != 2222 || config.ProxyJump != "bastion" {
		t.Fatalf("effective scalar values = %#v", config)
	}
	if !reflect.DeepEqual(config.IdentityFiles, []string{"~/.ssh/one", "~/.ssh/two"}) || config.IdentitiesOnly == nil || !*config.IdentitiesOnly {
		t.Fatalf("effective additive values = %#v", config)
	}
}

func TestVerifyManagedUsesPlainSSHAndChecksEveryManagedField(t *testing.T) {
	paths := fixturePaths(t)
	definition := ManagedDefinition{
		Alias: "lab", HostName: "host.example", User: "alice", Port: 2222,
		ProxyJump: "jump", IdentityFile: "~/.ssh/lab", IdentitiesOnly: boolPointer(true),
	}
	runner := &recordingRunner{result: RunResult{Stdout: []byte(
		"hostname host.example\n" +
			"user alice\n" +
			"port 2222\n" +
			"proxyjump jump\n" +
			"identityfile ~/.ssh/global\n" +
			"identityfile ~/.ssh/lab\n" +
			"identityfile ~/.ssh/default\n" +
			"identitiesonly yes\n",
	)}}
	service, err := NewService(paths, runner)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := service.VerifyManaged(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || runner.requests[0].Name != "ssh" || !reflect.DeepEqual(runner.requests[0].Args, []string{"-G", "lab"}) {
		t.Fatalf("verification request = %#v", runner.requests)
	}
	if !reflect.DeepEqual(effective.IdentityFiles, []string{"~/.ssh/global", "~/.ssh/lab", "~/.ssh/default"}) {
		t.Fatalf("effective identities = %#v", effective.IdentityFiles)
	}

	mismatch := effective
	mismatch.HostName = "wrong"
	mismatch.User = "wrong"
	mismatch.Port = 22
	mismatch.ProxyJump = "wrong"
	mismatch.IdentityFiles = []string{"~/.ssh/global"}
	mismatch.IdentitiesOnly = boolPointer(false)
	err = VerifyManagedEffective(definition, mismatch)
	if err == nil {
		t.Fatal("VerifyManagedEffective accepted mismatched fields")
	}
	for _, field := range []string{"HostName", "User", "Port", "ProxyJump", "IdentityFile", "IdentitiesOnly"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("verification error %q omits %s", err, field)
		}
	}
	if !containsOrderedValues([]string{"zero", "one", "middle", "two"}, []string{"one", "two"}) ||
		containsOrderedValues([]string{"two", "one"}, []string{"one", "two"}) {
		t.Fatal("ordered additive comparison is incorrect")
	}
}

func TestEffectiveSeparatesExitAndLaunchFailures(t *testing.T) {
	paths := fixturePaths(t)
	for _, test := range []struct {
		name   string
		runner *recordingRunner
	}{
		{name: "exit", runner: &recordingRunner{result: RunResult{ExitCode: 255, Stderr: []byte("bad config")}}},
		{name: "launch", runner: &recordingRunner{err: errors.New("missing executable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewService(paths, test.runner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Effective(context.Background(), "lab"); err == nil {
				t.Fatal("Effective succeeded")
			}
		})
	}
}

func TestParseEffectiveRejectsMalformedOutput(t *testing.T) {
	for _, output := range []string{"badline\n", "port nope\n", "identitiesonly maybe\n"} {
		if _, err := ParseEffective("lab", []byte(output)); err == nil {
			t.Errorf("ParseEffective(%q) succeeded", output)
		}
	}
}

func TestValidateLookupAliasRejectsOptionAndPatterns(t *testing.T) {
	for _, alias := range []string{"", "-oProxyCommand=x", "*.example", "has space", "line\nfeed"} {
		if err := ValidateLookupAlias(alias); err == nil {
			t.Errorf("ValidateLookupAlias(%q) succeeded", alias)
		}
	}
}
