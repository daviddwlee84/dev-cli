package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// A native executable fixture works on Windows as well as POSIX, without
	// invoking a shell or an installed provider. Other test subprocesses retain
	// their ordinary entry point even while this environment flag is inherited.
	if os.Getenv("DEV_STATIC_DOCTOR_GIT_STUB") == "1" && strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "git" {
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println("git version 2.53.0 (static doctor fixture)")
			os.Exit(0)
		}
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func staticDoctorPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, name)
	if err := os.Link(executable, destination); err != nil {
		source, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		copy, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(copy, source)
		closeErr := copy.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	t.Setenv("DEV_STATIC_DOCTOR_GIT_STUB", "1")
	return dir
}
