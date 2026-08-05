package workspacetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/tool/toolconfirmation"

	"materialmind/internal/toolpolicy"
)

func TestRunCommandRequestsApprovalWithResolvedRepositoryDirectory(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(repository, "services", "api")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeRepository)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{}
	result, err := runCommandWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolRunCommand,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeRepository,
	}, ctx, RunCommandArgs{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestRunCommandHelper", "--", "output", "literal value"},
		WorkingDirectory: "../..",
	}, nil)
	if err != nil {
		t.Fatalf("runCommandWithPolicy() error = %v", err)
	}
	if result.State != "approval_required" || result.WorkingDirectory != repository {
		t.Fatalf("runCommandWithPolicy() = %#v", result)
	}
	payload, ok := ctx.payload.(commandConfirmationPayload)
	if !ok {
		t.Fatalf("approval payload = %T, want commandConfirmationPayload", ctx.payload)
	}
	if payload.WorkingDirectory != repository || payload.Command != os.Args[0] || payload.TimeoutSeconds != 120 {
		t.Fatalf("approval payload = %#v", payload)
	}
	if !ctx.actions.SkipSummarization {
		t.Fatal("run_command did not skip summarization while awaiting approval")
	}
}

func TestRunCommandWorkspaceScopeRejectsParentDirectoryBeforeApproval(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{}
	_, err = runCommandWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolRunCommand,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ctx, RunCommandArgs{Command: os.Args[0], WorkingDirectory: ".."}, nil)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("runCommandWithPolicy() error = %v, want outside scope", err)
	}
	if ctx.payload != nil {
		t.Fatalf("out-of-scope command requested approval: %#v", ctx.payload)
	}
}

func TestRunCommandReturnsDenialReasonWithoutExecuting(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: false,
		Payload:   map[string]any{"reason": "Do not run project scripts."},
	}}
	result, err := runCommandWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolRunCommand,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ctx, RunCommandArgs{Command: os.Args[0], Args: []string{"-test.run=TestRunCommandHelper", "--", "output", "must-not-run"}}, nil)
	if err != nil {
		t.Fatalf("runCommandWithPolicy() error = %v", err)
	}
	if result.State != "denied" || result.Reason != "Do not run project scripts." || result.ExitCode != nil {
		t.Fatalf("runCommandWithPolicy() = %#v", result)
	}
}

func TestRunCommandExistingADKConfirmationTakesPrecedenceOverHandler(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: false,
		Payload:   map[string]any{"reason": "denied by ADK"},
	}}
	handlerCalled := false
	result, err := runCommandWithApprovalPolicy(
		access,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		ctx,
		RunCommandArgs{Command: os.Args[0]},
		nil,
		func(context.Context, ToolApprovalRequest) (ToolApprovalDecision, error) {
			handlerCalled = true
			return ToolApprovalDecision{Approved: true}, nil
		},
	)
	if err != nil {
		t.Fatalf("runCommandWithApprovalPolicy() error = %v", err)
	}
	if result.State != "denied" || result.Reason != "denied by ADK" {
		t.Fatalf("runCommandWithApprovalPolicy() = %#v", result)
	}
	if handlerCalled {
		t.Fatal("custom approval handler ran despite an existing ADK confirmation")
	}
}

func TestRunCommandApprovedADKConfirmationTakesPrecedenceOverHandler(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	args := RunCommandArgs{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestRunCommandHelper$", "--", "output", "native-approval"},
	}
	resolved, err := resolveCommand(access, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload:   commandApprovalPayload(resolved),
	}}
	handlerCalled := false
	result, err := runCommandWithApprovalPolicy(
		access,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		ctx,
		args,
		nil,
		func(context.Context, ToolApprovalRequest) (ToolApprovalDecision, error) {
			handlerCalled = true
			return ToolApprovalDecision{Approved: false}, nil
		},
	)
	if err != nil {
		t.Fatalf("runCommandWithApprovalPolicy() error = %v", err)
	}
	if result.State != "completed" || result.Stdout != "stdout:native-approval\n" {
		t.Fatalf("runCommandWithApprovalPolicy() = %#v", result)
	}
	if handlerCalled {
		t.Fatal("custom approval handler ran despite an approved ADK confirmation")
	}
}

func TestRunCommandUsesPerCallApprovalHandler(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{functionCallID: "command-1"}
	args := RunCommandArgs{
		Command:          os.Args[0],
		Args:             []string{"-test.run=TestRunCommandHelper", "--", "output", "must-not-run"},
		WorkingDirectory: ".",
		TimeoutSeconds:   7,
	}
	var request ToolApprovalRequest
	result, err := runCommandWithApprovalPolicy(
		access,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		ctx,
		args,
		nil,
		func(_ context.Context, current ToolApprovalRequest) (ToolApprovalDecision, error) {
			request = current
			return ToolApprovalDecision{Approved: false, Reason: "not now"}, nil
		},
	)
	if err != nil {
		t.Fatalf("runCommandWithApprovalPolicy() error = %v", err)
	}
	if result.State != "denied" || result.Reason != "not now" || result.ExitCode != nil {
		t.Fatalf("runCommandWithApprovalPolicy() = %#v", result)
	}
	if ctx.payload != nil {
		t.Fatalf("custom approval also requested ADK confirmation: %#v", ctx.payload)
	}
	if request.ToolCallID != "command-1" || request.ToolName != toolpolicy.ToolRunCommand {
		t.Fatalf("approval request = %#v", request)
	}
	if request.Input["workingDirectory"] != "." || request.Input["timeoutSeconds"] != 7 {
		t.Fatalf("approval input = %#v", request.Input)
	}
	if request.Payload["kind"] != toolpolicy.ToolRunCommand ||
		request.Payload["workingDirectory"] != workspace ||
		request.Payload["timeoutSeconds"] != 7 {
		t.Fatalf("approval payload = %#v", request.Payload)
	}
	payloadArgs, ok := request.Payload["args"].([]string)
	if !ok || !slices.Equal(payloadArgs, args.Args) {
		t.Fatalf("approval payload args = %#v", request.Payload["args"])
	}
}

func TestRunCommandRevalidatesResolvedPATHTargetAfterPerCallApproval(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("PATH executable lookup behavior is Unix-specific")
	}
	workspace := t.TempDir()
	original := filepath.Join(workspace, "original-command")
	replacement := filepath.Join(workspace, "replacement-command")
	commandLink := filepath.Join(workspace, "command")
	if err := os.WriteFile(original, []byte("original"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, commandLink); err != nil {
		t.Skipf("create executable symlink: %v", err)
	}
	t.Setenv("PATH", workspace)
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{functionCallID: "command-1"}
	_, err = runCommandWithApprovalPolicy(
		access,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		ctx,
		RunCommandArgs{Command: "command"},
		nil,
		func(context.Context, ToolApprovalRequest) (ToolApprovalDecision, error) {
			if removeErr := os.Remove(commandLink); removeErr != nil {
				return ToolApprovalDecision{}, removeErr
			}
			if linkErr := os.Symlink(replacement, commandLink); linkErr != nil {
				return ToolApprovalDecision{}, linkErr
			}
			return ToolApprovalDecision{Approved: true}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("runCommandWithApprovalPolicy() error = %v, want approval mismatch", err)
	}
}

func TestRunCommandRevalidatesWorkingDirectorySymlinkAfterPerCallApproval(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("directory symlink behavior is Unix-specific")
	}
	workspace := t.TempDir()
	originalDirectory := filepath.Join(workspace, "original")
	replacementDirectory := filepath.Join(workspace, "replacement")
	for _, directory := range []string{originalDirectory, replacementDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workingDirectoryLink := filepath.Join(workspace, "current")
	if err := os.Symlink(originalDirectory, workingDirectoryLink); err != nil {
		t.Skipf("create working-directory symlink: %v", err)
	}
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{functionCallID: "command-1"}
	_, err = runCommandWithApprovalPolicy(
		access,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		ctx,
		RunCommandArgs{Command: os.Args[0], WorkingDirectory: "current"},
		nil,
		func(context.Context, ToolApprovalRequest) (ToolApprovalDecision, error) {
			if removeErr := os.Remove(workingDirectoryLink); removeErr != nil {
				return ToolApprovalDecision{}, removeErr
			}
			if linkErr := os.Symlink(replacementDirectory, workingDirectoryLink); linkErr != nil {
				return ToolApprovalDecision{}, linkErr
			}
			return ToolApprovalDecision{Approved: true}, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("runCommandWithApprovalPolicy() error = %v, want approval mismatch", err)
	}
}

func TestSameCommandComparesCompleteResolvedCommand(t *testing.T) {
	original := resolvedCommand{
		command:              "command",
		args:                 []string{"one", "two"},
		executable:           "/canonical/command",
		invocationExecutable: "/path/command",
		workingDirectory:     "/workspace",
		timeout:              7 * time.Second,
	}
	changes := map[string]func(*resolvedCommand){
		"command":               func(command *resolvedCommand) { command.command = "other" },
		"arguments":             func(command *resolvedCommand) { command.args[1] = "changed" },
		"executable":            func(command *resolvedCommand) { command.executable = "/canonical/other" },
		"invocation executable": func(command *resolvedCommand) { command.invocationExecutable = "/other/command" },
		"working directory":     func(command *resolvedCommand) { command.workingDirectory = "/other" },
		"timeout":               func(command *resolvedCommand) { command.timeout++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := original
			changed.args = cloneStrings(original.args)
			change(&changed)
			if sameCommand(original, changed) {
				t.Fatalf("sameCommand() = true after changing %s", name)
			}
		})
	}
}

func TestRunCommandRejectsChangedInputAfterApproval(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	approvedArgs := RunCommandArgs{Command: os.Args[0], Args: []string{"approved"}}
	approved, err := resolveCommand(access, approvedArgs)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{confirmation: &toolconfirmation.ToolConfirmation{
		Confirmed: true,
		Payload: commandConfirmationPayload{
			Kind:             toolpolicy.ToolRunCommand,
			Command:          approved.command,
			Args:             approved.args,
			Executable:       approved.executable,
			WorkingDirectory: approved.workingDirectory,
			TimeoutSeconds:   int(approved.timeout / time.Second),
		},
	}}
	_, err = runCommandWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolRunCommand,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ctx, RunCommandArgs{Command: os.Args[0], Args: []string{"changed"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("runCommandWithPolicy() error = %v, want approval mismatch", err)
	}
}

func TestRunCommandExecutesApprovedArgumentsAndStreamsOutput(t *testing.T) {
	workspace := t.TempDir()
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	args := RunCommandArgs{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunCommandHelper", "--", "output", "literal;$(not-a-shell)"},
	}
	resolved, err := resolveCommand(access, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &fetchTestContext{
		functionCallID: "command-1",
		confirmation: &toolconfirmation.ToolConfirmation{
			Confirmed: true,
			Payload: commandConfirmationPayload{
				Kind:             toolpolicy.ToolRunCommand,
				Command:          resolved.command,
				Args:             resolved.args,
				Executable:       resolved.executable,
				WorkingDirectory: resolved.workingDirectory,
				TimeoutSeconds:   int(resolved.timeout / time.Second),
			},
		},
	}
	events := make([]CommandOutputEvent, 0)
	result, err := runCommandWithPolicy(access, toolpolicy.Permission{
		ToolName:         toolpolicy.ToolRunCommand,
		ConfirmationMode: toolpolicy.ConfirmationAsk,
		FilesystemScope:  toolpolicy.ScopeWorkspace,
	}, ctx, args, func(event CommandOutputEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("runCommandWithPolicy() error = %v", err)
	}
	if result.State != "completed" || result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("runCommandWithPolicy() = %#v", result)
	}
	if result.Stdout != "stdout:literal;$(not-a-shell)\n" || result.Stderr != "stderr:literal;$(not-a-shell)\n" {
		t.Fatalf("command output = stdout %q, stderr %q", result.Stdout, result.Stderr)
	}
	if len(events) != 2 {
		t.Fatalf("command output events = %#v", events)
	}
	streams := []string{events[0].Stream, events[1].Stream}
	slices.Sort(streams)
	if events[0].ToolCallID != "command-1" || events[1].ToolCallID != "command-1" || !slices.Equal(streams, []string{"stderr", "stdout"}) {
		t.Fatalf("command output events = %#v", events)
	}
}

func TestRunCommandPreservesPATHSymlinkForScriptInvocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix script execution and symlinks")
	}
	workspace := t.TempDir()
	binDirectory := filepath.Join(workspace, "bin")
	if err := os.Mkdir(binDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(binDirectory, "script-target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf '%s' \"$0\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation := filepath.Join(binDirectory, "approved-script")
	if err := os.Symlink(target, invocation); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	access, err := newFilesystemAccess(workspace, toolpolicy.ScopeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	var request ToolApprovalRequest
	result, err := runCommandWithApprovalPolicy(
		access,
		toolpolicy.Permission{
			ToolName:         toolpolicy.ToolRunCommand,
			ConfirmationMode: toolpolicy.ConfirmationAsk,
			FilesystemScope:  toolpolicy.ScopeWorkspace,
		},
		&fetchTestContext{functionCallID: "script-call"},
		RunCommandArgs{Command: "approved-script"},
		nil,
		func(_ context.Context, approval ToolApprovalRequest) (ToolApprovalDecision, error) {
			request = approval
			return ToolApprovalDecision{Approved: true}, nil
		},
	)
	if err != nil {
		t.Fatalf("runCommandWithApprovalPolicy() error = %v", err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if request.Payload["executable"] != canonicalTarget || request.Payload["invocationExecutable"] != invocation {
		t.Fatalf("approval payload = %#v", request.Payload)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != invocation {
		t.Fatalf("runCommandWithApprovalPolicy() = %#v", result)
	}
}

func TestExecuteCommandPreservesApprovedInvocationName(t *testing.T) {
	result, err := executeCommand(context.Background(), resolvedCommand{
		command:              "approved-invocation-name",
		args:                 []string{"-test.run=^TestRunCommandHelper$", "--", "argv0"},
		executable:           os.Args[0],
		invocationExecutable: os.Args[0],
		workingDirectory:     t.TempDir(),
		timeout:              defaultCommandTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("executeCommand() error = %v", err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "approved-invocation-name" {
		t.Fatalf("executeCommand() = %#v", result)
	}
}

func TestExecuteCommandTimesOut(t *testing.T) {
	result, err := executeCommand(context.Background(), resolvedCommand{
		command:              os.Args[0],
		args:                 []string{"-test.run=TestRunCommandHelper", "--", "sleep"},
		executable:           os.Args[0],
		invocationExecutable: os.Args[0],
		workingDirectory:     t.TempDir(),
		timeout:              50 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("executeCommand() error = %v", err)
	}
	if result.State != "timed_out" || !result.TimedOut {
		t.Fatalf("executeCommand() = %#v", result)
	}
}

func TestBoundedCommandOutputKeepsBeginningAndEnd(t *testing.T) {
	output := newBoundedCommandOutput(nil)
	data := strings.Repeat("a", maxCommandOutputBytes/2) + strings.Repeat("x", 128) + strings.Repeat("z", maxCommandOutputBytes/2)
	if _, err := output.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	result, truncated, omitted := output.Result()
	if !truncated || omitted != 128 {
		t.Fatalf("Result() truncated = %v, omitted = %d", truncated, omitted)
	}
	if !strings.HasPrefix(result, strings.Repeat("a", 256)) || !strings.HasSuffix(result, strings.Repeat("z", 256)) {
		t.Fatal("Result() did not retain the beginning and end")
	}
}

func TestExecuteCommandInheritsBackendEnvironment(t *testing.T) {
	t.Setenv("MATERIALMIND_TEST_ENVIRONMENT", "inherited value")
	result, err := executeCommand(context.Background(), resolvedCommand{
		command:              os.Args[0],
		args:                 []string{"-test.run=TestRunCommandHelper", "--", "environment", "MATERIALMIND_TEST_ENVIRONMENT"},
		executable:           os.Args[0],
		invocationExecutable: os.Args[0],
		workingDirectory:     t.TempDir(),
		timeout:              defaultCommandTimeout,
	}, nil)
	if err != nil {
		t.Fatalf("executeCommand() error = %v", err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "inherited value" {
		t.Fatalf("executeCommand() = %#v", result)
	}
}

func TestRunCommandHelper(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "output":
		value := ""
		if separator+2 < len(os.Args) {
			value = os.Args[separator+2]
		}
		_, _ = fmt.Fprintf(os.Stdout, "stdout:%s\n", value)
		_, _ = fmt.Fprintf(os.Stderr, "stderr:%s\n", value)
		os.Exit(7)
	case "wait-file":
		if separator+3 >= len(os.Args) {
			fmt.Fprintln(os.Stderr, "wait-file helper requires a name and release file")
			os.Exit(2)
		}
		name := os.Args[separator+2]
		releaseFile := os.Args[separator+3]
		_, _ = fmt.Fprintf(os.Stdout, "started:%s\n", name)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(releaseFile); err == nil {
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "timed out waiting for release file")
				os.Exit(2)
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = fmt.Fprintf(os.Stdout, "finished:%s\n", name)
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "argv0":
		_, _ = fmt.Fprint(os.Stdout, os.Args[0])
		os.Exit(0)
	case "environment":
		if separator+2 < len(os.Args) {
			_, _ = fmt.Fprint(os.Stdout, os.Getenv(os.Args[separator+2]))
		}
		os.Exit(0)
	}
}
