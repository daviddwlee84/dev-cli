package fleet

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

const powerShellCommandPrefix = "powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand "

func TestWindowsRemoteCommandRoutesAllowlistedFleetHelpers(t *testing.T) {
	request := testEncodedOpenRequest(t, OpenRequest{Name: "api", Path: `C:\src\api`})
	tests := [][]string{
		{"fleet", "_snapshot"},
		{"fleet", "_sync"},
		{"fleet", "_open-herdr", "--request", request},
		{"fleet", "_shell", "--request", request},
	}
	for _, args := range tests {
		t.Run(args[1], func(t *testing.T) {
			command, err := checkedRemoteCommand(Host{Name: "lab", RemoteOS: RemoteOSWindows, DevPath: "auto"}, args)
			if err != nil {
				t.Fatal(err)
			}
			script := decodePowerShellRemoteCommand(t, command)
			if !strings.Contains(script, "Get-Command -Name 'dev.exe'") || !strings.Contains(script, "exit 127") {
				t.Fatalf("PowerShell auto locator/no-dev contract missing:\n%s", script)
			}
			if !strings.Contains(script, "& $devExecutable @devArguments") || !strings.Contains(script, "$LASTEXITCODE") || !strings.Contains(script, "exit [int]$devExitCode") {
				t.Fatalf("PowerShell invocation/exit propagation missing:\n%s", script)
			}
			if got := decodePowerShellArgumentVector(t, script); !reflect.DeepEqual(got, args) {
				t.Fatalf("PowerShell argv = %#v, want %#v", got, args)
			}
		})
	}
}

func TestWindowsRemoteCommandEncodesArgumentsAndExecutableAsData(t *testing.T) {
	injection := `C:\repo'; Remove-Item -Recurse C:\; #`
	request := testEncodedOpenRequest(t, OpenRequest{Name: `$(Write-Error pwned)`, Path: injection})
	devPath := `C:\Program Files\dev'; Write-Error pwned; #.exe`
	args := []string{"fleet", "_shell", "--request", request}
	command, err := checkedRemoteCommand(Host{Name: "lab", RemoteOS: RemoteOSWindows, DevPath: devPath}, args)
	if err != nil {
		t.Fatal(err)
	}
	script := decodePowerShellRemoteCommand(t, command)
	for _, raw := range []string{injection, devPath, `$(Write-Error pwned)`} {
		if strings.Contains(script, raw) {
			t.Fatalf("untrusted value %q was interpolated into PowerShell:\n%s", raw, script)
		}
	}
	matches := powerShellPayloadPattern.FindAllStringSubmatch(script, -1)
	if len(matches) != 2 {
		t.Fatalf("PowerShell payload count = %d, want path and argv:\n%s", len(matches), script)
	}
	pathBytes, err := base64.StdEncoding.DecodeString(matches[0][1])
	if err != nil {
		t.Fatal(err)
	}
	if string(pathBytes) != devPath {
		t.Fatalf("decoded dev path = %q, want %q", pathBytes, devPath)
	}
	if got := decodePowerShellArgumentVector(t, script); !reflect.DeepEqual(got, args) {
		t.Fatalf("decoded argv = %#v, want %#v", got, args)
	}
}

func TestWindowsRemoteCommandPreservesSyncStdinByNotConsumingIt(t *testing.T) {
	command, err := checkedRemoteCommand(Host{Name: "lab", RemoteOS: RemoteOSWindows, DevPath: "auto"}, []string{"fleet", "_sync"})
	if err != nil {
		t.Fatal(err)
	}
	script := decodePowerShellRemoteCommand(t, command)
	lower := strings.ToLower(script)
	for _, consumer := range []string{"read-host", "console]::in", "$input", "standardinput", "< $null"} {
		if strings.Contains(lower, consumer) {
			t.Fatalf("PowerShell wrapper consumes or redirects stdin through %q:\n%s", consumer, script)
		}
	}
	if !strings.Contains(script, "& $devExecutable @devArguments") {
		t.Fatalf("native child invocation that inherits stdin is missing:\n%s", script)
	}
}

func TestWindowsRemoteCommandRejectsNonAllowlistedShapes(t *testing.T) {
	validRequest := testEncodedOpenRequest(t, OpenRequest{Name: "api"})
	tests := [][]string{
		nil,
		{"status"},
		{"fleet", "list"},
		{"fleet", "_snapshot", "extra"},
		{"fleet", "_sync", "--request"},
		{"fleet", "_shell"},
		{"fleet", "_shell", "--request", "not base64url!"},
		{"fleet", "_shell", "--other", validRequest},
	}
	for index, args := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			if _, err := checkedRemoteCommand(Host{Name: "lab", RemoteOS: RemoteOSWindows, DevPath: "auto"}, args); err == nil {
				t.Fatalf("checkedRemoteCommand accepted %#v", args)
			}
		})
	}
}

func TestPowerShellEncodedCommandIsUTF16LE(t *testing.T) {
	script := "$value = 'fleet-測試-😀'\n"
	encoded := encodePowerShellCommand(script)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE payload has odd byte length %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(raw[index*2:])
	}
	if got := string(utf16.Decode(units)); got != script {
		t.Fatalf("decoded script = %q, want %q", got, script)
	}
	if bytes.HasPrefix(raw, []byte{0xff, 0xfe}) {
		t.Fatal("PowerShell -EncodedCommand payload unexpectedly contains a BOM")
	}
}

func TestPOSIXRemoteCommandDoesNotExpandExplicitPathOnController(t *testing.T) {
	controllerRoot := t.TempDir()
	t.Setenv("REMOTE_DEV_ROOT", controllerRoot)
	command := remoteCommand(Host{RemoteOS: RemoteOSPOSIX, DevPath: "$REMOTE_DEV_ROOT/dev"}, []string{"fleet", "_snapshot"})
	if !strings.Contains(command, `'$REMOTE_DEV_ROOT/dev'`) || strings.Contains(command, controllerRoot) {
		t.Fatalf("POSIX remote command expanded a controller path: %s", command)
	}
}

var powerShellPayloadPattern = regexp.MustCompile(`FromBase64String\('([A-Za-z0-9+/=]+)'\)`)

func decodePowerShellRemoteCommand(t *testing.T, command string) string {
	t.Helper()
	if !strings.HasPrefix(command, powerShellCommandPrefix) {
		t.Fatalf("remote command prefix = %q", command)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, powerShellCommandPrefix))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("encoded PowerShell byte length = %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(raw[index*2:])
	}
	return string(utf16.Decode(units))
}

func decodePowerShellArgumentVector(t *testing.T, script string) []string {
	t.Helper()
	matches := powerShellPayloadPattern.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		t.Fatalf("PowerShell wrapper has no encoded payload:\n%s", script)
	}
	payload, err := base64.StdEncoding.DecodeString(matches[len(matches)-1][1])
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(payload, &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func testEncodedOpenRequest(t *testing.T, request OpenRequest) string {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}
