# Windows directory rename: Access is denied with a lease held

Observed in native Windows CI while preparing v0.3.0. Paths below are placeholders:

```text
rename <source> <destination>: Access is denied.
```

The blocking handle belonged to dev: its lifecycle lease file was inside the
directory being moved. Opening that file with `FILE_SHARE_DELETE` did not make
the containing directory rename legal. Windows requires files inside a renamed
directory to be closed; file replacement semantics do not remove that directory
restriction. See Microsoft's [FILE_RENAME_INFORMATION remarks](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information#remarks).

The current fix uses a `Global` native mutex keyed by the directory's physical
identity. A keeper pinned to one OS thread acquires and releases the mutex;
the movable lease holds no file handle beneath the renamed tree. Ordinary
Windows directory leases participate in the same mutex while retaining their
legacy file lease. Keep the mutex continuously held through the move and
catalog transaction: release/reacquire creates a writer gap.

Capture native identity eagerly. Windows path-backed `os.Stat` results can defer
file-ID lookup until `os.SameFile`; replacing the named directory between those
calls can make both observations resolve to the replacement. Read volume/file
IDs from an inspection handle before closing it, then compare fresh IDs at the
mutation boundary. Path strings and deferred identity are insufficient.

All concurrent Windows dev lifecycle writers must use v0.3.0+ to share this
movable lease. Older binaries, raw Git and external tools do not participate.

Relevant regression tests:

```sh
go test ./internal/lockx -run '^TestRelocatableLease' -count=1
go test ./internal/experiment -run '^TestDemote' -count=1
```

`TestRelocatableLeaseMovesWithTreeAndStillExcludes` covers rename with continuous
exclusion; adjacent tests cover cross-process contenders and substituted
directories. Demote tests cover moves, stale authority, rollback and recovery.
Require the native Windows relocation gate and full-suite audit before release;
passing POSIX tests or cross-compilation is not evidence of a passing Windows run.

Acquire execution leases when a command actually runs. A Bubble Tea command
returned from `Update` may be discarded without running its completion callback.
Preparing a skill mutation lease while constructing that command leaked the
lease and blocked `TestSkillsViewLoadsLazilyFiltersAndRunsExplicitActions`.
The native mutex correctly exposed a lifetime bug previously hidden by file
handle garbage collection. Keep preparation and release inside the execution
adapter's `Run`, including failed or canceled provider execution.
