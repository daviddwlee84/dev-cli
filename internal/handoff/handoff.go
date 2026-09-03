// Package handoff runs a rendered prompt through a configured local command.
// It deliberately owns no agent loop, provider integration, recipe, runtime,
// or lifecycle policy.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Mode distinguishes an unattended one-shot from a foreground conversation.
type Mode string

const (
	ModeRun  Mode = "run"
	ModeOpen Mode = "open"
)

// Transport says how the rendered prompt reaches the child.
type Transport string

const (
	TransportStdin Transport = "stdin"
	TransportFile  Transport = "file"
	TransportArgv  Transport = "argv"
)

const (
	PromptPlaceholder     = "{{prompt}}"
	PromptFilePlaceholder = "{{prompt_file}}"
	PromptFileEnv         = "DEV_PROMPT_FILE"
	maxArgPromptBytes     = 100 << 10
)

// Launcher is a normalized executable definition. Exactly one of Command or
// Shell is set; prompt contents are never interpolated into Shell.
type Launcher struct {
	Command     []string
	Shell       string
	Input       Transport
	LoadShellRC bool
	Timeout     time.Duration
}

// Spec is one invocation. IO is injected so CLI tests never have to replace
// process-global descriptors.
type Spec struct {
	Mode      Mode
	Launcher  Launcher
	Prompt    string
	Dir       string
	ShellPath string
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
	DryRun    bool
}

// Preview is safe to display: transported prompt contents and temporary paths
// are represented by fixed markers rather than copied into an error or log.
type Preview struct {
	Mode      Mode
	Dir       string
	Transport Transport
	Timeout   time.Duration
	Command   []string
	Shell     string
}

// Validate checks one launcher independently of a prompt invocation.
func Validate(mode Mode, launcher Launcher) error {
	if mode != ModeRun && mode != ModeOpen {
		return fmt.Errorf("unknown handoff mode %q", mode)
	}
	hasCommand := len(launcher.Command) > 0
	hasShell := strings.TrimSpace(launcher.Shell) != ""
	switch {
	case hasCommand && hasShell:
		return errors.New("set command or shell, not both")
	case !hasCommand && !hasShell:
		return errors.New("one of command or shell is required")
	case hasCommand && strings.TrimSpace(launcher.Command[0]) == "":
		return errors.New("command[0] must not be empty")
	}
	if launcher.LoadShellRC && !hasShell {
		return errors.New("load_shell_rc is valid only with shell")
	}
	if mode == ModeOpen && launcher.Timeout != 0 {
		return errors.New("open does not support a timeout; interactive process-tree cancellation needs terminal job control")
	}

	for _, argument := range launcher.Command {
		if (strings.Contains(argument, "{{") || strings.Contains(argument, "}}")) &&
			argument != PromptPlaceholder && argument != PromptFilePlaceholder {
			return fmt.Errorf("command placeholder %q must be one whole supported argv element", argument)
		}
	}
	promptCount, fileCount := placeholders(launcher.Command)
	if hasShell && (strings.Contains(launcher.Shell, "{{") || strings.Contains(launcher.Shell, "}}")) {
		return errors.New("shell must be static text; prompt placeholders are not allowed")
	}
	switch launcher.Input {
	case TransportStdin:
		if mode == ModeOpen {
			return errors.New("open cannot use stdin transport; stdin is reserved for the conversation")
		}
		if promptCount != 0 || fileCount != 0 {
			return errors.New("stdin transport must not contain prompt placeholders")
		}
	case TransportFile:
		if hasCommand {
			if promptCount != 0 || fileCount != 1 {
				return fmt.Errorf("file transport requires exactly one %s command element", PromptFilePlaceholder)
			}
		} else if !strings.Contains(launcher.Shell, "$"+PromptFileEnv) &&
			!strings.Contains(launcher.Shell, "${"+PromptFileEnv+"}") {
			return fmt.Errorf("shell file transport must reference $%s", PromptFileEnv)
		}
	case TransportArgv:
		if hasShell {
			return errors.New("argv transport requires command, not shell")
		}
		if promptCount != 1 || fileCount != 0 {
			return fmt.Errorf("argv transport requires exactly one %s command element", PromptPlaceholder)
		}
	default:
		return fmt.Errorf("input %q: want stdin, file or argv", launcher.Input)
	}
	return nil
}

func placeholders(command []string) (prompt, file int) {
	for _, argument := range command {
		switch argument {
		case PromptPlaceholder:
			prompt++
		case PromptFilePlaceholder:
			file++
		default:
			if strings.Contains(argument, PromptPlaceholder) || strings.Contains(argument, PromptFilePlaceholder) {
				// Count twice so validation rejects embedded forms such as
				// --prompt={{prompt}} rather than silently substituting them.
				prompt += 2
			}
		}
	}
	return prompt, file
}

// Run validates, optionally previews, then starts the child and waits.
func Run(ctx context.Context, spec Spec) (Preview, error) {
	if err := Validate(spec.Mode, spec.Launcher); err != nil {
		return Preview{}, err
	}
	if spec.Launcher.Input == TransportArgv && len(spec.Prompt) > maxArgPromptBytes {
		return Preview{}, fmt.Errorf("prompt is %d bytes; argv transport is limited to %d", len(spec.Prompt), maxArgPromptBytes)
	}
	preview := makePreview(spec)
	if spec.DryRun {
		return preview, nil
	}

	runCtx := ctx
	cancel := func() {}
	if spec.Launcher.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Launcher.Timeout)
	}
	defer cancel()

	promptFile, cleanup, err := materializePrompt(spec.Launcher.Input, spec.Prompt)
	if err != nil {
		return preview, err
	}
	defer cleanup()

	cmd, err := command(runCtx, spec, promptFile)
	if err != nil {
		return preview, err
	}
	if spec.Mode == ModeRun {
		// Batch launchers get their own process group so timeout cancellation
		// cannot leave a descendant agent running. An interactive child must
		// remain in the terminal's foreground process group; moving it without
		// a full job-control handoff would stop its stdin read with SIGTTIN.
		configureProcessTree(cmd)
		cmd.WaitDelay = 2 * time.Second
	}
	cmd.Dir = spec.Dir
	cmd.Stdout, cmd.Stderr = spec.Out, spec.Err
	if spec.Mode == ModeOpen {
		cmd.Stdin = spec.In
	} else if spec.Launcher.Input == TransportStdin {
		cmd.Stdin = strings.NewReader(spec.Prompt)
	}
	if spec.Launcher.Input == TransportFile && spec.Launcher.Shell != "" {
		cmd.Env = append(os.Environ(), PromptFileEnv+"="+promptFile)
	}
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return preview, fmt.Errorf("handoff %s exceeded %s", spec.Mode, spec.Launcher.Timeout)
		}
		return preview, fmt.Errorf("handoff %s failed: %w", spec.Mode, err)
	}
	return preview, nil
}

func materializePrompt(transport Transport, prompt string) (string, func(), error) {
	if transport != TransportFile {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "dev-prompt-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func command(ctx context.Context, spec Spec, promptFile string) (*exec.Cmd, error) {
	launcher := spec.Launcher
	if len(launcher.Command) > 0 {
		argv := append([]string(nil), launcher.Command...)
		for i, argument := range argv {
			switch argument {
			case PromptPlaceholder:
				argv[i] = spec.Prompt
			case PromptFilePlaceholder:
				argv[i] = promptFile
			}
		}
		return exec.CommandContext(ctx, argv[0], argv[1:]...), nil
	}
	shell := spec.ShellPath
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	if launcher.LoadShellRC {
		// Interactive shells load their rc files before executing -c text, so
		// aliases and functions are available without a POSIX-specific eval
		// bootstrap (the configured shell may be fish or another shell).
		return exec.CommandContext(ctx, shell, "-lic", launcher.Shell), nil
	}
	return exec.CommandContext(ctx, shell, "-c", launcher.Shell), nil
}

func makePreview(spec Spec) Preview {
	preview := Preview{
		Mode: spec.Mode, Dir: spec.Dir, Transport: spec.Launcher.Input,
		Timeout: spec.Launcher.Timeout,
	}
	if len(spec.Launcher.Command) > 0 {
		preview.Command = append([]string(nil), spec.Launcher.Command...)
		for i, argument := range preview.Command {
			switch argument {
			case PromptPlaceholder:
				preview.Command[i] = "<prompt>"
			case PromptFilePlaceholder:
				preview.Command[i] = "<temporary-prompt-file>"
			}
		}
	} else {
		preview.Shell = spec.Launcher.Shell
	}
	return preview
}
