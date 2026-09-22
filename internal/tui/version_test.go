package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestReleaseCheckWaitsForFirstViewAndUpdatesSameDashboard(t *testing.T) {
	checks := 0
	m := New(Actions{Release: ReleaseActions{
		Cached: ReleaseObservation{Stale: true},
		Check: func(context.Context) ReleaseObservation {
			checks++
			return ReleaseObservation{Latest: "v0.2.44", Newer: true}
		},
	}}, nil, nil).WithVersion("v0.2.43", true)
	batch, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init = %T/%d, want blink and release check", batch, len(batch))
	}
	ready := make(chan tea.Msg, 1)
	go func() { ready <- batch[1]() }()
	select {
	case <-ready:
		t.Fatal("release check became ready before first View")
	default:
	}
	if out := m.View(); !strings.Contains(out, "dev v0.2.43") || strings.Contains(out, "available") {
		t.Fatalf("initial version display = %q", out)
	}
	var msg tea.Msg
	select {
	case msg = <-ready:
	case <-time.After(time.Second):
		t.Fatal("first View did not release the check")
	}
	next, check := m.Update(msg)
	m = next.(Model)
	if _, duplicate := m.Update(msg); duplicate != nil {
		t.Fatal("duplicate ready message scheduled another check")
	}
	next, _ = m.Update(check())
	m = next.(Model)
	if checks != 1 || !strings.Contains(m.View(), "v0.2.44 available · dev upgrade") {
		t.Fatalf("checks=%d dashboard=%q", checks, m.View())
	}
}

func TestReleaseCacheAndDisabledChecksNeedNoBackgroundWork(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			m := New(Actions{Release: ReleaseActions{
				Cached: ReleaseObservation{Latest: "v0.2.44", Newer: true},
				Check: func(context.Context) ReleaseObservation {
					t.Fatal("fresh or disabled check ran")
					return ReleaseObservation{}
				},
			}}, nil, nil).WithVersion("v0.2.43-dirty", enabled)
			if command := m.requestReleaseCheck(); command != nil {
				t.Fatal("fresh or disabled observation scheduled a check")
			}
			out := m.View()
			if !strings.Contains(out, "v0.2.43-dirty") || strings.Contains(out, "available") != enabled {
				t.Fatalf("version or disabled hint = %q", out)
			}
		})
	}
}

func TestReleaseConfigChangeCancelsAndRejectsOldResult(t *testing.T) {
	var checkContext context.Context
	m := New(Actions{Release: ReleaseActions{
		Cached: ReleaseObservation{Stale: true},
		Check: func(ctx context.Context) ReleaseObservation {
			checkContext = ctx
			return ReleaseObservation{Latest: "v0.2.44", Newer: true}
		},
	}}, nil, nil).WithVersion("v0.2.43", true)
	m, check := m.startReleaseCheck(releaseCheckReadyMsg{generation: m.release.generation})
	late := check()
	disabled := false
	m.configGeneration = 1
	next, _ := m.Update(configMsg{generation: 1, update: ConfigUpdate{ReleaseChecksEnabled: &disabled}})
	m = next.(Model)
	if checkContext.Err() != context.Canceled {
		t.Fatal("accepted disabled config did not cancel the check")
	}
	next, _ = m.Update(late)
	m = next.(Model)
	if m.release.enabled || m.release.loading || strings.Contains(m.View(), "available") {
		t.Fatal("old release result escaped the disabled config generation")
	}
	enabled := true
	m.configGeneration = 2
	staleConfig := configMsg{generation: 1, update: ConfigUpdate{ReleaseChecksEnabled: &enabled}}
	next, _ = m.Update(staleConfig)
	m = next.(Model)
	if m.release.enabled {
		t.Fatal("stale config re-enabled release checks")
	}
	if command := m.applyReleaseChecks(true); command == nil {
		t.Fatal("re-enabling checks did not schedule a cache-aware check")
	}
	m.release.attempted = true
	if command := m.applyReleaseChecks(true); command != nil {
		t.Fatal("unrelated config reload repeated a completed release check")
	}
}

func TestReleaseCheckCancellationAndOfflineKeepKnownHint(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cached := ReleaseObservation{Latest: "v0.2.44", Newer: true, Stale: true}
			m := New(Actions{Release: ReleaseActions{
				Cached: cached,
				Check: func(ctx context.Context) ReleaseObservation {
					if canceled {
						<-ctx.Done()
						return ReleaseObservation{Latest: "v9.9.9", Newer: true}
					}
					cached.Err = errors.New("offline")
					return cached
				},
			}}, nil, nil).WithContext(ctx).WithVersion("v0.2.43", true)
			m.status = "task opened"
			m, command := m.startReleaseCheck(releaseCheckReadyMsg{generation: m.release.generation})
			if canceled {
				cancel()
			}
			next, _ := m.Update(command())
			m = next.(Model)
			out := m.View()
			if !strings.Contains(out, "v0.2.44 available (cached)") || strings.Contains(out, "v9.9.9") || !strings.Contains(out, "task opened") || m.err != nil {
				t.Fatalf("offline/canceled check corrupted displayed observations: %q", out)
			}
		})
	}
}

func TestReleaseCheckResumesOnlyInterruptedFleetHandoff(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			checks := 0
			m := New(Actions{Release: ReleaseActions{
				Cached: ReleaseObservation{Latest: "v0.2.44", Newer: true, Stale: interrupted},
				Check: func(context.Context) ReleaseObservation {
					checks++
					return ReleaseObservation{Latest: "v0.2.45", Newer: true}
				},
			}}, nil, nil).WithContext(t.Context()).WithFleetBackgroundRefresh(false).WithVersion("v0.2.43", true)
			_ = m.View()
			var old tea.Cmd
			if interrupted {
				m, old = m.startReleaseCheck(releaseCheckReadyMsg{generation: m.release.generation})
			}
			m.pendingFleetHandoff = &FleetHandoff{}
			m.startupRepo.loaded = true
			m = m.ResumeFleetHandoff(t.Context(), FleetExecutionResult{}, nil)
			if old != nil {
				next, _ := m.Update(old())
				m = next.(Model)
			}
			if got := m.release.observation.Latest; got != "v0.2.44" {
				t.Fatalf("handoff accepted the stopped program's result: %s", got)
			}
			command := m.Init()
			if !interrupted {
				if command != nil || checks != 0 {
					t.Fatal("handoff repeated a completed release observation")
				}
				return
			}
			ready := make(chan tea.Msg, 1)
			go func() { ready <- command() }()
			select {
			case <-ready:
				t.Fatal("resumed check ran before the new program's first frame")
			default:
			}
			_ = m.View()
			select {
			case msg := <-ready:
				next, check := m.Update(msg)
				m = next.(Model)
				next, _ = m.Update(check())
				m = next.(Model)
			case <-time.After(time.Second):
				t.Fatal("resumed check did not start")
			}
			if checks != 2 || m.release.observation.Latest != "v0.2.45" {
				t.Fatalf("interrupted request did not resume exactly once: checks=%d state=%+v", checks, m.release)
			}
		})
	}
}

func TestReleaseFooterFitsTerminalAndKeepsTabs(t *testing.T) {
	for _, width := range []int{40, 60, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := New(Actions{Release: ReleaseActions{Cached: ReleaseObservation{Latest: "v0.2.44", Newer: true}}}, nil, nil)
			m.width, m.height = width, 20
			before := m.buildHeaderLayout()
			m = m.WithVersion("v0.2.43-5-gabc1234-dirty", true)
			if m.buildHeaderLayout().line != before.line {
				t.Fatal("version footer changed the tab strip")
			}
			for _, line := range strings.Split(m.renderFooter(), "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("line exceeds %d columns: %q", width, line)
				}
			}
			if got := lineCount(m.View()); got > m.height {
				t.Fatalf("dashboard exceeds %d rows: %d", m.height, got)
			}
		})
	}
}
