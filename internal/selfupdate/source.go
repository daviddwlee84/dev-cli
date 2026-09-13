package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const moduleCommand = "github.com/daviddwlee84/dev-cli/cmd/dev"

type commandRunner func(*exec.Cmd) error

// NativeBuilder uses the installed host toolchain, never a downloaded Linux
// toolchain on Android. Construction is read-only and can precede confirmation.
type NativeBuilder struct {
	goPath string
	goos   string
	env    []string
	run    commandRunner
}

func PrepareNative(ctx context.Context, goos, goarch string) (*NativeBuilder, error) {
	return prepareNative(ctx, goos, goarch, exec.LookPath, func(cmd *exec.Cmd) error { return cmd.Run() })
}

func prepareNative(ctx context.Context, goos, goarch string, lookup func(string) (string, error), run commandRunner) (*NativeBuilder, error) {
	goPath, err := lookup("go")
	if err != nil {
		return nil, fmt.Errorf("source upgrade needs native Go: %s: %w", toolchainHint(goos), err)
	}
	overrides := map[string]string{
		"GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "",
		"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0", "GOMAXPROCS": "2",
	}
	if goos == "android" {
		clang, err := lookup("clang")
		if err != nil {
			return nil, fmt.Errorf("source upgrade on Termux needs Clang: %s: %w", toolchainHint(goos), err)
		}
		overrides["CGO_ENABLED"] = "1"
		overrides["CC"] = clang
	}
	b := &NativeBuilder{goPath: goPath, goos: goos, env: overlayEnv(os.Environ(), overrides), run: run}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, goPath, "env", "-json", "GOHOSTOS", "GOHOSTARCH")
	cmd.Env = b.env
	cmd.Dir = os.TempDir()
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := run(cmd); err != nil {
		return nil, fmt.Errorf("inspect native Go toolchain: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var host struct{ GOHOSTOS, GOHOSTARCH string }
	if err := json.Unmarshal(out.Bytes(), &host); err != nil {
		return nil, fmt.Errorf("read native Go platform: %w", err)
	}
	if host.GOHOSTOS != goos || host.GOHOSTARCH != goarch {
		return nil, fmt.Errorf("source upgrade needs native Go for %s/%s; found %s/%s (%s)", goos, goarch, host.GOHOSTOS, host.GOHOSTARCH, toolchainHint(goos))
	}
	return b, nil
}

// Build stages and validates an exact release without touching the installed
// binary. The returned cleanup must run after either replacement or failure.
// A non-nil archive must already be verified against the release's SHA256SUMS.
func (b *NativeBuilder) Build(ctx context.Context, tag, destDir string, archive []byte, stdout, stderr io.Writer) (path string, cleanup func(), err error) {
	if err := ValidateTag(tag); err != nil {
		return "", nil, err
	}
	stage, err := os.MkdirTemp(destDir, ".dev-upgrade-source-*")
	if err != nil {
		return "", nil, fmt.Errorf("stage source build next to the target: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(stage) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	buildCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	name := "dev"
	if b.goos == "windows" {
		name += ".exe"
	}
	path = filepath.Join(stage, name)
	// A version-qualified go install ignores the caller's go.mod and obtains
	// source through Go's configured verification policy for older releases.
	args := []string{"install", "-p", "2", "-trimpath", moduleCommand + "@" + tag}
	buildDir := stage
	if archive != nil {
		buildDir = filepath.Join(stage, "source")
		if err := extractSource(archive, buildDir); err != nil {
			return "", cleanup, fmt.Errorf("unpack verified source archive: %w", err)
		}
		args = []string{"build", "-p", "2", "-mod=readonly", "-trimpath", "-ldflags", "-s -w -X github.com/daviddwlee84/dev-cli/internal/cli.Version=" + tag, "-o", path, "./cmd/dev"}
	}
	cmd := exec.CommandContext(buildCtx, b.goPath, args...)
	cmd.Env = overlayEnv(b.env, map[string]string{"GOBIN": stage})
	cmd.Dir = buildDir
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := b.run(cmd); err != nil {
		return "", cleanup, fmt.Errorf("build dev %s from source (existing binary unchanged): %w; %s", tag, err, toolchainHint(b.goos))
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", cleanup, fmt.Errorf("inspect source build: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", cleanup, fmt.Errorf("source build is not a regular executable")
	}
	verifyCtx, verifyCancel := context.WithTimeout(buildCtx, 15*time.Second)
	defer verifyCancel()
	verify := exec.CommandContext(verifyCtx, path, "--version")
	verify.Env = overlayEnv(b.env, map[string]string{"DEV_NO_UPDATE_CHECK": "1"})
	verify.Dir = stage
	var output bytes.Buffer
	verify.Stdout, verify.Stderr = &output, &output
	if err := b.run(verify); err != nil {
		return "", cleanup, fmt.Errorf("run source build version check: %w", err)
	}
	if got := strings.TrimSpace(output.String()); got != "dev version "+tag {
		return "", cleanup, fmt.Errorf("source build version mismatch: wanted %s, got %q", tag, got)
	}
	return path, cleanup, nil
}

func toolchainHint(goos string) string {
	if goos == "android" {
		return "install/update Termux tools with `pkg install golang clang git`; retry `dev upgrade`"
	}
	return "install/update native Go to the release's go.mod requirement, then retry `dev upgrade`"
}

func overlayEnv(base []string, values map[string]string) []string {
	env := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := values[strings.ToUpper(key)]; !replace {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}
