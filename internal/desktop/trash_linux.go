package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func trashAvailable() error {
	if _, err := exec.LookPath("gio"); err != nil {
		return fmt.Errorf("%w: install GIO to enable Trash", ErrTrashUnavailable)
	}
	return nil
}

func trash(ctx context.Context, path string) error {
	body, err := exec.CommandContext(ctx, "gio", "trash", "--", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("move to Trash: %w: %s", err, strings.TrimSpace(string(body)))
	}
	return nil
}
