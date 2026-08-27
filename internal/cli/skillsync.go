package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Markers delimit the generated block inside the skill's command reference,
// so the hand-written parts of that file survive a regeneration.
const (
	genBegin = "<!-- BEGIN GENERATED COMMANDS -->"
	genEnd   = "<!-- END GENERATED COMMANDS -->"
)

// commandReferencePath is the file `dev skill sync` maintains, relative to the
// repository root. Sync only makes sense when run from a checkout of dev
// itself — the installed binary's copy is embedded and immutable.
const commandReferencePath = "internal/skill/files/references/commands.md"

// syncCommandReference writes the generated block into the skill source, or
// reports drift when check is set.
func syncCommandReference(app *App, generated string, check bool) error {
	path, err := findSkillSource()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated, err := replaceBlock(string(current), generated)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if string(current) == updated {
		fmt.Fprintf(app.Out, "command reference is current (%s)\n", commandReferencePath)
		return nil
	}
	if check {
		return errors.New("the skill's command reference is out of date — run `dev skill sync`")
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "updated %s\n", commandReferencePath)
	fmt.Fprintln(app.Out, "rebuild dev so the embedded copy matches: go build ./cmd/dev")
	return nil
}

// replaceBlock swaps the content between the generated markers.
func replaceBlock(doc, generated string) (string, error) {
	start := strings.Index(doc, genBegin)
	end := strings.Index(doc, genEnd)
	if start < 0 || end < 0 || end < start {
		return "", fmt.Errorf("missing the %s / %s markers", genBegin, genEnd)
	}
	return doc[:start+len(genBegin)] + "\n\n" + generated + doc[end:], nil
}

// findSkillSource walks up from the working directory looking for dev's own
// checkout, so `dev skill sync` works from anywhere inside it.
func findSkillSource() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, commandReferencePath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found — run `dev skill sync` from a checkout of dev-cli itself",
				commandReferencePath)
		}
		dir = parent
	}
}
