package submodule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/daviddwlee84/dev-cli/internal/safefile"
)

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	root, _, err := safefile.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	observed, err := root.Lstat(name)
	if os.IsNotExist(err) {
		_, err = safefile.CreatePrivateNoClobber(context.Background(), root, name, data, false)
		return err
	}
	if err != nil {
		return err
	}
	_, err = safefile.AtomicReplace(context.Background(), root, name, observed, data, 0600)
	return err
}
