// Package safefile provides bounded reads that reject non-regular files
// without blocking on repository-controlled FIFOs.
package safefile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
)

var (
	ErrNotRegular = errors.New("file is not regular")
	ErrTooLarge   = errors.New("file exceeds size limit")
)

// OpenRegular opens path without blocking on Unix special files and verifies
// the opened descriptor rather than trusting a path-level stat.
func OpenRegular(path string) (*os.File, os.FileInfo, error) {
	file, err := openReadOnly(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrNotRegular
	}
	return file, info, nil
}

// ReadAll reads at most max bytes from an already verified descriptor.
func ReadAll(ctx context.Context, file *os.File, max int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var body bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := file.Read(chunk)
		if n > 0 {
			if int64(body.Len()+n) > max {
				return nil, ErrTooLarge
			}
			_, _ = body.Write(chunk[:n])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return body.Bytes(), nil
			}
			return nil, readErr
		}
	}
}

// ReadRegular opens and reads a regular file under a hard byte bound.
func ReadRegular(ctx context.Context, path string, max int64) ([]byte, error) {
	file, info, err := OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > max {
		return nil, ErrTooLarge
	}
	return ReadAll(ctx, file, max)
}
