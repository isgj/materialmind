package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"materialmind/internal/store"
	"materialmind/internal/workspacetools"
)

func TestToolApprovalRequestsExtractsOriginalFetch(t *testing.T) {
	event := confirmationRequestEvent()
	requests, err := toolApprovalRequests(event)
	if err != nil {
		t.Fatalf("toolApprovalRequests() error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("len(toolApprovalRequests()) = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.ID != "approval-1" || request.ToolCallID != "fetch-1" || request.ToolName != "fetch_url" {
		t.Fatalf("toolApprovalRequests()[0] = %#v", request)
	}
	if request.InvocationID != "invocation-1" {
		t.Fatalf("toolApprovalRequests()[0].InvocationID = %q", request.InvocationID)
	}
	if request.Payload["kind"] != "fetch_url" || request.Payload["url"] != "https://example.com/docs" || request.Hint != "Allow this fetch?" {
		t.Fatalf("toolApprovalRequests()[0] = %#v", request)
	}
}

func TestHasApprovalForInvocationScopesPendingRequests(t *testing.T) {
	requests := map[string]ToolApprovalRequest{
		"approval-1": {ID: "approval-1", InvocationID: "invocation-1"},
		"approval-2": {ID: "approval-2", InvocationID: "invocation-2"},
	}
	if !hasApprovalForInvocation(requests, "invocation-1") {
		t.Fatal("hasApprovalForInvocation() = false for matching request")
	}
	if hasApprovalForInvocation(requests, "invocation-3") {
		t.Fatal("hasApprovalForInvocation() = true for unrelated invocation")
	}
	if !hasApprovalForInvocation(requests, "") {
		t.Fatal("hasApprovalForInvocation() = false for legacy request scope")
	}
	if hasApprovalForInvocation(nil, "") {
		t.Fatal("hasApprovalForInvocation() = true for empty requests")
	}
}

func TestConfirmationContentIncludesDecisionAndReason(t *testing.T) {
	content := confirmationContent([]ToolApprovalResolution{{
		ID: "approval-1", ToolCallID: "fetch-1", Approved: false, Reason: "Not this domain",
		Payload: map[string]any{"kind": "fetch_url", "url": "https://example.com/docs"},
	}})
	if content.Role != genai.RoleUser || len(content.Parts) != 1 {
		t.Fatalf("confirmationContent() = %#v", content)
	}
	response := content.Parts[0].FunctionResponse
	if response == nil || response.ID != "approval-1" || response.Name != toolconfirmation.FunctionCallName {
		t.Fatalf("confirmation response = %#v", response)
	}
	if response.Response["confirmed"] != false {
		t.Fatalf("confirmation response = %#v", response.Response)
	}
	payload, ok := response.Response["payload"].(map[string]any)
	if !ok || payload["reason"] != "Not this domain" || payload["url"] != "https://example.com/docs" {
		t.Fatalf("confirmation payload = %#v", response.Response["payload"])
	}
}

func TestPublishToolApprovalStartedOnlyPublishesApprovedDecisions(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	engine := &Engine{hub: hub}

	engine.publishToolApprovalStarted("run-1", ToolApprovalResolution{
		ID: "approval-denied", ToolCallID: "tool-denied", Approved: false,
	})
	engine.publishToolApprovalStarted("run-1", ToolApprovalResolution{
		ID: "approval-approved", ToolCallID: "tool-approved", Approved: true,
	})
	hub.Complete("run-1")

	events, ok := hub.Subscribe(t.Context(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	event, ok := <-events
	if !ok || event.Type != "tool_approval_started" {
		t.Fatalf("event = %#v, want tool_approval_started", event)
	}
	started, ok := event.Data.(ToolApprovalStarted)
	if !ok || started.ID != "approval-approved" || started.ToolCallID != "tool-approved" {
		t.Fatalf("event data = %#v", event.Data)
	}
	if _, extra := <-events; extra {
		t.Fatal("denied approval published an execution-start event")
	}
}

func TestWaitForToolApprovalsPreservesRequestOrder(t *testing.T) {
	active := &activeRun{
		runID:            "run-1",
		pendingApprovals: make(map[string]*pendingToolApproval),
	}
	engine := &Engine{active: map[string]*activeRun{"session-1": active}}
	first := newPendingTestApproval()
	second := newPendingTestApproval()
	active.pendingApprovals["approval-1"] = first
	active.pendingApprovals["approval-2"] = second
	resolveTestApproval(engine, "session-1", "approval-2", ToolApprovalResolution{
		ID: "approval-2", Approved: true,
	})
	resolveTestApproval(engine, "session-1", "approval-1", ToolApprovalResolution{
		ID: "approval-1", Approved: false,
	})

	result, err := engine.waitForToolApprovals(context.Background(), "session-1", "run-1", []ToolApprovalRequest{
		{ID: "approval-1"}, {ID: "approval-2"},
	})
	if err != nil {
		t.Fatalf("waitForToolApprovals() error = %v", err)
	}
	if result[0].ID != "approval-1" || result[1].ID != "approval-2" {
		t.Fatalf("waitForToolApprovals() = %#v", result)
	}
	if len(active.pendingApprovals) != 0 {
		t.Fatalf("pending approvals = %#v, want empty", active.pendingApprovals)
	}
}

func TestWaitForNextToolApprovalUsesResolutionOrder(t *testing.T) {
	active := &activeRun{
		runID: "run-1",
		pendingApprovals: map[string]*pendingToolApproval{
			"approval-1": newPendingTestApproval(),
			"approval-2": newPendingTestApproval(),
			"approval-3": newPendingTestApproval(),
		},
	}
	engine := &Engine{active: map[string]*activeRun{"session-1": active}}
	resolveTestApproval(engine, "session-1", "approval-3", ToolApprovalResolution{
		ID: "approval-3", Approved: true,
	})
	resolveTestApproval(engine, "session-1", "approval-1", ToolApprovalResolution{
		ID: "approval-1", Approved: true,
	})
	requests := map[string]ToolApprovalRequest{
		"approval-1": {ID: "approval-1"},
		"approval-2": {ID: "approval-2"},
		"approval-3": {ID: "approval-3"},
	}

	first, err := engine.waitForNextToolApproval(
		t.Context(),
		"session-1",
		"run-1",
		requests,
	)
	if err != nil {
		t.Fatalf("waitForNextToolApproval() error = %v", err)
	}
	if first.ID != "approval-3" {
		t.Fatalf("first resolved approval = %q, want approval-3", first.ID)
	}
	delete(requests, first.ID)

	second, err := engine.waitForNextToolApproval(
		t.Context(),
		"session-1",
		"run-1",
		requests,
	)
	if err != nil {
		t.Fatalf("waitForNextToolApproval() second error = %v", err)
	}
	if second.ID != "approval-1" {
		t.Fatalf("second resolved approval = %q, want approval-1", second.ID)
	}
	if _, pending := active.pendingApprovals["approval-2"]; !pending {
		t.Fatal("unresolved approval was removed")
	}
}

func TestRequestWorkspaceToolApprovalResumesEachResolvedCallIndependently(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	engine := &Engine{
		hub: hub,
		active: map[string]*activeRun{
			"session-1": {
				runID:             "run-1",
				pendingApprovals:  make(map[string]*pendingToolApproval),
				pendingUserInputs: make(map[string]*pendingUserInput),
			},
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	events, ok := hub.Subscribe(ctx, "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}

	type approvalResult struct {
		toolCallID string
		decision   workspacetools.ToolApprovalDecision
		err        error
	}
	results := make(chan approvalResult, 2)
	for _, toolCallID := range []string{"command-1", "command-2"} {
		go func() {
			decision, err := engine.requestWorkspaceToolApproval(
				ctx,
				"session-1",
				"run-1",
				workspacetools.ToolApprovalRequest{
					ToolCallID: toolCallID,
					ToolName:   "run_command",
					Input:      map[string]any{"command": toolCallID},
					Payload:    map[string]any{"kind": "run_command"},
				},
			)
			results <- approvalResult{toolCallID: toolCallID, decision: decision, err: err}
		}()
	}

	approvalIDs := make(map[string]string, 2)
	for len(approvalIDs) < 2 {
		select {
		case event := <-events:
			if event.Type != "tool_approval" {
				continue
			}
			request, requestOK := event.Data.(ToolApprovalRequest)
			if !requestOK {
				t.Fatalf("tool approval event data = %T", event.Data)
			}
			approvalIDs[request.ToolCallID] = request.ID
		case <-ctx.Done():
			t.Fatalf("timed out waiting for tool approvals: %v", ctx.Err())
		}
	}

	resolveTestApproval(engine, "session-1", approvalIDs["command-2"], ToolApprovalResolution{
		ID:         approvalIDs["command-2"],
		ToolCallID: "command-2",
		Approved:   true,
	})
	select {
	case result := <-results:
		if result.err != nil || result.toolCallID != "command-2" || !result.decision.Approved {
			t.Fatalf("first resumed approval = %#v", result)
		}
	case <-ctx.Done():
		t.Fatalf("resolved command did not resume: %v", ctx.Err())
	}

	resolveTestApproval(engine, "session-1", approvalIDs["command-1"], ToolApprovalResolution{
		ID:         approvalIDs["command-1"],
		ToolCallID: "command-1",
		Approved:   false,
		Reason:     "not now",
	})
	select {
	case result := <-results:
		if result.err != nil || result.toolCallID != "command-1" || result.decision.Approved || result.decision.Reason != "not now" {
			t.Fatalf("second resumed approval = %#v", result)
		}
	case <-ctx.Done():
		t.Fatalf("denied command did not resume: %v", ctx.Err())
	}

	hub.Complete("run-1")
	started := make([]ToolApprovalStarted, 0, 1)
	for event := range events {
		if event.Type != "tool_approval_started" {
			continue
		}
		value, startedOK := event.Data.(ToolApprovalStarted)
		if !startedOK {
			t.Fatalf("tool approval started data = %T", event.Data)
		}
		started = append(started, value)
	}
	if len(started) != 1 || started[0].ToolCallID != "command-2" {
		t.Fatalf("tool approval started events = %#v", started)
	}
}

func TestToolApprovalLimitCancelsActiveRunAndWakesWaiters(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	runContext, cancelWithCause := context.WithCancelCause(t.Context())
	defer cancelWithCause(nil)
	active := &activeRun{
		runID:            "run-1",
		cancel:           func() { cancelWithCause(nil) },
		cancelWithCause:  cancelWithCause,
		pendingApprovals: make(map[string]*pendingToolApproval),
	}
	engine := &Engine{
		hub:    hub,
		active: map[string]*activeRun{"session-1": active},
	}
	events, ok := hub.Subscribe(t.Context(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	results := make(chan error, maxPendingToolApprovals)
	for index := range maxPendingToolApprovals {
		go func() {
			_, err := engine.requestWorkspaceToolApproval(
				runContext,
				"session-1",
				"run-1",
				workspacetools.ToolApprovalRequest{
					ToolCallID: fmt.Sprintf("command-%d", index),
					ToolName:   "run_command",
				},
			)
			results <- err
		}()
	}
	for requested := 0; requested < maxPendingToolApprovals; {
		select {
		case event := <-events:
			if event.Type == "tool_approval" {
				requested++
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out after %d pending approval requests", requested)
		}
	}

	err := engine.registerToolApproval("session-1", "run-1", ToolApprovalRequest{
		ID:         "approval-over-limit",
		ToolCallID: "command-over-limit",
		ToolName:   "run_command",
	})
	if !errors.Is(err, errTooManyPendingToolApprovals) {
		t.Fatalf("registerToolApproval() error = %v, want limit error", err)
	}
	select {
	case <-runContext.Done():
	case <-time.After(time.Second):
		t.Fatal("approval admission failure did not cancel the active run")
	}
	if !errors.Is(context.Cause(runContext), errTooManyPendingToolApprovals) {
		t.Fatalf("cancellation cause = %v, want limit error", context.Cause(runContext))
	}
	for range maxPendingToolApprovals {
		select {
		case waiterErr := <-results:
			if !errors.Is(waiterErr, errTooManyPendingToolApprovals) {
				t.Fatalf("pending approval error = %v, want limit error", waiterErr)
			}
		case <-time.After(time.Second):
			t.Fatal("pending approval did not wake after overflow cancellation")
		}
	}
	engine.mu.Lock()
	pendingCount := len(active.pendingApprovals)
	engine.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending approval count = %d, want 0", pendingCount)
	}
	if engine.WaitingForUser("session-1") {
		t.Fatal("overflowed run still reports that it is waiting for approval")
	}
}

func TestToolApprovalLimitCountsOnlyUnresolvedRequests(t *testing.T) {
	cancelled := false
	active := &activeRun{
		runID: "run-1",
		cancelWithCause: func(error) {
			cancelled = true
		},
		pendingApprovals: make(map[string]*pendingToolApproval),
	}
	for index := range maxPendingToolApprovals {
		approval := &pendingToolApproval{
			request:  ToolApprovalRequest{ID: fmt.Sprintf("approval-%d", index)},
			resolved: make(chan struct{}),
		}
		if index == 0 {
			approval.resolutionOrder = 1
		}
		active.pendingApprovals[approval.request.ID] = approval
	}
	engine := &Engine{
		hub:    NewHub(),
		active: map[string]*activeRun{"session-1": active},
	}

	pending, err := engine.registerPendingToolApproval("session-1", "run-1", ToolApprovalRequest{
		ID: "approval-at-limit",
	})
	if err != nil {
		t.Fatalf("registerPendingToolApproval() error = %v", err)
	}
	if pending == nil || cancelled {
		t.Fatalf("registration = %#v, cancelled = %v", pending, cancelled)
	}
	_, err = engine.registerPendingToolApproval("session-1", "run-1", ToolApprovalRequest{
		ID: "approval-over-limit",
	})
	if !errors.Is(err, errTooManyPendingToolApprovals) || !cancelled {
		t.Fatalf("overflow error = %v, cancelled = %v", err, cancelled)
	}
}

func TestFinalRunOutcomeUsesCancellationCause(t *testing.T) {
	overflowContext, cancelWithCause := context.WithCancelCause(t.Context())
	cancelWithCause(errTooManyPendingToolApprovals)
	status, message := finalRunOutcome(overflowContext, "completed", "")
	if status != "failed" || message != errTooManyPendingToolApprovals.Error() {
		t.Fatalf("overflow outcome = (%q, %q)", status, message)
	}

	cancelledContext, cancel := context.WithCancel(t.Context())
	cancel()
	status, message = finalRunOutcome(cancelledContext, "completed", "ignored")
	if status != "cancelled" || message != "ignored" {
		t.Fatalf("ordinary cancellation outcome = (%q, %q)", status, message)
	}
}

func TestRequestWorkspaceToolApprovalRemovesPendingRequestOnCancellation(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	active := &activeRun{
		runID:             "run-1",
		pendingApprovals:  make(map[string]*pendingToolApproval),
		pendingUserInputs: make(map[string]*pendingUserInput),
	}
	engine := &Engine{
		hub:    hub,
		active: map[string]*activeRun{"session-1": active},
	}
	ctx, cancel := context.WithCancel(t.Context())
	events, ok := hub.Subscribe(t.Context(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}

	result := make(chan error, 1)
	go func() {
		_, err := engine.requestWorkspaceToolApproval(
			ctx,
			"session-1",
			"run-1",
			workspacetools.ToolApprovalRequest{
				ToolCallID: "command-1",
				ToolName:   "run_command",
			},
		)
		result <- err
	}()

	select {
	case event := <-events:
		if event.Type != "tool_approval" {
			t.Fatalf("first event type = %q, want tool_approval", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool approval")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("requestWorkspaceToolApproval() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled approval request did not return")
	}

	engine.mu.Lock()
	pendingCount := len(active.pendingApprovals)
	engine.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending approval count = %d, want 0", pendingCount)
	}
	if engine.WaitingForUser("session-1") {
		t.Fatal("WaitingForUser() = true after canceled approval")
	}
}

func TestWaitForToolApprovalsKeepsParallelWaitersIndependent(t *testing.T) {
	first := newPendingTestApproval()
	second := newPendingTestApproval()
	active := &activeRun{
		runID: "run-1",
		pendingApprovals: map[string]*pendingToolApproval{
			"approval-1": first,
			"approval-2": second,
		},
	}
	engine := &Engine{active: map[string]*activeRun{"session-1": active}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	type waitResult struct {
		decision ToolApprovalResolution
		err      error
	}
	firstResult := make(chan waitResult, 1)
	secondResult := make(chan waitResult, 1)
	go func() {
		result, err := engine.waitForToolApprovals(
			ctx,
			"session-1",
			"run-1",
			[]ToolApprovalRequest{{ID: "approval-1"}},
		)
		if err != nil {
			firstResult <- waitResult{err: err}
			return
		}
		firstResult <- waitResult{decision: result[0]}
	}()
	go func() {
		result, err := engine.waitForToolApprovals(
			ctx,
			"session-1",
			"run-1",
			[]ToolApprovalRequest{{ID: "approval-2"}},
		)
		if err != nil {
			secondResult <- waitResult{err: err}
			return
		}
		secondResult <- waitResult{decision: result[0]}
	}()

	resolveTestApproval(engine, "session-1", "approval-2", ToolApprovalResolution{
		ID: "approval-2", Approved: true,
	})
	resolveTestApproval(engine, "session-1", "approval-1", ToolApprovalResolution{
		ID: "approval-1", Approved: false,
	})
	if result := <-firstResult; result.err != nil || result.decision.ID != "approval-1" {
		t.Fatalf("first waiter = %#v", result)
	}
	if result := <-secondResult; result.err != nil || result.decision.ID != "approval-2" {
		t.Fatalf("second waiter = %#v", result)
	}
}

func newPendingTestApproval() *pendingToolApproval {
	return &pendingToolApproval{resolved: make(chan struct{})}
}

func resolveTestApproval(
	engine *Engine,
	sessionID, approvalID string,
	resolution ToolApprovalResolution,
) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	active := engine.active[sessionID]
	active.nextApprovalResolution++
	pending := active.pendingApprovals[approvalID]
	pending.resolution = resolution
	pending.resolutionOrder = active.nextApprovalResolution
	close(pending.resolved)
}

func TestWaitingForUserReflectsEveryActiveSession(t *testing.T) {
	engine := &Engine{active: map[string]*activeRun{
		"session-approval": {
			pendingApprovals: map[string]*pendingToolApproval{
				"approval-1": {request: ToolApprovalRequest{ID: "approval-1"}},
			},
		},
		"session-input": {
			pendingUserInputs: map[string]*pendingUserInput{"input-1": {}},
		},
		"session-running": {
			pendingApprovals: make(map[string]*pendingToolApproval),
		},
		"session-approved": {
			pendingApprovals: map[string]*pendingToolApproval{
				"approval-1": {resolutionOrder: 1},
			},
		},
	}}

	if !engine.WaitingForUser("session-approval") {
		t.Fatal("WaitingForUser() = false for a session with a pending approval")
	}
	if !engine.WaitingForUser("session-input") {
		t.Fatal("WaitingForUser() = false for a session with pending user input")
	}
	if engine.WaitingForUser("session-running") {
		t.Fatal("WaitingForUser() = true for a session without pending input")
	}
	if engine.WaitingForUser("session-approved") {
		t.Fatal("WaitingForUser() = true for an already resolved approval")
	}
	if engine.WaitingForUser("session-idle") {
		t.Fatal("WaitingForUser() = true for an inactive session")
	}
}

func TestPublishEventSuppressesInternalApprovalEvents(t *testing.T) {
	hub := NewHub()
	hub.Create("run-1")
	engine := &Engine{hub: hub}
	runRecord := store.Run{ID: "run-1"}

	engine.publishEvent(runRecord, &session.Event{
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "fetch-1", Name: "fetch_url", Args: map[string]any{"url": "https://example.com"},
		}}}}},
	})
	engine.publishEvent(runRecord, &session.Event{
		Actions: session.EventActions{RequestedToolConfirmations: map[string]toolconfirmation.ToolConfirmation{
			"fetch-1": {Hint: "Allow?"},
		}},
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "fetch-1", Name: "fetch_url", Response: map[string]any{"state": "approval_required"},
		}}}}},
	})
	engine.publishEvent(runRecord, confirmationRequestEvent())
	engine.publishEvent(runRecord, &session.Event{
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "fetch-1", Name: "fetch_url", Response: map[string]any{"state": "denied"},
		}}}}},
	})
	hub.Complete("run-1")

	events, ok := hub.Subscribe(context.Background(), "run-1", 0)
	if !ok {
		t.Fatal("Subscribe() ok = false")
	}
	var eventTypes []string
	for event := range events {
		eventTypes = append(eventTypes, event.Type)
	}
	if len(eventTypes) != 2 || eventTypes[0] != "tool_call" || eventTypes[1] != "tool_result" {
		t.Fatalf("stream event types = %#v", eventTypes)
	}
}

func confirmationRequestEvent() *session.Event {
	originalCall := &genai.FunctionCall{
		ID: "fetch-1", Name: "fetch_url", Args: map[string]any{"url": "https://example.com/docs#section"},
	}
	return &session.Event{InvocationID: "invocation-1", LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
		FunctionCall: &genai.FunctionCall{
			ID:   "approval-1",
			Name: toolconfirmation.FunctionCallName,
			Args: map[string]any{
				"originalFunctionCall": originalCall,
				"toolConfirmation": toolconfirmation.ToolConfirmation{
					Hint: "Allow this fetch?", Payload: map[string]any{"kind": "fetch_url", "url": "https://example.com/docs"},
				},
			},
		},
	}}}}}
}
