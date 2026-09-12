//go:build windows

package agentskill

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// npm exposes a .cmd shim on Windows. Encode literal invocation data for the
// system PowerShell; do not hand arbitrary shell syntax to cmd.exe. Batch-file
// expansion/control characters remain unsupported even inside quoted values.
func providerCommand(ctx context.Context, program string, args ...string) *exec.Cmd {
	ext := strings.ToLower(filepath.Ext(program))
	if ext != ".cmd" && ext != ".bat" {
		return exec.CommandContext(ctx, program, args...)
	}
	fail := func(message string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, program)
		cmd.Err = errors.New(message)
		return cmd
	}
	if len(args) == 0 || (args[0] != "remove" && args[0] != "--version" && args[0] != "--help") {
		return fail("this Windows npm shim supports reviewed removal only; other management actions require a direct executable")
	}
	values := append([]string{program}, args...)
	for _, value := range values {
		if strings.ContainsAny(value, "\"%!&|<>^\r\n\x00") {
			return fail("Windows skills shim arguments contain unsupported shell syntax; use literal names and sources")
		}
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return fail("system PowerShell path unavailable")
	}
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	script := "& " + strings.Join(quoted, " ") + "; exit $LASTEXITCODE"
	units := utf16.Encode([]rune(script))
	if len(units) > 8000 {
		return fail("selected Windows skills operation is too large; select fewer skills")
	}
	data := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(data[i*2:], u)
	}
	return exec.CommandContext(ctx, filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data))
}
