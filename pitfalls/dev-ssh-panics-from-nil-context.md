# Interactive dev ssh panics with a nil parent context

**Symptoms**: `panic: cannot create context from nil parent`, selecting Manage
from bare `dev ssh` crashes in `herdrremote.Service.List`.
**First seen**: 2026-09
**Status**: fixed in v0.2.25 with interactive-dispatch regression coverage.

## Symptom and cause

```text
panic: cannot create context from nil parent
context.WithTimeout
internal/herdrremote.Service.List
internal/sshflow.Service.List
internal/cli.newSSHManageCmd.func1
internal/cli.runSSHEntry
```

The interactive entry found a child Cobra command and invoked its `RunE`
directly. Cobra initializes the command that goes through `Execute`; `Find` and
`Context()` do not inherit a context for a directly invoked child. The child
therefore supplied nil to the Herdr inventory timeout. Organize lost the same
context, while the formatting helper already received the parent's context.

This is independent of an SSH host timing out: the failure occurs in local menu
orchestration before diagnosing a connection. The explicit `dev ssh manage`
subcommand passes through normal Cobra execution and avoids the dispatcher bug.

## Fix and prevention

The dispatcher calls `sub.SetContext(cmd.Context())` before `sub.RunE(sub, nil)`.
Replacing nil only at the Herdr timeout would hide cancellation/deadline loss;
starting another Cobra execution would repeat root initialization.

The regression tests execute the interactive root with injected pickers/runners,
check both dispatch targets, and retain parent values, deadlines and cancellation
through real management inventory. They require no live SSH or Herdr service.

```bash
go test ./internal/cli -run '^TestSSHEntry' -count=1
```

Related: [connection diagnostic research](../backlog/ssh-connection-diagnostics.md)
and [TCP succeeds while SSH times out](ssh-times-out-but-tcp-connects.md).
