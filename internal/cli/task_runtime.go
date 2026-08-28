package cli

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/dev-cli/internal/runtime"
	"github.com/daviddwlee84/dev-cli/internal/task"
)

func runtimeForTask(app *App, t *task.Task) runtime.Runtime {
	if t.RuntimeHandle == "" {
		t.RuntimeName = ""
		return app.Runtime()
	}
	if t.RuntimeName != "" {
		return app.runtimeNamed(t.RuntimeName)
	}
	return app.Runtime()
}

func setTaskRuntime(t *task.Task, rt runtime.Runtime, opened runtime.OpenResult) {
	t.RuntimeHandle = persistedRuntimeHandle(rt, opened)
	if t.RuntimeHandle == "" {
		t.RuntimeName = ""
		return
	}
	t.RuntimeName = rt.Name()
}

func clearTaskRuntime(t *task.Task) {
	t.RuntimeHandle = ""
	t.RuntimeName = ""
}

func runtimeHandleCovers(ctx context.Context, rt runtime.Runtime, handle, checkout string) (bool, error) {
	if handle == "" || rt == nil || rt.Name() == "none" {
		return false, nil
	}
	sessions, err := rt.List(ctx)
	if err != nil {
		return false, err
	}
	for _, session := range sessions {
		if session.Handle == handle && session.Covers(checkout) {
			return true, nil
		}
	}
	return false, nil
}

// closeTaskRuntime validates an opaque persisted handle before sending it back
// to its recorded backend. A stale handle is cleared without being fed to a
// different backend; a close error leaves both provenance fields intact.
func closeTaskRuntime(ctx context.Context, app *App, t *task.Task, checkout string) (runtime.Runtime, bool, error) {
	rt := runtimeForTask(app, t)
	if t.RuntimeHandle == "" {
		return rt, false, nil
	}
	live, err := runtimeHandleCovers(ctx, rt, t.RuntimeHandle, checkout)
	if err != nil {
		return rt, false, fmt.Errorf("validate %s runtime session %s: %w", rt.Name(), t.RuntimeHandle, err)
	}
	if !live {
		if t.RuntimeName == "" {
			return rt, false, fmt.Errorf("legacy runtime handle %s has no backend provenance and is not live in %s; reconcile it before cleanup", t.RuntimeHandle, rt.Name())
		}
		clearTaskRuntime(t)
		return rt, false, nil
	}
	if err := rt.Close(ctx, t.RuntimeHandle); err != nil {
		return rt, false, err
	}
	clearTaskRuntime(t)
	return rt, true, nil
}
