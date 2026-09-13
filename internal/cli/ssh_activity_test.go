package cli

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/sshflow"
	"github.com/daviddwlee84/dev-cli/internal/sshhost"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type activityRunner func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error)

func (f activityRunner) Run(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
	return f(ctx, r)
}

func TestSSHTUIBatchTestsBoundConcurrencyAndDoNotRecordUse(t *testing.T) {
	app, f := sshTUITestApp(t)
	app.Err = io.Discard
	f.appendRootConfig("Host third fourth fifth sixth\n HostName 192.0.2.20\n User operator\n")
	service, err := app.sshHosts()
	if err != nil {
		t.Fatal(err)
	}
	var profiles []sshflow.ConnectionProfile
	for _, alias := range []string{"first", "second", "third", "fourth", "fifth", "sixth"} {
		p, ok := sshActivityProfile(t.Context(), service, alias)
		if !ok {
			t.Fatal(alias)
		}
		profiles = append(profiles, p)
	}
	var active, maximum atomic.Int32
	app.sshHostRunner = activityRunner(func(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
		if r.Name != "ssh" {
			t.Errorf("unexpected executable %s", r.Name)
			return sshhost.RunResult{}, errors.New("unexpected executable")
		}
		if r.Args[0] == "-G" {
			return sshhost.RunResult{Stdout: []byte("hostname 192.0.2.20\nuser operator\nport 22\nproxyjump gateway\n")}, nil
		}
		count := active.Add(1)
		defer active.Add(-1)
		for count > maximum.Load() {
			old := maximum.Load()
			if maximum.CompareAndSwap(old, count) {
				break
			}
		}
		select {
		case <-time.After(20 * time.Millisecond):
			return sshhost.RunResult{}, nil
		case <-ctx.Done():
			return sshhost.RunResult{}, ctx.Err()
		}
	})
	count := 0
	app.sshHostService = nil
	result, err := sshTUITestProfiles(t.Context(), app, tui.SSHTestRequest{Profiles: profiles, Full: true}, func(p tui.SSHTestProgress) {
		count++
		if p.Err != nil || p.Record.LastTest == nil || p.Record.LastTest.Status != "ready" || !p.Record.LastUsed.IsZero() {
			t.Errorf("%+v", p)
		}
	})
	if err != nil || result.Completed != 6 || count != 6 || maximum.Load() > 4 || maximum.Load() < 2 {
		t.Fatalf("result=%+v error=%v count=%d concurrency=%d", result, err, count, maximum.Load())
	}
	for _, p := range profiles {
		record, err := sshActivityStore(app).Read(t.Context(), p.ID)
		if err != nil || record.LastTest == nil || !record.LastUsed.IsZero() {
			t.Fatalf("%+v %v", record, err)
		}
	}
}

func TestSSHTUIBatchCancellationAndSourceRevalidation(t *testing.T) {
	app, f := sshTUITestApp(t)
	service, _ := app.sshHosts()
	profile, ok := sshActivityProfile(t.Context(), service, "first")
	if !ok {
		t.Fatal("profile missing")
	}
	started := make(chan struct{})
	var once sync.Once
	app.sshHostRunner = activityRunner(func(ctx context.Context, r sshhost.RunRequest) (sshhost.RunResult, error) {
		if r.Args[0] == "-G" {
			return sshhost.RunResult{Stdout: []byte("hostname 192.0.2.20\nuser operator\nport 22\nproxyjump gateway\n")}, nil
		}
		once.Do(func() { close(started) })
		<-ctx.Done()
		return sshhost.RunResult{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	app.sshHostService = nil
	go func() { <-started; cancel() }()
	result, err := sshTUITestProfiles(ctx, app, tui.SSHTestRequest{Profiles: []sshflow.ConnectionProfile{profile}, Full: true}, nil)
	if !errors.Is(err, context.Canceled) || !result.Canceled {
		t.Fatalf("%+v %v", result, err)
	}
	f.appendRootConfig("# changed configuration\n")
	calls := 0
	app.sshHostRunner = activityRunner(func(context.Context, sshhost.RunRequest) (sshhost.RunResult, error) {
		calls++
		return sshhost.RunResult{}, nil
	})
	progress := sshTUITestProfile(t.Context(), app, profile, true)
	if progress.Err == nil || calls != 0 {
		t.Fatal("changed source was tested")
	}
}

func TestSSHUsageObserverWritesOnceAndRejectsChangedProfile(t *testing.T) {
	app, f := sshTUITestApp(t)
	service, _ := app.sshHosts()
	profile, ok := sshActivityProfile(t.Context(), service, "first")
	if !ok {
		t.Fatal("profile missing")
	}
	at := time.Now().UTC()
	upper, found := sshActivityProfile(t.Context(), service, "FIRST")
	if !found || upper != profile {
		t.Fatal("case-insensitive alias lost canonical activity identity")
	}
	observer := sshUseObserver(app, service, profile)
	observer(at)
	observer(at.Add(time.Hour))
	record, err := sshActivityStore(app).Read(t.Context(), profile.ID)
	if err != nil || !record.LastUsed.Equal(at) || record.LastTest != nil {
		t.Fatalf("%+v %v", record, err)
	}
	f.appendRootConfig("# changed before process start\n")
	sshUseObserver(app, service, profile)(at.Add(time.Hour))
	record, err = sshActivityStore(app).Read(t.Context(), profile.ID)
	if err != nil || !record.LastUsed.Equal(at) {
		t.Fatal("recorded wrong profile revision")
	}
}

func TestSSHCompletedDiagnosisSurvivesCancellationBeforePersistence(t *testing.T) {
	app, _ := sshTUITestApp(t)
	service, _ := app.sshHosts()
	profile, ok := sshActivityProfile(t.Context(), service, "first")
	if !ok {
		t.Fatal("profile missing")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	recordSSHDiagnosis(ctx, app, service, profile, sshhost.DiagnoseRequest{Target: "first"}, sshhost.Diagnosis{Status: "ready"})
	record, err := sshActivityStore(app).Read(t.Context(), profile.ID)
	if err != nil || record.LastTest == nil || record.LastTest.Status != "ready" || record.LastSuccess.IsZero() {
		t.Fatalf("completed result was downgraded: %+v %v", record, err)
	}
	if !record.LastUsed.IsZero() {
		t.Fatal("test changed usage")
	}
}
