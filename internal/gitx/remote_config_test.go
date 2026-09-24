package gitx_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/gitx"
)

func TestRemoteFromConfigDecodesNativeGitWrittenValues(t *testing.T) {
	for _, value := range []string{
		`C:\Users\fixture\AppData\Local\Temp\future.git`,
		`\\server\share\project.git`,
		`https://example.test/a#b;repo.git`,
		`/path/with "quotes" and spaces/repo.git`,
		"  /path/with spaces/repo.git  ",
		"/path/with\ttab\nand\bescapes",
		"/path/專案.git",
	} {
		t.Run(value, func(t *testing.T) {
			common := t.TempDir()
			config := filepath.Join(common, "config")
			if _, err := gitx.Run(t.Context(), "", "config", "--file", config, "remote.origin.url", value); err != nil {
				t.Fatal(err)
			}
			native, err := gitx.Run(t.Context(), "", "config", "--file", config, "--null", "--get", "remote.origin.url")
			if err != nil {
				t.Fatal(err)
			}
			native = strings.TrimSuffix(native, "\x00")
			if got := gitx.RemoteFromConfig(common, "origin"); got != native || got != value {
				t.Fatalf("static=%q native=%q requested=%q", got, native, value)
			}
		})
	}
}

func TestRemoteFromConfigMatchesNativeValueGrammar(t *testing.T) {
	for _, value := range []string{
		`  prefix" quoted #; middle "suffix  ; comment`,
		` ""   suffix `,
		` prefix   "" `,
		` plain   # comment ending with a backslash\`,
		" \"prefix\\\n  suffix\" ",
		" prefix\\\n  suffix ",
		" \"prefix\\\r\n  suffix\" ",
		" prefix\t\tmiddle   suffix ",
		` bare\"quote\\slash`,
		` "  protected spaces  " `,
	} {
		t.Run(value, func(t *testing.T) {
			common := t.TempDir()
			config := filepath.Join(common, "config")
			if err := os.WriteFile(config, []byte("[remote \"origin\"]\nurl ="+value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			native, err := gitx.Run(t.Context(), "", "config", "--file", config, "--null", "--get", "remote.origin.url")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := gitx.RemoteFromConfig(common, "origin"), strings.TrimSuffix(native, "\x00"); got != want {
				t.Fatalf("static=%q native=%q", got, want)
			}
		})
	}
}

func TestRemoteFromConfigRejectsInvalidEscapesAndQuotes(t *testing.T) {
	for _, value := range []string{` bad\q`, ` "unterminated`, ` "bad\123"`, ` bad\ `} {
		common := t.TempDir()
		config := filepath.Join(common, "config")
		if err := os.WriteFile(config, []byte("[remote \"origin\"]\nurl ="+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := gitx.Run(t.Context(), "", "config", "--file", config, "--get", "remote.origin.url"); err == nil {
			t.Fatalf("invalid fixture was accepted by Git: %q", value)
		}
		if got := gitx.RemoteFromConfig(common, "origin"); got != "" {
			t.Fatalf("invalid value was accepted: %q", got)
		}
	}
}
