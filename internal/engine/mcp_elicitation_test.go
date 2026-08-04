package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"materialmind/internal/mcpruntime"
)

func TestMCPElicitationWaitsForIndependentResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runEngine, runRecord := newUserInputTestEngine(t, ctx)
	events, ok := runEngine.Hub().Subscribe(ctx, runRecord.ID, 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}

	result := make(chan mcpruntime.ElicitationResolution, 1)
	errorsResult := make(chan error, 1)
	go func() {
		resolution, err := runEngine.requestMCPElicitation(ctx, mcpruntime.ElicitationRequest{
			ID:         "elicitation-1",
			SessionID:  runRecord.SessionID,
			ToolCallID: "tool-1",
			ServerID:   "server-1",
			ServerName: "Issue tracker",
			Mode:       "form",
			Message:    "Choose a project",
			RequestedSchema: map[string]any{
				"type": "object",
			},
		})
		result <- resolution
		errorsResult <- err
	}()

	event := <-events
	if event.Type != "mcp_elicitation_request" {
		t.Fatalf("event.Type = %q", event.Type)
	}
	if !runEngine.WaitingForUser(runRecord.SessionID) {
		t.Fatal("WaitingForUser() = false while elicitation is pending")
	}
	resolution, err := runEngine.ResolveMCPElicitation(
		ctx,
		runRecord.ID,
		"elicitation-1",
		mcpruntime.ElicitationActionAccept,
		map[string]any{"project": "MM"},
	)
	if err != nil {
		t.Fatalf("ResolveMCPElicitation() error = %v", err)
	}
	if resolution.ToolCallID != "tool-1" || resolution.Content["project"] != "MM" {
		t.Fatalf("ResolveMCPElicitation() = %#v", resolution)
	}
	if err := <-errorsResult; err != nil {
		t.Fatalf("requestMCPElicitation() error = %v", err)
	}
	if resolved := <-result; resolved.Action != mcpruntime.ElicitationActionAccept {
		t.Fatalf("requestMCPElicitation() = %#v", resolved)
	}
	if runEngine.WaitingForUser(runRecord.SessionID) {
		t.Fatal("WaitingForUser() = true after resolution")
	}
	if _, err := runEngine.ResolveMCPElicitation(
		ctx,
		runRecord.ID,
		"elicitation-1",
		mcpruntime.ElicitationActionCancel,
		nil,
	); !errors.Is(err, ErrMCPElicitationNotPending) {
		t.Fatalf("duplicate resolution error = %v", err)
	}
}
