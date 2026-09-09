package triage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// directoryFacts observes non-Git Try activity without following symlinks or
// treating a directory mtime alone as evidence that its files did not change.
func directoryFacts(ctx context.Context, root string) (time.Time, string, error) {
	var latest time.Time
	h := sha256.New()
	encoder := json.NewEncoder(h)
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return errors.New("directory observation exceeds 100000 entries; inspect individually")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		target := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		return encoder.Encode([]any{rel, info.Mode().String(), info.Size(), info.ModTime().UTC(), target})
	})
	return latest, hex.EncodeToString(h.Sum(nil)), err
}
