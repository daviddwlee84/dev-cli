package agenthistory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/pathx"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

// Untracking must not strand an already armed legacy commit handoff. Read only
// the common-directory/status contract here to avoid a dependency cycle with
// artifact, which consumes this package for newer external handoffs.
func (s *Service) checkPendingHandoffs(ctx context.Context) error {
	dir := filepath.Join(s.StateDir, "artifact-intents", "v1")
	entries, e := os.ReadDir(dir)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return errors.New("artifact handoff inventory unavailable")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := safefile.ReadStablePath(ctx, filepath.Join(dir, entry.Name()), maxRecordBytes)
		if e != nil {
			return errors.New("artifact handoff cannot be read safely")
		}
		var intent struct {
			Common string `json:"git_common_dir"`
			Status string `json:"status"`
		}
		if json.Unmarshal(b, &intent) != nil || intent.Common == "" {
			return errors.New("invalid artifact handoff inventory")
		}
		common, e := pathx.Canonical(intent.Common)
		if e != nil {
			return errors.New("artifact handoff identity unavailable")
		}
		if common != s.Common {
			continue
		}
		switch intent.Status {
		case "finalized", "discarded":
			continue
		case "armed", "finalizing", "failed":
			return errors.New("resolve existing prepared artifact handoffs before untracking history")
		default:
			return errors.New("unknown artifact handoff state")
		}
	}
	return nil
}
