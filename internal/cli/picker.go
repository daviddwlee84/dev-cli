package cli

import (
	"context"

	"github.com/daviddwlee84/dev-cli/internal/picker"
)

func (a *App) canPick() bool {
	return a.pickerSelect != nil || terminalPair(a.In, a.Out)
}

// pick opens a selector only when both application streams are terminals. The
// bool reports whether an interaction was attempted, allowing callers to retain
// their deterministic line-oriented fallback for pipes and tests.
func (a *App) pick(ctx context.Context, request picker.Request) (picker.Result, bool, error) {
	if len(request.Items) == 0 {
		return picker.Result{}, false, nil
	}
	if a.pickerSelect != nil {
		result, err := a.pickerSelect(ctx, request)
		return result, true, err
	}
	if !a.canPick() {
		return picker.Result{}, false, nil
	}
	result, err := picker.New(a.In, a.Out, a.Err, a.Cfg.Picker.Command).Select(ctx, request)
	return result, true, err
}
