package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/daviddwlee84/dev-cli/internal/feedback"
)

// Reuse the actual hygiene command adapters without another root Load, Cobra
// initializer, shell command, runtime resolution or implicit remote lookup.
func (w *tuiWorkflow) runHygieneInspection() error {
	w.result.Scoped = true
	request := w.request.Hygiene
	if request == nil || request.Checkout == "" {
		return errors.New("hygiene requires an exact selected checkout")
	}
	args := []string{"--repo", request.Checkout}
	switch request.Operation {
	case "status", "report":
		if request.Scope != "" {
			return errors.New("scan scope requires a scan operation")
		}
	case "scan":
		switch request.Scope {
		case "worktree", "staged", "history":
			args = append(args, "--scope", request.Scope)
		default:
			return errors.New("choose worktree, staged or history scan scope")
		}
	default:
		return errors.New("unsupported dashboard hygiene operation")
	}
	root := newHygieneCmd(&w.app)
	command, _, err := root.Find([]string{request.Operation})
	if err != nil {
		return err
	}
	if err := command.ParseFlags(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(w.ctx, os.Interrupt)
	command.SetContext(ctx)
	fmt.Fprintln(w.app.Out, "Hygiene checkout: "+feedback.Sanitize(request.Checkout))
	err = command.RunE(command, command.Flags().Args())
	stop()
	w.result.Status = "Returned from hygiene " + request.Operation
	if err != nil {
		w.result.Severity = "error"
		// Render the same sanitized adapter error before leaving the foreground.
		fmt.Fprintln(w.app.Err, err)
	}
	if w.app.canPick() {
		_, pause := newPrompter(&w.app).line("Enter to return to dashboard", "")
		if err == nil {
			err = pause
		}
	}
	return err
}
