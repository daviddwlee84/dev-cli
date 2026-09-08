package desktop

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"strings"
)

//go:embed recycle_windows.ps1
var recycleScript string

func trashAvailable() error {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return fmt.Errorf("%w: Windows PowerShell is missing", ErrTrashUnavailable)
	}
	return nil
}

func trash(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-Command", recycleScript)
	cmd.Stdin = strings.NewReader(path)
	body, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("move to Recycle Bin: %w: %s", err, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(string(body)) != "recycled" {
		return fmt.Errorf("Recycle Bin returned no verified completion")
	}
	return nil
}
