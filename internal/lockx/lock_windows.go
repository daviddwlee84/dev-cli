//go:build windows

package lockx

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type fileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquire(ctx context.Context, path string) (*fileLock, error) {
	return acquireWindows(ctx, path, false)
}

func acquireMovable(ctx context.Context, path string) (*fileLock, error) {
	return acquireWindows(ctx, path, true)
}

func acquireWindows(ctx context.Context, path string, movable bool) (*fileLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var file *os.File
	var err error
	if movable {
		var name *uint16
		name, err = windows.UTF16PtrFromString(path)
		if err == nil {
			var handle windows.Handle
			handle, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
				nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err == nil {
				file = os.NewFile(uintptr(handle), path)
			}
		}
	} else {
		file, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	lock := &fileLock{file: file}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		err = windows.LockFileEx(windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &lock.overlapped)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *fileLock) Close() error {
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	return errors.Join(unlockErr, l.file.Close())
}
