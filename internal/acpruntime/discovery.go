package acpruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"materialmind/internal/store"
)

const maxListedSessions = 10_000

type AgentImplementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type AgentAuthVariable struct {
	Name     string `json:"name"`
	Label    string `json:"label,omitempty"`
	Optional bool   `json:"optional"`
	Secret   bool   `json:"secret"`
}

type AgentAuthMethod struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Type        string              `json:"type"`
	Description string              `json:"description,omitempty"`
	Link        string              `json:"link,omitempty"`
	Variables   []AgentAuthVariable `json:"variables"`
	Supported   bool                `json:"supported"`
}

type AgentSessionCapabilities struct {
	List                  bool `json:"list"`
	Load                  bool `json:"load"`
	Resume                bool `json:"resume"`
	Close                 bool `json:"close"`
	AdditionalDirectories bool `json:"additionalDirectories"`
}

type AgentInspection struct {
	ProtocolVersion int                      `json:"protocolVersion"`
	Implementation  *AgentImplementation     `json:"implementation,omitempty"`
	AuthMethods     []AgentAuthMethod        `json:"authMethods"`
	Logout          bool                     `json:"logout"`
	Sessions        AgentSessionCapabilities `json:"sessions"`
	PromptImage     bool                     `json:"promptImage"`
	PromptAudio     bool                     `json:"promptAudio"`
	EmbeddedContext bool                     `json:"embeddedContext"`
	MCPStdio        bool                     `json:"mcpStdio"`
	MCPHTTP         bool                     `json:"mcpHttp"`
	MCPSSE          bool                     `json:"mcpSse"`
}

type RemoteSession struct {
	ID                    string   `json:"id"`
	WorkingDirectory      string   `json:"workingDirectory"`
	Title                 string   `json:"title,omitempty"`
	UpdatedAt             string   `json:"updatedAt,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories"`
}

func (m *Manager) InspectAgent(
	ctx context.Context,
	agent store.ACPAgent,
) (AgentInspection, error) {
	process, err := m.process(ctx, agent)
	if err != nil {
		return AgentInspection{}, err
	}
	return inspectInitialize(process.initialize), nil
}

func (m *Manager) Authenticate(
	ctx context.Context,
	agent store.ACPAgent,
	methodID string,
) (AgentInspection, error) {
	methodID = strings.TrimSpace(methodID)
	if methodID == "" {
		return AgentInspection{}, fmt.Errorf("%w: ACP authentication method is required", store.ErrInvalidInput)
	}
	process, err := m.process(ctx, agent)
	if err != nil {
		return AgentInspection{}, err
	}
	found := false
	for _, method := range process.initialize.AuthMethods {
		if method.Agent != nil && method.Agent.Id == methodID {
			found = true
			break
		}
		if authMethodID(method) == methodID {
			return AgentInspection{}, fmt.Errorf(
				"%w: ACP authentication method %q requires client support that is not enabled",
				ErrClientCapabilityUnsupported,
				methodID,
			)
		}
	}
	if !found {
		return AgentInspection{}, fmt.Errorf("%w: ACP authentication method %q is unavailable", store.ErrInvalidInput, methodID)
	}
	if _, err := process.connection.Authenticate(ctx, acp.AuthenticateRequest{MethodId: methodID}); err != nil {
		return AgentInspection{}, fmt.Errorf("authenticate ACP agent %q: %w", agent.Name, err)
	}
	return inspectInitialize(process.initialize), nil
}

func (m *Manager) Logout(
	ctx context.Context,
	agent store.ACPAgent,
) (AgentInspection, error) {
	process, err := m.process(ctx, agent)
	if err != nil {
		return AgentInspection{}, err
	}
	if process.initialize.AgentCapabilities.Auth.Logout == nil {
		return AgentInspection{}, fmt.Errorf("%w: ACP agent %q does not support logout", ErrClientCapabilityUnsupported, agent.Name)
	}
	if _, err := process.connection.Logout(ctx, acp.LogoutRequest{}); err != nil {
		return AgentInspection{}, fmt.Errorf("log out from ACP agent %q: %w", agent.Name, err)
	}
	return inspectInitialize(process.initialize), nil
}

func (m *Manager) ListSessions(
	ctx context.Context,
	agent store.ACPAgent,
	workingDirectory string,
) ([]RemoteSession, error) {
	process, err := m.process(ctx, agent)
	if err != nil {
		return nil, err
	}
	if process.initialize.AgentCapabilities.SessionCapabilities.List == nil {
		return nil, fmt.Errorf("%w: ACP agent %q does not support session discovery", ErrClientCapabilityUnsupported, agent.Name)
	}
	workingDirectory = strings.TrimSpace(workingDirectory)
	var cwd *string
	if workingDirectory != "" {
		workingDirectory = filepath.Clean(workingDirectory)
		if !filepath.IsAbs(workingDirectory) {
			return nil, fmt.Errorf("%w: ACP session working directory must be absolute", store.ErrInvalidInput)
		}
		cwd = &workingDirectory
	}

	result := make([]RemoteSession, 0)
	seenCursors := make(map[string]struct{})
	var cursor *string
	for {
		response, listErr := process.connection.ListSessions(ctx, acp.ListSessionsRequest{
			Cursor: cursor,
			Cwd:    cwd,
		})
		if listErr != nil {
			return nil, fmt.Errorf("list sessions from ACP agent %q: %w", agent.Name, listErr)
		}
		for _, item := range response.Sessions {
			if len(result) >= maxListedSessions {
				return nil, fmt.Errorf("ACP agent %q returned more than %d sessions", agent.Name, maxListedSessions)
			}
			remote := RemoteSession{
				ID:                    string(item.SessionId),
				WorkingDirectory:      item.Cwd,
				AdditionalDirectories: append([]string(nil), item.AdditionalDirectories...),
			}
			if item.Title != nil {
				remote.Title = *item.Title
			}
			if item.UpdatedAt != nil {
				remote.UpdatedAt = *item.UpdatedAt
			}
			result = append(result, remote)
		}
		if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" {
			return result, nil
		}
		next := *response.NextCursor
		if _, exists := seenCursors[next]; exists {
			return nil, fmt.Errorf("ACP agent %q repeated its session-list cursor", agent.Name)
		}
		seenCursors[next] = struct{}{}
		cursor = &next
	}
}

func (m *Manager) LoadExistingSession(
	ctx context.Context,
	agent store.ACPAgent,
	sessionID, workingDirectory string,
	additionalDirectories []string,
	mcpServers []store.SessionMCPServer,
	internalMCPToken string,
) (SessionState, error) {
	sessionID = strings.TrimSpace(sessionID)
	workingDirectory = filepath.Clean(strings.TrimSpace(workingDirectory))
	if sessionID == "" || !filepath.IsAbs(workingDirectory) {
		return SessionState{}, fmt.Errorf("%w: ACP session ID and absolute working directory are required", store.ErrInvalidInput)
	}
	process, err := m.process(ctx, agent)
	if err != nil {
		return SessionState{}, err
	}
	servers, err := configuredMCPServers(
		process.initialize,
		mcpServers,
		m.mcpBridge,
		m.internalMCP,
		internalMCPToken,
	)
	if err != nil {
		return SessionState{}, err
	}
	requestDirectories := supportedAdditionalDirectories(process.initialize, additionalDirectories)
	acpSessionID := acp.SessionId(sessionID)
	var options []acp.SessionConfigOption
	capabilities := process.initialize.AgentCapabilities
	switch {
	case capabilities.LoadSession:
		response, loadErr := process.connection.LoadSession(ctx, acp.LoadSessionRequest{
			SessionId:             acpSessionID,
			Cwd:                   workingDirectory,
			AdditionalDirectories: requestDirectories,
			McpServers:            servers,
		})
		if loadErr != nil {
			return SessionState{}, fmt.Errorf("load ACP session with %q: %w", agent.Name, loadErr)
		}
		options = response.ConfigOptions
	case capabilities.SessionCapabilities.Resume != nil:
		response, resumeErr := process.connection.ResumeSession(ctx, acp.ResumeSessionRequest{
			SessionId:             acpSessionID,
			Cwd:                   workingDirectory,
			AdditionalDirectories: requestDirectories,
			McpServers:            servers,
		})
		if resumeErr != nil {
			return SessionState{}, fmt.Errorf("resume ACP session with %q: %w", agent.Name, resumeErr)
		}
		options = response.ConfigOptions
	default:
		return SessionState{}, fmt.Errorf("%w: ACP agent %q cannot load discovered sessions", ErrClientCapabilityUnsupported, agent.Name)
	}
	process.mu.Lock()
	process.sessions[acpSessionID] = struct{}{}
	process.mu.Unlock()
	return SessionState{ID: sessionID, ConfigOptions: options}, nil
}

func inspectInitialize(initialize acp.InitializeResponse) AgentInspection {
	capabilities := initialize.AgentCapabilities
	result := AgentInspection{
		ProtocolVersion: int(initialize.ProtocolVersion),
		AuthMethods:     make([]AgentAuthMethod, 0, len(initialize.AuthMethods)),
		Logout:          capabilities.Auth.Logout != nil,
		Sessions: AgentSessionCapabilities{
			List:                  capabilities.SessionCapabilities.List != nil,
			Load:                  capabilities.LoadSession,
			Resume:                capabilities.SessionCapabilities.Resume != nil,
			Close:                 capabilities.SessionCapabilities.Close != nil,
			AdditionalDirectories: capabilities.SessionCapabilities.AdditionalDirectories != nil,
		},
		PromptImage:     capabilities.PromptCapabilities.Image,
		PromptAudio:     capabilities.PromptCapabilities.Audio,
		EmbeddedContext: capabilities.PromptCapabilities.EmbeddedContext,
		MCPStdio:        capabilities.McpCapabilities.Acp,
		MCPHTTP:         capabilities.McpCapabilities.Http,
		MCPSSE:          capabilities.McpCapabilities.Sse,
	}
	if initialize.AgentInfo != nil {
		result.Implementation = &AgentImplementation{
			Name:    initialize.AgentInfo.Name,
			Version: initialize.AgentInfo.Version,
		}
		if initialize.AgentInfo.Title != nil {
			result.Implementation.Title = *initialize.AgentInfo.Title
		}
	}
	for _, method := range initialize.AuthMethods {
		result.AuthMethods = append(result.AuthMethods, summarizeAuthMethod(method))
	}
	return result
}

func summarizeAuthMethod(method acp.AuthMethod) AgentAuthMethod {
	summary := AgentAuthMethod{Variables: []AgentAuthVariable{}}
	switch {
	case method.Agent != nil:
		summary.ID = method.Agent.Id
		summary.Name = method.Agent.Name
		summary.Type = "agent"
		summary.Description = stringPointer(method.Agent.Description)
		summary.Supported = true
	case method.EnvVar != nil:
		summary.ID = method.EnvVar.Id
		summary.Name = method.EnvVar.Name
		summary.Type = "env_var"
		summary.Description = stringPointer(method.EnvVar.Description)
		summary.Link = stringPointer(method.EnvVar.Link)
		for _, variable := range method.EnvVar.Vars {
			summary.Variables = append(summary.Variables, AgentAuthVariable{
				Name:     variable.Name,
				Label:    stringPointer(variable.Label),
				Optional: variable.Optional,
				Secret:   variable.Secret,
			})
		}
	case method.Terminal != nil:
		summary.ID = method.Terminal.Id
		summary.Name = method.Terminal.Name
		summary.Type = "terminal"
		summary.Description = stringPointer(method.Terminal.Description)
	}
	return summary
}

func authMethodID(method acp.AuthMethod) string {
	switch {
	case method.Agent != nil:
		return method.Agent.Id
	case method.EnvVar != nil:
		return method.EnvVar.Id
	case method.Terminal != nil:
		return method.Terminal.Id
	default:
		return ""
	}
}

func stringPointer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
