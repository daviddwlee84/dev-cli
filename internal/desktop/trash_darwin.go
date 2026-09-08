package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func trashAvailable() error {
	if _, err := exec.LookPath("/usr/bin/trash"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("/usr/bin/osascript"); err != nil {
		return fmt.Errorf("%w: %v", ErrTrashUnavailable, err)
	}
	return nil
}

// Foundation chooses the volume's Trash and reports failure when it cannot
// recycle. Finder's delete command is deliberately not used.
const trashScript = `ObjC.import('Foundation');
function run(argv) {
  // JXA's NSURL** bridge can crash after a successful move. Null out pointers
  // avoid that bridge entirely; the caller verifies the source is absent.
  if (!$.NSFileManager.defaultManager.trashItemAtURLResultingItemURLError($.NSURL.fileURLWithPath(argv[0]), null, null)) {
    throw new Error('system Trash rejected the item');
  }
}`

func trash(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", trashScript, path)
	// macOS 15+ ships this native Foundation-backed helper. --stopOnError is
	// essential: otherwise the tool can report a failed move with exit zero.
	if _, err := exec.LookPath("/usr/bin/trash"); err == nil {
		cmd = exec.CommandContext(ctx, "/usr/bin/trash", "--stopOnError", path)
	}
	body, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("move to Trash: %w: %s", err, strings.TrimSpace(string(body)))
	}
	return nil
}
