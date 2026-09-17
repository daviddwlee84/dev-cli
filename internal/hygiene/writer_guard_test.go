package hygiene

import (
	"strings"
	"testing"
)

func TestCheckWritersCallerExemptionRequiresProofOrExplicitOverride(t *testing.T) {
	const self = "52a38f41-fcea-4063-8e87-c68737f39bb8"
	const other = "01a08f36-adab-7183-ad3c-b58a05162903"
	caller := WriterAgent{Caller: true, Session: "claude:" + self}
	for _, tc := range []struct {
		name     string
		agents   []WriterAgent
		targets  []WriterTarget
		override bool
		allowed  bool
		reason   string
	}{
		{name: "no agents", targets: []WriterTarget{{Artifact: true}}, allowed: true},
		{name: "ordinary caller edit", agents: []WriterAgent{{Caller: true}}, targets: []WriterTarget{{}}, allowed: true},
		{name: "blocking agent on ordinary edit", agents: []WriterAgent{{Blocking: true}}, targets: []WriterTarget{{}}},
		{name: "other agent on artifact", agents: []WriterAgent{{Session: "codex:" + other}}, targets: []WriterTarget{{Artifact: true, Session: self}}},
		{name: "override never exempts another agent", agents: []WriterAgent{caller, {}}, targets: []WriterTarget{{Artifact: true, Session: other}}, override: true},
		{name: "caller with proven other session", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: other}, {}}, allowed: true},
		{name: "malformed target is unproven", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: "dead"}}, reason: "--allow-shared-checkout"},
		{name: "malformed caller is unproven", agents: []WriterAgent{{Caller: true, Session: "claude:dead"}}, targets: []WriterTarget{{Artifact: true, Session: other}}, reason: "--allow-shared-checkout"},
		{name: "different malformed IDs prove nothing", agents: []WriterAgent{{Caller: true, Session: "claude:beef"}}, targets: []WriterTarget{{Artifact: true, Session: "dead"}}, reason: "--allow-shared-checkout"},
		{name: "nil target UUID is unproven", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: "00000000-0000-0000-0000-000000000000"}}, reason: "--allow-shared-checkout"},
		{name: "compact target UUID is unproven", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: strings.ReplaceAll(other, "-", "")}}, reason: "--allow-shared-checkout"},
		{name: "unproven target retains explicit override", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: "dead"}}, override: true, allowed: true},
		{name: "unproven caller retains explicit override", agents: []WriterAgent{{Caller: true, Session: "claude:dead"}}, targets: []WriterTarget{{Artifact: true, Session: other}}, override: true, allowed: true},
		{name: "uppercase caller still protects own transcript", agents: []WriterAgent{{Caller: true, Session: "claude:" + strings.ToUpper(self)}}, targets: []WriterTarget{{Artifact: true, Session: self}}, override: true, reason: "own live transcript"},
		{name: "caller own transcript", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: strings.ToUpper(self)}}, reason: "own live transcript"},
		{name: "caller own transcript with override", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: self}}, override: true, reason: "own live transcript"},
		{name: "caller with unproven target", agents: []WriterAgent{caller}, targets: []WriterTarget{{Artifact: true, Session: other}, {Artifact: true}}, reason: "--allow-shared-checkout"},
		{name: "unknown caller session", agents: []WriterAgent{{Caller: true}}, targets: []WriterTarget{{Artifact: true, Session: other}}, reason: "--allow-shared-checkout"},
		{name: "unproven caller with override", agents: []WriterAgent{{Caller: true}}, targets: []WriterTarget{{Artifact: true}}, override: true, allowed: true},
		{name: "blocking caller", agents: []WriterAgent{{Caller: true, Blocking: true, Session: "claude:" + self}}, targets: []WriterTarget{{Artifact: true, Session: other}}, override: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckWriters(tc.agents, tc.targets, tc.override)
			if tc.allowed != (err == nil) {
				t.Fatalf("allowed=%t err=%v", tc.allowed, err)
			}
			if err != nil && (!strings.Contains(err.Error(), "recognized agent still occupies") || !strings.Contains(err.Error(), tc.reason)) {
				t.Fatalf("error=%v want reason %q", err, tc.reason)
			}
		})
	}
}
