package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"materialmind/internal/acpruntime"
	"materialmind/internal/store"
)

func (e *Engine) InspectACPAgent(
	ctx context.Context,
	agentID string,
) (acpruntime.AgentInspection, error) {
	agentRecord, err := e.store.GetACPAgent(ctx, agentID)
	if err != nil {
		return acpruntime.AgentInspection{}, err
	}
	return e.acpManager.InspectAgent(ctx, agentRecord)
}

func (e *Engine) AuthenticateACPAgent(
	ctx context.Context,
	agentID, methodID string,
) (acpruntime.AgentInspection, error) {
	agentRecord, err := e.store.GetACPAgent(ctx, agentID)
	if err != nil {
		return acpruntime.AgentInspection{}, err
	}
	return e.acpManager.Authenticate(ctx, agentRecord, methodID)
}

func (e *Engine) LogoutACPAgent(
	ctx context.Context,
	agentID string,
) (acpruntime.AgentInspection, error) {
	agentRecord, err := e.store.GetACPAgent(ctx, agentID)
	if err != nil {
		return acpruntime.AgentInspection{}, err
	}
	return e.acpManager.Logout(ctx, agentRecord)
}

func (e *Engine) ListACPAgentSessions(
	ctx context.Context,
	agentID, workingDirectory string,
) ([]acpruntime.RemoteSession, error) {
	agentRecord, err := e.store.GetACPAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return e.acpManager.ListSessions(ctx, agentRecord, workingDirectory)
}

func (e *Engine) ImportACPSession(
	ctx context.Context,
	agentID, remoteSessionID, workspaceID, title string,
) (store.AppSession, error) {
	workspace, err := e.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return store.AppSession{}, err
	}
	if !workspace.Available {
		return store.AppSession{}, fmt.Errorf("%w: workspace directory is unavailable", store.ErrInvalidInput)
	}
	agentRecord, err := e.store.GetACPAgent(ctx, agentID)
	if err != nil {
		return store.AppSession{}, err
	}
	remoteSessionID = strings.TrimSpace(remoteSessionID)
	remoteSessions, err := e.acpManager.ListSessions(ctx, agentRecord, workspace.RootPath)
	if err != nil {
		return store.AppSession{}, err
	}
	var remote *acpruntime.RemoteSession
	for index := range remoteSessions {
		if remoteSessions[index].ID == remoteSessionID {
			remote = &remoteSessions[index]
			break
		}
	}
	if remote == nil {
		return store.AppSession{}, fmt.Errorf("%w: ACP session %q was not listed for this workspace", store.ErrInvalidInput, remoteSessionID)
	}
	if filepath.Clean(remote.WorkingDirectory) != filepath.Clean(workspace.RootPath) {
		return store.AppSession{}, fmt.Errorf("%w: ACP session working directory does not match the workspace", store.ErrInvalidInput)
	}
	existing, err := e.store.ListAllSessions(ctx)
	if err != nil {
		return store.AppSession{}, err
	}
	for _, sessionRecord := range existing {
		if sessionRecord.ACPAgentID != nil &&
			*sessionRecord.ACPAgentID == agentID &&
			sessionRecord.ACPSessionID == remoteSessionID {
			return store.AppSession{}, fmt.Errorf("%w: ACP session is already imported", store.ErrConflict)
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = strings.TrimSpace(remote.Title)
	}
	if title == "" {
		title = "Imported ACP session"
	}
	item, err := e.store.CreateACPSession(ctx, workspaceID, title, agentID)
	if err != nil {
		return store.AppSession{}, err
	}
	rollback := func() {
		_ = e.store.DeleteSession(context.Background(), item.ID)
		e.revokeACPInternalMCPToken(item.ID)
	}
	mcpServers, err := e.store.GetSessionMCPServers(ctx, item.ID)
	if err != nil {
		rollback()
		return store.AppSession{}, err
	}
	permissions, err := e.store.GetSessionToolPermissions(ctx, item.ID)
	if err != nil {
		rollback()
		return store.AppSession{}, err
	}
	state, err := e.acpManager.LoadExistingSession(
		ctx,
		agentRecord,
		remoteSessionID,
		workspace.RootPath,
		acpAdditionalDirectories(workspace.RootPath, permissions),
		mcpServers,
		e.acpInternalMCPToken(item.ID),
	)
	if err != nil {
		rollback()
		return store.AppSession{}, err
	}
	configOptions, err := json.Marshal(state.ConfigOptions)
	if err != nil {
		rollback()
		return store.AppSession{}, fmt.Errorf("encode ACP session configuration: %w", err)
	}
	item, err = e.store.UpdateACPSessionConnection(ctx, item.ID, state.ID, configOptions)
	if err != nil {
		rollback()
		return store.AppSession{}, err
	}
	return item, nil
}
