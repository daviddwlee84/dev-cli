package experiment

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/pathx"
)

// ErrCrossFilesystem identifies a transition that cannot use one atomic move.
var ErrCrossFilesystem = errors.New("cross-filesystem move is not supported")

// FinalizeError reports whether a failed post-move catalog transition was
// successfully rolled back. Cause remains available through errors.Is/As.
type FinalizeError struct {
	Operation   string
	Source      string
	Destination string
	Cause       error
	RollbackErr error
}

func (e *FinalizeError) Error() string {
	operation := e.Operation
	if operation == "" {
		operation = "graduation"
	}
	if e.RollbackErr == nil {
		return fmt.Sprintf("finalize %s catalog after moving %s to %s: %v; move rolled back",
			operation, e.Source, e.Destination, e.Cause)
	}
	return fmt.Sprintf("finalize %s catalog after moving %s to %s: %v; rollback to %s also failed: %v",
		operation, e.Source, e.Destination, e.Cause, e.Source, e.RollbackErr)
}

func (e *FinalizeError) Unwrap() []error {
	if e.RollbackErr == nil {
		return []error{e.Cause}
	}
	return []error{e.Cause, e.RollbackErr}
}

func (s *Service) availableGraduationDestination(components []string) (string, error) {
	intended := filepath.Join(append([]string{s.projectRoot}, components...)...)
	if err := rejectExisting(intended); err != nil {
		return "", err
	}
	destination, err := pathx.JoinChild(s.projectRoot, components...)
	if err != nil {
		return "", err
	}
	if err := rejectExisting(destination); err != nil {
		return "", err
	}
	return destination, nil
}

func storedLocationMatches(location catalog.Location, source string) bool {
	return filepath.Clean(location.CurrentPath) == source || filepath.Clean(location.RealPath) == source
}

func (s *Service) resolveGraduateItem(ctx context.Context, request GraduateRequest) (Item, []Diagnostic, error) {
	if strings.TrimSpace(request.Ref) != "" {
		return s.ResolveWithOptions(ctx, request.Ref, ResolveOptions{
			IncludeDeprecated: true,
			IncludeArchived:   true,
		})
	}
	current := request.CurrentDir
	if current == "" {
		var err error
		current, err = s.getwd()
		if err != nil {
			return Item{}, nil, fmt.Errorf("get current directory: %w", err)
		}
	}
	canonicalCurrent, err := pathx.CanonicalChild(s.triesRoot, current)
	if err != nil {
		return Item{}, nil, fmt.Errorf("%s is not inside %s — name the try to graduate", current, s.triesRoot)
	}
	canonicalRoot, err := pathx.Canonical(s.triesRoot)
	if err != nil {
		return Item{}, nil, err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalCurrent)
	if err != nil {
		return Item{}, nil, err
	}
	first, _, _ := strings.Cut(relative, string(filepath.Separator))
	if first == "" || first == "." || strings.HasPrefix(first, ".") {
		return Item{}, nil, fmt.Errorf("%s is not inside a visible try under %s", current, s.triesRoot)
	}
	return s.ResolveWithOptions(ctx, filepath.Join(s.triesRoot, first), ResolveOptions{
		IncludeDeprecated: true,
		IncludeArchived:   true,
	})
}

func (s *Service) hasCommit(ctx context.Context, directory string) bool {
	if unix, _, err := s.gitLastCommit(ctx, directory); err == nil && unix > 0 {
		return true
	}
	_, err := s.gitRun(ctx, directory, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func (s *Service) ensureInitialCommit(ctx context.Context, directory, name string) (bool, error) {
	if s.hasCommit(ctx, directory) {
		return false, nil
	}
	empty, err := isEmptyWorkTree(directory)
	if err != nil {
		return false, err
	}
	if empty {
		readme := filepath.Join(directory, "README.md")
		file, err := os.OpenFile(readme, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return false, err
		}
		if _, err := fmt.Fprintf(file, "# %s\n", name); err != nil {
			_ = file.Close()
			return false, err
		}
		if err := file.Close(); err != nil {
			return false, err
		}
	}
	if _, err := s.gitRun(ctx, directory, "add", "--all"); err != nil {
		return false, err
	}
	tracked, err := s.gitRun(ctx, directory, "ls-files", "--cached")
	if err != nil {
		return false, err
	}
	commitArgs := []string{"commit"}
	if tracked == "" {
		// A Try containing only globally ignored files still needs a HEAD, but dev
		// must not force-add or overwrite those user files merely to manufacture it.
		commitArgs = append(commitArgs, "--allow-empty")
	}
	commitArgs = append(commitArgs, "-m", "chore: graduate experiment into a project")
	if _, err := s.gitRun(ctx, directory, commitArgs...); err != nil {
		return false, err
	}
	return true, nil
}

func rejectExisting(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect move destination %s: %w", path, err)
	}
	return nil
}

func isEmptyWorkTree(directory string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return false, nil
		}
	}
	return true, nil
}

func nearestExisting(path string) (string, error) {
	path = filepath.Clean(path)
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		path = parent
	}
}
