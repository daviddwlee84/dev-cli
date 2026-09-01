package machineid

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestLoadOrCreatePersistsStablePrivateUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "identity.json")
	store := NewStore(path)
	first, err := store.LoadOrCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(first); err != nil {
		t.Fatalf("created machine ID: %v", err)
	}
	second, err := NewStore(path).LoadOrCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("second ID = %q, want %q", second, first)
	}
	if loaded, err := store.Load(); err != nil || loaded != first {
		t.Fatalf("Load = %q, %v; want %q", loaded, err, first)
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("identity modes = dir %04o file %04o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestLoadOrCreateSerializesConcurrentCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity", "identity.json")
	store := NewStore(path)
	var generated atomic.Int32
	store.generate = func() string {
		generated.Add(1)
		return "00000000-0000-4000-8000-000000000123"
	}
	const workers = 16
	results := make(chan string, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			id, err := store.LoadOrCreate(context.Background())
			if err != nil {
				errorsFound <- err
				return
			}
			results <- id
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("LoadOrCreate: %v", err)
	}
	for id := range results {
		if id != "00000000-0000-4000-8000-000000000123" {
			t.Errorf("ID = %q", id)
		}
	}
	if got := generated.Load(); got != 1 {
		t.Fatalf("generator called %d times, want 1", got)
	}
}

func TestLoadRejectsMalformedWrongVersionUnknownAndTrailingData(t *testing.T) {
	validID := uuid.NewString()
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "version", body: `{"schema_version":2,"machine_id":"` + validID + `"}`, want: "schema_version"},
		{name: "unknown", body: `{"schema_version":1,"machine_id":"` + validID + `","extra":true}`, want: "unknown field"},
		{name: "uuid", body: `{"schema_version":1,"machine_id":"NOT-A-UUID"}`, want: "canonical non-zero UUID"},
		{name: "trailing", body: `{"schema_version":1,"machine_id":"` + validID + `"} {}`, want: "multiple JSON values"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "identity.json")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewStore(path).Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsPublicModeWithoutReplacingIdentity(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "identity.json")
	id := uuid.NewString()
	body := `{"schema_version":1,"machine_id":"` + id + `"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if _, err := store.LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "want 0600") {
		t.Fatalf("LoadOrCreate = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatal("invalid existing identity was overwritten")
	}
}

func TestLoadOrCreateRejectsPublicDirectoryWithoutChmoddingIt(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(filepath.Join(directory, "identity.json")).LoadOrCreate(context.Background()); err == nil || !strings.Contains(err.Error(), "want 0700") {
		t.Fatalf("LoadOrCreate = %v", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("public parent mode changed to %04o", info.Mode().Perm())
	}
}

func TestDefaultPathUsesXDGDataHome(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	want := filepath.Join(data, "dev", "machine", "v1", "identity.json")
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
}

func TestValidateRequiresCanonicalNonzeroUUID(t *testing.T) {
	for _, value := range []string{"", "00000000-0000-0000-0000-000000000000", strings.ToUpper(uuid.NewString())} {
		if err := Validate(value); err == nil {
			t.Errorf("Validate(%q) succeeded", value)
		}
	}
}
