//go:build unix

package fleet

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func newAskpassSecretCarrier(cmd *exec.Cmd) (*askpassSecretCarrier, string, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, "", err
	}
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, reader)
	return &askpassSecretCarrier{reader: reader, writer: writer}, strconv.Itoa(fd), nil
}

func openAskpassSecret(identifier string) (*os.File, error) {
	fd, err := strconv.Atoi(identifier)
	if err != nil || fd < 3 {
		return nil, errors.New("invalid inherited askpass file descriptor")
	}
	file := os.NewFile(uintptr(fd), "dev-fleet-askpass")
	if file == nil {
		return nil, errors.New("cannot open inherited askpass file descriptor")
	}
	return file, nil
}
