package tui

import "golang.org/x/sys/unix"

func flushTerminalFD(fd uintptr) error { return unix.IoctlSetInt(int(fd), unix.TCFLSH, unix.TCIFLUSH) }
