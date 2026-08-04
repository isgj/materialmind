package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"materialmind/internal/store"
)

const maxPendingToolApprovals = 32

var ErrToolApprovalNotPending = errors.New("tool approval is not pending")

type ToolApprovalRequest struct {
	ID           string               `json:"id"`
	ToolCallID   string               `json:"toolCallId"`
	ToolName     string               `json:"toolName"`
	Input        map[string]any       `json:"input"`
	Payload      map[string]any       `json:"payload,omitempty"`
	Hint         string               `json:"hint,omitempty"`
	Options      []ToolApprovalOption `json:"options,omitempty"`
	InvocationID string               `json:"-"`
}

type ToolApprovalOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type ToolApprovalResolution struct {
	ID         string         `json:"id"`
	ToolCallID string         `json:"toolCallId"`
	Approved   bool           `json:"approved"`
	Reason     string         `json:"reason,omitempty"`
	OptionID   string         `json:"optionId,omitempty"`
	Payload    map[string]any `json:"-"`
}

type ToolApprovalStarted struct {
	ID         string `json:"id"`
	ToolCallID string `json:"toolCallId"`
}

type pendingToolApproval struct {
	request         ToolApprovalRequest
	resolved        chan struct{}
	resolution      ToolApprovalResolution
	resolutionOrder uint64
}

func (e *Engine) ResolveToolApproval(
	ctx context.Context,
	runID, approvalID string,
	approved bool,
	reason, optionID string,
) (ToolApprovalResolution, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 2000 {
		return ToolApprovalResolution{}, fmt.Errorf("%w: refusal reason must be at most 2000 characters", store.ErrInvalidInput)
	}
	runRecord, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return ToolApprovalResolution{}, err
	}
	e.mu.Lock()
	active, ok := e.active[runRecord.SessionID]
	if !ok || active.runID != runID {
		e.mu.Unlock()
		return ToolApprovalResolution{}, ErrToolApprovalNotPending
	}
	pending, ok := active.pendingApprovals[approvalID]
	if !ok {
		e.mu.Unlock()
		return ToolApprovalResolution{}, ErrToolApprovalNotPending
	}
	if pending.resolutionOrder != 0 {
		e.mu.Unlock()
		return ToolApprovalResolution{}, ErrToolApprovalNotPending
	}
	request := pending.request
	optionID = strings.TrimSpace(optionID)
	if optionID != "" {
		optionFound := false
		for _, option := range request.Options {
			if option.ID != optionID {
				continue
			}
			optionFound = true
			approved = strings.HasPrefix(option.Kind, "allow_")
			break
		}
		if !optionFound {
			e.mu.Unlock()
			return ToolApprovalResolution{}, fmt.Errorf("%w: approval option is not available", store.ErrInvalidInput)
		}
	}
	if approved {
		reason = ""
	}
	resolution := ToolApprovalResolution{
		ID:         approvalID,
		ToolCallID: request.ToolCallID,
		Approved:   approved,
		Reason:     reason,
		OptionID:   optionID,
		Payload:    approvalResponsePayload(request.Payload, reason),
	}
	active.nextApprovalResolution++
	pending.resolution = resolution
	pending.resolutionOrder = active.nextApprovalResolution
	close(pending.resolved)
	e.mu.Unlock()

	e.hub.Publish(runID, "tool_approval_resolved", resolution)
	return resolution, nil
}

func (e *Engine) registerToolApproval(sessionID, runID string, request ToolApprovalRequest) error {
	e.mu.Lock()
	active, ok := e.active[sessionID]
	if !ok || active.runID != runID {
		e.mu.Unlock()
		return ErrToolApprovalNotPending
	}
	if _, exists := active.pendingApprovals[request.ID]; exists {
		e.mu.Unlock()
		return nil
	}
	if len(active.pendingApprovals) >= maxPendingToolApprovals {
		e.mu.Unlock()
		return fmt.Errorf("too many pending tool approvals")
	}
	active.pendingApprovals[request.ID] = &pendingToolApproval{
		request:  request,
		resolved: make(chan struct{}),
	}
	e.mu.Unlock()

	e.hub.Publish(runID, "tool_approval", request)
	return nil
}

func (e *Engine) waitForToolApprovals(ctx context.Context, sessionID, runID string, requests []ToolApprovalRequest) ([]ToolApprovalResolution, error) {
	remaining := make(map[string]ToolApprovalRequest, len(requests))
	resultIndex := make(map[string]int, len(requests))
	for index, request := range requests {
		remaining[request.ID] = request
		resultIndex[request.ID] = index
	}

	result := make([]ToolApprovalResolution, len(requests))
	for len(remaining) > 0 {
		decision, err := e.waitForNextToolApproval(
			ctx,
			sessionID,
			runID,
			remaining,
		)
		if err != nil {
			return nil, err
		}
		result[resultIndex[decision.ID]] = decision
		delete(remaining, decision.ID)
	}
	return result, nil
}

func (e *Engine) waitForNextToolApproval(
	ctx context.Context,
	sessionID, runID string,
	requests map[string]ToolApprovalRequest,
) (ToolApprovalResolution, error) {
	if len(requests) == 0 {
		return ToolApprovalResolution{}, ErrToolApprovalNotPending
	}
	for {
		if err := ctx.Err(); err != nil {
			return ToolApprovalResolution{}, err
		}
		e.mu.Lock()
		active, ok := e.active[sessionID]
		if !ok || active.runID != runID {
			e.mu.Unlock()
			return ToolApprovalResolution{}, ErrToolApprovalNotPending
		}

		var next *pendingToolApproval
		pending := make([]*pendingToolApproval, 0, len(requests))
		for approvalID := range requests {
			current, exists := active.pendingApprovals[approvalID]
			if !exists {
				e.mu.Unlock()
				return ToolApprovalResolution{}, ErrToolApprovalNotPending
			}
			pending = append(pending, current)
			if current.resolutionOrder != 0 &&
				(next == nil || current.resolutionOrder < next.resolutionOrder) {
				next = current
			}
		}
		if next != nil {
			resolution := next.resolution
			delete(active.pendingApprovals, resolution.ID)
			e.mu.Unlock()
			return resolution, nil
		}

		cases := make([]reflect.SelectCase, 1, len(pending)+1)
		cases[0] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())}
		for _, current := range pending {
			cases = append(cases, reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(current.resolved),
			})
		}
		e.mu.Unlock()

		selected, _, _ := reflect.Select(cases)
		if selected == 0 {
			return ToolApprovalResolution{}, ctx.Err()
		}
	}
}

func (e *Engine) publishToolApprovalStarted(
	runID string,
	decision ToolApprovalResolution,
) {
	if !decision.Approved {
		return
	}
	e.hub.Publish(runID, "tool_approval_started", ToolApprovalStarted{
		ID:         decision.ID,
		ToolCallID: decision.ToolCallID,
	})
}

func toolApprovalRequests(event *session.Event) ([]ToolApprovalRequest, error) {
	if event == nil || event.Content == nil {
		return nil, nil
	}
	requests := make([]ToolApprovalRequest, 0)
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionCall == nil || !isConfirmationCall(part.FunctionCall.Name) {
			continue
		}
		originalCall, err := toolconfirmation.OriginalCallFrom(part.FunctionCall)
		if err != nil {
			return nil, fmt.Errorf("decode tool approval request: %w", err)
		}
		confirmation, err := confirmationFromCall(part.FunctionCall)
		if err != nil {
			return nil, err
		}
		payload, err := confirmationPayloadMap(confirmation)
		if err != nil {
			return nil, err
		}
		request := ToolApprovalRequest{
			ID:           part.FunctionCall.ID,
			ToolCallID:   originalCall.ID,
			ToolName:     originalCall.Name,
			Input:        originalCall.Args,
			Payload:      payload,
			Hint:         confirmation.Hint,
			InvocationID: event.InvocationID,
		}
		if request.Payload == nil {
			request.Payload = make(map[string]any)
		}
		if _, ok := request.Payload["kind"]; !ok && originalCall.Name == "fetch_url" {
			request.Payload["kind"] = "fetch_url"
			if value, ok := originalCall.Args["url"].(string); ok {
				request.Payload["url"] = value
			}
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func hasApprovalForInvocation(
	requests map[string]ToolApprovalRequest,
	invocationID string,
) bool {
	if invocationID == "" {
		return len(requests) > 0
	}
	for _, request := range requests {
		if request.InvocationID == invocationID {
			return true
		}
	}
	return false
}

func confirmationPayloadMap(confirmation toolconfirmation.ToolConfirmation) (map[string]any, error) {
	if confirmation.Payload == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(confirmation.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode tool approval payload: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("decode tool approval payload: %w", err)
	}
	return payload, nil
}

func confirmationContent(decisions []ToolApprovalResolution) *genai.Content {
	parts := make([]*genai.Part, 0, len(decisions))
	for _, decision := range decisions {
		payload := approvalResponsePayload(decision.Payload, decision.Reason)
		parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:   decision.ID,
			Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{
				"confirmed": decision.Approved,
				"payload":   payload,
			},
		}})
	}
	return &genai.Content{Role: genai.RoleUser, Parts: parts}
}

func approvalResponsePayload(payload map[string]any, reason string) map[string]any {
	response := maps.Clone(payload)
	if response == nil {
		response = make(map[string]any)
	}
	response["reason"] = reason
	return response
}

func confirmationFromCall(call *genai.FunctionCall) (toolconfirmation.ToolConfirmation, error) {
	value, ok := call.Args["toolConfirmation"]
	if !ok {
		return toolconfirmation.ToolConfirmation{}, fmt.Errorf("decode tool approval request: toolConfirmation is missing")
	}
	if confirmation, ok := value.(toolconfirmation.ToolConfirmation); ok {
		return confirmation, nil
	}
	if confirmation, ok := value.(*toolconfirmation.ToolConfirmation); ok && confirmation != nil {
		return *confirmation, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return toolconfirmation.ToolConfirmation{}, fmt.Errorf("encode tool approval request: %w", err)
	}
	var confirmation toolconfirmation.ToolConfirmation
	if err := json.Unmarshal(encoded, &confirmation); err != nil {
		return toolconfirmation.ToolConfirmation{}, fmt.Errorf("decode tool approval request: %w", err)
	}
	return confirmation, nil
}

func isConfirmationCall(name string) bool {
	return name == toolconfirmation.FunctionCallName
}

func isPendingToolResponse(event *session.Event, functionCallID string) bool {
	if event == nil {
		return false
	}
	_, pending := event.Actions.RequestedToolConfirmations[functionCallID]
	return pending
}
