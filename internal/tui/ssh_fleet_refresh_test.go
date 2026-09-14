package tui

import (
	"context"
	"errors"
	"testing"
)

func TestSSHMembershipChangeReloadsFleetHostTree(t *testing.T) {
	for _, changed := range []bool{true, false} {
		hostLoads := 0
		m := treeModel(Actions{
			SSH: SSHActions{Load: func(context.Context) (SSHInventory, error) { return sshTestInventory("machine"), nil }},
			LoadFleetHosts: func(context.Context) (FleetHostsResult, error) {
				hostLoads++
				hosts := treeHosts()
				hosts.Hosts = append(hosts.Hosts, FleetHostDescriptor{Key: "g", Name: "gamma", EndpointID: "g1", Target: "ssh-gamma", SSHAlias: "ssh-gamma", OS: "windows"})
				return hosts, nil
			},
		})
		m, _ = treeAccept(m, treeHosts())
		next, cmd := m.Update(sshWorkflowMsg{result: SSHWorkflowResult{Status: "SSH setup: partial", MembershipChanged: changed}, err: errors.New("herdr unknown")})
		m = treeRun(t, next.(Model), cmd)
		found := false
		for _, host := range m.fleetTree.hosts {
			found = found || host.descriptor.Name == "gamma"
		}
		if changed && (hostLoads != 1 || !found) || !changed && (hostLoads != 0 || found) {
			t.Fatalf("membership changed=%v: host loads=%d new host visible=%v", changed, hostLoads, found)
		}
	}
}
