package perftrace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecorderUsesRelativeTimeBoundsEventsAndFreezes(t *testing.T) {
	at := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return at }
	recorder := NewWithClock(clock, 2)

	recorder.Mark(CLIExecuteBegin, Fields{})
	at = at.Add(1500 * time.Microsecond)
	rows := 0
	recorder.Mark(TUIProducerRemote, Fields{
		View: ViewRemote, Stage: StageSnapshotAccepted, Source: SourceLive,
		Freshness: FreshnessFresh, Outcome: OutcomeSuccess, Generation: 3, Rows: &rows,
	})
	at = at.Add(time.Millisecond)
	recorder.Mark(TUIProducerRepos, Fields{})

	snapshot := recorder.Freeze()
	if snapshot.SchemaVersion != SchemaVersion || snapshot.DroppedEvents != 1 || len(snapshot.Events) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Events[0].AtMicros != 0 || snapshot.Events[1].AtMicros != 1500 {
		t.Fatalf("relative times = %d, %d", snapshot.Events[0].AtMicros, snapshot.Events[1].AtMicros)
	}
	if snapshot.Events[1].Rows == nil || *snapshot.Events[1].Rows != 0 {
		t.Fatalf("zero-row result was not retained: %+v", snapshot.Events[1])
	}

	recorder.Mark(TUIProducerSkills, Fields{})
	if after := recorder.Freeze(); len(after.Events) != 2 || after.DroppedEvents != 1 {
		t.Fatalf("late event changed frozen snapshot: %+v", after)
	}
}

func TestRecorderSpanAndMarkOnce(t *testing.T) {
	at := time.Date(2026, time.August, 31, 11, 0, 0, 0, time.UTC)
	recorder := NewWithClock(func() time.Time { return at }, 10)

	recorder.MarkOnce(TUIInitialViewReturned, Fields{})
	recorder.MarkOnce(TUIInitialViewReturned, Fields{})
	finish := recorder.Start(AppLoad, Fields{})
	at = at.Add(2500 * time.Microsecond)
	finish(OutcomeSuccess)
	finish(OutcomeFailed)

	events := recorder.Freeze().Events
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[1].DurationUS != 2500 || events[1].Outcome != OutcomeSuccess {
		t.Fatalf("span = %+v", events[1])
	}
}

func TestRecorderIsSafeForConcurrentProducers(t *testing.T) {
	recorder := New(2048)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 100 {
				recorder.Mark(TUIProducerRepos, Fields{View: ViewRepos})
			}
		})
	}
	workers.Wait()

	snapshot := recorder.Freeze()
	if len(snapshot.Events) != 1600 || snapshot.DroppedEvents != 0 {
		t.Fatalf("events=%d dropped=%d", len(snapshot.Events), snapshot.DroppedEvents)
	}
	for index, event := range snapshot.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence = %d", index, event.Sequence)
		}
	}
}

func TestWriteNewUsesPrivateFileAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	snapshot := Snapshot{SchemaVersion: SchemaVersion, Events: []Event{{Sequence: 1, Name: CLIExecuteBegin}}}
	if err := WriteNew(path, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Events) != 1 || decoded.Events[0].Name != CLIExecuteBegin {
		t.Fatalf("decoded = %+v", decoded)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode = %o, want 600", got)
		}
	}
	if err := WriteNew(path, Snapshot{}); err == nil {
		t.Fatal("second write unexpectedly overwrote the trace")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(data) {
		t.Fatal("existing trace changed after rejected write")
	}
}

func TestTraceSchemaCannotCarrySensitivePayloads(t *testing.T) {
	secret := "/Users/private/work/secret-repo"
	recorder := New(10)
	recorder.Mark(TUIProducerRepos, Fields{
		View: ViewRepos, Stage: StageFinished, Source: SourceLive,
		Outcome: OutcomeFailed,
	})
	data, err := json.Marshal(recorder.Freeze())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("trace leaked sensitive value: %s", data)
	}
	for _, forbidden := range []string{"path", "error", "command", "key", "url", "handle"} {
		if strings.Contains(strings.ToLower(string(data)), `"`+forbidden+`"`) {
			t.Fatalf("trace schema unexpectedly contains %q: %s", forbidden, data)
		}
	}
}

func TestWriteNewRejectsRelativePath(t *testing.T) {
	if err := WriteNew("trace.json", Snapshot{}); err == nil {
		t.Fatal("relative trace path was accepted")
	}
}
