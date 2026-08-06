package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"materialmind/internal/acpinternal"
	"materialmind/internal/acpruntime"
	"materialmind/internal/mcpruntime"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
	"materialmind/internal/workspacetools"
)

type acpRunHandler struct {
	engine        *Engine
	ctx           context.Context
	run           store.Run
	session       store.AppSession
	workspaceRoot string
	permissions   []toolpolicy.Permission

	mu                 sync.Mutex
	sequence           int
	currentSegmentKind string
	currentSegmentID   string
	segments           map[string]string
	lastMessageID      string
	tools              map[string]*acpToolState
	terminalTools      map[string]string
	pendingTerminals   map[string]map[string]string
}

type acpPlanUpdate struct {
	ID      string         `json:"id"`
	Entries []acpPlanEntry `json:"entries"`
}

type acpPlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type acpUsageUpdate struct {
	Used       int       `json:"used"`
	Size       int       `json:"size"`
	Percentage float64   `json:"percentage"`
	Cost       *acp.Cost `json:"cost,omitempty"`
}

type acpToolStatusUpdate struct {
	ID     string             `json:"id"`
	Status acp.ToolCallStatus `json:"status"`
}

type acpToolState struct {
	id            string
	title         string
	kind          acp.ToolKind
	status        acp.ToolCallStatus
	rawInput      any
	rawOutput     any
	content       []acp.ToolCallContent
	locations     []acp.ToolCallLocation
	callPublished bool
	stdout        string
	stderr        string
	streamedOut   string
	streamedErr   string
	streamedMeta  string
}

func (e *Engine) executeACP(
	ctx context.Context,
	runRecord store.Run,
	sessionRecord store.AppSession,
	workspace store.Workspace,
	agentRecord store.ACPAgent,
	mcpServers []store.SessionMCPServer,
	permissions []toolpolicy.Permission,
	done chan struct{},
) {
	finalStatus := "completed"
	var finalError string
	defer func() {
		if recovered := recover(); recovered != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("ACP run panicked: %v", recovered)
		}
		finalStatus, finalError = finalRunOutcome(ctx, finalStatus, finalError)
		updated, err := e.store.UpdateRun(
			context.Background(),
			runRecord.ID,
			finalStatus,
			runRecord.InvocationID,
			finalError,
		)
		if err == nil {
			e.hub.Publish(runRecord.ID, "run", updated)
		}
		if finalError != "" {
			slog.Error(
				"agent run failed",
				"session_id", runRecord.SessionID,
				"run_id", runRecord.ID,
				"runtime", runRecord.RuntimeType,
				"error", finalError,
			)
			e.hub.Publish(runRecord.ID, "run_error", map[string]string{"message": finalError})
		}
		e.hub.Publish(runRecord.ID, "done", map[string]string{"status": finalStatus})
		e.hub.Complete(runRecord.ID)
		e.mu.Lock()
		delete(e.active, runRecord.SessionID)
		e.mu.Unlock()
		close(done)
	}()

	running, err := e.store.UpdateRun(ctx, runRecord.ID, "running", runRecord.InvocationID, "")
	if err != nil {
		finalStatus, finalError = "failed", err.Error()
		return
	}
	e.hub.Publish(runRecord.ID, "run", running)

	handler := &acpRunHandler{
		engine:           e,
		ctx:              ctx,
		run:              runRecord,
		session:          sessionRecord,
		workspaceRoot:    workspace.RootPath,
		permissions:      permissions,
		segments:         make(map[string]string),
		tools:            make(map[string]*acpToolState),
		terminalTools:    make(map[string]string),
		pendingTerminals: make(map[string]map[string]string),
	}
	e.mu.Lock()
	if active := e.active[sessionRecord.ID]; active != nil && active.runID == runRecord.ID {
		active.acpHandler = handler
	}
	e.mu.Unlock()
	preferredConfigOptions, err := decodeACPConfigOptions(sessionRecord.ACPConfigOptions)
	if err != nil {
		finalStatus, finalError = "failed", err.Error()
		return
	}
	response, restoredOptions, err := e.acpManager.Prompt(
		ctx,
		agentRecord,
		sessionRecord.ACPSessionID,
		workspace.RootPath,
		runRecord.UserMessage,
		runRecord.Attachments,
		acpAdditionalDirectories(workspace.RootPath, permissions),
		mcpServers,
		e.acpInternalMCPToken(sessionRecord.ID),
		preferredConfigOptions,
		handler,
	)
	if len(restoredOptions) > 0 {
		handler.saveConfigOptions(restoredOptions)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			finalStatus = "cancelled"
			return
		}
		finalStatus, finalError = "failed", err.Error()
		return
	}
	if response.StopReason == acp.StopReasonCancelled {
		finalStatus = "cancelled"
		return
	}
	if message := handler.finalMessage(); message != "" {
		e.hub.Publish(runRecord.ID, "message_complete", map[string]string{"text": message})
	}
}

func (h *acpRunHandler) ReadTextFile(
	_ context.Context,
	request acp.ReadTextFileRequest,
) (acp.ReadTextFileResponse, error) {
	toolCallID := "acp-fs-read:" + uuid.NewString()
	input := map[string]any{"path": request.Path}
	if request.Line != nil {
		input["startLine"] = *request.Line
	}
	if request.Limit != nil {
		input["limit"] = *request.Limit
	}
	if err := h.publishClientToolCall(h.ctx, toolCallID, toolpolicy.ToolReadFile, input); err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	permission, ok := toolpolicy.PermissionFor(h.permissions, toolpolicy.ToolReadFile)
	if !ok {
		err := fmt.Errorf("read_file permission is unavailable")
		h.publishClientToolError(toolCallID, toolpolicy.ToolReadFile, err)
		return acp.ReadTextFileResponse{}, err
	}
	prepared, err := workspacetools.PrepareTextFileRead(
		h.workspaceRoot,
		permission,
		request.Path,
		request.Line,
		request.Limit,
	)
	if err != nil {
		h.publishClientToolError(toolCallID, toolpolicy.ToolReadFile, err)
		return acp.ReadTextFileResponse{}, err
	}
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk {
		approved, reason, approvalErr := h.requestClientToolApproval(
			toolCallID,
			toolpolicy.ToolReadFile,
			input,
			map[string]any{
				"kind":         "filesystem_access",
				"operation":    "read",
				"path":         prepared.Path(),
				"absolutePath": prepared.AbsolutePath(),
			},
			fmt.Sprintf("Allow the agent to read %s?", prepared.AbsolutePath()),
		)
		if approvalErr != nil {
			h.publishClientToolError(toolCallID, toolpolicy.ToolReadFile, approvalErr)
			return acp.ReadTextFileResponse{}, approvalErr
		}
		if !approved {
			err = refusedACPFileOperation("read", prepared.Path(), reason)
			h.publishClientToolResult(toolCallID, toolpolicy.ToolReadFile, map[string]any{
				"state": "denied", "path": prepared.Path(), "reason": reason,
			})
			return acp.ReadTextFileResponse{}, err
		}
	}
	result, err := prepared.Execute()
	if err != nil {
		h.publishClientToolError(toolCallID, toolpolicy.ToolReadFile, err)
		return acp.ReadTextFileResponse{}, err
	}
	h.publishClientToolResult(toolCallID, toolpolicy.ToolReadFile, map[string]any{
		"state":     result.State,
		"path":      result.Path,
		"content":   result.Content,
		"startLine": result.StartLine,
		"endLine":   result.EndLine,
		"truncated": result.Truncated,
	})
	return acp.ReadTextFileResponse{Content: result.Content}, nil
}

func (h *acpRunHandler) WriteTextFile(
	_ context.Context,
	request acp.WriteTextFileRequest,
) (acp.WriteTextFileResponse, error) {
	toolCallID := "acp-fs-write:" + uuid.NewString()
	input := map[string]any{
		"path":         request.Path,
		"contentBytes": len(request.Content),
	}
	if err := h.publishClientToolCall(h.ctx, toolCallID, toolpolicy.ToolEditFile, input); err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	permission, ok := toolpolicy.PermissionFor(h.permissions, toolpolicy.ToolEditFile)
	if !ok {
		err := fmt.Errorf("edit_file permission is unavailable")
		h.publishClientToolError(toolCallID, toolpolicy.ToolEditFile, err)
		return acp.WriteTextFileResponse{}, err
	}
	prepared, err := workspacetools.PrepareTextFileWrite(
		h.workspaceRoot,
		permission,
		request.Path,
		request.Content,
	)
	if err != nil {
		h.publishClientToolError(toolCallID, toolpolicy.ToolEditFile, err)
		return acp.WriteTextFileResponse{}, err
	}
	preview := prepared.Preview()
	if permission.ConfirmationMode == toolpolicy.ConfirmationAsk && !preview.Noop {
		approved, reason, approvalErr := h.requestClientToolApproval(
			toolCallID,
			toolpolicy.ToolEditFile,
			input,
			map[string]any{
				"kind": "file_edit",
				"diff": preview.Diff,
				"files": []map[string]any{{
					"operation": preview.Operation,
					"path":      preview.Path,
					"diff":      preview.Diff,
				}},
			},
			fmt.Sprintf("Allow the agent to %s %s?", preview.Operation, preview.AbsolutePath),
		)
		if approvalErr != nil {
			h.publishClientToolError(toolCallID, toolpolicy.ToolEditFile, approvalErr)
			return acp.WriteTextFileResponse{}, approvalErr
		}
		if !approved {
			err = refusedACPFileOperation("write", preview.Path, reason)
			h.publishClientToolResult(toolCallID, toolpolicy.ToolEditFile, map[string]any{
				"state": "denied", "path": preview.Path, "reason": reason,
			})
			return acp.WriteTextFileResponse{}, err
		}
	}
	if err := prepared.Apply(); err != nil {
		h.publishClientToolError(toolCallID, toolpolicy.ToolEditFile, err)
		return acp.WriteTextFileResponse{}, err
	}
	state := "applied"
	if preview.Noop {
		state = "unchanged"
	}
	h.publishClientToolResult(toolCallID, toolpolicy.ToolEditFile, map[string]any{
		"state": state,
		"path":  preview.Path,
		"files": []map[string]any{{
			"operation": preview.Operation,
			"path":      preview.Path,
			"diff":      preview.Diff,
		}},
	})
	return acp.WriteTextFileResponse{}, nil
}

func (h *acpRunHandler) requestClientToolApproval(
	toolCallID, toolName string,
	input, payload map[string]any,
	hint string,
) (bool, string, error) {
	request := ToolApprovalRequest{
		ID:         "acp-client-approval:" + uuid.NewString(),
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Input:      input,
		Payload:    payload,
		Hint:       hint,
	}
	if err := h.engine.registerToolApproval(h.session.ID, h.run.ID, request); err != nil {
		return false, "", err
	}
	decisions, err := h.engine.waitForToolApprovals(
		h.ctx,
		h.session.ID,
		h.run.ID,
		[]ToolApprovalRequest{request},
	)
	if err != nil {
		return false, "", err
	}
	decision := decisions[0]
	h.engine.publishToolApprovalStarted(h.run.ID, decision)
	return decision.Approved, decision.Reason, nil
}

func (h *acpRunHandler) publishClientToolCall(
	ctx context.Context,
	toolCallID, toolName string,
	input map[string]any,
) error {
	_, err := h.engine.store.UpsertACPTranscriptItem(ctx, h.session.ID, store.TranscriptItem{
		ID:           h.run.ID + ":tool:" + toolCallID + ":call",
		InvocationID: h.run.InvocationID,
		Kind:         "tool_call",
		ToolName:     toolName,
		ToolCallID:   toolCallID,
		ToolInput:    input,
		Provider:     store.RuntimeACP,
		Model:        h.run.ACPAgentName,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	h.engine.hub.Publish(h.run.ID, "tool_call", map[string]any{
		"id": toolCallID, "name": toolName, "input": input,
	})
	return nil
}

func (h *acpRunHandler) publishClientToolResult(
	toolCallID, toolName string,
	output map[string]any,
) {
	_, err := h.engine.store.UpsertACPTranscriptItem(
		context.Background(),
		h.session.ID,
		store.TranscriptItem{
			ID:           h.run.ID + ":tool:" + toolCallID + ":result",
			InvocationID: h.run.InvocationID,
			Kind:         "tool_result",
			ToolName:     toolName,
			ToolCallID:   toolCallID,
			ToolOutput:   output,
			Provider:     store.RuntimeACP,
			Model:        h.run.ACPAgentName,
			CreatedAt:    time.Now().UTC(),
		},
	)
	if err != nil {
		slog.Error("save ACP client tool result", "tool_call_id", toolCallID, "error", err)
	}
	h.engine.hub.Publish(h.run.ID, "tool_result", map[string]any{
		"id": toolCallID, "name": toolName, "output": output,
	})
}

func (h *acpRunHandler) publishClientToolError(toolCallID, toolName string, err error) {
	h.publishClientToolResult(toolCallID, toolName, map[string]any{
		"state": "failed",
		"error": err.Error(),
	})
}

func refusedACPFileOperation(operation, filePath, reason string) error {
	message := fmt.Sprintf("%s %q was refused by the user", operation, filePath)
	if strings.TrimSpace(reason) != "" {
		message += ": " + strings.TrimSpace(reason)
	}
	return errors.New(message)
}

func (e *Engine) SetACPSessionConfigOption(
	ctx context.Context,
	sessionID, configID string,
	value any,
) (store.AppSession, error) {
	e.mu.Lock()
	_, running := e.active[sessionID]
	e.mu.Unlock()
	if running {
		return store.AppSession{}, ErrSessionBusy
	}
	sessionRecord, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		return store.AppSession{}, err
	}
	if sessionRecord.RuntimeType != store.RuntimeACP ||
		sessionRecord.ACPAgentID == nil ||
		sessionRecord.ACPSessionID == "" {
		return store.AppSession{}, fmt.Errorf("%w: session does not use an ACP agent runtime", store.ErrInvalidInput)
	}
	workspace, err := e.store.GetWorkspace(ctx, sessionRecord.WorkspaceID)
	if err != nil {
		return store.AppSession{}, err
	}
	agentRecord, err := e.store.GetACPAgent(ctx, *sessionRecord.ACPAgentID)
	if err != nil {
		return store.AppSession{}, err
	}
	mcpServers, err := e.store.GetSessionMCPServers(ctx, sessionID)
	if err != nil {
		return store.AppSession{}, err
	}
	permissions, err := e.store.GetSessionToolPermissions(ctx, sessionID)
	if err != nil {
		return store.AppSession{}, err
	}
	preferredConfigOptions, err := decodeACPConfigOptions(sessionRecord.ACPConfigOptions)
	if err != nil {
		return store.AppSession{}, err
	}
	options, err := e.acpManager.SetSessionConfigOption(
		ctx,
		agentRecord,
		sessionRecord.ACPSessionID,
		workspace.RootPath,
		acpAdditionalDirectories(workspace.RootPath, permissions),
		mcpServers,
		e.acpInternalMCPToken(sessionID),
		preferredConfigOptions,
		configID,
		value,
	)
	if err != nil {
		return store.AppSession{}, err
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return store.AppSession{}, fmt.Errorf("encode ACP session configuration: %w", err)
	}
	return e.store.UpdateACPSessionConfigOptions(ctx, sessionID, encoded)
}

func acpAdditionalDirectories(
	workspaceRoot string,
	permissions []toolpolicy.Permission,
) []string {
	repositoryAllowed := false
	for _, permission := range permissions {
		if permission.FilesystemScope == toolpolicy.ScopeRepository {
			repositoryAllowed = true
			break
		}
	}
	if !repositoryAllowed {
		return nil
	}
	repositoryRoot, ok := toolpolicy.FindRepositoryRoot(workspaceRoot)
	if !ok || repositoryRoot == workspaceRoot {
		return nil
	}
	return []string{repositoryRoot}
}

func (h *acpRunHandler) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	update := notification.Update
	switch {
	case update.AgentMessageChunk != nil:
		return h.appendText(
			ctx,
			"message",
			update.AgentMessageChunk.MessageId,
			contentBlockText(update.AgentMessageChunk.Content),
		)
	case update.AgentThoughtChunk != nil:
		return h.appendText(
			ctx,
			"thought",
			update.AgentThoughtChunk.MessageId,
			contentBlockText(update.AgentThoughtChunk.Content),
		)
	case update.ToolCall != nil:
		kind := update.ToolCall.Kind
		status := update.ToolCall.Status
		title := update.ToolCall.Title
		return h.applyToolUpdate(ctx, acp.ToolCallUpdate{
			ToolCallId: update.ToolCall.ToolCallId,
			Title:      &title,
			Kind:       &kind,
			Status:     &status,
			RawInput:   update.ToolCall.RawInput,
			RawOutput:  update.ToolCall.RawOutput,
			Content:    update.ToolCall.Content,
			Locations:  update.ToolCall.Locations,
			Meta:       mergeACPMetadata(notification.Meta, update.ToolCall.Meta),
		})
	case update.ToolCallUpdate != nil:
		return h.applyToolUpdate(ctx, acp.ToolCallUpdate{
			ToolCallId: update.ToolCallUpdate.ToolCallId,
			Title:      update.ToolCallUpdate.Title,
			Kind:       update.ToolCallUpdate.Kind,
			Status:     update.ToolCallUpdate.Status,
			RawInput:   update.ToolCallUpdate.RawInput,
			RawOutput:  update.ToolCallUpdate.RawOutput,
			Content:    update.ToolCallUpdate.Content,
			Locations:  update.ToolCallUpdate.Locations,
			Meta:       mergeACPMetadata(notification.Meta, update.ToolCallUpdate.Meta),
		})
	case update.Plan != nil:
		return h.savePlan(ctx, update.Plan.Entries)
	case update.ConfigOptionUpdate != nil:
		h.saveConfigOptions(update.ConfigOptionUpdate.ConfigOptions)
	case update.SessionInfoUpdate != nil && update.SessionInfoUpdate.Title != nil:
		title := strings.TrimSpace(*update.SessionInfoUpdate.Title)
		if title != "" {
			if updated, err := h.engine.store.UpdateSession(ctx, h.session.ID, title, nil); err == nil {
				h.session = updated
			}
		}
	case update.UsageUpdate != nil:
		usage := acpUsageUpdate{
			Used: update.UsageUpdate.Used,
			Size: update.UsageUpdate.Size,
			Cost: update.UsageUpdate.Cost,
		}
		if usage.Size > 0 {
			usage.Percentage = min(100, max(0, float64(usage.Used)/float64(usage.Size)*100))
		}
		h.engine.hub.Publish(h.run.ID, "acp_usage", usage)
	}
	return nil
}

func (h *acpRunHandler) RequestPermission(
	ctx context.Context,
	request acp.RequestPermissionRequest,
) (acp.RequestPermissionResponse, error) {
	if err := h.applyToolUpdate(ctx, request.ToolCall); err != nil {
		return acp.RequestPermissionResponse{}, err
	}

	h.mu.Lock()
	tool := h.tools[string(request.ToolCall.ToolCallId)]
	if tool != nil && tool.isInternalSessionNotesCall() {
		h.mu.Unlock()
		optionID := defaultPermissionOption(request.Options, true)
		if optionID == "" {
			return acp.RequestPermissionResponse{
				Outcome: acp.NewRequestPermissionOutcomeCancelled(),
			}, nil
		}
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId(optionID)),
		}, nil
	}
	approval := ToolApprovalRequest{
		ID:         uuid.NewString(),
		ToolCallID: string(request.ToolCall.ToolCallId),
		ToolName:   acpToolName(tool.kind),
		Input:      tool.input(),
		Payload:    tool.approvalPayload(h.session),
		Hint:       tool.title,
		Options:    make([]ToolApprovalOption, 0, len(request.Options)),
	}
	h.mu.Unlock()
	for _, option := range request.Options {
		approval.Options = append(approval.Options, ToolApprovalOption{
			ID:   string(option.OptionId),
			Name: option.Name,
			Kind: string(option.Kind),
		})
	}
	if err := h.engine.registerToolApproval(h.session.ID, h.run.ID, approval); err != nil {
		return acp.RequestPermissionResponse{}, err
	}
	decisions, err := h.engine.waitForToolApprovals(
		h.ctx,
		h.session.ID,
		h.run.ID,
		[]ToolApprovalRequest{approval},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return acp.RequestPermissionResponse{
				Outcome: acp.NewRequestPermissionOutcomeCancelled(),
			}, nil
		}
		return acp.RequestPermissionResponse{}, err
	}
	decision := decisions[0]
	optionID := decision.OptionID
	if optionID == "" {
		optionID = defaultPermissionOption(request.Options, decision.Approved)
	}
	if optionID == "" {
		return acp.RequestPermissionResponse{
			Outcome: acp.NewRequestPermissionOutcomeCancelled(),
		}, nil
	}
	response := acp.RequestPermissionResponse{
		Outcome: acp.NewRequestPermissionOutcomeSelected(acp.PermissionOptionId(optionID)),
	}
	if decision.Reason != "" {
		response.Meta = map[string]any{"materialmind.dev/refusalReason": decision.Reason}
	}
	return response, nil
}

func (h *acpRunHandler) RequestElicitation(
	_ context.Context,
	request acpruntime.ElicitationRequest,
) (acpruntime.ElicitationResolution, error) {
	toolCallID := strings.TrimSpace(request.ToolCallID)
	if toolCallID == "" {
		toolCallID = "acp-elicitation:" + request.ID
	}
	agentID := ""
	if h.session.ACPAgentID != nil {
		agentID = *h.session.ACPAgentID
	}
	agentName := strings.TrimSpace(h.run.ACPAgentName)
	if agentName == "" {
		agentName = "ACP agent"
	}
	resolution, err := h.engine.requestMCPElicitation(h.ctx, mcpruntime.ElicitationRequest{
		ID:              request.ID,
		Source:          "acp",
		SessionID:       h.session.ID,
		ToolCallID:      toolCallID,
		ServerID:        agentID,
		ServerName:      agentName,
		Mode:            request.Mode,
		Message:         request.Message,
		URL:             request.URL,
		ElicitationID:   request.ElicitationID,
		RequestedSchema: request.RequestedSchema,
	})
	if err != nil {
		return acpruntime.ElicitationResolution{}, err
	}
	return acpruntime.ElicitationResolution{
		ID:      resolution.ID,
		Action:  resolution.Action,
		Content: resolution.Content,
	}, nil
}

func (h *acpRunHandler) CompleteElicitation(
	_ context.Context,
	requestID, elicitationID string,
) error {
	h.engine.hub.Publish(h.run.ID, "acp_elicitation_complete", map[string]string{
		"id":            requestID,
		"elicitationId": elicitationID,
	})
	return nil
}

func (h *acpRunHandler) TerminalOutput(event acpruntime.TerminalOutputEvent) {
	if event.Text == "" {
		return
	}
	h.mu.Lock()
	toolID := h.terminalTools[event.TerminalID]
	tool := h.tools[toolID]
	if tool == nil {
		pending := h.pendingTerminals[event.TerminalID]
		if pending == nil {
			pending = make(map[string]string)
			h.pendingTerminals[event.TerminalID] = pending
		}
		pending[event.Stream] = appendACPCommandOutput(pending[event.Stream], event.Text)
		h.mu.Unlock()
		return
	}
	tool.appendCommandOutput(event.Stream, event.Text)
	h.mu.Unlock()
	h.publishCommandOutput(toolID, event.Stream, event.Text)
}

func (h *acpRunHandler) appendText(
	ctx context.Context,
	kind string,
	messageID *string,
	text string,
) error {
	if text == "" {
		return nil
	}
	h.mu.Lock()
	id := ""
	if messageID != nil && *messageID != "" {
		id = h.run.ID + ":" + kind + ":" + *messageID
	} else if h.currentSegmentKind == kind {
		id = h.currentSegmentID
	}
	if id == "" {
		h.sequence++
		id = fmt.Sprintf("%s:%s:%d", h.run.ID, kind, h.sequence)
	}
	h.currentSegmentKind = kind
	h.currentSegmentID = id
	h.segments[id] += text
	fullText := h.segments[id]
	if kind == "message" {
		h.lastMessageID = id
	}
	h.mu.Unlock()

	_, err := h.engine.store.UpsertACPTranscriptItem(ctx, h.session.ID, store.TranscriptItem{
		ID:           id,
		InvocationID: h.run.InvocationID,
		Kind:         kind,
		Role:         "assistant",
		Text:         fullText,
		Provider:     store.RuntimeACP,
		Model:        h.run.ACPAgentName,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	eventType := "message_delta"
	if kind == "thought" {
		eventType = "thought_delta"
	}
	h.engine.hub.Publish(h.run.ID, eventType, map[string]string{"id": id, "text": text})
	return nil
}

func (h *acpRunHandler) savePlan(ctx context.Context, entries []acp.PlanEntry) error {
	plan := newACPPlanUpdate(h.run.ID, entries)
	lines := make([]string, 0, len(plan.Entries)+1)
	lines = append(lines, "Plan")
	for _, entry := range plan.Entries {
		marker := " "
		switch entry.Status {
		case string(acp.PlanEntryStatusCompleted):
			marker = "x"
		case string(acp.PlanEntryStatusInProgress):
			marker = ">"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", marker, entry.Content))
	}
	text := strings.Join(lines, "\n")
	_, err := h.engine.store.UpsertACPTranscriptItem(ctx, h.session.ID, store.TranscriptItem{
		ID:           plan.ID,
		InvocationID: h.run.InvocationID,
		Kind:         "plan",
		Role:         "assistant",
		Text:         text,
		ToolOutput:   map[string]any{"entries": plan.Entries},
		Provider:     store.RuntimeACP,
		Model:        h.run.ACPAgentName,
		CreatedAt:    time.Now().UTC(),
	})
	if err == nil {
		h.engine.hub.Publish(h.run.ID, "plan_update", plan)
	}
	return err
}

func newACPPlanUpdate(runID string, entries []acp.PlanEntry) acpPlanUpdate {
	plan := acpPlanUpdate{
		ID:      runID + ":plan",
		Entries: make([]acpPlanEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		plan.Entries = append(plan.Entries, acpPlanEntry{
			Content:  entry.Content,
			Priority: string(entry.Priority),
			Status:   string(entry.Status),
		})
	}
	return plan
}

func (h *acpRunHandler) saveConfigOptions(options []acp.SessionConfigOption) {
	if len(options) == 0 {
		return
	}
	preferred, err := decodeACPConfigOptions(h.session.ACPConfigOptions)
	if err == nil {
		options = mergeACPConfigOptions(preferred, options)
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return
	}
	if updated, err := h.engine.store.UpdateACPSessionConfigOptions(
		context.Background(),
		h.session.ID,
		encoded,
	); err == nil {
		h.session = updated
	}
}

func decodeACPConfigOptions(raw json.RawMessage) ([]acp.SessionConfigOption, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var options []acp.SessionConfigOption
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, fmt.Errorf("decode stored ACP session configuration: %w", err)
	}
	return options, nil
}

func mergeACPConfigOptions(
	preferred, reported []acp.SessionConfigOption,
) []acp.SessionConfigOption {
	if len(preferred) == 0 {
		return reported
	}
	preferredByID := make(map[string]acp.SessionConfigOption, len(preferred))
	for _, option := range preferred {
		preferredByID[acpConfigOptionID(option)] = option
	}
	merged := make([]acp.SessionConfigOption, 0, len(reported))
	for _, option := range reported {
		preference := preferredByID[acpConfigOptionID(option)]
		switch {
		case option.Select != nil && preference.Select != nil:
			copy := *option.Select
			if acpConfigSelectHasValue(copy.Options, preference.Select.CurrentValue) {
				copy.CurrentValue = preference.Select.CurrentValue
			}
			option.Select = &copy
		case option.Boolean != nil && preference.Boolean != nil:
			copy := *option.Boolean
			copy.CurrentValue = preference.Boolean.CurrentValue
			option.Boolean = &copy
		}
		merged = append(merged, option)
	}
	return merged
}

func acpConfigOptionID(option acp.SessionConfigOption) string {
	if option.Select != nil {
		return string(option.Select.Id)
	}
	if option.Boolean != nil {
		return string(option.Boolean.Id)
	}
	return ""
}

func acpConfigSelectHasValue(
	options acp.SessionConfigSelectOptions,
	value acp.SessionConfigValueId,
) bool {
	if options.Ungrouped != nil {
		for _, option := range *options.Ungrouped {
			if option.Value == value {
				return true
			}
		}
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			for _, option := range group.Options {
				if option.Value == value {
					return true
				}
			}
		}
	}
	return false
}

func (h *acpRunHandler) applyToolUpdate(ctx context.Context, update acp.ToolCallUpdate) error {
	h.mu.Lock()
	h.currentSegmentKind = ""
	h.currentSegmentID = ""
	id := string(update.ToolCallId)
	tool := h.tools[id]
	if tool == nil {
		tool = &acpToolState{id: id, kind: acp.ToolKindOther}
		h.tools[id] = tool
	}
	if update.Title != nil {
		tool.title = *update.Title
	}
	if update.Kind != nil {
		tool.kind = *update.Kind
	}
	statusChanged := update.Status != nil && tool.status != *update.Status
	if update.Status != nil {
		tool.status = *update.Status
	}
	if update.RawInput != nil {
		tool.rawInput = update.RawInput
	}
	if update.RawOutput != nil {
		tool.rawOutput = update.RawOutput
	}
	if update.Content != nil {
		tool.content = update.Content
	}
	if update.Locations != nil {
		tool.locations = update.Locations
	}
	if tool.isInternalSessionNotesCall() {
		h.mu.Unlock()
		return nil
	}
	toolName := acpToolName(tool.kind)
	pendingOutput := h.attachToolTerminals(tool)
	metadataOutput := tool.streamMetadataDeltas(update.Meta)
	streamOutput := tool.streamDeltas()
	input := tool.input()
	output := tool.output()
	publishCall := !tool.callPublished
	if publishCall {
		tool.callPublished = true
	}
	publishResult := tool.status == acp.ToolCallStatusCompleted ||
		tool.status == acp.ToolCallStatusFailed
	statusUpdate := acpToolStatusUpdate{ID: id, Status: tool.status}
	h.mu.Unlock()

	_, err := h.engine.store.UpsertACPTranscriptItem(ctx, h.session.ID, store.TranscriptItem{
		ID:           h.run.ID + ":tool:" + id + ":call",
		InvocationID: h.run.InvocationID,
		Kind:         "tool_call",
		ToolName:     toolName,
		ToolCallID:   id,
		ToolInput:    input,
		Provider:     store.RuntimeACP,
		Model:        h.run.ACPAgentName,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	if publishCall {
		h.engine.hub.Publish(h.run.ID, "tool_call", map[string]any{
			"id": id, "name": toolName, "input": input,
		})
	}
	if statusChanged {
		h.engine.hub.Publish(h.run.ID, "tool_status", statusUpdate)
	}
	for stream, text := range pendingOutput {
		h.publishCommandOutput(id, stream, text)
	}
	for stream, text := range metadataOutput {
		h.publishCommandOutput(id, stream, text)
	}
	for stream, text := range streamOutput {
		h.publishCommandOutput(id, stream, text)
	}
	if !publishResult {
		return nil
	}
	_, err = h.engine.store.UpsertACPTranscriptItem(ctx, h.session.ID, store.TranscriptItem{
		ID:           h.run.ID + ":tool:" + id + ":result",
		InvocationID: h.run.InvocationID,
		Kind:         "tool_result",
		ToolName:     toolName,
		ToolCallID:   id,
		ToolOutput:   output,
		Provider:     store.RuntimeACP,
		Model:        h.run.ACPAgentName,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	h.engine.hub.Publish(h.run.ID, "tool_result", map[string]any{
		"id": id, "name": toolName, "output": output,
	})
	return nil
}

func (tool *acpToolState) isInternalSessionNotesCall() bool {
	input := toStringMap(tool.rawInput)
	server, _ := input["server"].(string)
	name, _ := input["tool"].(string)
	if server == acpinternal.ServerName &&
		(name == acpinternal.ToolReadSessionNotes || name == acpinternal.ToolUpdateSessionNotes) {
		return true
	}
	return tool.title == "mcp."+acpinternal.ServerName+"."+acpinternal.ToolReadSessionNotes ||
		tool.title == "mcp."+acpinternal.ServerName+"."+acpinternal.ToolUpdateSessionNotes
}

func (h *acpRunHandler) attachToolTerminals(tool *acpToolState) map[string]string {
	pendingOutput := make(map[string]string)
	for _, content := range tool.content {
		if content.Terminal == nil {
			continue
		}
		terminalID := content.Terminal.TerminalId
		h.terminalTools[terminalID] = tool.id
		for stream, text := range h.pendingTerminals[terminalID] {
			tool.appendCommandOutput(stream, text)
			pendingOutput[stream] = appendACPCommandOutput(pendingOutput[stream], text)
		}
		delete(h.pendingTerminals, terminalID)
	}
	return pendingOutput
}

func (h *acpRunHandler) publishCommandOutput(toolID, stream, text string) {
	if text == "" {
		return
	}
	h.engine.hub.Publish(h.run.ID, "command_output", map[string]string{
		"toolCallId": toolID,
		"stream":     stream,
		"text":       text,
	})
}

func (h *acpRunHandler) finalMessage() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.segments[h.lastMessageID]
}

func (tool *acpToolState) input() map[string]any {
	input := toStringMap(tool.rawInput)
	copyACPField(input, "cwd", "workingDirectory")
	input["title"] = tool.title
	input["kind"] = string(tool.kind)
	if len(tool.locations) > 0 {
		locations := make([]map[string]any, 0, len(tool.locations))
		for _, location := range tool.locations {
			item := map[string]any{"path": location.Path}
			if location.Line != nil {
				item["line"] = *location.Line
			}
			locations = append(locations, item)
		}
		input["locations"] = locations
		if _, ok := input["path"]; !ok {
			input["path"] = tool.locations[0].Path
		}
	}
	files := tool.diffFiles()
	if len(files) > 0 {
		input["files"] = files
	}
	return input
}

func (tool *acpToolState) output() map[string]any {
	output := toStringMap(tool.rawOutput)
	copyACPField(output, "exit_code", "exitCode")
	copyACPField(output, "formatted_output", "stdout")
	copyACPField(output, "content_type", "contentType")
	copyACPField(output, "status_code", "httpStatus")
	copyACPField(output, "final_url", "finalUrl")
	if _, ok := output["stdout"]; !ok && tool.stdout != "" {
		output["stdout"] = tool.stdout
	}
	if _, ok := output["stderr"]; !ok && tool.stderr != "" {
		output["stderr"] = tool.stderr
	}
	output["state"] = string(tool.status)
	output["title"] = tool.title
	output["kind"] = string(tool.kind)
	files := tool.diffFiles()
	if len(files) > 0 {
		output["files"] = files
	}
	content := tool.textContent()
	if len(content) > 0 {
		output["content"] = content
	}
	if tool.status == acp.ToolCallStatusFailed && tool.kind != acp.ToolKindExecute {
		if _, ok := output["error"]; !ok {
			output["error"] = tool.title
		}
	}
	return output
}

func (tool *acpToolState) appendCommandOutput(stream, text string) {
	switch stream {
	case "stderr":
		tool.stderr = appendACPCommandOutput(tool.stderr, text)
	default:
		tool.stdout = appendACPCommandOutput(tool.stdout, text)
	}
}

func (tool *acpToolState) streamDeltas() map[string]string {
	if tool.kind != acp.ToolKindExecute ||
		tool.status == acp.ToolCallStatusCompleted ||
		tool.status == acp.ToolCallStatusFailed ||
		tool.hasTerminal() {
		return nil
	}
	output := toStringMap(tool.rawOutput)
	copyACPField(output, "formatted_output", "stdout")
	result := make(map[string]string)
	tool.streamedOut, result["stdout"] = cumulativeACPOutputDelta(
		tool.streamedOut,
		stringACPField(output, "stdout"),
	)
	tool.streamedErr, result["stderr"] = cumulativeACPOutputDelta(
		tool.streamedErr,
		stringACPField(output, "stderr"),
	)
	for stream, text := range result {
		if text == "" {
			delete(result, stream)
			continue
		}
		tool.appendCommandOutput(stream, text)
	}
	return result
}

func (tool *acpToolState) streamMetadataDeltas(metadata map[string]any) map[string]string {
	text, cumulative := acpTerminalOutputMetadata(metadata)
	if text == "" {
		return nil
	}
	if cumulative {
		current, delta := cumulativeACPOutputDelta(tool.streamedMeta, text)
		if delta == "" {
			return nil
		}
		tool.streamedMeta = current
		text = delta
	} else {
		tool.streamedMeta = appendACPCommandOutput(tool.streamedMeta, text)
	}

	previousLength := len(tool.stdout)
	tool.appendCommandOutput("stdout", text)
	if len(tool.stdout) == previousLength {
		return nil
	}
	return map[string]string{"stdout": tool.stdout[previousLength:]}
}

func (tool *acpToolState) hasTerminal() bool {
	for _, content := range tool.content {
		if content.Terminal != nil {
			return true
		}
	}
	return false
}

func acpTerminalOutputMetadata(metadata map[string]any) (string, bool) {
	for _, candidate := range []struct {
		key        string
		cumulative bool
	}{
		{key: "terminal_output_delta"},
		{key: "terminal_output", cumulative: true},
	} {
		value, ok := metadata[candidate.key]
		if !ok {
			continue
		}
		if text := stringACPField(toStringMap(value), "data"); text != "" {
			return text, candidate.cumulative
		}
	}
	return "", false
}

func cumulativeACPOutputDelta(previous, current string) (string, string) {
	if current == previous {
		return previous, ""
	}
	if strings.HasPrefix(current, previous) {
		return current, current[len(previous):]
	}
	return previous, ""
}

func stringACPField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func appendACPCommandOutput(current, addition string) string {
	const maxOutputBytes = 512 * 1024
	remaining := maxOutputBytes - len(current)
	if remaining <= 0 {
		return current
	}
	if len(addition) > remaining {
		addition = addition[:remaining]
		for len(addition) > 0 && !utf8.ValidString(addition) {
			addition = addition[:len(addition)-1]
		}
	}
	return current + addition
}

func mergeACPMetadata(outer, inner map[string]any) map[string]any {
	if len(outer) == 0 {
		return inner
	}
	merged := maps.Clone(outer)
	maps.Copy(merged, inner)
	return merged
}

func (tool *acpToolState) approvalPayload(sessionRecord store.AppSession) map[string]any {
	input := tool.input()
	payload := maps.Clone(input)
	switch tool.kind {
	case acp.ToolKindEdit, acp.ToolKindDelete, acp.ToolKindMove:
		payload["kind"] = "file_edit"
		payload["files"] = tool.diffFiles()
	case acp.ToolKindFetch:
		payload["kind"] = "fetch_url"
	case acp.ToolKindExecute:
		payload["kind"] = "run_command"
		if _, ok := payload["workingDirectory"]; !ok {
			payload["workingDirectory"] = "."
		}
		if _, ok := payload["timeoutSeconds"]; !ok {
			payload["timeoutSeconds"] = 120
		}
	default:
		payload["kind"] = "acp_tool"
		payload["toolKind"] = string(tool.kind)
	}
	payload["acpSessionId"] = sessionRecord.ACPSessionID
	return payload
}

func (tool *acpToolState) diffFiles() []map[string]any {
	files := make([]map[string]any, 0)
	for _, content := range tool.content {
		if content.Diff == nil {
			continue
		}
		operation := "update"
		oldPath := "a/" + content.Diff.Path
		newPath := "b/" + content.Diff.Path
		oldText := ""
		if content.Diff.OldText == nil {
			operation = "create"
			oldPath = "/dev/null"
		} else {
			oldText = *content.Diff.OldText
		}
		if tool.kind == acp.ToolKindDelete {
			operation = "delete"
			newPath = "/dev/null"
		}
		diff := udiff.Unified(oldPath, newPath, oldText, content.Diff.NewText)
		if diff == "" {
			diff = fmt.Sprintf("--- %s\n+++ %s\n", oldPath, newPath)
		}
		files = append(files, map[string]any{
			"operation": operation,
			"path":      content.Diff.Path,
			"diff":      diff,
		})
	}
	return files
}

func (tool *acpToolState) textContent() []string {
	result := make([]string, 0)
	for _, content := range tool.content {
		if content.Content == nil {
			continue
		}
		if text := contentBlockText(content.Content.Content); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func acpToolName(kind acp.ToolKind) string {
	switch kind {
	case acp.ToolKindRead:
		return "read_file"
	case acp.ToolKindEdit:
		return "edit_file"
	case acp.ToolKindDelete:
		return "edit_file"
	case acp.ToolKindMove:
		return "edit_file"
	case acp.ToolKindSearch:
		return "grep"
	case acp.ToolKindExecute:
		return "run_command"
	case acp.ToolKindThink:
		return "acp_think"
	case acp.ToolKindFetch:
		return "fetch_url"
	case acp.ToolKindSwitchMode:
		return "acp_switch_mode"
	default:
		return "acp_tool"
	}
}

func contentBlockText(content acp.ContentBlock) string {
	if content.Text == nil {
		return ""
	}
	return content.Text.Text
}

func toStringMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return maps.Clone(typed)
	}
	if value == nil {
		return make(map[string]any)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"value": fmt.Sprint(value)}
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return map[string]any{"value": value}
	}
	return result
}

func copyACPField(target map[string]any, source, destination string) {
	if _, exists := target[destination]; exists {
		return
	}
	if value, exists := target[source]; exists {
		target[destination] = value
	}
}

func defaultPermissionOption(options []acp.PermissionOption, approved bool) string {
	preferred := []acp.PermissionOptionKind{
		acp.PermissionOptionKindRejectOnce,
		acp.PermissionOptionKindRejectAlways,
	}
	if approved {
		preferred = []acp.PermissionOptionKind{
			acp.PermissionOptionKindAllowOnce,
			acp.PermissionOptionKindAllowAlways,
		}
	}
	for _, kind := range preferred {
		for _, option := range options {
			if option.Kind == kind {
				return string(option.OptionId)
			}
		}
	}
	return ""
}
