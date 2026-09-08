//go:build !darwin && !linux && !windows

package desktop

import "context"

func trashAvailable() error               { return ErrTrashUnavailable }
func trash(context.Context, string) error { return ErrTrashUnavailable }
