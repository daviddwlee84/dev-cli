package sshhost

import (
	"context"
	"fmt"
	"reflect"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
)

// A native alias is parsed again when SSH starts. Bind the entire static source
// closure before evaluation: Match exec can change an already-parsed source
// while ssh -G still reports the previously observed effective configuration.
type connectionSourceSnapshot struct {
	inventory Inventory
	files     configedit.Plan
}

func (s *Service) captureConnectionSources(ctx context.Context) (*connectionSourceSnapshot, error) {
	inventory, err := s.Discover(ctx)
	if err != nil {
		return nil, err
	}
	if !inventory.Complete {
		return nil, fmt.Errorf("native SSH source closure is incomplete: %w", ErrUnsupportedRoute)
	}
	paths := []string{inventory.Root}
	seen := map[string]bool{inventory.Root: true}
	for _, file := range inventory.Files {
		if !seen[file.Path] {
			paths = append(paths, file.Path)
			seen[file.Path] = true
		}
	}
	guard, err := configedit.New(ctx, nil, nil, paths)
	if err != nil {
		return nil, err
	}
	snapshot := &connectionSourceSnapshot{inventory: inventory, files: guard}
	if err := s.checkConnectionSources(ctx, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Service) checkConnectionSources(ctx context.Context, snapshot *connectionSourceSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("native SSH source snapshot is missing: %w", ErrSourceChanged)
	}
	if err := snapshot.files.Check(ctx); err != nil {
		return fmt.Errorf("native SSH source changed: %w", ErrSourceChanged)
	}
	inventory, err := s.Discover(ctx)
	if err != nil {
		return err
	}
	if !inventory.Complete || !reflect.DeepEqual(inventory, snapshot.inventory) {
		return ErrSourceChanged
	}
	return nil
}
