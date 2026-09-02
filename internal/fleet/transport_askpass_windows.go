//go:build windows

package fleet

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

func newAskpassSecretCarrier(cmd *exec.Cmd) (*askpassSecretCarrier, string, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, "", err
	}
	closeBoth := func() {
		_ = reader.Close()
		_ = writer.Close()
	}
	readerHandle := windows.Handle(reader.Fd())
	if err := windows.SetHandleInformation(readerHandle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		closeBoth()
		return nil, "", err
	}
	if err := windows.SetHandleInformation(windows.Handle(writer.Fd()), windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeBoth()
		return nil, "", err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.NoInheritHandles {
		closeBoth()
		return nil, "", errors.New("askpass requires inherited handles")
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(
		cmd.SysProcAttr.AdditionalInheritedHandles,
		syscall.Handle(readerHandle),
	)
	return &askpassSecretCarrier{reader: reader, writer: writer}, strconv.FormatUint(uint64(readerHandle), 10), nil
}

func openAskpassSecret(identifier string) (*os.File, error) {
	value, err := strconv.ParseUint(identifier, 10, 64)
	if err != nil || value == 0 || windows.Handle(value) == windows.InvalidHandle {
		return nil, errors.New("invalid inherited askpass handle")
	}
	file := os.NewFile(uintptr(value), "dev-fleet-askpass")
	if file == nil {
		return nil, errors.New("cannot open inherited askpass handle")
	}
	return file, nil
}
