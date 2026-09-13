package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sourceArchive(t *testing.T, extra *tar.Header) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tarfile := tar.NewWriter(gz)
	if err := tarfile.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "release commit"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "go.sum", "cmd/dev/main.go", "internal/skill/dev-cli/SKILL.md"} {
		data := []byte("source\n")
		if err := tarfile.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarfile.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if extra != nil {
		if err := tarfile.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	_ = tarfile.Close() // An over-limit header intentionally has no body.
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestExtractSourceRejectsTraversalAndUnsafeEntries(t *testing.T) {
	for _, header := range []tar.Header{
		{Name: "../outside", Typeflag: tar.TypeReg},
		{Name: "/outside", Typeflag: tar.TypeReg},
		{Name: `C:\outside`, Typeflag: tar.TypeReg},
		{Name: ".git/config", Typeflag: tar.TypeReg},
		{Name: ".specstory/history/private.md", Typeflag: tar.TypeReg},
		{Name: "go.mod", Typeflag: tar.TypeReg},
		{Name: "linked", Typeflag: tar.TypeLink, Linkname: "../outside"},
		{Name: "huge", Typeflag: tar.TypeReg, Size: 129 << 20},
	} {
		t.Run(header.Name, func(t *testing.T) {
			if err := extractSource(sourceArchive(t, &header), filepath.Join(t.TempDir(), "source")); err == nil {
				t.Fatal("unsafe source archive accepted")
			}
		})
	}
}

func TestSourceArchiveBuildUsesLocalVerifiedSource(t *testing.T) {
	dir := t.TempDir()
	archive := sourceArchive(t, &tar.Header{Name: "CLAUDE.md", Typeflag: tar.TypeSymlink, Linkname: "AGENTS.md"})
	builder := &NativeBuilder{goPath: "go", goos: "android"}
	var built bool
	builder.run = func(cmd *exec.Cmd) error {
		if cmd.Args[0] == "go" {
			built = true
			args := strings.Join(cmd.Args[1:], " ")
			if !strings.HasPrefix(args, "build -p 2 -mod=readonly -trimpath -ldflags ") || !strings.Contains(args, "internal/cli.Version=v0.2.34") || strings.Contains(args, "@") {
				t.Fatalf("archive build must use local source and inject exact version: %v", cmd.Args)
			}
			if data, err := os.ReadFile(filepath.Join(cmd.Dir, "go.mod")); err != nil || string(data) != "source\n" {
				t.Fatalf("build did not use extracted source: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(cmd.Dir, "CLAUDE.md")); !os.IsNotExist(err) {
				t.Fatal("archive symlink was materialized")
			}
			return os.WriteFile(filepath.Join(envMap(cmd.Env)["GOBIN"], "dev"), []byte("candidate"), 0o755)
		}
		fmt.Fprintln(cmd.Stdout, "dev version v0.2.34")
		return nil
	}
	path, cleanup, err := builder.Build(context.Background(), "v0.2.34", dir, archive, io.Discard, io.Discard)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil || !built || path == "" {
		t.Fatalf("archive build = %q, built = %v, err = %v", path, built, err)
	}
}

func TestInvalidSourceArchiveStopsBeforeGoAndCleansStage(t *testing.T) {
	dir := t.TempDir()
	builder := &NativeBuilder{goPath: "go", run: func(*exec.Cmd) error {
		t.Fatal("Go must not run with invalid source")
		return nil
	}}
	if _, _, err := builder.Build(context.Background(), "v0.2.34", dir, []byte("broken"), io.Discard, io.Discard); err == nil {
		t.Fatal("invalid archive accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid archive stage not cleaned: %v, %v", entries, err)
	}
}
