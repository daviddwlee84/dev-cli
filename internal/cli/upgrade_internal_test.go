package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallMethodCommand(t *testing.T) {
	cases := map[installMethod]string{
		methodStandalone: "",
		methodHomebrew:   "brew upgrade dev-cli",
		methodScoop:      "scoop update dev-cli",
		methodGo:         "go install github.com/daviddwlee84/dev-cli/cmd/dev@latest",
	}
	for method, want := range cases {
		if got := method.command(); got != want {
			t.Errorf("%s command = %q, want %q", method.label(), got, want)
		}
	}
}

func TestRunManagedUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command mapping is covered on Windows; this execution test uses a POSIX fixture")
	}
	dir := t.TempDir()
	brew := filepath.Join(dir, "brew")
	if err := os.WriteFile(brew, []byte("#!/bin/sh\nprintf 'manager args: %s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out, errOut bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errOut}
	if err := runManagedUpgrade(context.Background(), app, methodHomebrew); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "brew upgrade dev-cli") || !strings.Contains(got, "manager args: upgrade dev-cli") {
		t.Fatalf("managed upgrade output = %q", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("managed upgrade stderr = %q", errOut.String())
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.3.0", true},
		{"v0.2.0", "v0.2.1", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.3.0", "v0.2.9", false},
		{"v1.0.0", "v0.9.9", false},
		{"v0.2.0-rc.1", "v0.3.0", true},
		{"", "v0.3.0", false}, // a source build never nags
		{"v0.2.0", "not-a-tag", false},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDetectInstallMethod(t *testing.T) {
	cases := map[string]struct {
		path string
		want installMethod
	}{
		"homebrew cellar": {"/opt/homebrew/Cellar/dev-cli/0.2.0/bin/dev", methodHomebrew},
		"linuxbrew":       {"/home/linuxbrew/.linuxbrew/bin/dev", methodHomebrew},
		"scoop app":       {`C:/Users/x/scoop/apps/dev-cli/current/dev.exe`, methodScoop},
		"scoop shim":      {`C:/Users/x/scoop/shims/dev.exe`, methodScoop},
		"standalone":      {"/usr/local/bin/dev", methodStandalone},
		"home local bin":  {"/home/x/.local/bin/dev", methodStandalone},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := detectInstallMethod(c.path); got != c.want {
				t.Errorf("detectInstallMethod(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestDetectInstallMethodGoBin(t *testing.T) {
	t.Setenv("GOBIN", "/home/x/gobin")
	if got := detectInstallMethod("/home/x/gobin/dev"); got != methodGo {
		t.Errorf("GOBIN binary = %v, want methodGo", got)
	}
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/home/x/go")
	if got := detectInstallMethod(filepath.FromSlash("/home/x/go/bin/dev")); got != methodGo {
		t.Errorf("GOPATH/bin binary = %v, want methodGo", got)
	}
}

func TestOtherDevInstalls(t *testing.T) {
	current := detectedInstall{Path: "/usr/local/bin/dev", Resolved: "/usr/local/Cellar/dev-cli/0.2.2/bin/dev", Method: methodHomebrew}
	installs := []detectedInstall{
		current,
		{Path: "/another/link/dev", Resolved: current.Resolved, Method: methodHomebrew},
		{Path: "/home/x/.local/bin/dev", Resolved: "/home/x/.local/bin/dev", Method: methodStandalone},
	}
	others := otherDevInstalls(current, installs)
	if len(others) != 1 || others[0].Path != "/home/x/.local/bin/dev" {
		t.Fatalf("otherDevInstalls() = %+v", others)
	}
}

func TestVerifyChecksum(t *testing.T) {
	archive := []byte("pretend archive bytes")
	sum := sha256.Sum256(archive)
	line := hex.EncodeToString(sum[:]) + "  dev-cli_v9.9.9_linux_amd64.tar.gz\n"
	sums := []byte("deadbeef  other.zip\n" + line)

	if err := verifyChecksum(archive, sums, "dev-cli_v9.9.9_linux_amd64.tar.gz"); err != nil {
		t.Fatalf("matching checksum should verify: %v", err)
	}
	if err := verifyChecksum([]byte("tampered"), sums, "dev-cli_v9.9.9_linux_amd64.tar.gz"); err == nil {
		t.Error("a mismatched archive must be rejected")
	}
	if err := verifyChecksum(archive, sums, "missing-asset.tar.gz"); err == nil {
		t.Error("an asset absent from SHA256SUMS must be rejected")
	}
}

func TestExtractFromTarGz(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	want := []byte("#!/binary\x00payload")
	_ = tw.WriteHeader(&tar.Header{Name: "dev", Mode: 0o755, Size: int64(len(want))})
	_, _ = tw.Write(want)
	tw.Close()
	gz.Close()

	got, err := extractBinary(buf.Bytes(), "dev-cli_v1_linux_amd64.tar.gz", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q", got, want)
	}
	if _, err := extractBinary(buf.Bytes(), "x.tar.gz", "dev.exe"); err == nil {
		t.Error("a missing member must error")
	}
}

func TestExtractFromZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("dev.exe")
	want := []byte("MZ\x00windows binary")
	_, _ = w.Write(want)
	zw.Close()

	got, err := extractBinary(buf.Bytes(), "dev-cli_v1_windows_amd64.zip", "dev.exe")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q", got, want)
	}
}

func TestReplaceBinaryReplacesInPlace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dev")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "dev-upgrade-123")
	if err := os.WriteFile(staged, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(staged, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target after replace = %q, want %q", got, "new")
	}
}
