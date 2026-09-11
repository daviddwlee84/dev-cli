package tui

import "golang.org/x/sys/windows"

func flushTerminalFD(fd uintptr) error { return windows.FlushConsoleInputBuffer(windows.Handle(fd)) }
