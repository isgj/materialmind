package mcpruntime

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"materialmind/internal/store"
)

const (
	ElicitationActionAccept  = "accept"
	ElicitationActionDecline = "decline"
	ElicitationActionCancel  = "cancel"
)

type ElicitationRequest struct {
	ID              string `json:"id"`
	Source          string `json:"source"`
	SessionID       string `json:"sessionId"`
	ToolCallID      string `json:"toolCallId"`
	ServerID        string `json:"serverId"`
	ServerName      string `json:"serverName"`
	Mode            string `json:"mode"`
	Message         string `json:"message"`
	URL             string `json:"url,omitempty"`
	ElicitationID   string `json:"elicitationId,omitempty"`
	RequestedSchema any    `json:"requestedSchema,omitempty"`
}

type ElicitationResolution struct {
	ID         string         `json:"id"`
	ToolCallID string         `json:"toolCallId"`
	Action     string         `json:"action"`
	Content    map[string]any `json:"content,omitempty"`
}

type ElicitationHandler func(
	context.Context,
	ElicitationRequest,
) (ElicitationResolution, error)

type toolCallContextKey struct{}

func (m *Manager) handleElicitation(
	ctx context.Context,
	connectionKey string,
	server store.MCPServer,
	request *mcp.ElicitRequest,
) (*mcp.ElicitResult, error) {
	if m.elicitation == nil || request == nil || request.Params == nil {
		return nil, fmt.Errorf("MCP elicitation is unavailable")
	}
	mode := strings.TrimSpace(request.Params.Mode)
	if mode == "" {
		mode = "form"
	}
	requestedURL := strings.TrimSpace(request.Params.URL)
	if mode == "url" {
		var err error
		requestedURL, err = safeElicitationURL(requestedURL)
		if err != nil {
			return nil, err
		}
	}

	call, _ := ctx.Value(toolCallContextKey{}).(*activeToolCall)
	if call == nil || call.connectionKey != connectionKey {
		call = m.onlyActiveCallForConnection(connectionKey)
	}
	callTimeout, _ := ctx.Value(callTimeoutContextKey{}).(*callInactivityTimeout)
	if callTimeout == nil && call != nil {
		callTimeout = call.timeout
	}
	if callTimeout != nil {
		resumeTimeout := callTimeout.pause()
		defer resumeTimeout()
	}
	sessionID := sessionIDFromConnectionKey(connectionKey)
	toolCallID := ""
	if call != nil {
		sessionID = call.sessionID
		toolCallID = call.toolCallID
	}
	if sessionID == "" {
		return nil, fmt.Errorf("MCP elicitation is only available during a session tool call")
	}
	requestID := uuid.NewString()
	if toolCallID == "" {
		toolCallID = "mcp-elicitation:" + requestID
	}
	resolution, err := m.elicitation(ctx, ElicitationRequest{
		ID:              requestID,
		Source:          "mcp",
		SessionID:       sessionID,
		ToolCallID:      toolCallID,
		ServerID:        server.ID,
		ServerName:      server.Name,
		Mode:            mode,
		Message:         strings.TrimSpace(request.Params.Message),
		URL:             requestedURL,
		ElicitationID:   strings.TrimSpace(request.Params.ElicitationID),
		RequestedSchema: request.Params.RequestedSchema,
	})
	if err != nil {
		return nil, err
	}
	if resolution.ID != requestID {
		return nil, fmt.Errorf("MCP elicitation response does not match the request")
	}
	if !validElicitationAction(resolution.Action) {
		return nil, fmt.Errorf("invalid MCP elicitation action %q", resolution.Action)
	}
	if resolution.Action != ElicitationActionAccept {
		resolution.Content = nil
	}
	return &mcp.ElicitResult{
		Action:  resolution.Action,
		Content: resolution.Content,
	}, nil
}

func (m *Manager) onlyActiveCallForConnection(connectionKey string) *activeToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched *activeToolCall
	for _, call := range m.activeCalls {
		if call.connectionKey != connectionKey {
			continue
		}
		if matched != nil {
			return nil
		}
		matched = call
	}
	return matched
}

func safeElicitationURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.IsAbs() == false || parsed.Hostname() == "" {
		return "", fmt.Errorf("MCP elicitation URL must be an absolute HTTP or HTTPS URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("MCP elicitation URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("MCP elicitation URL must not contain credentials")
	}
	return parsed.String(), nil
}

func validElicitationAction(action string) bool {
	return action == ElicitationActionAccept ||
		action == ElicitationActionDecline ||
		action == ElicitationActionCancel
}
