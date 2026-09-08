// Package desktop provides explicit native desktop handoffs. Paths and URLs
// are arguments or stdin data, never interpolated into executable scripts.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

var ErrTrashUnavailable = errors.New("system Trash is unavailable; archive instead, or separately confirm permanent deletion")

func OpenURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(raw, "\x00\r\n") {
		return errors.New("invalid repository HTTPS URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "/usr/bin/open", raw)
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; [Console]::InputEncoding=[Text.UTF8Encoding]::new($false); Start-Process -FilePath ([Console]::In.ReadToEnd())")
		cmd.Stdin = strings.NewReader(raw)
	default:
		if bin, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.CommandContext(ctx, bin, raw)
		} else {
			cmd = exec.CommandContext(ctx, "gio", "open", raw)
		}
	}
	if body, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open browser: %w: %s", err, strings.TrimSpace(string(body)))
	}
	return nil
}

func TrashAvailable() error { return trashAvailable() }

// Trash never falls back to permanent deletion, including on unsupported
// volumes. An error after dispatch is an indeterminate result to the caller.
func Trash(ctx context.Context, path string) error {
	if err := TrashAvailable(); err != nil {
		return err
	}
	return trash(ctx, path)
}
