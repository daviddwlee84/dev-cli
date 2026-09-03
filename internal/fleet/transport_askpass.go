package fleet

import (
	"errors"
	"io"
	"os"
)

type askpassSecretCarrier struct {
	reader *os.File
	writer *os.File
}

func (carrier *askpassSecretCarrier) parentAfterStart() error {
	if carrier.reader == nil {
		return nil
	}
	err := carrier.reader.Close()
	carrier.reader = nil
	return err
}

func (carrier *askpassSecretCarrier) writeSecret(secret []byte) error {
	if carrier.writer == nil {
		return errors.New("askpass secret pipe is closed")
	}
	written, writeErr := carrier.writer.Write(secret)
	if writeErr == nil && written != len(secret) {
		writeErr = io.ErrShortWrite
	}
	closeErr := carrier.writer.Close()
	carrier.writer = nil
	return errors.Join(writeErr, closeErr)
}

func (carrier *askpassSecretCarrier) close() error {
	var readerErr, writerErr error
	if carrier.reader != nil {
		readerErr = carrier.reader.Close()
		carrier.reader = nil
	}
	if carrier.writer != nil {
		writerErr = carrier.writer.Close()
		carrier.writer = nil
	}
	return errors.Join(readerErr, writerErr)
}
