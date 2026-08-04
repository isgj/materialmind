package workspacetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func TestExecuteCommandTimesOut(t *testing.T) {
	result, err := executeCommand(context.Background(), resolvedCommand{
		command:          os.Args[0],
		args:             []string{"-test.run=TestRunCommandHelper", "--", "sleep"},
		executable:       os.Args[0],
		workingDirectory: t.TempDir(),
		timeout:          50 * time.Millisecond,
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
		command:          os.Args[0],
		args:             []string{"-test.run=TestRunCommandHelper", "--", "environment", "MATERIALMIND_TEST_ENVIRONMENT"},
		executable:       os.Args[0],
		workingDirectory: t.TempDir(),
		timeout:          defaultCommandTimeout,
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
	case "sleep":
		time.Sleep(5 * time.Second)
	case "environment":
		if separator+2 < len(os.Args) {
			_, _ = fmt.Fprint(os.Stdout, os.Getenv(os.Args[separator+2]))
		}
		os.Exit(0)
	}
}
