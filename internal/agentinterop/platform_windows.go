//go:build windows

package agentinterop

import "fmt"

// FileMode on Windows cannot prove privacy of mixed-credential recovery
// payloads. Keep mutation disabled until held-handle DACL cloning, secure
// creation, and persistent file IDs have an independently verified adapter.
func platformTransfers() error {
	return fmt.Errorf("native Windows transfers require a verified private ACL/recovery adapter; use native agent tools: %w", ErrUnsupported)
}
