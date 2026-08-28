//go:build windows

package note

// Windows does not expose a portable directory fsync through os.File. Atomic
// rename still protects against torn content; the file itself is synced first.
func syncNoteDir(string) error { return nil }
