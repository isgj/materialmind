package workspacetools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

const (
	defaultCommandTimeout  = 120 * time.Second
	maxCommandTimeout      = 15 * time.Minute
	maxCommandOutputBytes  = 256 * 1024
	maxLiveOutputBytes     = 512 * 1024
	maxCommandLength       = 4096
	maxCommandArgumentSize = 16 * 1024
	maxCommandArguments    = 256
	maxCommandInputSize    = 64 * 1024
)

type RunCommandArgs struct {
	Command          string   `json:"command" jsonschema:"Executable name or path. The executable is run directly without implicit shell parsing."`
	Args             []string `json:"args,omitempty" jsonschema:"Arguments passed directly to the executable in order."`
	WorkingDirectory string   `json:"workingDirectory,omitempty" jsonschema:"Optional working directory relative to the workspace. Defaults to the workspace root and must remain inside the configured workspace or repository scope."`
	TimeoutSeconds   int      `json:"timeoutSeconds,omitempty" jsonschema:"Optional timeout in seconds. Defaults to 120 and cannot exceed 900."`
}

type RunCommandResult struct {
	State              string   `json:"state"`
	Command            string   `json:"command"`
	Args               []string `json:"args"`
	WorkingDirectory   string   `json:"workingDirectory"`
	TimeoutSeconds     int      `json:"timeoutSeconds"`
	ExitCode           *int     `json:"exitCode,omitempty"`
	Stdout             string   `json:"stdout,omitempty"`
	Stderr             string   `json:"stderr,omitempty"`
	DurationMS         int64    `json:"durationMs,omitempty"`
	TimedOut           bool     `json:"timedOut,omitempty"`
	StdoutTruncated    bool     `json:"stdoutTruncated,omitempty"`
	StderrTruncated    bool     `json:"stderrTruncated,omitempty"`
	StdoutBytesOmitted int64    `json:"stdoutBytesOmitted,omitempty"`
	StderrBytesOmitted int64    `json:"stderrBytesOmitted,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	Error              string   `json:"error,omitempty"`
}

type CommandOutputEvent struct {
	ToolCallID string `json:"toolCallId"`
	Stream     string `json:"stream"`
	Text       string `json:"text"`
}

type CommandOutputSink func(CommandOutputEvent)

type CommandResultEvent struct {
	ToolCallID string         `json:"toolCallId"`
	Output     map[string]any `json:"output"`
}

type CommandResultSink func(CommandResultEvent)

type commandConfirmationPayload struct {
	Kind                 string   `json:"kind"`
	Command              string   `json:"command"`
	Args                 []string `json:"args"`
	Executable           string   `json:"executable"`
	InvocationExecutable string   `json:"invocationExecutable"`
	WorkingDirectory     string   `json:"workingDirectory"`
	TimeoutSeconds       int      `json:"timeoutSeconds"`
}

type resolvedCommand struct {
	command              string
	args                 []string
	executable           string
	invocationExecutable string
	workingDirectory     string
	timeout              time.Duration
}

func newRunCommandTool(
	rootPath string,
	permission toolpolicy.Permission,
	outputSink CommandOutputSink,
	resultSink CommandResultSink,
	requestApproval ToolApprovalHandler,
) (tool.Tool, error) {
	access, err := newFilesystemAccess(rootPath, permission.FilesystemScope)
	if err != nil {
		return nil, err
	}
	// This wrapper is shared by all invocations so concurrent sibling commands
	// cannot call a non-thread-safe output sink at the same time.
	outputSink = synchronizedCommandOutputSink(outputSink)
	baseTool, err := functiontool.New(
		functiontool.Config{
			Name: toolpolicy.ToolRunCommand,
			Description: "Run one non-interactive executable and return bounded stdout, stderr, and its exit code. Arguments are passed directly without shell parsing. " +
				"Use an explicit shell executable only when shell syntax is required. The working directory is constrained by policy, but the process itself has the backend operating-system user's access. " + access.Description(),
		},
		func(ctx agent.Context, args RunCommandArgs) (RunCommandResult, error) {
			result, runErr := runCommandWithApprovalPolicy(
				access,
				permission,
				ctx,
				args,
				outputSink,
				requestApproval,
			)
			emitCommandResult(ctx, result, runErr, resultSink)
			return result, runErr
		},
	)
	if err != nil {
		return nil, err
	}
	approvalAware, err := newApprovalAwareTool(
		baseTool,
		func(input map[string]any, confirmation *toolconfirmation.ToolConfirmation) (map[string]any, error) {
			args, err := decodeRunCommandArgs(input)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveCommand(access, args)
			if err != nil {
				return nil, err
			}
			return commandResultMap(RunCommandResult{
				State:            "denied",
				Command:          resolved.command,
				Args:             resolved.args,
				WorkingDirectory: resolved.workingDirectory,
				TimeoutSeconds:   int(resolved.timeout / time.Second),
				Reason:           approvalReason(confirmation),
			})
		},
	)
	if err != nil {
		return nil, err
	}
	return approvalAware, nil
}

func emitCommandResult(
	ctx agent.Context,
	result RunCommandResult,
	runErr error,
	sink CommandResultSink,
) {
	if sink == nil || ctx.FunctionCallID() == "" ||
		result.State == "approval_required" || ctx.Err() != nil {
		return
	}
	var output map[string]any
	if runErr != nil {
		output = map[string]any{"error": runErr.Error()}
	} else {
		mapped, err := commandResultMap(result)
		if err != nil {
			return
		}
		output = mapped
	}
	sink(CommandResultEvent{
		ToolCallID: ctx.FunctionCallID(),
		Output:     output,
	})
}

func runCommandWithPolicy(
	access filesystemAccess,
	permission toolpolicy.Permission,
	ctx agent.Context,
	args RunCommandArgs,
	sink CommandOutputSink,
) (RunCommandResult, error) {
	return runCommandWithApprovalPolicy(access, permission, ctx, args, sink, nil)
}

func runCommandWithApprovalPolicy(
	access filesystemAccess,
	permission toolpolicy.Permission,
	ctx agent.Context,
	args RunCommandArgs,
	sink CommandOutputSink,
	requestApproval ToolApprovalHandler,
) (RunCommandResult, error) {
	resolved, err := resolveCommand(access, args)
	if err != nil {
		return RunCommandResult{}, err
	}
	confirmation := ctx.ToolConfirmation()
	if confirmation == nil && permission.ConfirmationMode == toolpolicy.ConfirmationAsk {
		if requestApproval != nil {
			decision, approvalErr := requestCommandApproval(
				ctx,
				args,
				resolved,
				requestApproval,
			)
			if approvalErr != nil {
				return RunCommandResult{}, approvalErr
			}
			if !decision.Approved {
				result := commandResult(resolved, "denied")
				result.Reason = decision.Reason
				return result, nil
			}
			revalidated, revalidationErr := resolveCommand(access, args)
			if revalidationErr != nil {
				return RunCommandResult{}, fmt.Errorf(
					"revalidate approved command: %w",
					revalidationErr,
				)
			}
			if !sameCommand(revalidated, resolved) {
				return RunCommandResult{}, fmt.Errorf(
					"approved command does not match the requested command",
				)
			}
			return executeCommand(ctx, revalidated, sink)
		}
		if err := requestCommandConfirmation(ctx, resolved); err != nil {
			return RunCommandResult{}, err
		}
		return commandResult(resolved, "approval_required"), nil
	}
	if confirmation != nil && !confirmation.Confirmed {
		result := commandResult(resolved, "denied")
		result.Reason = approvalReason(confirmation)
		return result, nil
	}
	if confirmation != nil {
		approved, err := approvedCommand(confirmation)
		if err != nil {
			return RunCommandResult{}, err
		}
		if !sameCommand(resolved, approved) {
			return RunCommandResult{}, fmt.Errorf("approved command does not match the requested command")
		}
		resolved = approved
	}
	return executeCommand(ctx, resolved, sink)
}

func requestCommandApproval(
	ctx agent.Context,
	args RunCommandArgs,
	command resolvedCommand,
	handler ToolApprovalHandler,
) (ToolApprovalDecision, error) {
	payload := commandApprovalPayload(command)
	decision, err := handler(ctx, ToolApprovalRequest{
		ToolCallID: ctx.FunctionCallID(),
		ToolName:   toolpolicy.ToolRunCommand,
		Input:      commandApprovalInput(args),
		Payload:    commandApprovalPayloadMap(payload),
		Hint:       fmt.Sprintf("Allow the agent to run %s?", command.command),
	})
	if err != nil {
		return ToolApprovalDecision{}, fmt.Errorf("request command approval: %w", err)
	}
	return decision, nil
}

func requestCommandConfirmation(ctx agent.Context, command resolvedCommand) error {
	payload := commandApprovalPayload(command)
	if err := ctx.RequestConfirmation(fmt.Sprintf("Allow the agent to run %s?", command.command), payload); err != nil {
		return fmt.Errorf("request command approval: %w", err)
	}
	ctx.Actions().SkipSummarization = true
	return nil
}

func commandApprovalPayload(command resolvedCommand) commandConfirmationPayload {
	return commandConfirmationPayload{
		Kind:                 toolpolicy.ToolRunCommand,
		Command:              command.command,
		Args:                 cloneStrings(command.args),
		Executable:           command.executable,
		InvocationExecutable: command.invocationExecutable,
		WorkingDirectory:     command.workingDirectory,
		TimeoutSeconds:       int(command.timeout / time.Second),
	}
}

func commandApprovalInput(args RunCommandArgs) map[string]any {
	input := map[string]any{"command": args.Command}
	if len(args.Args) > 0 {
		input["args"] = cloneStrings(args.Args)
	}
	if args.WorkingDirectory != "" {
		input["workingDirectory"] = args.WorkingDirectory
	}
	if args.TimeoutSeconds != 0 {
		input["timeoutSeconds"] = args.TimeoutSeconds
	}
	return input
}

func commandApprovalPayloadMap(payload commandConfirmationPayload) map[string]any {
	return map[string]any{
		"kind":                 payload.Kind,
		"command":              payload.Command,
		"args":                 cloneStrings(payload.Args),
		"executable":           payload.Executable,
		"invocationExecutable": payload.InvocationExecutable,
		"workingDirectory":     payload.WorkingDirectory,
		"timeoutSeconds":       payload.TimeoutSeconds,
	}
}

func approvedCommand(confirmation *toolconfirmation.ToolConfirmation) (resolvedCommand, error) {
	if confirmation == nil || confirmation.Payload == nil {
		return resolvedCommand{}, fmt.Errorf("command approval payload is missing")
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return resolvedCommand{}, fmt.Errorf("encode command approval payload: %w", err)
	}
	var payload commandConfirmationPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return resolvedCommand{}, fmt.Errorf("decode command approval payload: %w", err)
	}
	if payload.Kind != toolpolicy.ToolRunCommand {
		return resolvedCommand{}, fmt.Errorf("command approval payload has invalid kind %q", payload.Kind)
	}
	if payload.TimeoutSeconds <= 0 || payload.TimeoutSeconds > int(maxCommandTimeout/time.Second) {
		return resolvedCommand{}, fmt.Errorf("command approval payload has invalid timeout")
	}
	invocationExecutable := payload.InvocationExecutable
	if invocationExecutable == "" {
		// Confirmations created before invocationExecutable was added used the
		// executable field for both the PATH lookup result and canonical target.
		invocationExecutable = payload.Executable
		payload.Executable, err = validateCommandExecutable(payload.Executable)
		if err != nil {
			return resolvedCommand{}, fmt.Errorf("validate approved executable: %w", err)
		}
	}
	return resolvedCommand{
		command:              payload.Command,
		args:                 cloneStrings(payload.Args),
		executable:           payload.Executable,
		invocationExecutable: invocationExecutable,
		workingDirectory:     payload.WorkingDirectory,
		timeout:              time.Duration(payload.TimeoutSeconds) * time.Second,
	}, nil
}

func resolveCommand(access filesystemAccess, args RunCommandArgs) (resolvedCommand, error) {
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return resolvedCommand{}, fmt.Errorf("command is required")
	}
	if len(command) > maxCommandLength {
		return resolvedCommand{}, fmt.Errorf("command must be at most %d bytes", maxCommandLength)
	}
	if strings.ContainsRune(command, 0) || strings.ContainsAny(command, "\r\n") {
		return resolvedCommand{}, fmt.Errorf("command contains unsupported control characters")
	}
	if len(args.Args) > maxCommandArguments {
		return resolvedCommand{}, fmt.Errorf("command accepts at most %d arguments", maxCommandArguments)
	}
	totalSize := len(command)
	for index, argument := range args.Args {
		if strings.ContainsRune(argument, 0) {
			return resolvedCommand{}, fmt.Errorf("argument %d contains a null byte", index+1)
		}
		if len(argument) > maxCommandArgumentSize {
			return resolvedCommand{}, fmt.Errorf("argument %d must be at most %d bytes", index+1, maxCommandArgumentSize)
		}
		totalSize += len(argument)
	}
	if totalSize > maxCommandInputSize {
		return resolvedCommand{}, fmt.Errorf("command and arguments must be at most %d bytes in total", maxCommandInputSize)
	}

	workingDirectory, err := resolveCommandWorkingDirectory(access, args.WorkingDirectory)
	if err != nil {
		return resolvedCommand{}, err
	}
	executable, invocationExecutable, err := resolveCommandExecutable(command, workingDirectory)
	if err != nil {
		return resolvedCommand{}, err
	}
	timeout := time.Duration(args.TimeoutSeconds) * time.Second
	if args.TimeoutSeconds == 0 {
		timeout = defaultCommandTimeout
	}
	if timeout <= 0 || timeout > maxCommandTimeout {
		return resolvedCommand{}, fmt.Errorf("timeoutSeconds must be between 1 and %d", int(maxCommandTimeout/time.Second))
	}
	return resolvedCommand{
		command:              command,
		args:                 cloneStrings(args.Args),
		executable:           executable,
		invocationExecutable: invocationExecutable,
		workingDirectory:     workingDirectory,
		timeout:              timeout,
	}, nil
}

func resolveCommandWorkingDirectory(access filesystemAccess, rawPath string) (string, error) {
	resolved, err := access.Resolve(rawPath)
	if err != nil {
		return "", err
	}
	realBoundary, err := filepath.EvalSymlinks(access.boundaryRoot)
	if err != nil {
		return "", fmt.Errorf("resolve command boundary: %w", err)
	}
	realDirectory, err := filepath.EvalSymlinks(resolved.Absolute)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", resolved.Display, err)
	}
	relative, err := filepath.Rel(realBoundary, realDirectory)
	if err != nil || pathEscapes(relative) {
		return "", fmt.Errorf("%w: %q", errPathOutsideScope, resolved.Display)
	}
	info, err := os.Stat(realDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect working directory %q: %w", resolved.Display, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", resolved.Display)
	}
	return filepath.Clean(realDirectory), nil
}

func resolveCommandExecutable(command, workingDirectory string) (string, string, error) {
	invocationExecutable := command
	if !filepath.IsAbs(command) {
		if filepath.Dir(command) != "." {
			invocationExecutable = filepath.Join(workingDirectory, command)
		} else {
			resolved, err := exec.LookPath(command)
			if err != nil {
				return "", "", fmt.Errorf("find executable %q: %w", command, err)
			}
			invocationExecutable = resolved
		}
	}
	absolute, err := filepath.Abs(invocationExecutable)
	if err != nil {
		return "", "", fmt.Errorf("resolve executable %q: %w", command, err)
	}
	invocationExecutable = filepath.Clean(absolute)
	canonicalExecutable, err := validateCommandExecutable(invocationExecutable)
	if err != nil {
		return "", "", err
	}
	return canonicalExecutable, invocationExecutable, nil
}

func validateCommandExecutable(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve executable %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect executable %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable %q is not a regular file", path)
	}
	return resolved, nil
}

func executeCommand(ctx context.Context, command resolvedCommand, sink CommandOutputSink) (RunCommandResult, error) {
	commandContext, cancel := context.WithTimeout(ctx, command.timeout)
	defer cancel()

	result := commandResult(command, "completed")
	started := time.Now()
	sink = synchronizedCommandOutputSink(sink)
	stdout := newBoundedCommandOutput(commandOutputCallback(ctx, sink, "stdout"))
	stderr := newBoundedCommandOutput(commandOutputCallback(ctx, sink, "stderr"))
	process := exec.CommandContext(commandContext, command.invocationExecutable, command.args...)
	// Preserve invocation-name semantics for symlink-dispatched programs and
	// the invocation path passed to shebang interpreters.
	process.Args[0] = command.command
	process.Dir = command.workingDirectory
	process.Env = os.Environ()
	process.Stdout = stdout
	process.Stderr = stderr
	process.WaitDelay = time.Second
	configureCommandProcess(process)

	err := process.Run()
	cleanupCommandProcess(process)
	result.DurationMS = time.Since(started).Milliseconds()
	result.Stdout, result.StdoutTruncated, result.StdoutBytesOmitted = stdout.Result()
	result.Stderr, result.StderrTruncated, result.StderrBytesOmitted = stderr.Result()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		result.State = "timed_out"
		result.TimedOut = true
		result.Error = fmt.Sprintf("command exceeded its %d second timeout", result.TimeoutSeconds)
		if exitCode, ok := commandExitCode(err); ok {
			result.ExitCode = &exitCode
		}
		return result, nil
	}
	if err == nil {
		exitCode := 0
		result.ExitCode = &exitCode
		return result, nil
	}
	if exitCode, ok := commandExitCode(err); ok {
		result.ExitCode = &exitCode
		return result, nil
	}
	result.State = "failed"
	result.Error = err.Error()
	return result, nil
}

func synchronizedCommandOutputSink(sink CommandOutputSink) CommandOutputSink {
	if sink == nil {
		return nil
	}
	var mu sync.Mutex
	return func(event CommandOutputEvent) {
		mu.Lock()
		defer mu.Unlock()
		sink(event)
	}
}

func commandOutputCallback(ctx context.Context, sink CommandOutputSink, stream string) func(string) {
	if sink == nil {
		return nil
	}
	toolContext, ok := ctx.(agent.Context)
	if !ok {
		return nil
	}
	toolCallID := toolContext.FunctionCallID()
	if toolCallID == "" {
		return nil
	}
	return func(output string) {
		sink(CommandOutputEvent{ToolCallID: toolCallID, Stream: stream, Text: output})
	}
}

func commandExitCode(err error) (int, bool) {
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 0, false
	}
	return exitError.ExitCode(), true
}

func commandResult(command resolvedCommand, state string) RunCommandResult {
	return RunCommandResult{
		State:            state,
		Command:          command.command,
		Args:             cloneStrings(command.args),
		WorkingDirectory: command.workingDirectory,
		TimeoutSeconds:   int(command.timeout / time.Second),
	}
}

func cloneStrings(values []string) []string {
	return append([]string{}, values...)
}

func sameCommand(left, right resolvedCommand) bool {
	return left.command == right.command &&
		left.executable == right.executable &&
		left.invocationExecutable == right.invocationExecutable &&
		left.workingDirectory == right.workingDirectory &&
		left.timeout == right.timeout &&
		slices.Equal(left.args, right.args)
}

func decodeRunCommandArgs(input map[string]any) (RunCommandArgs, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return RunCommandArgs{}, fmt.Errorf("encode run_command input: %w", err)
	}
	var args RunCommandArgs
	if err := json.Unmarshal(encoded, &args); err != nil {
		return RunCommandArgs{}, fmt.Errorf("decode run_command input: %w", err)
	}
	return args, nil
}

func commandResultMap(result RunCommandResult) (map[string]any, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode run_command result: %w", err)
	}
	var mapped map[string]any
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil, fmt.Errorf("decode run_command result: %w", err)
	}
	return mapped, nil
}

type boundedCommandOutput struct {
	mu       sync.Mutex
	captured []byte
	head     []byte
	tail     []byte
	total    int64
	live     int
	onOutput func(string)
}

func newBoundedCommandOutput(onOutput func(string)) *boundedCommandOutput {
	return &boundedCommandOutput{onOutput: onOutput}
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()

	written := len(data)
	output.total += int64(written)
	if output.head == nil && len(output.captured)+written <= maxCommandOutputBytes {
		output.captured = append(output.captured, data...)
	} else if output.head == nil {
		half := maxCommandOutputBytes / 2
		output.head = make([]byte, 0, half)
		if len(output.captured) >= half {
			output.head = append(output.head, output.captured[:half]...)
		} else {
			output.head = append(output.head, output.captured...)
			output.head = append(output.head, data[:half-len(output.head)]...)
		}
		if written >= half {
			output.tail = append([]byte{}, data[written-half:]...)
		} else {
			fromCaptured := half - written
			output.tail = append([]byte{}, output.captured[len(output.captured)-fromCaptured:]...)
			output.tail = append(output.tail, data...)
		}
		output.captured = nil
	} else {
		half := maxCommandOutputBytes / 2
		if written >= half {
			output.tail = append(output.tail[:0], data[written-half:]...)
		} else {
			if overflow := len(output.tail) + written - half; overflow > 0 {
				copy(output.tail, output.tail[overflow:])
				output.tail = output.tail[:len(output.tail)-overflow]
			}
			output.tail = append(output.tail, data...)
		}
	}

	if output.onOutput != nil && output.live < maxLiveOutputBytes {
		keep := min(maxLiveOutputBytes-output.live, written)
		if keep > 0 {
			output.onOutput(cleanCommandOutput(data[:keep]))
			output.live += keep
		}
	}
	return written, nil
}

func (output *boundedCommandOutput) Result() (string, bool, int64) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.head == nil {
		return cleanCommandOutput(output.captured), false, 0
	}
	omitted := output.total - int64(len(output.head)+len(output.tail))
	marker := fmt.Sprintf("\n... %d bytes omitted ...\n", omitted)
	return cleanCommandOutput(output.head) + marker + cleanCommandOutput(output.tail), true, omitted
}

func cleanCommandOutput(data []byte) string {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}
