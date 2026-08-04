package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"materialmind/internal/acpinternal"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
	"materialmind/internal/workspacetools"
)

var (
	ErrACPInternalUnauthorized = errors.New("ACP internal MCP token is invalid")
	ErrACPInternalUnavailable  = errors.New("ACP session tool is unavailable")
)

func (e *Engine) acpInternalMCPToken(sessionID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.acpInternalMCPEnabled {
		return ""
	}
	if token := e.acpInternalTokensBySession[sessionID]; token != "" {
		return token
	}
	token := rand.Text()
	e.acpInternalTokensBySession[sessionID] = token
	e.acpInternalSessionsByToken[token] = sessionID
	return token
}

func (e *Engine) revokeACPInternalMCPToken(sessionID string) {
	e.mu.Lock()
	token := e.acpInternalTokensBySession[sessionID]
	delete(e.acpInternalTokensBySession, sessionID)
	delete(e.acpInternalSessionsByToken, token)
	e.mu.Unlock()
}

func (e *Engine) CallACPInternalTool(
	ctx context.Context,
	token, toolName string,
	arguments json.RawMessage,
) (any, error) {
	e.mu.Lock()
	sessionID, authenticated := e.acpInternalSessionsByToken[token]
	active := e.active[sessionID]
	var handler *acpRunHandler
	if active != nil {
		handler = active.acpHandler
	}
	e.mu.Unlock()
	if !authenticated {
		return nil, ErrACPInternalUnauthorized
	}
	if handler == nil {
		return nil, fmt.Errorf("%w: the ACP session has no active run", ErrACPInternalUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	switch toolName {
	case acpinternal.ToolReadSessionNotes:
		var args workspacetools.ReadSessionNotesArgs
		if err := decodeACPInternalArguments(arguments, &args); err != nil {
			return nil, err
		}
		return handler.readSessionNotes()
	case acpinternal.ToolUpdateSessionNotes:
		var args workspacetools.UpdateSessionNotesArgs
		if err := decodeACPInternalArguments(arguments, &args); err != nil {
			return nil, err
		}
		return handler.updateSessionNotes(args)
	default:
		return nil, fmt.Errorf("%w: unknown tool %q", store.ErrInvalidInput, toolName)
	}
}

func decodeACPInternalArguments(raw json.RawMessage, destination any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: invalid ACP session tool arguments: %v", store.ErrInvalidInput, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: ACP session tool arguments must contain one object", store.ErrInvalidInput)
	}
	return nil
}

func (h *acpRunHandler) readSessionNotes() (workspacetools.ReadSessionNotesResult, error) {
	const toolName = toolpolicy.ToolReadSessionNotes
	toolCallID := "acp-session-notes:" + uuid.NewString()
	input := map[string]any{}
	if err := h.publishClientToolCall(h.ctx, toolCallID, toolName, input); err != nil {
		return workspacetools.ReadSessionNotesResult{}, err
	}
	permission, ok := toolpolicy.PermissionFor(h.permissions, toolName)
	if !ok {
		err := fmt.Errorf("read_session_notes permission is unavailable")
		h.publishClientToolError(toolCallID, toolName, err)
		return workspacetools.ReadSessionNotesResult{}, err
	}
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk {
		approved, reason, err := h.requestClientToolApproval(
			toolCallID,
			toolName,
			input,
			map[string]any{"kind": "session_notes", "operation": "read"},
			"Allow the agent to read the session notes?",
		)
		if err != nil {
			h.publishClientToolError(toolCallID, toolName, err)
			return workspacetools.ReadSessionNotesResult{}, err
		}
		if !approved {
			result := workspacetools.ReadSessionNotesResult{State: "denied", Reason: reason}
			h.publishClientToolResult(toolCallID, toolName, sessionNotesResultMap(result))
			return result, nil
		}
	}
	result, err := h.engine.readSessionNotes(h.ctx, h.session.ID)
	if err != nil {
		h.publishClientToolError(toolCallID, toolName, err)
		return workspacetools.ReadSessionNotesResult{}, err
	}
	h.publishClientToolResult(toolCallID, toolName, sessionNotesResultMap(result))
	return result, nil
}

func (h *acpRunHandler) updateSessionNotes(
	args workspacetools.UpdateSessionNotesArgs,
) (workspacetools.UpdateSessionNotesResult, error) {
	const toolName = toolpolicy.ToolUpdateSessionNotes
	toolCallID := "acp-session-notes:" + uuid.NewString()
	input := map[string]any{
		"content":          args.Content,
		"expectedRevision": args.ExpectedRevision,
	}
	if err := h.publishClientToolCall(h.ctx, toolCallID, toolName, input); err != nil {
		return workspacetools.UpdateSessionNotesResult{}, err
	}
	permission, ok := toolpolicy.PermissionFor(h.permissions, toolName)
	if !ok {
		err := fmt.Errorf("update_session_notes permission is unavailable")
		h.publishClientToolError(toolCallID, toolName, err)
		return workspacetools.UpdateSessionNotesResult{}, err
	}
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk {
		approved, reason, err := h.requestClientToolApproval(
			toolCallID,
			toolName,
			input,
			map[string]any{
				"kind":             "session_notes",
				"operation":        "update",
				"expectedRevision": args.ExpectedRevision,
			},
			"Allow the agent to replace the session notes?",
		)
		if err != nil {
			h.publishClientToolError(toolCallID, toolName, err)
			return workspacetools.UpdateSessionNotesResult{}, err
		}
		if !approved {
			result := workspacetools.UpdateSessionNotesResult{
				State:            "denied",
				ExpectedRevision: args.ExpectedRevision,
				Bytes:            len(args.Content),
				Reason:           reason,
			}
			h.publishClientToolResult(toolCallID, toolName, sessionNotesResultMap(result))
			return result, nil
		}
	}
	result, err := h.engine.updateSessionNotes(h.ctx, h.session.ID, args)
	if err != nil {
		h.publishClientToolError(toolCallID, toolName, err)
		return workspacetools.UpdateSessionNotesResult{}, err
	}
	h.publishClientToolResult(toolCallID, toolName, sessionNotesResultMap(result))
	return result, nil
}

func sessionNotesResultMap(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"state": "failed", "error": err.Error()}
	}
	result := make(map[string]any)
	if err := json.Unmarshal(encoded, &result); err != nil {
		return map[string]any{"state": "failed", "error": err.Error()}
	}
	return result
}
