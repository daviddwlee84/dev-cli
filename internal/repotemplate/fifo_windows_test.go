//go:build windows

package repotemplate_test

import "errors"

func makeFIFO(string) error { return errors.New("FIFO unsupported") }
