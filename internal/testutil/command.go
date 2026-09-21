package testutil

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Capture the build tool before a test replaces PATH/HOME to isolate providers.
// Only fixture compilation uses this environment; the fixture itself inherits
// the caller's test environment like a real subprocess.
var fixtureGo, fixtureGoErr = exec.LookPath("go")
var fixtureBuildEnv = os.Environ()
var fixturePrograms = struct {
	sync.Mutex
	data map[[32]byte][]byte
}{data: make(map[[32]byte][]byte)}

// GoCommand installs a native, stdlib-only executable fixture. Unlike a shebang
// file, it can shadow a real tool on Windows and run without a shell or SDK on
// the fixture's PATH. Programs are compiled once per source per test process.
func GoCommand(t testing.TB, dir, name, source string) string {
	t.Helper()
	if name == "" || filepath.Base(name) != name {
		t.Fatalf("fixture executable must have a basename: %q", name)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	data, err := compileFixture(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func compileFixture(source string) ([]byte, error) {
	fixturePrograms.Lock()
	defer fixturePrograms.Unlock()
	key := sha256.Sum256([]byte(source))
	if data, ok := fixturePrograms.data[key]; ok {
		return data, nil
	}
	if fixtureGoErr != nil {
		return nil, fmt.Errorf("native executable fixture requires Go: %w", fixtureGoErr)
	}
	dir, err := os.MkdirTemp("", "dev-command-fixture-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		return nil, err
	}
	binary := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command(fixtureGo, "build", "-trimpath", "-buildvcs=false", "-o", binary, "main.go")
	cmd.Dir = dir
	for _, entry := range fixtureBuildEnv {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOWORK", "GOFLAGS":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("compile native command fixture: %w\n%s", err, output)
	}
	data, err := os.ReadFile(binary)
	if err == nil {
		fixturePrograms.data[key] = data
	}
	return data, err
}
