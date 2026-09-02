package sshhost

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

const runnerHelperEnvironment = "DEV_SSHHOST_RUNNER_HELPER"

func TestExecRunnerHelperProcess(t *testing.T) {
	sizeText := os.Getenv(runnerHelperEnvironment)
	if sizeText == "" {
		return
	}
	size, err := strconv.Atoi(sizeText)
	if err != nil || size < 0 {
		os.Exit(97)
	}
	stdout := bytes.Repeat([]byte{'o'}, size)
	stderr := bytes.Repeat([]byte{'e'}, size)
	if _, err := os.Stdout.Write(stdout); err != nil {
		os.Exit(98)
	}
	if _, err := os.Stderr.Write(stderr); err != nil {
		os.Exit(99)
	}
	os.Exit(0)
}

func runnerHelperRequest(size int, interactive bool) RunRequest {
	return RunRequest{
		Name: os.Args[0], Args: []string{"-test.run=^TestExecRunnerHelperProcess$"},
		Env:         []string{runnerHelperEnvironment + "=" + strconv.Itoa(size)},
		Interactive: interactive, Display: "runner output fixture",
	}
}

func TestExecRunnerBoundsNoninteractiveOutputWithIndicators(t *testing.T) {
	result, err := (ExecRunner{}).Run(context.Background(), runnerHelperRequest(maxCapturedOutputBytes+8192, false))
	if err != nil {
		t.Fatal(err)
	}
	if !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("truncation flags = stdout %v, stderr %v", result.StdoutTruncated, result.StderrTruncated)
	}
	for name, output := range map[string][]byte{"stdout": result.Stdout, "stderr": result.Stderr} {
		if !bytes.HasSuffix(output, []byte(outputTruncationMarker)) {
			t.Errorf("%s has no truncation marker", name)
		}
		if len(output) != maxCapturedOutputBytes+len(outputTruncationMarker) {
			t.Errorf("%s length = %d", name, len(output))
		}
	}
}

func TestOutputTruncationMarkerCannotParseAsEffectiveConfig(t *testing.T) {
	output := append([]byte("hostname target.example\n"), []byte(outputTruncationMarker)...)
	if _, err := ParseEffective("target", output); err == nil {
		t.Fatal("truncated effective SSH output parsed as complete")
	}
}

func TestExecRunnerStreamsInteractiveOutput(t *testing.T) {
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		stdoutReader.Close()
		stdoutWriter.Close()
		t.Fatal(err)
	}
	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	result, runErr := (ExecRunner{}).Run(context.Background(), runnerHelperRequest(32, true))
	os.Stdout, os.Stderr = originalStdout, originalStderr
	stdoutWriter.Close()
	stderrWriter.Close()
	stdout, stdoutErr := io.ReadAll(stdoutReader)
	stderr, stderrErr := io.ReadAll(stderrReader)
	stdoutReader.Close()
	stderrReader.Close()
	if runErr != nil || stdoutErr != nil || stderrErr != nil {
		t.Fatalf("run = %v, stdout read = %v, stderr read = %v", runErr, stdoutErr, stderrErr)
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("interactive result retained output: %#v", result)
	}
	if strings.TrimSpace(string(stdout)) != strings.Repeat("o", 32) || strings.TrimSpace(string(stderr)) != strings.Repeat("e", 32) {
		t.Fatalf("streamed stdout/stderr lengths = %d/%d", len(stdout), len(stderr))
	}
}

func TestExecRunnerCanBoundMachineOutputWhileInteractive(t *testing.T) {
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = stderrWriter
	request := runnerHelperRequest(64, true)
	request.CaptureStdout = true
	result, runErr := (ExecRunner{}).Run(context.Background(), request)
	os.Stderr = originalStderr
	stderrWriter.Close()
	stderr, stderrErr := io.ReadAll(stderrReader)
	stderrReader.Close()
	if runErr != nil || stderrErr != nil {
		t.Fatalf("run = %v, stderr read = %v", runErr, stderrErr)
	}
	if string(result.Stdout) != strings.Repeat("o", 64) || len(result.Stderr) != 0 || string(stderr) != strings.Repeat("e", 64) {
		t.Fatalf("captured/streamed output = %d/%d/%d", len(result.Stdout), len(result.Stderr), len(stderr))
	}
}
