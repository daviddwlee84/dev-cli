package sshhost

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticRouteCollectors(t *testing.T) {
	for _, tt := range []struct{ goos, output, iface string }{
		{"darwin", "destination: 192.0.2.30\n gateway: 192.0.2.1\n interface: utun4\n", "utun4"},
		{"linux", `[{"dst":"192.0.2.30","gateway":"192.0.2.1","dev":"tun0","prefsrc":"192.0.2.2"}]`, "tun0"},
		{"windows", `{"interface":"7","source":"192.0.2.2","gateway":"192.0.2.1","destination":"0.0.0.0/0"}`, "7"},
	} {
		t.Run(tt.goos, func(t *testing.T) {
			runner := diagnosticRunnerFunc(func(ctx context.Context, r RunRequest) (RunResult, error) {
				if tt.goos == "windows" {
					if len(r.Stdin) == 0 || !strings.Contains(strings.Join(r.Args, " "), "-EncodedCommand") || strings.Contains(strings.Join(r.Args, " "), "192.0.2.30") {
						t.Fatal("Windows parameters are not separate data")
					}
					var q DiagnosticRouteQuery
					if json.Unmarshal(r.Stdin, &q) != nil || q.Address != "192.0.2.30" {
						t.Fatal("bad stdin")
					}
				}
				return RunResult{Stdout: []byte(tt.output)}, nil
			})
			got, err := collectDiagnosticRoute(context.Background(), runner, tt.goos, DiagnosticRouteQuery{Address: "192.0.2.30", Port: 22})
			if err != nil || got.Interface != tt.iface || got.Code != "route_observed" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
func TestDiagnosticRouteInvalidAndScoped(t *testing.T) {
	runner := diagnosticRunnerFunc(func(context.Context, RunRequest) (RunResult, error) {
		return RunResult{Stdout: []byte("invalid secret-output")}, nil
	})
	for _, goos := range []string{"linux", "darwin", "windows"} {
		result, err := collectDiagnosticRoute(context.Background(), runner, goos, DiagnosticRouteQuery{Address: "2001:db8::1", Port: 22})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%+v %v", result, err)
		}
		result, err = collectDiagnosticRoute(context.Background(), runner, goos, DiagnosticRouteQuery{Address: "fe80::1", Port: 22})
		if err == nil || result.Code != "scope_required" {
			t.Fatal(result, err)
		}
	}
}

// Required CI smoke inspects only local route tables; it sends no packets.
func TestDiagnosticNativeLoopbackRoutes(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("native collector smoke runs in CI")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("unsupported collector OS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, address := range []string{"127.0.0.1", "::1"} {
		result, err := collectDiagnosticRoute(ctx, ExecRunner{}, runtime.GOOS, DiagnosticRouteQuery{Address: address, Port: 22})
		if err != nil || result.Interface == "" {
			t.Fatalf("%s: %+v %v", address, result, err)
		}
	}
}
