package acpruntime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

const (
	acpTerminalHelperEnvironment = "MATERIALMIND_ACP_TERMINAL_HELPER"
	acpTerminalInheritedEnv      = "MATERIALMIND_ACP_TERMINAL_INHERITED"
	acpTerminalOverrideEnv       = "MATERIALMIND_ACP_TERMINAL_OVERRIDE"
)

func TestClientTerminalStreamsAndRetainsOutput(t *testing.T) {
	t.Setenv(acpTerminalInheritedEnv, "inherited")
	t.Setenv(acpTerminalOverrideEnv, "parent")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handler := newRecordingTerminalHandler()
	client := newClient()
	sessionID := acp.SessionId("terminal-session")
	client.register(ctx, sessionID, t.TempDir(), handler)
	t.Cleanup(client.closeTerminals)

	response, err := client.CreateTerminal(ctx, acp.CreateTerminalRequest{
		SessionId: sessionID,
		Command:   executable,
		Args:      []string{"-test.run=^TestACPTerminalHelperProcess$"},
		Env: []acp.EnvVariable{
			{Name: acpTerminalHelperEnvironment, Value: "stream"},
			{Name: acpTerminalOverrideEnv, Value: "override"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTerminal() error = %v", err)
	}

	select {
	case event := <-handler.events:
		if event.TerminalID != response.TerminalId ||
			event.Stream != "stdout" ||
			event.Text != "inherited:override\n" {
			t.Fatalf("first terminal event = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("terminal did not stream its first output")
	}

	exit, err := client.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{
		SessionId:  sessionID,
		TerminalId: response.TerminalId,
	})
	if err != nil {
		t.Fatalf("WaitForTerminalExit() error = %v", err)
	}
	if exit.ExitCode == nil || *exit.ExitCode != 7 {
		t.Fatalf("WaitForTerminalExit() = %#v, want exit code 7", exit)
	}
	output, err := client.TerminalOutput(ctx, acp.TerminalOutputRequest{
		SessionId:  sessionID,
		TerminalId: response.TerminalId,
	})
	if err != nil {
		t.Fatalf("TerminalOutput() error = %v", err)
	}
	if output.Output != "inherited:override\nstderr\n" ||
		output.Truncated ||
		output.ExitStatus == nil ||
		output.ExitStatus.ExitCode == nil ||
		*output.ExitStatus.ExitCode != 7 {
		t.Fatalf("TerminalOutput() = %#v", output)
	}
	if events := handler.recordedEvents(); len(events) != 2 ||
		events[1].Stream != "stderr" ||
		events[1].Text != "stderr\n" {
		t.Fatalf("terminal events = %#v", events)
	}

	if _, err := client.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{
		SessionId:  sessionID,
		TerminalId: response.TerminalId,
	}); err != nil {
		t.Fatalf("ReleaseTerminal() error = %v", err)
	}
	if _, err := client.TerminalOutput(ctx, acp.TerminalOutputRequest{
		SessionId:  sessionID,
		TerminalId: response.TerminalId,
	}); err == nil {
		t.Fatal("TerminalOutput() after release succeeded, want an error")
	}
}

func TestClientTerminalTruncatesFromBeginning(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := newClient()
	sessionID := acp.SessionId("truncated-terminal-session")
	client.register(ctx, sessionID, t.TempDir(), newRecordingTerminalHandler())
	t.Cleanup(client.closeTerminals)
	limit := 12

	response, err := client.CreateTerminal(ctx, acp.CreateTerminalRequest{
		SessionId:       sessionID,
		Command:         executable,
		Args:            []string{"-test.run=^TestACPTerminalHelperProcess$"},
		Env:             []acp.EnvVariable{{Name: acpTerminalHelperEnvironment, Value: "truncate"}},
		OutputByteLimit: &limit,
	})
	if err != nil {
		t.Fatalf("CreateTerminal() error = %v", err)
	}
	if _, err := client.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{
		SessionId:  sessionID,
		TerminalId: response.TerminalId,
	}); err != nil {
		t.Fatalf("WaitForTerminalExit() error = %v", err)
	}
	output, err := client.TerminalOutput(ctx, acp.TerminalOutputRequest{
		SessionId:  sessionID,
		TerminalId: response.TerminalId,
	})
	if err != nil {
		t.Fatalf("TerminalOutput() error = %v", err)
	}
	if !output.Truncated || len(output.Output) > limit || !strings.HasSuffix(output.Output, "suffix\n") {
		t.Fatalf("TerminalOutput() = %#v", output)
	}
}

func TestACPTerminalHelperProcess(t *testing.T) {
	switch os.Getenv(acpTerminalHelperEnvironment) {
	case "stream":
		fmt.Printf(
			"%s:%s\n",
			os.Getenv(acpTerminalInheritedEnv),
			os.Getenv(acpTerminalOverrideEnv),
		)
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "stderr")
		os.Exit(7)
	case "truncate":
		fmt.Print("prefix-" + strings.Repeat("x", 64) + "-suffix\n")
		os.Exit(0)
	}
}

type recordingTerminalHandler struct {
	mu     sync.Mutex
	events chan TerminalOutputEvent
	all    []TerminalOutputEvent
}

func newRecordingTerminalHandler() *recordingTerminalHandler {
	return &recordingTerminalHandler{events: make(chan TerminalOutputEvent, 16)}
}

func (*recordingTerminalHandler) SessionUpdate(
	context.Context,
	acp.SessionNotification,
) error {
	return nil
}

func (*recordingTerminalHandler) RequestPermission(
	context.Context,
	acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeCancelled(),
	}, nil
}

func (handler *recordingTerminalHandler) TerminalOutput(event TerminalOutputEvent) {
	handler.mu.Lock()
	handler.all = append(handler.all, event)
	handler.mu.Unlock()
	handler.events <- event
}

func (handler *recordingTerminalHandler) recordedEvents() []TerminalOutputEvent {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]TerminalOutputEvent{}, handler.all...)
}
