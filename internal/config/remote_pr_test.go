package config

import (
	"testing"
	"time"
)

func TestRemotePRConfigDefaultsAndBounds(t *testing.T) {
	c := Default()
	p := c.EffectiveRemotePRs()
	if p.PageSize != 50 || p.LargeRepoThreshold != 50 || p.CacheTTL.Duration != 5*time.Minute {
		t.Fatalf("defaults=%+v", p)
	}
	c.TUI.Remote.PRs.PageSize = 101
	if err := c.Validate(); err == nil {
		t.Fatal("accepted page larger than provider bound")
	}
	c.TUI.Remote.PRs = RemotePRs{PageSize: 20, LargeRepoThreshold: 100, CacheTTL: Duration{Duration: time.Minute}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if p = c.EffectiveRemotePRs(); p.PageSize != 20 || p.LargeRepoThreshold != 100 {
		t.Fatalf("settings=%+v", p)
	}
}
