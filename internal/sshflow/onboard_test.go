package sshflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestOnboardingOrderingAndIndependentPartialResults(t *testing.T) {
	p, err := PlanOnboarding([]OnboardTarget{{Alias: "a", Auth: "key", To: "both", RemoteOS: "posix", Port: 22}, {Alias: "b", Auth: "existing", To: "fleet", RemoteOS: "posix", Port: 22}})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	r, err := ApplyOnboarding(context.Background(), p, func(_ context.Context, target OnboardTarget, stage string) error {
		calls = append(calls, target.Alias+":"+stage)
		if stage == "herdr" {
			return errors.New("native approval pending")
		}
		return nil
	})
	want := []string{"a:configure", "b:configure", "a:authenticate", "a:bind", "a:herdr", "a:fleet", "b:bind", "b:fleet"}
	if err == nil || r.Status != "partial" || !reflect.DeepEqual(calls, want) {
		t.Fatalf("%v %#v %v", calls, r, err)
	}
}

func TestOnboardingFailedKeyRetainsBindingButNeverRegisters(t *testing.T) {
	p, _ := PlanOnboarding([]OnboardTarget{{Alias: "a", Auth: "key", To: "fleet", RemoteOS: "posix", Port: 22}})
	var calls []string
	_, err := ApplyOnboarding(context.Background(), p, func(_ context.Context, _ OnboardTarget, stage string) error {
		calls = append(calls, stage)
		if stage == "authenticate" {
			return errors.New("key unproven")
		}
		return nil
	})
	if err == nil || !reflect.DeepEqual(calls, []string{"configure", "authenticate", "bind"}) {
		t.Fatal(calls, err)
	}
	p.Targets[0].Auth = "existing"
	if _, err = ApplyOnboarding(context.Background(), p, func(context.Context, OnboardTarget, string) error { t.Fatal("mutated plan applied"); return nil }); err == nil {
		t.Fatal("accepted altered plan")
	}
}
