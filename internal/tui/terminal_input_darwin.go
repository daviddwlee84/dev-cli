package tui

import "golang.org/x/sys/unix"

func flushTerminalFD(fd uintptr) error { return unix.IoctlSetPointerInt(int(fd), unix.TIOCFLUSH, 1) }
