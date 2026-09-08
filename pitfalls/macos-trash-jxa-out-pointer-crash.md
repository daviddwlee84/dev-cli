# macOS Trash succeeds but osascript exits with segmentation fault

Observed: 2026-09-08, native temporary-directory smoke test.

`NSFileManager.trashItemAtURL:resultingItemURL:error:` can move the item and
then crash through the JXA `NSURL **` out-pointer bridge when reading the
resulting URL. The process exit status therefore does not prove the source
directory remained untouched. The test artifact was recovered and cleaned.

Use `/usr/bin/trash --stopOnError` where available (the system utility ships
with macOS 15+). The stop-on-error option is required to propagate move failure.
On older systems call Foundation with null out pointers, avoiding that bridge.
Never retry a failed Trash invocation as permanent deletion.

`DEV_TEST_NATIVE_TRASH=1 go test ./internal/desktop -run TestNativeTrashRoundTrip`
exercises the preferred helper and Foundation fallback. It moves only a unique
temporary directory and restores only its exact filesystem identity afterward.
