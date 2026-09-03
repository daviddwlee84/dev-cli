//go:build windows

package safefile

import "os"

// Native Windows portable-file transfer remains capability-blocked. Directory
// handles cannot provide the same fsync contract as supported Unix targets, so
// preserve atomic publication and let higher-level capability negotiation fail
// before treating it as durable transfer evidence.
func syncRoot(*os.Root) error { return nil }
