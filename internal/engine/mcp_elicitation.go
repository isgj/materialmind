package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"materialmind/internal/mcpruntime"
	"materialmind/internal/store"
)

const (
	maxPendingMCPElicitations = 8
	maxMCPElicitationBytes    = 64 * 1024
)

var ErrMCPElicitationNotPending = errors.New("MCP elicitation is not pending")

type pendingMCPElicitation struct {
	request  mcpruntime.ElicitationRequest
	response chan mcpruntime.ElicitationResolution
}

func (e *Engine) requestMCPElicitation(
	ctx context.Context,
	request mcpruntime.ElicitationRequest,
) (mcpruntime.ElicitationResolution, error) {
	pending := &pendingMCPElicitation{
		request:  request,
		response: make(chan mcpruntime.ElicitationResolution, 1),
	}

	e.mu.Lock()
	active := e.active[request.SessionID]
	if active == nil {
		e.mu.Unlock()
		return mcpruntime.ElicitationResolution{}, ErrMCPElicitationNotPending
	}
	if active.pendingMCPElicitations == nil {
		active.pendingMCPElicitations = make(map[string]*pendingMCPElicitation)
	}
	if len(active.pendingMCPElicitations) >= maxPendingMCPElicitations {
		e.mu.Unlock()
		return mcpruntime.ElicitationResolution{}, fmt.Errorf("too many pending MCP elicitations")
	}
	active.pendingMCPElicitations[request.ID] = pending
	runID := active.runID
	e.mu.Unlock()

	e.hub.Publish(runID, "mcp_elicitation_request", request)
	defer e.removePendingMCPElicitation(request.SessionID, runID, request.ID, pending)

	select {
	case <-ctx.Done():
		return mcpruntime.ElicitationResolution{}, ctx.Err()
	case resolution := <-pending.response:
		return resolution, nil
	}
}

func (e *Engine) ResolveMCPElicitation(
	ctx context.Context,
	runID, requestID, action string,
	content map[string]any,
) (mcpruntime.ElicitationResolution, error) {
	action = strings.TrimSpace(action)
	if action != mcpruntime.ElicitationActionAccept &&
		action != mcpruntime.ElicitationActionDecline &&
		action != mcpruntime.ElicitationActionCancel {
		return mcpruntime.ElicitationResolution{}, fmt.Errorf(
			"%w: action must be accept, decline, or cancel",
			store.ErrInvalidInput,
		)
	}
	if action != mcpruntime.ElicitationActionAccept {
		content = nil
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return mcpruntime.ElicitationResolution{}, fmt.Errorf(
			"%w: elicitation content is invalid",
			store.ErrInvalidInput,
		)
	}
	if len(encoded) > maxMCPElicitationBytes {
		return mcpruntime.ElicitationResolution{}, fmt.Errorf(
			"%w: elicitation content must be at most %d bytes",
			store.ErrInvalidInput,
			maxMCPElicitationBytes,
		)
	}

	runRecord, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return mcpruntime.ElicitationResolution{}, err
	}
	e.mu.Lock()
	active := e.active[runRecord.SessionID]
	if active == nil || active.runID != runID {
		e.mu.Unlock()
		return mcpruntime.ElicitationResolution{}, ErrMCPElicitationNotPending
	}
	pending := active.pendingMCPElicitations[requestID]
	if pending == nil {
		e.mu.Unlock()
		return mcpruntime.ElicitationResolution{}, ErrMCPElicitationNotPending
	}
	resolution := mcpruntime.ElicitationResolution{
		ID:         requestID,
		ToolCallID: pending.request.ToolCallID,
		Action:     action,
		Content:    content,
	}
	select {
	case pending.response <- resolution:
		delete(active.pendingMCPElicitations, requestID)
	default:
		e.mu.Unlock()
		return mcpruntime.ElicitationResolution{}, fmt.Errorf(
			"queue MCP elicitation response: %w",
			ErrSessionBusy,
		)
	}
	e.mu.Unlock()

	e.hub.Publish(runID, "mcp_elicitation_resolved", resolution)
	return resolution, nil
}

func (e *Engine) removePendingMCPElicitation(
	sessionID, runID, requestID string,
	pending *pendingMCPElicitation,
) {
	e.mu.Lock()
	defer e.mu.Unlock()
	active := e.active[sessionID]
	if active == nil ||
		active.runID != runID ||
		active.pendingMCPElicitations[requestID] != pending {
		return
	}
	delete(active.pendingMCPElicitations, requestID)
}
