package engine

import (
	"context"
	"fmt"

	"materialmind/internal/mcpruntime"
	"materialmind/internal/store"
)

type MCPToolCancellation struct {
	ToolCallID string `json:"toolCallId"`
	Cancelled  bool   `json:"cancelled"`
}

func (e *Engine) ListMCPServerTools(
	ctx context.Context,
	serverID string,
) (mcpruntime.ToolCatalog, error) {
	server, err := e.store.GetMCPServer(ctx, serverID)
	if err != nil {
		return mcpruntime.ToolCatalog{}, err
	}
	return e.mcpManager.ListServerTools(ctx, server)
}

func (e *Engine) SessionMCPContent(
	ctx context.Context,
	sessionID string,
) ([]mcpruntime.SessionContentServer, error) {
	sessionRecord, workspace, servers, err := e.sessionMCPContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	_ = sessionRecord
	return e.mcpManager.SessionContent(ctx, sessionID, workspace.RootPath, servers), nil
}

func (e *Engine) ReadMCPResource(
	ctx context.Context,
	sessionID, serverID, uri string,
) (mcpruntime.ResourceRead, error) {
	_, workspace, servers, err := e.sessionMCPContext(ctx, sessionID)
	if err != nil {
		return mcpruntime.ResourceRead{}, err
	}
	return e.mcpManager.ReadSessionResource(
		ctx,
		sessionID,
		workspace.RootPath,
		serverID,
		uri,
		servers,
	)
}

func (e *Engine) GetMCPPrompt(
	ctx context.Context,
	sessionID, serverID, name string,
	arguments map[string]string,
) (mcpruntime.PromptExpansion, error) {
	_, workspace, servers, err := e.sessionMCPContext(ctx, sessionID)
	if err != nil {
		return mcpruntime.PromptExpansion{}, err
	}
	return e.mcpManager.GetSessionPrompt(
		ctx,
		sessionID,
		workspace.RootPath,
		serverID,
		name,
		arguments,
		servers,
	)
}

func (e *Engine) sessionMCPContext(
	ctx context.Context,
	sessionID string,
) (store.AppSession, store.Workspace, []store.SessionMCPServer, error) {
	sessionRecord, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		return store.AppSession{}, store.Workspace{}, nil, err
	}
	workspace, err := e.store.GetWorkspace(ctx, sessionRecord.WorkspaceID)
	if err != nil {
		return store.AppSession{}, store.Workspace{}, nil, err
	}
	if !workspace.Available {
		return store.AppSession{}, store.Workspace{}, nil, fmt.Errorf(
			"%w: workspace directory is unavailable",
			store.ErrInvalidInput,
		)
	}
	servers, err := e.store.GetSessionMCPServers(ctx, sessionID)
	if err != nil {
		return store.AppSession{}, store.Workspace{}, nil, err
	}
	return sessionRecord, workspace, servers, nil
}

func (e *Engine) StartMCPOAuth(
	ctx context.Context,
	serverID string,
) (mcpruntime.OAuthStart, error) {
	return e.mcpManager.StartOAuth(ctx, serverID)
}

func (e *Engine) CompleteMCPOAuth(state, code, oauthError string) error {
	return e.mcpManager.CompleteOAuth(state, code, oauthError)
}

func (e *Engine) MCPOAuthStatus(
	ctx context.Context,
	serverID string,
) (mcpruntime.OAuthStatus, error) {
	return e.mcpManager.OAuthStatus(ctx, serverID)
}

func (e *Engine) DisconnectMCPOAuth(ctx context.Context, serverID string) error {
	return e.mcpManager.DisconnectOAuth(ctx, serverID)
}

func (e *Engine) MCPServerChanged(serverID string) {
	e.mcpManager.CloseServer(serverID)
}

func (e *Engine) DeleteMCPServer(ctx context.Context, serverID string) error {
	e.mcpManager.CloseServer(serverID)
	if err := e.store.DeleteMCPServer(ctx, serverID); err != nil {
		return err
	}
	return e.mcpManager.ForgetServer(serverID)
}

func (e *Engine) CancelMCPToolCall(
	ctx context.Context,
	runID, toolCallID string,
) (MCPToolCancellation, error) {
	runRecord, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return MCPToolCancellation{}, err
	}
	e.mu.Lock()
	active := e.active[runRecord.SessionID]
	isActive := active != nil && active.runID == runID
	e.mu.Unlock()
	if !isActive {
		return MCPToolCancellation{}, fmt.Errorf(
			"%w: run is no longer active",
			store.ErrConflict,
		)
	}
	if !e.mcpManager.CancelToolCall(runRecord.SessionID, toolCallID) {
		return MCPToolCancellation{}, fmt.Errorf(
			"%w: MCP tool call is no longer running",
			store.ErrConflict,
		)
	}
	return MCPToolCancellation{ToolCallID: toolCallID, Cancelled: true}, nil
}

func (e *Engine) publishMCPEvent(event mcpruntime.Event) {
	if event.SessionID == "" {
		return
	}
	e.mu.Lock()
	active := e.active[event.SessionID]
	runID := ""
	if active != nil {
		runID = active.runID
	}
	e.mu.Unlock()
	if runID != "" {
		e.hub.Publish(runID, event.Type, event)
	}
}

func (e *Engine) ReplaceSessionMCPServers(
	ctx context.Context,
	sessionID string,
	assignments []store.MCPServerAssignment,
) ([]store.SessionMCPServer, error) {
	e.mu.Lock()
	_, running := e.active[sessionID]
	e.mu.Unlock()
	if running {
		return nil, ErrSessionBusy
	}
	sessionRecord, err := e.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sessionRecord.RuntimeType == store.RuntimeACP && sessionRecord.ACPSessionID != "" {
		return nil, fmt.Errorf(
			"%w: MCP servers for an established ACP session cannot be changed",
			store.ErrConflict,
		)
	}
	servers, err := e.store.ReplaceSessionMCPServers(ctx, sessionID, assignments)
	if err != nil {
		return nil, err
	}
	e.mcpManager.CloseSession(sessionID)
	return servers, nil
}
