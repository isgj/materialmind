package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strings"

	"materialmind/internal/agentmodel"
	"materialmind/internal/credentialstore"
	"materialmind/internal/engine"
	"materialmind/internal/llmcredentials"
	"materialmind/internal/mcpruntime"
	"materialmind/internal/store"
	"materialmind/internal/toolpolicy"
)

const (
	maxRequestBody        = 1 << 20
	maxRunAttachmentCount = 10
	maxRunAttachmentSize  = 10 << 20
	maxRunAttachmentsSize = 25 << 20
	maxMultipartRunSize   = maxRunAttachmentsSize + (1 << 20)
)

type startRunRequest struct {
	Message         string                `json:"message"`
	LLMModelID      string                `json:"llmModelId"`
	ReasoningEffort *string               `json:"reasoningEffort"`
	Attachments     []store.RunAttachment `json:"-"`
}

type API struct {
	store       *store.Store
	engine      *engine.Engine
	credentials credentialstore.Store
}

func New(dataStore *store.Store, runEngine *engine.Engine) http.Handler {
	credentials := credentialstore.Store(credentialstore.NewMemory())
	if runEngine != nil {
		credentials = runEngine.Credentials()
	}
	api := &API{store: dataStore, engine: runEngine, credentials: credentials}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", api.health)
	mux.HandleFunc("POST /api/internal/acp-session-tools", api.callACPInternalTool)
	mux.HandleFunc("GET /api/storage-settings", api.getStorageSettings)
	mux.HandleFunc("PUT /api/storage-settings", api.updateStorageSettings)
	mux.HandleFunc("GET /api/backup", api.downloadBackup)
	mux.HandleFunc("GET /api/workspaces", api.listWorkspaces)
	mux.HandleFunc("POST /api/workspaces", api.createWorkspace)
	mux.HandleFunc("PATCH /api/workspaces/{id}", api.updateWorkspace)
	mux.HandleFunc("DELETE /api/workspaces/{id}", api.deleteWorkspace)
	mux.HandleFunc("GET /api/workspaces/{id}/tool-permissions", api.getWorkspaceToolPermissions)
	mux.HandleFunc("PUT /api/workspaces/{id}/tool-permissions", api.replaceWorkspaceToolPermissions)
	mux.HandleFunc("GET /api/llm-providers", api.listLLMProviders)
	mux.HandleFunc("POST /api/llm-providers", api.createLLMProvider)
	mux.HandleFunc("PATCH /api/llm-providers/{id}", api.updateLLMProvider)
	mux.HandleFunc("DELETE /api/llm-providers/{id}", api.deleteLLMProvider)
	mux.HandleFunc("GET /api/llm-providers/{id}/available-models", api.listAvailableLLMModels)
	mux.HandleFunc("GET /api/llm-models", api.listLLMModels)
	mux.HandleFunc("POST /api/llm-models", api.createLLMModel)
	mux.HandleFunc("PATCH /api/llm-models/{id}", api.updateLLMModel)
	mux.HandleFunc("DELETE /api/llm-models/{id}", api.deleteLLMModel)
	mux.HandleFunc("GET /api/acp-agents", api.listACPAgents)
	mux.HandleFunc("POST /api/acp-agents", api.createACPAgent)
	mux.HandleFunc("PATCH /api/acp-agents/{id}", api.updateACPAgent)
	mux.HandleFunc("DELETE /api/acp-agents/{id}", api.deleteACPAgent)
	mux.HandleFunc("GET /api/acp-agents/{id}/capabilities", api.inspectACPAgent)
	mux.HandleFunc("POST /api/acp-agents/{id}/authenticate", api.authenticateACPAgent)
	mux.HandleFunc("POST /api/acp-agents/{id}/logout", api.logoutACPAgent)
	mux.HandleFunc("GET /api/acp-agents/{id}/sessions", api.listACPAgentSessions)
	mux.HandleFunc("POST /api/acp-agents/{id}/sessions/import", api.importACPAgentSession)
	mux.HandleFunc("GET /api/mcp-servers", api.listMCPServers)
	mux.HandleFunc("POST /api/mcp-servers", api.createMCPServer)
	mux.HandleFunc("PATCH /api/mcp-servers/{id}", api.updateMCPServer)
	mux.HandleFunc("DELETE /api/mcp-servers/{id}", api.deleteMCPServer)
	mux.HandleFunc("PUT /api/mcp-servers/{id}/defaults", api.updateMCPServerDefaults)
	mux.HandleFunc("GET /api/mcp-servers/{id}/tools", api.listMCPServerTools)
	mux.HandleFunc("POST /api/mcp-servers/{id}/oauth/start", api.startMCPOAuth)
	mux.HandleFunc("GET /api/mcp-servers/{id}/oauth/status", api.mcpOAuthStatus)
	mux.HandleFunc("DELETE /api/mcp-servers/{id}/oauth", api.disconnectMCPOAuth)
	mux.HandleFunc("GET /api/mcp-oauth/callback", api.completeMCPOAuth)
	mux.HandleFunc("GET /api/workspaces/{id}/mcp-servers", api.getWorkspaceMCPServers)
	mux.HandleFunc("PUT /api/workspaces/{id}/mcp-servers", api.replaceWorkspaceMCPServers)
	mux.HandleFunc("GET /api/sessions", api.listAllSessions)
	mux.HandleFunc("GET /api/workspaces/{workspaceID}/sessions", api.listSessions)
	mux.HandleFunc("POST /api/sessions", api.createSession)
	mux.HandleFunc("GET /api/sessions/{id}", api.getSession)
	mux.HandleFunc("GET /api/sessions/{id}/notes", api.getSessionNotes)
	mux.HandleFunc("PATCH /api/sessions/{id}", api.updateSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", api.deleteSession)
	mux.HandleFunc("PUT /api/sessions/{id}/acp-config/{configID}", api.setACPSessionConfigOption)
	mux.HandleFunc("GET /api/sessions/{id}/tool-permissions", api.getSessionToolPermissions)
	mux.HandleFunc("PUT /api/sessions/{id}/tool-permissions", api.replaceSessionToolPermissions)
	mux.HandleFunc("GET /api/sessions/{id}/mcp-servers", api.getSessionMCPServers)
	mux.HandleFunc("PUT /api/sessions/{id}/mcp-servers", api.replaceSessionMCPServers)
	mux.HandleFunc("GET /api/sessions/{id}/mcp-content", api.listSessionMCPContent)
	mux.HandleFunc("POST /api/sessions/{id}/mcp-resources/read", api.readSessionMCPResource)
	mux.HandleFunc("POST /api/sessions/{id}/mcp-prompts/get", api.getSessionMCPPrompt)
	mux.HandleFunc("GET /api/sessions/{id}/transcript", api.transcript)
	mux.HandleFunc("GET /api/sessions/{id}/transcript-page", api.transcriptPage)
	mux.HandleFunc("GET /api/sessions/{id}/runs", api.listRuns)
	mux.HandleFunc("POST /api/sessions/{id}/runs", api.startRun)
	mux.HandleFunc("POST /api/runs/{id}/cancel", api.cancelRun)
	mux.HandleFunc(
		"POST /api/runs/{id}/mcp-tools/{toolCallID}/cancel",
		api.cancelMCPToolCall,
	)
	mux.HandleFunc(
		"POST /api/runs/{id}/mcp-elicitations/{requestID}",
		api.resolveMCPElicitation,
	)
	mux.HandleFunc("POST /api/runs/{id}/tool-approvals/{approvalID}", api.resolveToolApproval)
	mux.HandleFunc("POST /api/runs/{id}/user-inputs/{requestID}", api.resolveUserInput)
	mux.HandleFunc("GET /api/runs/{id}/events", api.streamRun)
	mux.HandleFunc("GET /api/run-attachments/{id}", api.getRunAttachment)
	return mux
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type mcpServerRequest struct {
	Name                    string                     `json:"name"`
	Transport               string                     `json:"transport"`
	Command                 string                     `json:"command"`
	Arguments               []string                   `json:"arguments"`
	Environment             []store.MCPVariableBinding `json:"environment"`
	URL                     string                     `json:"url"`
	Headers                 []store.MCPVariableBinding `json:"headers"`
	AuthType                string                     `json:"authType"`
	BearerTokenEnvVar       string                     `json:"bearerTokenEnvVar"`
	OAuthClientMode         string                     `json:"oauthClientMode"`
	OAuthClientID           string                     `json:"oauthClientId"`
	OAuthClientSecretEnvVar string                     `json:"oauthClientSecretEnvVar"`
	OAuthScopes             []string                   `json:"oauthScopes"`
}

type mcpAssignmentRequest struct {
	Assignments []mcpAssignmentInput `json:"assignments"`
}

type mcpDefaultsRequest struct {
	Enabled          bool                      `json:"enabled"`
	ConfirmationMode string                    `json:"confirmationMode"`
	ToolPermissions  []store.MCPToolPermission `json:"toolPermissions"`
}

type mcpAssignmentInput struct {
	ServerID         string                    `json:"serverId"`
	Enabled          bool                      `json:"enabled"`
	ConfirmationMode string                    `json:"confirmationMode"`
	ToolPermissions  []store.MCPToolPermission `json:"toolPermissions"`
}

func (a *API) listMCPServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListMCPServers(r.Context())
	writeResult(w, items, err)
}

func (a *API) createMCPServer(w http.ResponseWriter, r *http.Request) {
	var request mcpServerRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.CreateMCPServer(r.Context(), request.mcpServer())
	writeCreated(w, item, err)
}

func (a *API) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	var request mcpServerRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	id := r.PathValue("id")
	previous, previousErr := a.store.GetMCPServer(r.Context(), id)
	if previousErr != nil {
		writeAPIError(w, previousErr)
		return
	}
	item, err := a.store.UpdateMCPServer(r.Context(), id, request.mcpServer())
	if err == nil && a.engine != nil {
		a.engine.MCPServerChanged(id)
		if previous.AuthType == store.MCPAuthOAuth && oauthMCPConfigChanged(previous, item) {
			err = a.engine.DisconnectMCPOAuth(r.Context(), id)
		}
	}
	writeResult(w, item, err)
}

func (a *API) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	var err error
	if a.engine == nil {
		err = a.store.DeleteMCPServer(r.Context(), r.PathValue("id"))
	} else {
		err = a.engine.DeleteMCPServer(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) updateMCPServerDefaults(w http.ResponseWriter, r *http.Request) {
	var request mcpDefaultsRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.UpdateMCPServerDefaults(
		r.Context(),
		r.PathValue("id"),
		request.Enabled,
		request.ConfirmationMode,
		request.ToolPermissions,
	)
	writeResult(w, item, err)
}

func (a *API) listMCPServerTools(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "agent engine is unavailable")
		return
	}
	items, err := a.engine.ListMCPServerTools(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) startMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "agent engine is unavailable")
		return
	}
	result, err := a.engine.StartMCPOAuth(r.Context(), r.PathValue("id"))
	writeResult(w, result, err)
}

func (a *API) mcpOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "agent engine is unavailable")
		return
	}
	result, err := a.engine.MCPOAuthStatus(r.Context(), r.PathValue("id"))
	writeResult(w, result, err)
}

func (a *API) disconnectMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "agent engine is unavailable")
		return
	}
	if err := a.engine.DisconnectMCPOAuth(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) completeMCPOAuth(w http.ResponseWriter, r *http.Request) {
	if a.engine == nil {
		writeOAuthCallbackPage(w, "Authorization failed", "The agent engine is unavailable.", false)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	oauthError := strings.TrimSpace(r.URL.Query().Get("error"))
	if state == "" || code == "" && oauthError == "" {
		writeOAuthCallbackPage(
			w,
			"Authorization failed",
			"The OAuth callback is missing required values.",
			false,
		)
		return
	}
	if err := a.engine.CompleteMCPOAuth(state, code, oauthError); err != nil {
		writeOAuthCallbackPage(w, "Authorization failed", err.Error(), false)
		return
	}
	if oauthError != "" {
		writeOAuthCallbackPage(
			w,
			"Authorization declined",
			"The authorization server declined the request.",
			false,
		)
		return
	}
	writeOAuthCallbackPage(
		w,
		"Authorization received",
		"MaterialMind is completing the connection. You can close this window.",
		true,
	)
}

func (a *API) getWorkspaceMCPServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.GetWorkspaceMCPServers(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) replaceWorkspaceMCPServers(w http.ResponseWriter, r *http.Request) {
	var request mcpAssignmentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	items, err := a.store.ReplaceWorkspaceMCPServers(
		r.Context(),
		r.PathValue("id"),
		request.assignments(),
	)
	writeResult(w, items, err)
}

func (a *API) getSessionMCPServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.sessionMCPAssignments(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) replaceSessionMCPServers(w http.ResponseWriter, r *http.Request) {
	var request mcpAssignmentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if a.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "agent engine is unavailable")
		return
	}
	_, err := a.engine.ReplaceSessionMCPServers(
		r.Context(),
		r.PathValue("id"),
		request.assignments(),
	)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	items, err := a.sessionMCPAssignments(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) listSessionMCPContent(w http.ResponseWriter, r *http.Request) {
	items, err := a.engine.SessionMCPContent(r.Context(), r.PathValue("id"))
	writeResult(w, items, err)
}

func (a *API) readSessionMCPResource(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ServerID string `json:"serverId"`
		URI      string `json:"uri"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	result, err := a.engine.ReadMCPResource(
		r.Context(),
		r.PathValue("id"),
		request.ServerID,
		request.URI,
	)
	writeResult(w, result, err)
}

func (a *API) getSessionMCPPrompt(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ServerID  string            `json:"serverId"`
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	result, err := a.engine.GetMCPPrompt(
		r.Context(),
		r.PathValue("id"),
		request.ServerID,
		request.Name,
		request.Arguments,
	)
	writeResult(w, result, err)
}

func (a *API) sessionMCPAssignments(
	ctx context.Context,
	sessionID string,
) ([]store.MCPServerAssignment, error) {
	configured, err := a.store.GetSessionMCPServers(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	servers, err := a.store.ListMCPServers(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.SessionMCPServer, len(configured))
	for _, item := range configured {
		byID[item.ID] = item
	}
	result := make([]store.MCPServerAssignment, 0, len(servers))
	for _, server := range servers {
		item, enabled := byID[server.ID]
		if enabled {
			result = append(result, store.MCPServerAssignment{
				Server:           item.MCPServer,
				Enabled:          true,
				ConfirmationMode: item.ConfirmationMode,
				ToolPermissions:  item.ToolPermissions,
			})
			continue
		}
		result = append(result, store.MCPServerAssignment{
			Server:           server,
			Enabled:          false,
			ConfirmationMode: store.MCPConfirmationAsk,
			ToolPermissions:  []store.MCPToolPermission{},
		})
	}
	return result, nil
}

func (request mcpServerRequest) mcpServer() store.MCPServer {
	return store.MCPServer{
		Name:                    request.Name,
		Transport:               request.Transport,
		Command:                 request.Command,
		Arguments:               request.Arguments,
		Environment:             request.Environment,
		URL:                     request.URL,
		Headers:                 request.Headers,
		AuthType:                request.AuthType,
		BearerTokenEnvVar:       request.BearerTokenEnvVar,
		OAuthClientMode:         request.OAuthClientMode,
		OAuthClientID:           request.OAuthClientID,
		OAuthClientSecretEnvVar: request.OAuthClientSecretEnvVar,
		OAuthScopes:             request.OAuthScopes,
	}
}

func (request mcpAssignmentRequest) assignments() []store.MCPServerAssignment {
	result := make([]store.MCPServerAssignment, 0, len(request.Assignments))
	for _, item := range request.Assignments {
		result = append(result, store.MCPServerAssignment{
			Server:           store.MCPServer{ID: item.ServerID},
			Enabled:          item.Enabled,
			ConfirmationMode: item.ConfirmationMode,
			ToolPermissions:  item.ToolPermissions,
		})
	}
	return result
}

func oauthMCPConfigChanged(previous, next store.MCPServer) bool {
	return previous.Transport != next.Transport ||
		previous.URL != next.URL ||
		previous.AuthType != next.AuthType ||
		previous.OAuthClientMode != next.OAuthClientMode ||
		previous.OAuthClientID != next.OAuthClientID ||
		previous.OAuthClientSecretEnvVar != next.OAuthClientSecretEnvVar ||
		!slices.Equal(previous.OAuthScopes, next.OAuthScopes)
}

func writeOAuthCallbackPage(
	w http.ResponseWriter,
	title, message string,
	closeWindow bool,
) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set(
		"Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'",
	)
	w.WriteHeader(http.StatusOK)
	closeScript := ""
	if closeWindow {
		closeScript = `<script>window.setTimeout(function(){window.close()},800)</script>`
	}
	_, _ = fmt.Fprintf(
		w,
		`<!doctype html><html><head><meta name="viewport" content="width=device-width"><title>%s</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#111;color:#eee;font:16px Roboto,Arial,sans-serif}main{max-width:34rem;padding:2rem}h1{font-size:1.5rem;color:#fa2c9c}p{line-height:1.5}</style></head><body><main><h1>%s</h1><p>%s</p></main>%s</body></html>`,
		template.HTMLEscapeString(title),
		template.HTMLEscapeString(title),
		template.HTMLEscapeString(message),
		closeScript,
	)
}

func (a *API) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkspaces(r.Context())
	writeResult(w, items, err)
}

func (a *API) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string `json:"name"`
		RootPath string `json:"rootPath"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.CreateWorkspace(r.Context(), request.Name, request.RootPath)
	writeCreated(w, item, err)
}

func (a *API) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.UpdateWorkspace(r.Context(), r.PathValue("id"), request.Name)
	writeResult(w, item, err)
}

func (a *API) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteWorkspace(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getWorkspaceToolPermissions(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("id")
	permissions, err := a.store.GetWorkspaceToolPermissions(r.Context(), workspaceID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	response, err := a.workspaceToolPermissionResponse(r.Context(), workspaceID, permissions)
	writeResult(w, response, err)
}

func (a *API) replaceWorkspaceToolPermissions(w http.ResponseWriter, r *http.Request) {
	var request toolPermissionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	workspaceID := r.PathValue("id")
	permissions, err := a.store.ReplaceWorkspaceToolPermissions(r.Context(), workspaceID, request.Permissions)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	response, err := a.workspaceToolPermissionResponse(r.Context(), workspaceID, permissions)
	writeResult(w, response, err)
}

func (a *API) listLLMProviders(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListLLMProviders(r.Context())
	if err == nil {
		for index := range items {
			items[index] = a.withLLMProviderCredentialStatus(items[index])
		}
	}
	writeResult(w, items, err)
}

func (a *API) createLLMProvider(w http.ResponseWriter, r *http.Request) {
	var request llmProviderRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.AuthType = strings.ToLower(strings.TrimSpace(request.AuthType))
	if strings.TrimSpace(request.BearerToken) != "" &&
		request.AuthType != store.LLMAuthBearerKeyring {
		writeAPIError(w, fmt.Errorf(
			"%w: credential can only be supplied for OS keyring authentication",
			store.ErrInvalidInput,
		))
		return
	}
	item, err := a.store.CreateLLMProviderWithAuth(
		r.Context(),
		request.Name,
		request.APICompatibility,
		request.BaseURL,
		request.AuthType,
		request.BearerTokenEnvVar,
	)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if item.AuthType == store.LLMAuthBearerKeyring {
		if err := llmcredentials.Set(a.credentials, item.ID, request.BearerToken); err != nil {
			if rollbackErr := a.store.DeleteLLMProvider(r.Context(), item.ID); rollbackErr != nil {
				slog.Error(
					"roll back LLM provider after credential error",
					"provider_id",
					item.ID,
					"error",
					rollbackErr,
				)
			}
			writeAPIError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, a.withLLMProviderCredentialStatus(item))
}

func (a *API) updateLLMProvider(w http.ResponseWriter, r *http.Request) {
	var request llmProviderRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	request.AuthType = strings.ToLower(strings.TrimSpace(request.AuthType))
	providerID := r.PathValue("id")
	previous, err := a.store.GetLLMProvider(r.Context(), providerID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if request.AuthType == "" {
		switch {
		case strings.TrimSpace(request.BearerTokenEnvVar) != "":
			request.AuthType = store.LLMAuthBearerEnv
		case previous.AuthType == store.LLMAuthBearerKeyring:
			request.AuthType = store.LLMAuthBearerKeyring
		default:
			request.AuthType = store.LLMAuthNone
		}
	}
	token := strings.TrimSpace(request.BearerToken)
	if token != "" && request.AuthType != store.LLMAuthBearerKeyring {
		writeAPIError(w, fmt.Errorf(
			"%w: credential can only be supplied for OS keyring authentication",
			store.ErrInvalidInput,
		))
		return
	}
	if request.AuthType == store.LLMAuthBearerKeyring &&
		previous.AuthType != store.LLMAuthBearerKeyring &&
		token == "" {
		writeAPIError(w, fmt.Errorf(
			"%w: credential is required when enabling OS keyring authentication",
			store.ErrInvalidInput,
		))
		return
	}
	item, err := a.store.UpdateLLMProviderWithAuth(
		r.Context(),
		providerID,
		request.Name,
		request.APICompatibility,
		request.BaseURL,
		request.AuthType,
		request.BearerTokenEnvVar,
	)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if item.AuthType == store.LLMAuthBearerKeyring && token != "" {
		if err := llmcredentials.Set(a.credentials, providerID, token); err != nil {
			a.restoreLLMProvider(r.Context(), previous)
			writeAPIError(w, err)
			return
		}
	}
	if previous.AuthType == store.LLMAuthBearerKeyring &&
		item.AuthType != store.LLMAuthBearerKeyring {
		if err := llmcredentials.Delete(a.credentials, providerID); err != nil {
			slog.Warn(
				"delete unused LLM provider credential",
				"provider_id",
				providerID,
				"error",
				err,
			)
		}
	}
	writeJSON(w, http.StatusOK, a.withLLMProviderCredentialStatus(item))
}

type llmProviderRequest struct {
	Name              string `json:"name"`
	APICompatibility  string `json:"apiCompatibility"`
	BaseURL           string `json:"baseUrl"`
	AuthType          string `json:"authType"`
	BearerTokenEnvVar string `json:"bearerTokenEnvVar"`
	BearerToken       string `json:"bearerToken"`
}

func (a *API) withLLMProviderCredentialStatus(item store.LLMProvider) store.LLMProvider {
	available, backend, err := llmcredentials.Available(a.credentials, item)
	if err != nil {
		slog.Warn(
			"check LLM provider credential",
			"provider_id",
			item.ID,
			"error",
			err,
		)
	}
	item.CredentialAvailable = available
	item.CredentialBackend = backend
	return item
}

func (a *API) restoreLLMProvider(ctx context.Context, previous store.LLMProvider) {
	if _, err := a.store.UpdateLLMProviderWithAuth(
		ctx,
		previous.ID,
		previous.Name,
		previous.APICompatibility,
		previous.BaseURL,
		previous.AuthType,
		previous.BearerTokenEnvVar,
	); err != nil {
		slog.Error(
			"restore LLM provider after credential error",
			"provider_id",
			previous.ID,
			"error",
			err,
		)
	}
}

func (a *API) deleteLLMProvider(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	item, err := a.store.GetLLMProvider(r.Context(), providerID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := a.store.DeleteLLMProvider(r.Context(), providerID); err != nil {
		writeAPIError(w, err)
		return
	}
	if item.AuthType == store.LLMAuthBearerKeyring {
		if err := llmcredentials.Delete(a.credentials, providerID); err != nil {
			slog.Warn(
				"delete LLM provider credential",
				"provider_id",
				providerID,
				"error",
				err,
			)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listAvailableLLMModels(w http.ResponseWriter, r *http.Request) {
	providerRecord, err := a.store.GetLLMProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	bearerToken, err := llmcredentials.Resolve(a.credentials, providerRecord)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	provider, err := agentmodel.NewProvider(agentmodel.ProviderConfig{
		Compatibility:     providerRecord.APICompatibility,
		BaseURL:           providerRecord.BaseURL,
		BearerTokenEnvVar: providerRecord.BearerTokenEnvVar,
		BearerToken:       bearerToken,
		CredentialScope:   providerRecord.ID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	lister, ok := provider.(agentmodel.ModelLister)
	if !ok {
		writeError(w, http.StatusBadRequest, "this provider does not support model discovery")
		return
	}
	items, err := lister.ListModels(r.Context())
	if err != nil {
		slog.Warn("list provider models", "provider_id", providerRecord.ID, "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) listLLMModels(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListLLMModels(r.Context())
	writeResult(w, items, err)
}

func (a *API) createLLMModel(w http.ResponseWriter, r *http.Request) {
	var request llmModelRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.CreateLLMModel(
		r.Context(), request.LLMProviderID, request.Name, request.ModelID, request.GenerationSettings,
	)
	writeCreated(w, item, err)
}

func (a *API) updateLLMModel(w http.ResponseWriter, r *http.Request) {
	var request llmModelRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.UpdateLLMModel(
		r.Context(), r.PathValue("id"), request.LLMProviderID, request.Name, request.ModelID, request.GenerationSettings,
	)
	writeResult(w, item, err)
}

type llmModelRequest struct {
	LLMProviderID string `json:"llmProviderId"`
	Name          string `json:"name"`
	ModelID       string `json:"modelId"`
	store.GenerationSettings
}

func (a *API) deleteLLMModel(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteLLMModel(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listACPAgents(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListACPAgents(r.Context())
	writeResult(w, items, err)
}

func (a *API) createACPAgent(w http.ResponseWriter, r *http.Request) {
	var request acpAgentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.CreateACPAgent(r.Context(), request.Name, request.Command, request.Arguments)
	writeCreated(w, item, err)
}

func (a *API) updateACPAgent(w http.ResponseWriter, r *http.Request) {
	var request acpAgentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.UpdateACPAgent(
		r.Context(),
		r.PathValue("id"),
		request.Name,
		request.Command,
		request.Arguments,
	)
	writeResult(w, item, err)
}

type acpAgentRequest struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
}

func (a *API) deleteACPAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if err := a.store.DeleteACPAgent(r.Context(), agentID); err != nil {
		writeAPIError(w, err)
		return
	}
	a.engine.StopACPAgent(agentID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) inspectACPAgent(w http.ResponseWriter, r *http.Request) {
	item, err := a.engine.InspectACPAgent(r.Context(), r.PathValue("id"))
	writeResult(w, item, err)
}

func (a *API) authenticateACPAgent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		MethodID string `json:"methodId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.engine.AuthenticateACPAgent(r.Context(), r.PathValue("id"), request.MethodID)
	writeResult(w, item, err)
}

func (a *API) logoutACPAgent(w http.ResponseWriter, r *http.Request) {
	item, err := a.engine.LogoutACPAgent(r.Context(), r.PathValue("id"))
	writeResult(w, item, err)
}

func (a *API) listACPAgentSessions(w http.ResponseWriter, r *http.Request) {
	items, err := a.engine.ListACPAgentSessions(
		r.Context(),
		r.PathValue("id"),
		r.URL.Query().Get("cwd"),
	)
	writeResult(w, items, err)
}

func (a *API) importACPAgentSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RemoteSessionID string `json:"remoteSessionId"`
		WorkspaceID     string `json:"workspaceId"`
		Title           string `json:"title"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.engine.ImportACPSession(
		r.Context(),
		r.PathValue("id"),
		request.RemoteSessionID,
		request.WorkspaceID,
		request.Title,
	)
	writeCreated(w, item, err)
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListSessions(r.Context(), r.PathValue("workspaceID"))
	if err == nil {
		items = a.withRuntimeSessionStatuses(items)
	}
	writeResult(w, items, err)
}

func (a *API) listAllSessions(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListAllSessions(r.Context())
	if err == nil {
		items = a.withRuntimeSessionStatuses(items)
	}
	writeResult(w, items, err)
}

func (a *API) createSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		WorkspaceID string  `json:"workspaceId"`
		Title       string  `json:"title"`
		RuntimeType string  `json:"runtimeType"`
		LLMModelID  *string `json:"llmModelId"`
		ACPAgentID  *string `json:"acpAgentId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	var item store.AppSession
	var err error
	switch request.RuntimeType {
	case "", store.RuntimeADK:
		item, err = a.engine.CreateSession(
			r.Context(),
			request.WorkspaceID,
			request.Title,
			request.LLMModelID,
		)
	case store.RuntimeACP:
		if request.ACPAgentID == nil {
			err = fmt.Errorf("%w: ACP agent is required", store.ErrInvalidInput)
		} else {
			item, err = a.engine.CreateACPSession(
				r.Context(),
				request.WorkspaceID,
				request.Title,
				*request.ACPAgentID,
			)
		}
	default:
		err = fmt.Errorf("%w: unsupported session runtime %q", store.ErrInvalidInput, request.RuntimeType)
	}
	writeCreated(w, item, err)
}

func (a *API) getSession(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetSession(r.Context(), r.PathValue("id"))
	if err == nil {
		item = a.withRuntimeSessionStatus(item)
	}
	writeResult(w, item, err)
}

func (a *API) getSessionNotes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	item, err := a.store.GetSessionNotes(r.Context(), r.PathValue("id"))
	writeResult(w, item, err)
}

func (a *API) updateSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title      string  `json:"title"`
		LLMModelID *string `json:"llmModelId"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.store.UpdateSession(r.Context(), r.PathValue("id"), request.Title, request.LLMModelID)
	if err == nil {
		item = a.withRuntimeSessionStatus(item)
	}
	writeResult(w, item, err)
}

func (a *API) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.DeleteSession(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) setACPSessionConfigOption(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Value any `json:"value"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := a.engine.SetACPSessionConfigOption(
		r.Context(),
		r.PathValue("id"),
		r.PathValue("configID"),
		request.Value,
	)
	writeResult(w, item, err)
}

func (a *API) getSessionToolPermissions(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	permissions, err := a.store.GetSessionToolPermissions(r.Context(), sessionID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	response, err := a.sessionToolPermissionResponse(r.Context(), sessionID, permissions)
	writeResult(w, response, err)
}

func (a *API) replaceSessionToolPermissions(w http.ResponseWriter, r *http.Request) {
	var request toolPermissionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	sessionID := r.PathValue("id")
	permissions, err := a.store.ReplaceSessionToolPermissions(r.Context(), sessionID, request.Permissions)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	response, err := a.sessionToolPermissionResponse(r.Context(), sessionID, permissions)
	writeResult(w, response, err)
}

type toolPermissionRequest struct {
	Permissions []toolpolicy.Permission `json:"permissions"`
}

type toolPermissionResponse struct {
	OwnerType      string                  `json:"ownerType"`
	OwnerID        string                  `json:"ownerId"`
	OwnerName      string                  `json:"ownerName"`
	WorkspaceID    string                  `json:"workspaceId"`
	WorkspaceName  string                  `json:"workspaceName"`
	WorkspaceRoot  string                  `json:"workspaceRoot"`
	RepositoryRoot string                  `json:"repositoryRoot,omitempty"`
	SessionStatus  string                  `json:"sessionStatus,omitempty"`
	Definitions    []toolpolicy.Definition `json:"definitions"`
	Permissions    []toolpolicy.Permission `json:"permissions"`
}

func (a *API) workspaceToolPermissionResponse(ctx context.Context, workspaceID string, permissions []toolpolicy.Permission) (toolPermissionResponse, error) {
	workspace, err := a.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return toolPermissionResponse{}, err
	}
	repositoryRoot, _ := toolpolicy.FindRepositoryRoot(workspace.RootPath)
	return toolPermissionResponse{
		OwnerType:      "workspace",
		OwnerID:        workspace.ID,
		OwnerName:      workspace.Name,
		WorkspaceID:    workspace.ID,
		WorkspaceName:  workspace.Name,
		WorkspaceRoot:  workspace.RootPath,
		RepositoryRoot: repositoryRoot,
		Definitions:    toolpolicy.Definitions(),
		Permissions:    permissions,
	}, nil
}

func (a *API) sessionToolPermissionResponse(ctx context.Context, sessionID string, permissions []toolpolicy.Permission) (toolPermissionResponse, error) {
	sessionRecord, err := a.store.GetSession(ctx, sessionID)
	if err != nil {
		return toolPermissionResponse{}, err
	}
	workspace, err := a.store.GetWorkspace(ctx, sessionRecord.WorkspaceID)
	if err != nil {
		return toolPermissionResponse{}, err
	}
	sessionRecord = a.withRuntimeSessionStatus(sessionRecord)
	repositoryRoot, _ := toolpolicy.FindRepositoryRoot(workspace.RootPath)
	return toolPermissionResponse{
		OwnerType:      "session",
		OwnerID:        sessionRecord.ID,
		OwnerName:      sessionRecord.Title,
		WorkspaceID:    workspace.ID,
		WorkspaceName:  workspace.Name,
		WorkspaceRoot:  workspace.RootPath,
		RepositoryRoot: repositoryRoot,
		SessionStatus:  sessionRecord.Status,
		Definitions:    toolpolicy.Definitions(),
		Permissions:    permissions,
	}, nil
}

func (a *API) withRuntimeSessionStatuses(items []store.AppSession) []store.AppSession {
	for index := range items {
		items[index] = a.withRuntimeSessionStatus(items[index])
	}
	return items
}

func (a *API) withRuntimeSessionStatus(item store.AppSession) store.AppSession {
	if a.engine != nil && a.engine.WaitingForUser(item.ID) {
		item.Status = "waiting"
	}
	return item
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeCodedError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeCodedError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body must not exceed 1 MiB")
		} else {
			writeCodedError(w, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON with no unknown fields")
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeCodedError(w, http.StatusBadRequest, "invalid_json", "Request body must contain exactly one JSON value")
		return false
	}
	return true
}

func writeCreated(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func writeResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeAPIError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrConflict), errors.Is(err, engine.ErrSessionBusy), errors.Is(err, engine.ErrEngineShuttingDown), errors.Is(err, engine.ErrToolApprovalNotPending), errors.Is(err, engine.ErrUserInputNotPending), errors.Is(err, engine.ErrMCPElicitationNotPending), errors.Is(err, mcpruntime.ErrOAuthRequired):
		status = http.StatusConflict
	}
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "internal server error"
	}
	writeError(w, status, message)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeCodedError(w, status, errorCodeForStatus(status), message)
}

func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	case http.StatusBadGateway:
		return "bad_gateway"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		if status >= http.StatusInternalServerError {
			return "internal_error"
		}
		return "request_failed"
	}
}
