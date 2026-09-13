package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	out := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		out[key] = value
	}
	return out
}

func TestPrepareNativeRequiresHostToolchain(t *testing.T) {
	for _, tc := range []struct {
		name, missing, host, want string
	}{
		{"native Android", "", "android", ""},
		{"missing Go", "go", "android", "pkg install golang clang git"},
		{"missing Clang", "clang", "android", "needs Clang"},
		{"Linux toolchain on Android", "", "linux", "needs native Go for android/arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, error) {
				if name == tc.missing {
					return "", exec.ErrNotFound
				}
				return filepath.Join(t.TempDir(), name), nil
			}
			builder, err := prepareNative(context.Background(), "android", "arm64", lookup, func(cmd *exec.Cmd) error {
				if !reflect.DeepEqual(cmd.Args[1:], []string{"env", "-json", "GOHOSTOS", "GOHOSTARCH"}) {
					t.Fatalf("preflight must not build/download: %v", cmd.Args)
				}
				env := envMap(cmd.Env)
				if env["GOTOOLCHAIN"] != "local" || env["GOWORK"] != "off" {
					t.Fatal("preflight may download a toolchain or read the caller's workspace")
				}
				fmt.Fprintf(cmd.Stdout, `{"GOHOSTOS":%q,"GOHOSTARCH":"arm64"}`, tc.host)
				return nil
			})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			env := envMap(builder.env)
			if env["CGO_ENABLED"] != "1" || filepath.Base(env["CC"]) != "clang" || env["GOOS"] != "android" || env["GOARCH"] != "arm64" {
				t.Fatalf("incorrect Android toolchain: GOOS=%q GOARCH=%q CGO_ENABLED=%q CC=%q", env["GOOS"], env["GOARCH"], env["CGO_ENABLED"], env["CC"])
			}
		})
	}
}

func TestSourceBuildStagesExactReleaseAndPreservesTarget(t *testing.T) {
	for _, mode := range []string{"success", "compile failure", "wrong version", "symlink", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "dev")
			if err := os.WriteFile(target, []byte("existing binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			var stage string
			builder := &NativeBuilder{goPath: "go", goos: "android", env: []string{"GOBIN=/do-not-use", "GOTOOLCHAIN=local", "GOMAXPROCS=2"}}
			builder.run = func(cmd *exec.Cmd) error {
				if cmd.Args[0] == "go" {
					if !reflect.DeepEqual(cmd.Args[1:], []string{"install", "-p", "2", "-trimpath", moduleCommand + "@v0.2.34"}) {
						t.Fatalf("must pin source tag and bound workers: %v", cmd.Args)
					}
					stage = envMap(cmd.Env)["GOBIN"]
					if stage != cmd.Dir || filepath.Dir(stage) != dir {
						t.Fatalf("stage must be private and on target filesystem: %s", stage)
					}
					if mode == "compile failure" {
						return errors.New("compiler failed")
					}
					if mode == "cancelled" {
						return cmd.Run() // CommandContext must reject before starting Go.
					}
					candidate := filepath.Join(stage, "dev")
					if mode == "symlink" {
						if err := os.Symlink(target, candidate); err != nil {
							t.Skipf("symlinks unavailable: %v", err)
						}
						return nil
					}
					return os.WriteFile(candidate, []byte("candidate"), 0o755)
				}
				if cmd.Args[0] != filepath.Join(stage, "dev") || !reflect.DeepEqual(cmd.Args[1:], []string{"--version"}) {
					t.Fatalf("must validate staged executable: %v", cmd.Args)
				}
				version := "v0.2.34"
				if mode == "wrong version" {
					version = "v0.2.33"
				}
				fmt.Fprintln(cmd.Stdout, "dev version "+version)
				return nil
			}
			path, cleanup, err := builder.Build(ctx, "v0.2.34", dir, nil, io.Discard, io.Discard)
			if cleanup != nil {
				defer cleanup()
			}
			data, _ := os.ReadFile(target)
			if string(data) != "existing binary" {
				t.Fatal("staging changed existing binary")
			}
			if mode == "success" {
				if err != nil || path != filepath.Join(stage, "dev") {
					t.Fatalf("candidate = %s, err = %v", path, err)
				}
				cleanup()
			} else if err == nil {
				t.Fatal("failed source build must not produce a candidate")
			}
			if _, err := os.Stat(stage); !os.IsNotExist(err) {
				t.Fatalf("stage not cleaned: %v", err)
			}
		})
	}
}

func TestOverlayEnvReplacesInheritedBuildSettings(t *testing.T) {
	env := overlayEnv([]string{"GOBIN=unsafe", "gobin=also-unsafe", "GOPROXY=https://proxy.example", "GOTOOLCHAIN=auto"}, map[string]string{"GOBIN": "stage", "GOTOOLCHAIN": "local"})
	got := envMap(env)
	if len(env) != 3 || got["GOBIN"] != "stage" || got["GOTOOLCHAIN"] != "local" || got["GOPROXY"] != "https://proxy.example" {
		t.Fatalf("environment = %v", env)
	}
}
