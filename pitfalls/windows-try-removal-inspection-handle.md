# Windows Try removal: file is being used by another process

Observed in native Windows CI while preparing v0.2.20, 2026-09-09.

Both a fake Trash rename and permanent removal failed with:

```text
The process cannot access the file because it is being used by another process.
```

The blocking handle belonged to dev itself: Go's `os.Root` keeps its directory
from being renamed or deleted on Windows. Keeping the source inspection root
open across the effect therefore blocked every removal.

Keep the parent root held, verify the selected child, close only the source
inspection handle, and revalidate its persistent file identity immediately
before the move/removal. Never close another process or weaken an occupancy
blocker to address this error. The removal round-trip tests exercise this on
Windows, and the required CI gates run before the broad advisory suite.
